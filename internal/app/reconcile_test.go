package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"github.com/rake-pro/dropwarden/internal/browser"
	"github.com/rake-pro/dropwarden/internal/config"
	"github.com/rake-pro/dropwarden/internal/domain"
	"github.com/rake-pro/dropwarden/internal/store"
	"github.com/rake-pro/dropwarden/internal/watcher"
)

// ---- fakes ----

// fakeSession is a browser.Session that launches nothing; it only records that
// the reconciler closed it.
type fakeSession struct {
	events chan browser.Event
	closed atomic.Bool
}

func newFakeSession() *fakeSession { return &fakeSession{events: make(chan browser.Event)} }

func (f *fakeSession) Navigate(context.Context, string) error          { return nil }
func (f *fakeSession) Reload(context.Context, bool) error              { return nil }
func (f *fakeSession) CurrentURL(context.Context) (string, error)      { return "", nil }
func (f *fakeSession) Eval(context.Context, string, any) error         { return nil }
func (f *fakeSession) Click(context.Context, string) (bool, error)     { return false, nil }
func (f *fakeSession) SeedOnNewDocument(context.Context, string) error { return nil }
func (f *fakeSession) Events() <-chan browser.Event                    { return f.events }
func (f *fakeSession) ShiftWindow(int, int) error                      { return nil }
func (f *fakeSession) SetMinimized(context.Context, bool) error        { return nil }
func (f *fakeSession) Alive() bool                                     { return !f.closed.Load() }
func (f *fakeSession) Close() error                                    { f.closed.Store(true); return nil }

type nopEmitter struct{}

func (nopEmitter) Emit(string, any) {}

// pair identifies one (profile, platform) runtime in the recorders below.
type pair struct {
	profile string
	plat    domain.Platform
}

// fakeBinder records everything the reconciler publishes.
type fakeBinder struct {
	mu         sync.Mutex
	attached   map[pair]bool
	needsLogin map[pair]bool
	detached   []pair
	added      []string
	removed    []string
}

func newFakeBinder() *fakeBinder {
	return &fakeBinder{attached: map[pair]bool{}, needsLogin: map[pair]bool{}}
}

func (b *fakeBinder) AddProfile(name string, _ *store.Store) {
	b.mu.Lock()
	b.added = append(b.added, name)
	b.mu.Unlock()
}

func (b *fakeBinder) RemoveProfile(name string) {
	b.mu.Lock()
	b.removed = append(b.removed, name)
	b.mu.Unlock()
}

func (b *fakeBinder) Attach(profile string, plat domain.Platform, _ *watcher.Engine, _ watcher.Discoverer, _ browser.Session, needsLogin bool) {
	b.mu.Lock()
	b.attached[pair{profile, plat}] = true
	b.needsLogin[pair{profile, plat}] = needsLogin
	b.mu.Unlock()
}

func (b *fakeBinder) Detach(profile string, plat domain.Platform) {
	b.mu.Lock()
	delete(b.attached, pair{profile, plat})
	b.detached = append(b.detached, pair{profile, plat})
	b.mu.Unlock()
}

func (b *fakeBinder) isAttached(profile string, plat domain.Platform) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.attached[pair{profile, plat}]
}

// harness is an App wired to fakes: no Chrome, no real engine work.
type harness struct {
	app     *App
	binder  *fakeBinder
	cancel  context.CancelFunc
	mu      sync.Mutex
	starts  map[pair]int
	fail    map[pair]bool
	fresh   map[pair]bool
	session map[pair]*fakeSession
}

