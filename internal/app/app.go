// Package app assembles the dropwarden object graph from configuration: config,
// logging, and every enabled profile's SQLite store are opened eagerly; the
// browsers, controllers, discoverers, and watcher engines are built on demand by
// StartRuntime (which launches Chrome). Both the CLI and the Wails app build on
// this. The GUI additionally drives the runtime through Reconcile (reconcile.go),
// which starts and stops (profile, platform) runtimes while the app runs.
package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/rs/zerolog"

	"github.com/rake-pro/dropwarden/internal/browser"
	"github.com/rake-pro/dropwarden/internal/config"
	"github.com/rake-pro/dropwarden/internal/discovery"
	"github.com/rake-pro/dropwarden/internal/domain"
	"github.com/rake-pro/dropwarden/internal/kick"
	"github.com/rake-pro/dropwarden/internal/logging"
	"github.com/rake-pro/dropwarden/internal/store"
	"github.com/rake-pro/dropwarden/internal/twitch"
	"github.com/rake-pro/dropwarden/internal/watcher"
)

// PlatformRuntime is one (profile, platform) runtime: its own Chrome, page
// driver, discoverer, and watcher engine, plus the lifecycle handles that let
// the reconciler tear it down without restarting the app.
type PlatformRuntime struct {
	Session browser.Session
	Pages   watcher.Pager
	Disco   watcher.Discoverer
	Engine  *watcher.Engine

	// NeedsLogin is true when this runtime came up on a (profile, platform)
	// session nobody has ever logged in on, so the engine starts paused and the
	// UI prompts for the manual login.
	NeedsLogin bool

	// group is the profile's SHARED Chrome window when this runtime is a tab in
	// it (the default layout); nil in separate_window mode, where Session is a
	// Managed owning its own Chrome. window is this runtime's handle on the
	// coordinator that owns that window's minimize/restore state.
	group  *browser.Group
	window *browser.WindowHandle

	// cancel stops this runtime's engine goroutine; done is closed when that
	// goroutine has returned. Both are nil for a runtime built by StartRuntime
	// (the CLI runs the engine itself).
	cancel context.CancelFunc
	done   chan struct{}
}

// ProfileRuntime is one person's runtime: their own store (opened eagerly) and
// one PlatformRuntime per platform they run. Kick is nil unless kick is enabled
// for that profile; Twitch is nil until the runtime brings Chrome up.
type ProfileRuntime struct {
	Name  string
	Log   zerolog.Logger
	Store *store.Store

	Twitch *PlatformRuntime
	Kick   *PlatformRuntime

	// browser is the profile's SHARED Chrome window: ONE instance hosting every
	// enabled platform's tab (Twitch owns it, Kick gets a second tab). nil in
	// separate_window mode, where each platform launches its own Chrome.
	// window coordinates that window's minimize/restore across the tabs.
	// Both are guarded by App.mu.
	browser *browser.Group
	window  *browser.WindowCoordinator
}

// platform returns the profile's runtime slot for a platform.
func (pr *ProfileRuntime) platform(p domain.Platform) *PlatformRuntime {
	if p.OrTwitch() == domain.PlatformKick {
		return pr.Kick
	}
	return pr.Twitch
}

// setPlatform assigns (or clears) the profile's runtime slot for a platform.
func (pr *ProfileRuntime) setPlatform(p domain.Platform, rt *PlatformRuntime) {
	if p.OrTwitch() == domain.PlatformKick {
		pr.Kick = rt
		return
	}
	pr.Twitch = rt
}