func newHarness(t *testing.T, cfg *config.Config) *harness {
	t.Helper()
	t.Setenv("DROPWARDEN_HOME", t.TempDir())
	h := &harness{
		binder:  newFakeBinder(),
		starts:  map[pair]int{},
		fail:    map[pair]bool{},
		fresh:   map[pair]bool{},
		session: map[pair]*fakeSession{},
	}
	a := &App{Cfg: cfg, ConfigPath: filepath.Join(t.TempDir(), "config.yaml"), Log: zerolog.Nop(), desired: cfg}
	a.startPlat = func(ctx context.Context, pr *ProfileRuntime, plat domain.Platform, stagger time.Duration) (*PlatformRuntime, error) {
		k := pair{pr.Name, plat}
		h.mu.Lock()
		h.starts[k]++
		failed, fresh := h.fail[k], h.fresh[k]
		h.mu.Unlock()
		if failed {
			return nil, fmt.Errorf("launch refused for %s/%s", pr.Name, plat)
		}
		sess := newFakeSession()
		h.mu.Lock()
		h.session[k] = sess
		h.mu.Unlock()
		// A paused engine sits in waitIdle, so Run exits purely on ctx cancel:
		// exactly the lifecycle the reconciler drives.
		ecfg := *cfg
		ecfg.Watcher.EnabledOnStart = false
		return &PlatformRuntime{
			Session:    sess,
			NeedsLogin: fresh,
			Engine: watcher.New(watcher.Deps{
				Cfg: &ecfg, Emit: nopEmitter{}, Log: zerolog.Nop(),
				Platform: plat, Profile: pr.Name,
			}),
		}, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	h.app = a
	h.cancel = cancel
	t.Cleanup(func() { cancel(); a.Close() })
	a.cfgMu.Lock()
	a.liveCtx = ctx
	a.emit = nopEmitter{}
	a.binder = h.binder
	a.cfgMu.Unlock()
	return h
}

func (h *harness) startCount(profile string, plat domain.Platform) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.starts[pair{profile, plat}]
}

func (h *harness) sessionFor(profile string, plat domain.Platform) *fakeSession {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.session[pair{profile, plat}]
}

// setConfig publishes a new desired config, as a settings commit does.
func (h *harness) setConfig(cfg *config.Config) {
	h.app.cfgMu.Lock()
	h.app.desired = cfg
	h.app.cfgMu.Unlock()
}

// testConfig builds a valid config with the named profiles, all enabled.
func testConfig(t *testing.T, names ...string) *config.Config {
	t.Helper()
	cfg, err := config.StarterDefaults()
	if err != nil {
		t.Fatalf("starter defaults: %v", err)
	}
	cfg.Profiles = nil
	for _, n := range names {
		dir := ""
		if n == config.MainProfileName {
			dir = "."
		}
		cfg.Profiles = append(cfg.Profiles, config.ProfileConfig{Name: n, Dir: dir, Enabled: true})
	}
	return cfg
}

// clone deep-copies the parts of a config the tests mutate.
func clone(cfg *config.Config) *config.Config {
	cp := *cfg
	cp.Profiles = append([]config.ProfileConfig(nil), cfg.Profiles...)
	return &cp
}

func boolPtr(b bool) *bool { return &b }

// ---- tests ----

func TestReconcileStartsEnabledProfiles(t *testing.T) {
	cfg := testConfig(t, config.MainProfileName, "second")
	h := newHarness(t, cfg)
	h.app.ReconcileSync()

	for _, name := range []string{config.MainProfileName, "second"} {
		if h.startCount(name, domain.PlatformTwitch) != 1 {
			t.Fatalf("%s twitch should have started once, got %d", name, h.startCount(name, domain.PlatformTwitch))
		}
		if !h.binder.isAttached(name, domain.PlatformTwitch) {
			t.Fatalf("%s twitch should be attached", name)
		}
	}
	if h.startCount(config.MainProfileName, domain.PlatformKick) != 0 {
		t.Fatal("kick is disabled by default and must not start")
	}
	// A second pass with unchanged config must be a no-op.
	h.app.ReconcileSync()
	if h.startCount("second", domain.PlatformTwitch) != 1 {
		t.Fatalf("an unchanged reconcile must not restart a running runtime: %d", h.startCount("second", domain.PlatformTwitch))
	}
}

func TestReconcileDisableProfileStopsItAndKeepsStore(t *testing.T) {
	cfg := testConfig(t, config.MainProfileName, "second")
	h := newHarness(t, cfg)
	h.app.ReconcileSync()
	sess := h.sessionFor("second", domain.PlatformTwitch)

	off := clone(cfg)
	off.Profile("second").Enabled = false
	h.setConfig(off)
	h.app.ReconcileSync()

	if h.binder.isAttached("second", domain.PlatformTwitch) {
		t.Fatal("a disabled profile's runtime must be detached")
	}
	if !sess.closed.Load() {
		t.Fatal("a disabled profile's browser must be closed")
	}
	// The store stays open: the UI still lists that profile's history/queue.
	pr := h.app.Profile("second")
	if pr == nil || pr.Store == nil {
		t.Fatal("disabling a profile must keep its store open")
	}
	if _, err := pr.Store.GetQueue(); err != nil {
		t.Fatalf("store should still be usable after a disable: %v", err)
	}

	// Re-enabling starts it again.
	h.setConfig(cfg)
	h.app.ReconcileSync()
	if h.startCount("second", domain.PlatformTwitch) != 2 {
		t.Fatalf("re-enabling should relaunch, got %d starts", h.startCount("second", domain.PlatformTwitch))
	}
	if !h.binder.isAttached("second", domain.PlatformTwitch) {
		t.Fatal("re-enabled runtime should be attached again")
	}
}

func TestReconcileKickToggleBothDirections(t *testing.T) {
	cfg := testConfig(t, config.MainProfileName)
	h := newHarness(t, cfg)
	h.app.ReconcileSync()
	if h.startCount(config.MainProfileName, domain.PlatformKick) != 0 {
		t.Fatal("kick must start disabled")
	}

	on := clone(cfg)
	on.Profile(config.MainProfileName).Kick.Enabled = boolPtr(true)
	h.setConfig(on)
	h.app.ReconcileSync()
	if h.startCount(config.MainProfileName, domain.PlatformKick) != 1 {
		t.Fatal("enabling kick must start its runtime without a restart")
	}
	if !h.binder.isAttached(config.MainProfileName, domain.PlatformKick) {
		t.Fatal("kick runtime should be attached")
	}
	kickSess := h.sessionFor(config.MainProfileName, domain.PlatformKick)

	off := clone(on)
	off.Profile(config.MainProfileName).Kick.Enabled = boolPtr(false)
	h.setConfig(off)
	h.app.ReconcileSync()
	if h.binder.isAttached(config.MainProfileName, domain.PlatformKick) {
		t.Fatal("disabling kick must detach its runtime")
	}
	if !kickSess.closed.Load() {
		t.Fatal("disabling kick must close its browser")
	}
	// Twitch is untouched throughout.
	if h.startCount(config.MainProfileName, domain.PlatformTwitch) != 1 {
		t.Fatal("a kick toggle must not disturb the twitch runtime")
	}
}

func TestReconcileAddedProfileStartsFreshAndPaused(t *testing.T) {
	cfg := testConfig(t, config.MainProfileName)
	h := newHarness(t, cfg)
	h.app.ReconcileSync()

	added := clone(cfg)
	added.Profiles = append(added.Profiles, config.ProfileConfig{Name: "greg", Enabled: true})
	// A brand-new profile's Chrome dir has never been used, so its runtime
	// comes up needing a login.
	h.mu.Lock()
	h.fresh[pair{"greg", domain.PlatformTwitch}] = true
	h.mu.Unlock()
	h.setConfig(added)
	h.app.ReconcileSync()

	if h.startCount("greg", domain.PlatformTwitch) != 1 {
		t.Fatal("an added profile must start without an app restart")
	}
	if h.app.Profile("greg") == nil {
		t.Fatal("an added profile must get a runtime slot and a store")
	}
	h.binder.mu.Lock()
	var addedGreg int
	for _, n := range h.binder.added {
		if n == "greg" {
			addedGreg++
		}
	}
	needsLogin := h.binder.needsLogin[pair{"greg", domain.PlatformTwitch}]
	h.binder.mu.Unlock()
	if addedGreg != 1 {
		t.Fatalf("the binder should have been told about greg exactly once, got %d", addedGreg)
	}
	if !needsLogin {
		t.Fatal("a fresh chrome profile must be published as needing a login")
	}
}