// App holds the eagerly-opened resources.
type App struct {
	// Cfg is the config this process STARTED with. After StartLive, settings
	// saves swap in a newer one: read the live config via Config().
	Cfg        *config.Config
	ConfigPath string
	Log        zerolog.Logger

	// mu guards Profiles and every runtime slot inside it. The HTTP handlers
	// reconcile concurrently with the running app.
	mu sync.Mutex
	// Profiles are the profiles this process has runtimes (or at least stores)
	// for, in the order they came up. Read it via ProfileList under the lock.
	Profiles []*ProfileRuntime

	// intents is the desired RUN state per (profile, platform): whether its
	// Chrome should be up, and if so whether it is watching or parked on the
	// login page. An absent entry has never been seeded; reconcileOnce fills the
	// boot default. A user Start/Stop/Login overwrites it live. Guarded by mu.
	intents map[intentKey]runIntent

	// seedWatchAll makes seedIntent default every enabled platform to watch,
	// short-circuiting the enabled-on-start + first-run-login policy. It is a
	// test seam so the reconcile-mechanics tests can exercise launch/stop plumbing
	// without staging logged-in Chrome dirs; production leaves it false.
	seedWatchAll bool

	// cfgMu guards desired, the config the reconciler works from.
	cfgMu   sync.RWMutex
	desired *config.Config

	// Live runtime wiring, set by StartLive.
	liveCtx    context.Context
	emit       watcher.Emitter
	binder     Binder
	startPlat  startFunc
	bootedOnce bool

	// lastEngineStart is the wall-clock time of the most recently started
	// engine (boot or live), guarded by mu. It feeds liveStartStagger (see
	// reconcile.go): a live start landing soon after another gets a randomized
	// stagger instead of firing its discovery burst instantly.
	lastEngineStart time.Time

	// nowFn and staggerRand are injectable for deterministic reconcile tests;
	// nil uses time.Now / math/rand's default source.
	nowFn       func() time.Time
	staggerRand func() float64

	// openWindow and launchSession are the two browser entry points, as fields
	// so the window lifecycle (one Chrome per profile with a tab per platform,
	// versus one Chrome per platform) is unit-testable without Chrome. Both are
	// nil in production, where the real browser package functions run.
	openWindow    func(ctx context.Context, bcfg config.BrowserConfig, profileDir string, dimOpacity float64, log zerolog.Logger) (browser.WindowHost, error)
	launchSession func(ctx context.Context, bcfg config.BrowserConfig, profileDir string, dimOpacity float64, log zerolog.Logger) (browser.Session, error)

	// recMu serializes reconcile passes; rerun coalesces a trigger that lands
	// mid-pass into exactly one trailing pass.
	recMu       sync.Mutex
	reconciling bool
	rerun       bool
}

// Options tune assembly.
type Options struct {
	Console bool // pretty console logging (CLI)
}

// Open loads config, sets up logging, and opens every enabled profile's store.
// It does NOT launch Chrome, so it is safe for commands like doctor and report.
func Open(opts Options) (*App, error) {
	cfg, path, err := config.LoadOrInit()
	if err != nil {
		return nil, err
	}
	logPath, _ := config.LogPath()
	logger, err := logging.Setup(logging.Options{
		Level:     cfg.Logging.Level,
		File:      cfg.Logging.File,
		LogPath:   logPath,
		MaxSizeMB: cfg.Logging.MaxSizeMB,
		Console:   opts.Console,
	})
	if err != nil {
		return nil, err
	}
	a := &App{Cfg: cfg, ConfigPath: path, Log: logger, desired: cfg, intents: map[intentKey]runIntent{}}
	for _, p := range cfg.Profiles {
		if !p.Enabled {
			continue
		}
		plog := logger.With().Str("profile", p.Name).Logger()
		st, err := openProfileStore(cfg, p.Name)
		if err != nil {
			// One profile's unusable store must not take down the others; a
			// single-profile install still fails via the empty check below.
			plog.Error().Err(err).Msg("app: open profile store failed; skipping this profile")
			continue
		}
		a.Profiles = append(a.Profiles, &ProfileRuntime{Name: p.Name, Log: plog, Store: st})
	}
	if len(a.Profiles) == 0 {
		return nil, fmt.Errorf("app: no enabled profiles in %s", path)
	}
	return a, nil
}

// Config returns the live config: the one this process started with until a
// settings save reconciles a newer one in.
func (a *App) Config() *config.Config {
	a.cfgMu.RLock()
	defer a.cfgMu.RUnlock()
	return a.desired
}

// openProfileStore creates a profile's data directory if needed and opens its
// own SQLite database.
func openProfileStore(cfg *config.Config, name string) (*store.Store, error) {
	pp, err := cfg.ProfilePaths(name)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(pp.DB), 0o700); err != nil {
		return nil, fmt.Errorf("create profile dir: %w", err)
	}
	return store.Open(pp.DB)
}

// Main returns the first opened profile runtime: the only one in a
// single-profile install, and what the CLI harness operates on.
func (a *App) Main() *ProfileRuntime {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.Profiles) == 0 {
		return nil
	}
	return a.Profiles[0]
}

// Profile returns the named profile runtime, or nil.
func (a *App) Profile(name string) *ProfileRuntime {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.profileLocked(name)
}

func (a *App) profileLocked(name string) *ProfileRuntime {
	for _, p := range a.Profiles {
		if p.Name == name {
			return p
		}
	}
	return nil
}

// StartRuntime launches each profile's Chrome instances and builds their
// controllers, page drivers, discoverers, and watcher engines. emit receives
// engine events from every engine. One profile's launch failure is logged and
// that profile is skipped: a broken profile must never take down the others.
// It errors only when NO profile came up.
//
// This is the CLI entry point: it builds the runtimes but never runs their
// engine loops (the CLI drives one engine itself). The GUI uses StartLive.
func (a *App) StartRuntime(ctx context.Context, emit watcher.Emitter) error {
	a.cfgMu.Lock()
	a.emit = emit
	a.cfgMu.Unlock()
	started := 0
	for idx, pr := range a.Profiles {
		if err := a.startProfileRuntime(ctx, pr, idx); err != nil {
			pr.Log.Error().Err(err).Msg("app: profile runtime failed to start; skipping this profile")
			continue
		}
		started++
	}
	if started == 0 {
		return fmt.Errorf("app: no profile runtime could be started")
	}
	return nil
}

// startProfileRuntime brings up one profile's Twitch runtime (required) and its
// Kick runtime (additive: a Kick failure leaves the profile Twitch-only). idx
// is the profile's position in a.Profiles (0 for the first-started profile),
// which seeds both of its engines' first-discovery stagger.
func (a *App) startProfileRuntime(ctx context.Context, pr *ProfileRuntime, idx int) error {
	rt, err := a.startPlatformRuntime(ctx, pr, domain.PlatformTwitch, discoveryStagger(idx))
	if err != nil {
		return err
	}
	pr.Twitch = rt
	eff, err := a.Config().Effective(pr.Name)
	if err != nil {
		return err
	}
	if eff.KickEnabled {
		krt, err := a.startPlatformRuntime(ctx, pr, domain.PlatformKick, discoveryStagger(idx))
		if err != nil {
			// Kick is additive: a Kick launch failure must never take down the
			// working Twitch runtime.
			pr.Log.Error().Err(err).Msg("app: kick runtime failed to start; continuing twitch-only")
		} else {
			pr.Kick = krt
		}
	}
	return nil
}

// discoveryStaggerStep spaces each profile's engines' first discovery pass so
// N profiles do not burst their platform's GQL/API fetches simultaneously at
// boot; the first-started profile (idx 0) keeps today's un-staggered timing.
const discoveryStaggerStep = 90 * time.Second

// discoveryStagger returns the first-discovery stagger for the idx-th started
// profile (its position in a.Profiles): 0 for the first profile, one more
// discoveryStaggerStep per profile after that.
func discoveryStagger(idx int) time.Duration {
	return time.Duration(idx) * discoveryStaggerStep
}

// startFunc builds one (profile, platform) runtime. It is a field on App so the
// reconciler can be unit-tested without Chrome.
type startFunc func(ctx context.Context, pr *ProfileRuntime, plat domain.Platform, stagger time.Duration) (*PlatformRuntime, error)