func TestReconcileRemovedProfileClosesStore(t *testing.T) {
	cfg := testConfig(t, config.MainProfileName, "second")
	h := newHarness(t, cfg)
	h.app.ReconcileSync()
	pr := h.app.Profile("second")
	if pr == nil {
		t.Fatal("second profile should be up")
	}

	gone := clone(cfg)
	gone.Profiles = gone.Profiles[:1]
	h.setConfig(gone)
	h.app.ReconcileSync()

	if h.app.Profile("second") != nil {
		t.Fatal("a removed profile must be dropped from the runtime list")
	}
	h.binder.mu.Lock()
	removed := append([]string(nil), h.binder.removed...)
	h.binder.mu.Unlock()
	if len(removed) != 1 || removed[0] != "second" {
		t.Fatalf("the binder should have been told to forget second: %v", removed)
	}
	if _, err := pr.Store.GetQueue(); err == nil {
		t.Fatal("a removed profile's store must be closed")
	}
	// The data directory is never deleted.
	root, _ := config.Dir()
	if _, err := os.Stat(filepath.Join(root, "profiles", "second", "dropwarden.db")); err != nil {
		t.Fatalf("removing a profile must leave its data on disk: %v", err)
	}
}

func TestReconcileLaunchFailureIsNotFatal(t *testing.T) {
	cfg := testConfig(t, config.MainProfileName)
	cfg.Profile(config.MainProfileName).Kick.Enabled = boolPtr(true)
	h := newHarness(t, cfg)
	h.mu.Lock()
	h.fail[pair{config.MainProfileName, domain.PlatformKick}] = true
	h.mu.Unlock()

	h.app.ReconcileSync()

	if !h.binder.isAttached(config.MainProfileName, domain.PlatformTwitch) {
		t.Fatal("a kick launch failure must not take twitch down")
	}
	if h.binder.isAttached(config.MainProfileName, domain.PlatformKick) {
		t.Fatal("a failed launch must leave the runtime absent, not attached")
	}
	// The next pass retries it; still absent, still not fatal.
	h.app.ReconcileSync()
	if h.startCount(config.MainProfileName, domain.PlatformKick) != 2 {
		t.Fatalf("a failed runtime should be retried on the next pass, got %d", h.startCount(config.MainProfileName, domain.PlatformKick))
	}
}

func TestReconcileConcurrentCommitsDoNotDoubleStart(t *testing.T) {
	cfg := testConfig(t, config.MainProfileName, "second")
	h := newHarness(t, cfg)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h.app.ReconcileSync()
		}()
	}
	wg.Wait()

	for _, name := range []string{config.MainProfileName, "second"} {
		if n := h.startCount(name, domain.PlatformTwitch); n != 1 {
			t.Fatalf("%s started %d times; concurrent commits must not double-start", name, n)
		}
	}
}

func TestChromeDirFresh(t *testing.T) {
	dir := t.TempDir()
	if !chromeDirFresh(filepath.Join(dir, "missing")) {
		t.Fatal("a missing chrome dir is fresh")
	}
	empty := filepath.Join(dir, "empty")
	if err := os.MkdirAll(empty, 0o700); err != nil {
		t.Fatal(err)
	}
	if !chromeDirFresh(empty) {
		t.Fatal("an empty chrome dir (pre-created by add-profile) is fresh")
	}
	if err := os.WriteFile(filepath.Join(empty, "Local State"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if chromeDirFresh(empty) {
		t.Fatal("a used chrome dir must not read as fresh")
	}
}

// ---- shared window (one Chrome per profile, Kick as a tab) ----

// fakeWindow is a browser.WindowHost that hands out fake tabs instead of
// launching Chrome, so the whole real start/teardown path can be exercised.
type fakeWindow struct {
	profileDir string

	mu    sync.Mutex
	owner *fakeSession
	tabs  []*fakeSession

	done      chan struct{}
	closed    atomic.Bool
	closeOnce sync.Once
}

func newFakeWindow(dir string) *fakeWindow {
	return &fakeWindow{profileDir: dir, owner: newFakeSession(), done: make(chan struct{})}
}

func (w *fakeWindow) Owner() browser.Session { return w.owner }

func (w *fakeWindow) NewTab(context.Context) (browser.Session, error) {
	s := newFakeSession()
	w.mu.Lock()
	w.tabs = append(w.tabs, s)
	w.mu.Unlock()
	return s, nil
}

func (w *fakeWindow) Alive() bool           { return !w.closed.Load() }
func (w *fakeWindow) Done() <-chan struct{} { return w.done }

func (w *fakeWindow) extraTabs() []*fakeSession {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]*fakeSession(nil), w.tabs...)
}