// startPlatformRuntime launches one (profile, platform) Chrome and builds its
// engine, through the injectable seam when one is installed.
func (a *App) startPlatformRuntime(ctx context.Context, pr *ProfileRuntime, plat domain.Platform, stagger time.Duration) (*PlatformRuntime, error) {
	if a.startPlat != nil {
		return a.startPlat(ctx, pr, plat, stagger)
	}
	if plat.OrTwitch() == domain.PlatformKick {
		return a.startKickRuntime(ctx, pr, stagger)
	}
	return a.startTwitchRuntime(ctx, pr, stagger)
}

// chromeDirFresh reports whether a Chrome user-data dir has never been used:
// missing, or present but empty (AddProfile pre-creates the directories). A
// fresh dir means nobody has logged in on that (profile, platform) session yet,
// so its engine must come up paused with a needs-login prompt instead of
// silently watching nothing.
func chromeDirFresh(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return true
	}
	return len(entries) == 0
}

// platformMarker is the per-(profile, platform) first-run marker inside the
// profile's data dir. The Chrome-dir freshness rule cannot answer for a platform
// that does not own a Chrome dir: in the shared-window layout Kick is a tab in
// the Twitch browser, whose dir stops being fresh the moment Twitch launches.
// The marker is written once, when that platform's runtime first comes up.
func platformMarker(dataDir string, plat domain.Platform) string {
	return filepath.Join(dataDir, ".dw-"+string(plat.OrTwitch())+"-initialized")
}

// pendingLoginMarker is the sibling marker that records a needs-login prompt
// the user has NOT yet answered. Without it, quitting the app before logging in
// would silently drop the prompt on the next boot (the Chrome dir stops being
// fresh the moment Chrome launches, logged in or not) and the engine would
// watch nothing while logged out.
func pendingLoginMarker(dataDir string, plat domain.Platform) string {
	return filepath.Join(dataDir, ".dw-"+string(plat.OrTwitch())+"-login-pending")
}

// platformFirstRun reports whether this (profile, platform) still needs its
// first manual login.
//
// An unanswered needs-login prompt (the pending marker) survives restarts and
// always wins. Otherwise: Twitch (and either platform in separate_window mode)
// owns its own Chrome user-data dir, so freshness of that dir is the signal -
// a non-fresh dir with no pending marker is a session from before the marker
// scheme, grandfathered as logged in. Shared-mode Kick has no dir of its own,
// so only its initialized marker answers - which also means an existing
// chrome-profile-kick login does NOT carry into shared mode: the first shared
// start reports needsLogin.
func platformFirstRun(dataDir, chromeDir string, plat domain.Platform, shared bool) bool {
	if _, err := os.Stat(pendingLoginMarker(dataDir, plat)); err == nil {
		return true
	}
	if shared && plat.OrTwitch() == domain.PlatformKick {
		_, err := os.Stat(platformMarker(dataDir, plat))
		return err != nil
	}
	return chromeDirFresh(chromeDir)
}

// markPlatformStarted records that a (profile, platform) session is good to go:
// its next start is not treated as a first run. Written when a runtime comes up
// already past its first login, and by MarkSessionReady when the user answers
// the needs-login prompt. Best effort: a marker we failed to write only costs
// one extra needs-login prompt.
func markPlatformStarted(dataDir string, plat domain.Platform, log zerolog.Logger) {
	writeMarker(platformMarker(dataDir, plat), dataDir, log)
	if err := os.Remove(pendingLoginMarker(dataDir, plat)); err != nil && !os.IsNotExist(err) {
		log.Warn().Err(err).Msg("app: could not remove the pending-login marker")
	}
}

// markLoginPending records an unanswered needs-login prompt so it survives an
// app restart (see pendingLoginMarker).
func markLoginPending(dataDir string, plat domain.Platform, log zerolog.Logger) {
	writeMarker(pendingLoginMarker(dataDir, plat), dataDir, log)
}

func writeMarker(path, dataDir string, log zerolog.Logger) {
	if _, err := os.Stat(path); err == nil {
		return
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		log.Warn().Err(err).Str("path", path).Msg("app: could not create the profile data dir for a marker")
		return
	}
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		log.Warn().Err(err).Str("path", path).Msg("app: could not write a marker")
	}
}

// openLoginPage points a first-run session's tab at the platform's site so the
// user can actually log in: the engine starts PAUSED on a first run and would
// otherwise never navigate, leaving the welcome flow staring at about:blank
// (Kick especially, which has no login probe to navigate for it). Best effort;
// the user can always navigate the tab by hand.
func openLoginPage(ctx context.Context, sess browser.Session, url string, log zerolog.Logger) {
	if err := sess.Navigate(ctx, url); err != nil {
		log.Warn().Err(err).Str("url", url).Msg("app: could not open the login page in the watcher tab")
	}
}

// MarkSessionReady persists the answer to a needs-login prompt (the login probe
// came back authenticated, or the user pressed Start): the pending marker goes
// away and the session is recorded as initialized, so the prompt does not come
// back on the next boot. The service layer calls this when it clears the
// in-memory needsLogin flag.
func (a *App) MarkSessionReady(profile string, plat domain.Platform) {
	dataDir, err := a.profileDataDir(profile)
	if err != nil {
		a.Log.Warn().Err(err).Str("profile", profile).Msg("app: could not resolve the data dir to persist a login ack")
		return
	}
	markPlatformStarted(dataDir, plat, a.Log.With().Str("profile", profile).Logger())
}

// launchWindow opens one shared Chrome window, through the injectable seam when
// one is installed.
func (a *App) launchWindow(ctx context.Context, bcfg config.BrowserConfig, profileDir string, dimOpacity float64, log zerolog.Logger) (browser.WindowHost, error) {
	if a.openWindow != nil {
		return a.openWindow(ctx, bcfg, profileDir, dimOpacity, log)
	}
	inst, err := browser.LaunchInstance(ctx, bcfg, profileDir, dimOpacity, log)
	if err != nil {
		return nil, err
	}
	return inst, nil
}

// launchBrowser opens one platform's OWN Chrome (separate_window mode), through
// the injectable seam when one is installed.
func (a *App) launchBrowser(ctx context.Context, bcfg config.BrowserConfig, profileDir string, dimOpacity float64, log zerolog.Logger) (browser.Session, error) {
	if a.launchSession != nil {
		return a.launchSession(ctx, bcfg, profileDir, dimOpacity, log)
	}
	return browser.LaunchWith(ctx, bcfg, profileDir, dimOpacity, log)
}

// sharedWindow reports the profile window layout: one Chrome per PROFILE with
// Kick as a tab in the Twitch window (the default), or the legacy one Chrome per
// (profile, platform).
func (a *App) sharedWindow() bool { return !a.Config().KickSeparateWindow() }

// profileDataDir resolves a profile's data directory (where its first-run
// markers live).
func (a *App) profileDataDir(name string) (string, error) {
	pp, err := a.Config().ProfilePaths(name)
	if err != nil {
		return "", err
	}
	return pp.Dir, nil
}

// ensureProfileGroup returns the profile's shared Chrome window, launching it
// (and its window coordinator) the first time a platform asks for it. The launch
// closure is what the group re-invokes on every relaunch, so the window policy
// is reapplied to the replacement browser too.
func (a *App) ensureProfileGroup(ctx context.Context, pr *ProfileRuntime, pcfg *config.Config, profileDir string) (*browser.Group, *browser.WindowCoordinator, error) {
	a.mu.Lock()
	g, w := pr.browser, pr.window
	a.mu.Unlock()
	if g != nil && !g.Closed() {
		return g, w, nil
	}

	log := pr.Log.With().Str("window", "shared").Logger()
	open := func(lctx context.Context) (browser.WindowHost, error) {
		host, err := a.launchWindow(lctx, pcfg.Browser, profileDir, pcfg.Browser.Burnin.DimOpacity, log)
		if err != nil {
			return nil, err
		}
		if pcfg.Browser.Window.StartMinimized {
			if err := host.SetMinimized(lctx, true); err != nil {
				log.Warn().Err(err).Msg("app: minimize watcher window failed")
			} else {
				log.Info().Msg("app: watcher window minimized")
			}
		}
		return host, nil
	}

	g, err := browser.NewGroup(ctx, open, log)
	if err != nil {
		return nil, nil, fmt.Errorf("app: launch browser: %w", err)
	}
	w = browser.NewWindowCoordinator(pcfg.Browser.Window.StartMinimized, log)
	w.SetAlwaysOnBottom(pcfg.Browser.Window.AlwaysOnBottom)
	a.mu.Lock()
	pr.browser, pr.window = g, w
	a.mu.Unlock()
	return g, w, nil
}