func (w *fakeWindow) Close() error {
	w.closed.Store(true)
	_ = w.owner.Close()
	for _, t := range w.extraTabs() {
		_ = t.Close()
	}
	w.closeOnce.Do(func() { close(w.done) })
	return nil
}

// bharness runs the REAL per-platform start path (controllers, engines, window
// coordinator, shared-window group) against fake browsers, so the window
// lifecycle itself is under test rather than a stand-in for it.
type bharness struct {
	app    *App
	binder *fakeBinder

	mu       sync.Mutex
	windows  []*fakeWindow
	launched []string // profile dir per separate-mode Chrome launch
}

func newBrowserHarness(t *testing.T, cfg *config.Config) *bharness {
	t.Helper()
	t.Setenv("DROPWARDEN_HOME", t.TempDir())
	// Engines sit in waitIdle so Run exits purely on ctx cancel, exactly the
	// lifecycle the reconciler drives.
	cfg.Watcher.EnabledOnStart = false

	h := &bharness{binder: newFakeBinder()}
	a := &App{Cfg: cfg, ConfigPath: filepath.Join(t.TempDir(), "config.yaml"), Log: zerolog.Nop(), desired: cfg}
	a.openWindow = func(_ context.Context, _ config.BrowserConfig, dir string, _ float64, _ zerolog.Logger) (browser.WindowHost, error) {
		w := newFakeWindow(dir)
		h.mu.Lock()
		h.windows = append(h.windows, w)
		h.mu.Unlock()
		return w, nil
	}
	a.launchSession = func(_ context.Context, _ config.BrowserConfig, dir string, _ float64, _ zerolog.Logger) (browser.Session, error) {
		h.mu.Lock()
		h.launched = append(h.launched, dir)
		h.mu.Unlock()
		return newFakeSession(), nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	h.app = a
	t.Cleanup(func() { cancel(); a.Close() })
	a.cfgMu.Lock()
	a.liveCtx = ctx
	a.emit = nopEmitter{}
	a.binder = h.binder
	a.cfgMu.Unlock()
	return h
}

func (h *bharness) windowList() []*fakeWindow {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]*fakeWindow(nil), h.windows...)
}

func (h *bharness) launchedDirs() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.launched...)
}

func TestSharedWindowKickStartsAsATabOnTheLiveBrowser(t *testing.T) {
	cfg := testConfig(t, config.MainProfileName)
	h := newBrowserHarness(t, cfg)
	h.app.ReconcileSync()

	wins := h.windowList()
	if len(wins) != 1 {
		t.Fatalf("a profile must cost exactly one chrome window, got %d", len(wins))
	}
	if n := len(wins[0].extraTabs()); n != 0 {
		t.Fatalf("twitch alone must not open extra tabs, got %d", n)
	}

	on := clone(cfg)
	on.Profile(config.MainProfileName).Kick.Enabled = boolPtr(true)
	h.app.cfgMu.Lock()
	h.app.desired = on
	h.app.cfgMu.Unlock()
	h.app.ReconcileSync()

	if got := len(h.windowList()); got != 1 {
		t.Fatalf("enabling kick must NOT launch a second chrome: %d windows", got)
	}
	if n := len(wins[0].extraTabs()); n != 1 {
		t.Fatalf("enabling kick must open one tab on the live window, got %d", n)
	}
	if !h.binder.isAttached(config.MainProfileName, domain.PlatformKick) {
		t.Fatal("the kick tab runtime should be attached")
	}
	if len(h.launchedDirs()) != 0 {
		t.Fatalf("shared mode must never take the separate-instance path: %v", h.launchedDirs())
	}
}