// pausedConfig returns cp with enabled_on_start forced off, so watcher.New
// builds the engine paused. Used for a first-ever launch of a session, where
// the user must log in before watching starts.
func pausedConfig(cp *config.Config) *config.Config {
	paused := *cp
	paused.Watcher.EnabledOnStart = false
	return &paused
}

// startTwitchRuntime brings up the profile's Twitch session and engine. Twitch
// always drives the profile's FIRST tab: in the shared layout it owns the
// profile's one Chrome window (so stopping it stops the whole window, Kick
// included), and in separate_window mode it owns a Chrome of its own.
func (a *App) startTwitchRuntime(ctx context.Context, pr *ProfileRuntime, stagger time.Duration) (*PlatformRuntime, error) {
	cfg := a.Config()
	pcfg, err := cfg.ProfileRuntimeConfig(pr.Name)
	if err != nil {
		return nil, err
	}
	profileDir, _, err := a.chromeDirs(pr.Name)
	if err != nil {
		return nil, err
	}
	dataDir, err := a.profileDataDir(pr.Name)
	if err != nil {
		return nil, err
	}
	shared := a.sharedWindow()
	needsLogin := platformFirstRun(dataDir, profileDir, domain.PlatformTwitch, shared)
	if in, ok := a.intentOf(pr.Name, domain.PlatformTwitch); ok {
		// The GUI drives login-vs-watch through the run intent; the filesystem
		// first-run check is only the CLI's fallback, where nothing seeds one.
		needsLogin = in == intentLogin
	}
	log := pr.Log.With().Str("platform", "twitch").Logger()

	// initTab is what makes a raw tab Twitch-ready. The supervisor re-runs it on
	// every rebuilt tab, so an overnight Chrome crash comes back with the GQL
	// capture shim installed instead of silently capturing nothing.
	initTab := func(ictx context.Context, s browser.Session) error {
		if err := twitch.InstallCapture(ictx, s); err != nil {
			log.Warn().Err(err).Msg("app: install capture shim failed")
		}
		return nil
	}

	var (
		sess  browser.Session
		group *browser.Group
		coord *browser.WindowCoordinator
	)
	if shared {
		g, w, err := a.ensureProfileGroup(ctx, pr, pcfg, profileDir)
		if err != nil {
			return nil, err
		}
		s, err := g.Session(ctx, string(domain.PlatformTwitch), initTab)
		if err != nil {
			return nil, fmt.Errorf("app: open twitch tab: %w", err)
		}
		group, sess, coord = g, s, w
	} else {
		// The launch closure fully initializes a Twitch-ready session; the
		// Managed wrapper re-invokes it whenever Chrome dies (CDP disconnect,
		// crash), so an overnight browser crash recovers instead of
		// retry-looping on a dead socket forever.
		launch := func(lctx context.Context) (browser.Session, error) {
			s, err := a.launchBrowser(lctx, pcfg.Browser, profileDir, pcfg.Browser.Burnin.DimOpacity, log)
			if err != nil {
				return nil, err
			}
			_ = initTab(lctx, s)
			if pcfg.Browser.Window.StartMinimized {
				if err := s.SetMinimized(lctx, true); err != nil {
					log.Warn().Err(err).Msg("app: minimize watcher window failed")
				} else {
					log.Info().Msg("app: watcher window minimized")
				}
			}
			return s, nil
		}
		s0, err := launch(ctx)
		if err != nil {
			return nil, fmt.Errorf("app: launch browser: %w", err)
		}
		sess = browser.NewManaged(s0, launch, log)
		coord = browser.NewWindowCoordinator(pcfg.Browser.Window.StartMinimized, log)
		coord.SetAlwaysOnBottom(pcfg.Browser.Window.AlwaysOnBottom)
	}

	// A first run persists the unanswered prompt instead of marking the session
	// initialized: quitting before logging in must re-prompt on the next boot.
	if needsLogin {
		markLoginPending(dataDir, domain.PlatformTwitch, log)
		openLoginPage(ctx, sess, twitch.URLBase, log)
	} else {
		markPlatformStarted(dataDir, domain.PlatformTwitch, log)
	}
	ctrl := twitch.NewController(sess)
	pages := twitch.NewPages(ctrl)
	disco := discovery.New(pr.Store, pages, pcfg, log)
	ecfg := pcfg
	if needsLogin {
		ecfg = pausedConfig(pcfg)
		log.Info().Msg("app: first run for this session; engine starts paused pending manual login")
	}
	win := coord.Register(string(domain.PlatformTwitch), sess)
	return &PlatformRuntime{
		Session:    sess,
		Pages:      pages,
		Disco:      disco,
		NeedsLogin: needsLogin,
		group:      group,
		window:     win,
		Engine: watcher.New(watcher.Deps{
			Store: pr.Store, Ctrl: ctrl, Pages: pages, Disco: disco,
			Cfg: ecfg, Emit: a.emitter(), Log: log, Window: win,
			Platform: domain.PlatformTwitch, ChannelHost: "twitch.tv", Profile: pr.Name,
			DiscoveryStagger: stagger,
		}),
	}, nil
}

// startKickRuntime brings up the profile's Kick session and engine. In the
// shared layout that is a second TAB in the profile's live Twitch window (no
// relaunch, and no second Chrome); in separate_window mode it is a second Chrome
// instance on its own profile dir. The Kick engine always runs on the profile's
// derived kick config, never the root config.
func (a *App) startKickRuntime(ctx context.Context, pr *ProfileRuntime, stagger time.Duration) (*PlatformRuntime, error) {
	cfg := a.Config()
	kcfg, err := cfg.ProfileKickRuntimeConfig(pr.Name)
	if err != nil {
		return nil, err
	}
	twitchDir, profileDir, err := a.chromeDirs(pr.Name)
	if err != nil {
		return nil, err
	}
	dataDir, err := a.profileDataDir(pr.Name)
	if err != nil {
		return nil, err
	}
	shared := a.sharedWindow()
	needsLogin := platformFirstRun(dataDir, profileDir, domain.PlatformKick, shared)
	if in, ok := a.intentOf(pr.Name, domain.PlatformKick); ok {
		// The GUI drives login-vs-watch through the run intent; the filesystem
		// first-run check is only the CLI's fallback, where nothing seeds one.
		needsLogin = in == intentLogin
	}
	klog := pr.Log.With().Str("platform", "kick").Logger()

	var (
		sess  browser.Session
		group *browser.Group
		coord *browser.WindowCoordinator
	)
	if shared {
		// The shared window is the PROFILE's one Chrome (Twitch's config and
		// user-data dir - Kick rides the same session as a tab). It is
		// reference-counted by its tabs, so Kick can bring it up itself when
		// Twitch is not running rather than depend on Twitch having opened it.
		pcfg, err := cfg.ProfileRuntimeConfig(pr.Name)
		if err != nil {
			return nil, err
		}
		g, w, err := a.ensureProfileGroup(ctx, pr, pcfg, twitchDir)
		if err != nil {
			return nil, fmt.Errorf("app: bring up the profile window for kick: %w", err)
		}
		group, coord = g, w
		if sess, err = group.Session(ctx, string(domain.PlatformKick), nil); err != nil {
			return nil, fmt.Errorf("app: open kick tab: %w", err)
		}
	} else {
		launch := func(lctx context.Context) (browser.Session, error) {
			s, err := a.launchBrowser(lctx, kcfg.Browser, profileDir, kcfg.Browser.Burnin.DimOpacity, klog)
			if err != nil {
				return nil, err
			}
			if kcfg.Browser.Window.StartMinimized {
				if err := s.SetMinimized(lctx, true); err != nil {
					klog.Warn().Err(err).Msg("app: minimize kick window failed")
				}
			}
			return s, nil
		}
		s0, err := launch(ctx)
		if err != nil {
			return nil, fmt.Errorf("app: launch kick browser: %w", err)
		}
		sess = browser.NewManaged(s0, launch, klog)
		coord = browser.NewWindowCoordinator(kcfg.Browser.Window.StartMinimized, klog)
		coord.SetAlwaysOnBottom(kcfg.Browser.Window.AlwaysOnBottom)
	}

	// Same pending/initialized split as the Twitch path above.
	if needsLogin {
		markLoginPending(dataDir, domain.PlatformKick, klog)
		openLoginPage(ctx, sess, kick.URLBase, klog)
	} else {
		markPlatformStarted(dataDir, domain.PlatformKick, klog)
	}
	ctrl := kick.NewController(sess)
	pages := kick.NewPages(ctrl)
	disco := kick.NewDiscoverer(pr.Store, pages, kcfg, klog)
	ecfg := kcfg
	if needsLogin {
		ecfg = pausedConfig(kcfg)
		klog.Info().Msg("app: first run for this session; engine starts paused pending manual login")
	}
	klog.Info().Bool("shared_window", shared).Msg("app: kick runtime up")
	win := coord.Register(string(domain.PlatformKick), sess)
	return &PlatformRuntime{
		Session:    sess,
		Pages:      pages,
		Disco:      disco,
		NeedsLogin: needsLogin,
		group:      group,
		window:     win,
		Engine: watcher.New(watcher.Deps{
			Store: pr.Store, Ctrl: ctrl, Pages: pages, Disco: disco,
			Cfg: ecfg, Emit: a.emitter(), Log: klog, Window: win,
			Platform: domain.PlatformKick, ChannelHost: "kick.com", Profile: pr.Name,
			DiscoveryStagger: stagger,
		}),
	}, nil
}