func TestSharedWindowKickStopClosesOnlyItsTab(t *testing.T) {
	cfg := testConfig(t, config.MainProfileName)
	cfg.Profile(config.MainProfileName).Kick.Enabled = boolPtr(true)
	h := newBrowserHarness(t, cfg)
	h.app.ReconcileSync()

	wins := h.windowList()
	if len(wins) != 1 || len(wins[0].extraTabs()) != 1 {
		t.Fatalf("expected one window with one kick tab, got %d windows", len(wins))
	}
	kickTab := wins[0].extraTabs()[0]

	off := clone(cfg)
	off.Profile(config.MainProfileName).Kick.Enabled = boolPtr(false)
	h.app.cfgMu.Lock()
	h.app.desired = off
	h.app.cfgMu.Unlock()
	h.app.ReconcileSync()

	if wins[0].closed.Load() {
		t.Fatal("stopping kick must not close the profile's chrome window")
	}
	if !kickTab.closed.Load() {
		t.Fatal("stopping kick must close its tab")
	}
	if wins[0].owner.closed.Load() {
		t.Fatal("stopping kick must leave the twitch tab running")
	}
	if !h.binder.isAttached(config.MainProfileName, domain.PlatformTwitch) {
		t.Fatal("twitch must stay attached across a kick stop")
	}

	// Re-enabling opens a fresh tab on the same window, still no relaunch.
	h.app.cfgMu.Lock()
	h.app.desired = cfg
	h.app.cfgMu.Unlock()
	h.app.ReconcileSync()
	if got := len(h.windowList()); got != 1 {
		t.Fatalf("re-enabling kick must reuse the live window: %d windows", got)
	}
	if n := len(wins[0].extraTabs()); n != 2 {
		t.Fatalf("re-enabling kick should have opened one more tab, got %d", n)
	}
}

func TestSharedWindowStoppingTheProfileClosesTheBrowser(t *testing.T) {
	cfg := testConfig(t, config.MainProfileName, "second")
	cfg.Kick.Enabled = true
	h := newBrowserHarness(t, cfg)
	h.app.ReconcileSync()

	wins := h.windowList()
	if len(wins) != 2 {
		t.Fatalf("two profiles running both platforms must cost two windows, got %d", len(wins))
	}

	off := clone(cfg)
	off.Profile("second").Enabled = false
	h.app.cfgMu.Lock()
	h.app.desired = off
	h.app.cfgMu.Unlock()
	h.app.ReconcileSync()

	var stopped, running int
	for _, w := range wins {
		if w.closed.Load() {
			stopped++
		} else {
			running++
		}
	}
	if stopped != 1 || running != 1 {
		t.Fatalf("disabling one profile must close exactly its own window: stopped=%d running=%d", stopped, running)
	}
	if h.binder.isAttached("second", domain.PlatformKick) || h.binder.isAttached("second", domain.PlatformTwitch) {
		t.Fatal("a disabled profile's runtimes must be detached")
	}
}

func TestSeparateWindowFallbackLaunchesTwoInstances(t *testing.T) {
	cfg := testConfig(t, config.MainProfileName)
	cfg.Kick.Enabled = true
	cfg.Kick.Browser.SeparateWindow = true
	h := newBrowserHarness(t, cfg)
	h.app.ReconcileSync()

	if got := len(h.windowList()); got != 0 {
		t.Fatalf("separate_window must not use the shared-window path: %d windows", got)
	}
	dirs := h.launchedDirs()
	if len(dirs) != 2 {
		t.Fatalf("separate_window must launch one chrome per platform, got %d: %v", len(dirs), dirs)
	}
	if dirs[0] == dirs[1] {
		t.Fatalf("the two instances must use different profile dirs, both %q", dirs[0])
	}
	for _, plat := range []domain.Platform{domain.PlatformTwitch, domain.PlatformKick} {
		if !h.binder.isAttached(config.MainProfileName, plat) {
			t.Fatalf("%s runtime should be attached in separate_window mode", plat)
		}
	}
}