// chromeDirs resolves a profile's two Chrome user-data dirs. The browser
// profile_dir overrides are single-instance settings, so they apply to the main
// profile only; every other profile uses its own directory layout.
func (a *App) chromeDirs(name string) (string, string, error) {
	cfg := a.Config()
	if name == config.MainProfileName {
		twitchDir, err := cfg.ProfileDir()
		if err != nil {
			return "", "", err
		}
		kickDir, err := cfg.KickProfileDir()
		if err != nil {
			return "", "", err
		}
		return twitchDir, kickDir, nil
	}
	pp, err := cfg.ProfilePaths(name)
	if err != nil {
		return "", "", err
	}
	return pp.ChromeProfile, pp.ChromeProfileKick, nil
}

// Engines returns every engine that should run, profile by profile (twitch
// first, then kick).
func (a *App) Engines() []*watcher.Engine {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []*watcher.Engine
	for _, pr := range a.Profiles {
		for _, plat := range []*PlatformRuntime{pr.Twitch, pr.Kick} {
			if plat != nil && plat.Engine != nil {
				out = append(out, plat.Engine)
			}
		}
	}
	return out
}

// Close releases runtime and eager resources: every engine goroutine is stopped
// and every Chrome and store closed.
func (a *App) Close() {
	a.mu.Lock()
	profiles := append([]*ProfileRuntime(nil), a.Profiles...)
	a.Profiles = nil
	a.mu.Unlock()
	for _, pr := range profiles {
		for _, plat := range platformStopOrder {
			if rt := pr.platform(plat); rt != nil {
				a.teardown(pr, plat, rt)
			}
		}
		if pr.Store != nil {
			_ = pr.Store.Close()
		}
	}
}