func TestSharedModeKickNeedsLoginOnItsFirstStart(t *testing.T) {
	cfg := testConfig(t, config.MainProfileName)
	cfg.Profile(config.MainProfileName).Kick.Enabled = boolPtr(true)
	h := newBrowserHarness(t, cfg)

	// A pre-existing chrome-profile-kick login does NOT carry into the shared
	// window: kick must still surface needsLogin on its first shared start.
	paths, err := cfg.ProfilePaths(config.MainProfileName)
	if err != nil {
		t.Fatalf("ProfilePaths: %v", err)
	}
	if err := os.MkdirAll(paths.ChromeProfileKick, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(paths.ChromeProfileKick, "Local State"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}

	h.app.ReconcileSync()
	h.binder.mu.Lock()
	first := h.binder.needsLogin[pair{config.MainProfileName, domain.PlatformKick}]
	h.binder.mu.Unlock()
	if !first {
		t.Fatal("the first shared-window kick start must report needsLogin")
	}
	if _, err := os.Stat(filepath.Join(paths.Dir, ".dw-kick-login-pending")); err != nil {
		t.Fatalf("an unanswered prompt must be persisted as the pending marker: %v", err)
	}
	if _, err := os.Stat(filepath.Join(paths.Dir, ".dw-kick-initialized")); err == nil {
		t.Fatal("a first start must NOT mark the session initialized before the login is answered")
	}

	// The prompt was never answered, so a restart of the runtime re-prompts:
	// quitting the app before logging in must not silently drop the prompt.
	restartKick := func() {
		off := clone(cfg)
		off.Profile(config.MainProfileName).Kick.Enabled = boolPtr(false)
		h.app.cfgMu.Lock()
		h.app.desired = off
		h.app.cfgMu.Unlock()
		h.app.ReconcileSync()
		h.app.cfgMu.Lock()
		h.app.desired = cfg
		h.app.cfgMu.Unlock()
		h.app.ReconcileSync()
	}
	restartKick()
	h.binder.mu.Lock()
	second := h.binder.needsLogin[pair{config.MainProfileName, domain.PlatformKick}]
	h.binder.mu.Unlock()
	if !second {
		t.Fatal("an unanswered prompt must survive a runtime restart")
	}

	// Answering it (login-check success or a manual Start) persists the ack;
	// only then does a restart stop re-prompting.
	h.app.MarkSessionReady(config.MainProfileName, domain.PlatformKick)
	restartKick()
	h.binder.mu.Lock()
	third := h.binder.needsLogin[pair{config.MainProfileName, domain.PlatformKick}]
	h.binder.mu.Unlock()
	if third {
		t.Fatal("an answered prompt must not come back")
	}
}

func TestPlatformFirstRunRules(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "profile")
	chromeDir := filepath.Join(dataDir, "chrome-profile")
	if err := os.MkdirAll(chromeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(chromeDir, "Local State"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Twitch owns its chrome dir in both layouts, so with no pending prompt a
	// used dir is grandfathered as logged in.
	if platformFirstRun(dataDir, chromeDir, domain.PlatformTwitch, true) {
		t.Fatal("a used twitch chrome dir is not a first run")
	}
	// An unanswered prompt beats every other signal, used dir included.
	markLoginPending(dataDir, domain.PlatformTwitch, zerolog.Nop())
	if !platformFirstRun(dataDir, chromeDir, domain.PlatformTwitch, true) {
		t.Fatal("a pending prompt must survive the chrome dir becoming used")
	}
	markPlatformStarted(dataDir, domain.PlatformTwitch, zerolog.Nop())
	if platformFirstRun(dataDir, chromeDir, domain.PlatformTwitch, true) {
		t.Fatal("answering the prompt must clear the pending marker")
	}
	// Shared-mode kick has no dir of its own: only its markers answer.
	if !platformFirstRun(dataDir, chromeDir, domain.PlatformKick, true) {
		t.Fatal("shared kick with no marker is a first run")
	}
	markPlatformStarted(dataDir, domain.PlatformKick, zerolog.Nop())
	if platformFirstRun(dataDir, chromeDir, domain.PlatformKick, true) {
		t.Fatal("shared kick with an initialized marker is not a first run")
	}
	// Separate mode still keys on the platform's own chrome dir, marker or not.
	if !platformFirstRun(dataDir, filepath.Join(dataDir, "chrome-profile-kick"), domain.PlatformKick, false) {
		t.Fatal("separate-mode kick keys on its own (missing) chrome dir")
	}
}

func TestPausedConfigDisablesEnabledOnStart(t *testing.T) {
	cfg, _ := config.StarterDefaults()
	cfg.Watcher.EnabledOnStart = true
	paused := pausedConfig(cfg)
	if paused.Watcher.EnabledOnStart {
		t.Fatal("a first-ever launch must start the engine paused")
	}
	if !cfg.Watcher.EnabledOnStart {
		t.Fatal("pausedConfig must not mutate the caller's config")
	}
}
