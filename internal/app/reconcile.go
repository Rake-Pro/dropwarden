package app

import (
	"context"
	"time"

	"github.com/rake-pro/dropwarden/internal/browser"
	"github.com/rake-pro/dropwarden/internal/config"
	"github.com/rake-pro/dropwarden/internal/domain"
	"github.com/rake-pro/dropwarden/internal/store"
	"github.com/rake-pro/dropwarden/internal/watcher"
)

// platformOrder is the canonical order runtimes are STARTED in: Twitch first,
// because in the shared-window layout it opens the profile's Chrome and Kick
// only gets a tab in it.
var platformOrder = []domain.Platform{domain.PlatformTwitch, domain.PlatformKick}

// platformStopOrder is the reverse: tabs go before the window they live in, so
// Kick's engine is never left driving a browser Twitch already closed.
var platformStopOrder = []domain.Platform{domain.PlatformKick, domain.PlatformTwitch}

// engineStopTimeout bounds how long a stop waits for an engine goroutine to
// return before closing its browser anyway. A wedged engine must not hold up a
// profile toggle forever.
const engineStopTimeout = 10 * time.Second

// runIntent is the desired lifecycle of one (profile, platform). Chrome exists
// only for a runtime whose intent is not off: launching a browser nobody is
// watching (or logging into) is pointless, so the reconciler brings a window up
// exactly when the intent asks for one and tears it down the moment it does not.
type runIntent int

const (
	intentOff   runIntent = iota // no Chrome, no engine
	intentWatch                  // Chrome up, engine watching the queue
	intentLogin                  // Chrome up on the login page, engine parked
)

// intentKey identifies one (profile, platform) intent. The platform is always
// normalized through OrTwitch so an empty platform never forks a second entry.
type intentKey struct {
	profile string
	plat    domain.Platform
}

// intentOf returns the recorded intent for a (profile, platform) and whether one
// has been seeded yet.
func (a *App) intentOf(profile string, plat domain.Platform) (runIntent, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	in, ok := a.intents[intentKey{profile, plat.OrTwitch()}]
	return in, ok
}

// setIntent records a (profile, platform) intent. Callers reconcile afterwards.
func (a *App) setIntent(profile string, plat domain.Platform, in runIntent) {
	a.mu.Lock()
	if a.intents == nil {
		a.intents = map[intentKey]runIntent{}
	}
	a.intents[intentKey{profile, plat.OrTwitch()}] = in
	a.mu.Unlock()
}

// StartWatching intends a (profile, platform) to watch. If its runtime is
// already up (a login-mode window the user just finished logging into) the
// engine is unpaused in place; otherwise the reconciler launches its Chrome. It
// is the run-control seam the service layer drives from the dashboard.
func (a *App) StartWatching(profile string, plat domain.Platform) {
	a.setIntent(profile, plat, intentWatch)
	a.mu.Lock()
	pr := a.profileLocked(profile)
	var rt *PlatformRuntime
	if pr != nil {
		rt = pr.platform(plat)
	}
	a.mu.Unlock()
	if rt != nil && rt.Engine != nil {
		// Already up (login mode, or a redundant Start): unpause in place rather
		// than churn the window. The engine's discovery navigates off the login
		// page on its own.
		rt.Engine.Start()
		return
	}
	a.Reconcile(nil)
}

// StopWatching intends a (profile, platform) to stop. Chrome exists only while a
// watcher runs, so the reconcile pass this triggers tears the window down.
func (a *App) StopWatching(profile string, plat domain.Platform) {
	a.setIntent(profile, plat, intentOff)
	a.Reconcile(nil)
}

// BeginLogin intends a (profile, platform) to come up parked on its login page:
// the one time a window opens with no watcher running, because logging in needs
// the browser. A no-op when the runtime is already up (the user has a window to
// log into already).
func (a *App) BeginLogin(profile string, plat domain.Platform) {
	a.setIntent(profile, plat, intentLogin)
	a.mu.Lock()
	pr := a.profileLocked(profile)
	up := pr != nil && pr.platform(plat) != nil
	a.mu.Unlock()
	if up {
		return
	}
	a.Reconcile(nil)
}

// NeedsLogin reports whether a (profile, platform) still needs its first manual
// login, read from the on-disk first-run markers so it is known even while the
// runtime is down (nothing has launched Chrome). It seeds the boot intent and
// tells the dashboard when to offer a Log in button.
func (a *App) NeedsLogin(profile string, plat domain.Platform) bool {
	dataDir, err := a.profileDataDir(profile)
	if err != nil {
		return false
	}
	twitchDir, kickDir, err := a.chromeDirs(profile)
	if err != nil {
		return false
	}
	chromeDir := twitchDir
	if plat.OrTwitch() == domain.PlatformKick {
		chromeDir = kickDir
	}
	return platformFirstRun(dataDir, chromeDir, plat, a.sharedWindow())
}

// enabledOnStart reads a (profile, platform)'s enabled-on-start preference from
// the right derived runtime config (Kick runs on its own).
func (a *App) enabledOnStart(cfg *config.Config, profile string, plat domain.Platform) bool {
	if plat.OrTwitch() == domain.PlatformKick {
		kcfg, err := cfg.ProfileKickRuntimeConfig(profile)
		if err != nil {
			return false
		}
		return kcfg.Watcher.EnabledOnStart
	}
	pcfg, err := cfg.ProfileRuntimeConfig(profile)
	if err != nil {
		return false
	}
	return pcfg.Watcher.EnabledOnStart
}

// seedIntent returns a (profile, platform)'s intent, defaulting one the first
// time it is seen: watch when it is set to enable on start and is already past
// its first login, otherwise off. A user Start/Stop/Login recorded later always
// wins, because a seeded entry is never overwritten here.
func (a *App) seedIntent(cfg *config.Config, profile string, plat domain.Platform) runIntent {
	if in, ok := a.intentOf(profile, plat); ok {
		return in
	}
	in := intentOff
	if a.seedWatchAll || (a.enabledOnStart(cfg, profile, plat) && !a.NeedsLogin(profile, plat)) {
		in = intentWatch
	}
	a.setIntent(profile, plat, in)
	return in
}

// Binder receives runtime handles as (profile, platform) runtimes come up and
// go down, so the service layer's view of the world follows the reconciler.
// It is an interface here so internal/app never imports internal/service.
type Binder interface {
	// AddProfile registers a profile whose store this process has opened.
	AddProfile(name string, st *store.Store)
	// RemoveProfile forgets a profile whose store is about to be closed.
	RemoveProfile(name string)
	// Attach publishes one live (profile, platform) runtime. needsLogin is
	// true when the runtime came up against a fresh Chrome profile and is
	// waiting for the user to log in.
	Attach(profile string, plat domain.Platform, e *watcher.Engine, d watcher.Discoverer, sess browser.Session, needsLogin bool)
	// Detach drops one (profile, platform) runtime that has stopped.
	Detach(profile string, plat domain.Platform)
}

// StartLive brings the runtime up in reconciled mode: every enabled
// (profile, platform) is launched, attached to the binder, and its engine
// goroutine started, and every later Reconcile follows the config live. It
// blocks for the initial pass, so callers run it in a goroutine to keep the UI
// responsive while Chrome starts.
func (a *App) StartLive(ctx context.Context, emit watcher.Emitter, binder Binder) {
	a.cfgMu.Lock()
	a.liveCtx = ctx
	a.emit = emit
	a.binder = binder
	a.cfgMu.Unlock()
	a.ReconcileSync()
}

// Reconcile swaps in a newly saved config and starts a reconcile pass in the
// background. Launching Chrome takes seconds, so the HTTP handler that saved
// the config must never block on it: the UI polls /api/state and sees the
// runtime appear (or stay absent, on a launch failure).
func (a *App) Reconcile(cfg *config.Config) {
	if cfg != nil {
		a.cfgMu.Lock()
		a.desired = cfg
		a.cfgMu.Unlock()
	}
	go a.ReconcileSync()
}

// ReconcileSync runs a reconcile pass and blocks until it (and any pass its own
// arrival coalesced) is done. Only one pass ever runs at a time: a call landing
// mid-pass marks a single trailing rerun and returns, so a burst of settings
// saves cannot start the same runtime twice.
func (a *App) ReconcileSync() {
	a.recMu.Lock()
	if a.reconciling {
		a.rerun = true
		a.recMu.Unlock()
		return
	}
	a.reconciling = true
	a.recMu.Unlock()
	for {
		a.reconcileOnce()
		a.recMu.Lock()
		if !a.rerun {
			a.reconciling = false
			a.recMu.Unlock()
			return
		}
		a.rerun = false
		a.recMu.Unlock()
	}
}

// reconcileOnce diffs the desired state (the live config: enabled profiles and
// each one's effective kick setting) against the runtimes that are actually
// running, stops the deltas, then starts the deltas. Stops run first so a
// machine at its Chrome budget frees a window before opening the next.
func (a *App) reconcileOnce() {
	cfg := a.Config()
	ctx, binder := a.live()
	if ctx == nil {
		return // not running in live mode (CLI): nothing to reconcile
	}
	if err := ctx.Err(); err != nil {
		return // app shutting down
	}

	// desired holds only the platforms something INTENDS to run (watch or
	// login): Chrome comes up for those and no others. A merely-enabled platform
	// with intent off keeps its store and its dashboard slot but launches no
	// browser.
	desired := map[string][]domain.Platform{}
	var order []string
	configured := map[string]bool{}
	for _, pc := range cfg.Profiles {
		configured[pc.Name] = true
		eff, err := cfg.Effective(pc.Name)
		if err != nil || !eff.Enabled {
			continue
		}
		order = append(order, eff.Name)
		plats := []domain.Platform{domain.PlatformTwitch}
		if eff.KickEnabled {
			plats = append(plats, domain.PlatformKick)
		}
		var run []domain.Platform
		for _, plat := range plats {
			if a.seedIntent(cfg, eff.Name, plat) != intentOff {
				run = append(run, plat)
			}
		}
		// The profile's shared window is reference-counted by its tabs and either
		// platform can bring it up, so Kick no longer depends on Twitch running:
		// every platform something intends to run gets launched.
		if len(run) > 0 {
			desired[eff.Name] = run
		}
	}

	a.mu.Lock()
	existing := append([]*ProfileRuntime(nil), a.Profiles...)
	a.mu.Unlock()

	// Stop every runtime the config no longer wants, and forget profiles that
	// were removed outright. A profile that is merely DISABLED keeps its store
	// open: the UI still lists its queue and history.
	for _, pr := range existing {
		want := desired[pr.Name]
		for _, plat := range platformStopOrder {
			a.mu.Lock()
			rt := pr.platform(plat)
			if rt != nil && !hasPlatform(want, plat) {
				pr.setPlatform(plat, nil)
			} else {
				rt = nil
			}
			a.mu.Unlock()
			if rt == nil {
				continue
			}
			pr.Log.Info().Str("platform", string(plat)).Msg("app: stopping runtime")
			a.teardown(pr, plat, rt)
		}
		if configured[pr.Name] {
			continue
		}
		a.mu.Lock()
		a.forgetProfileLocked(pr.Name)
		a.mu.Unlock()
		if binder != nil {
			binder.RemoveProfile(pr.Name)
		}
		if pr.Store != nil {
			_ = pr.Store.Close()
		}
		pr.Log.Info().Msg("app: profile removed from config; store closed")
	}

	// Start every runtime the config wants that is not up. The first pass (app
	// boot) staggers each profile's first discovery; a live start is a user
	// action, so it gets no delay.
	for idx, name := range order {
		pr, err := a.ensureProfile(cfg, name)
		if err != nil {
			a.Log.Error().Err(err).Str("profile", name).Msg("app: open profile store failed; skipping this profile")
			continue
		}
		stagger := time.Duration(0)
		if !a.bootedOnce {
			stagger = discoveryStagger(idx)
		}
		for _, plat := range desired[name] {
			a.mu.Lock()
			up := pr.platform(plat) != nil
			a.mu.Unlock()
			if up {
				continue
			}
			a.startLive(ctx, binder, pr, plat, stagger)
		}
	}
	a.bootedOnce = true
}

// startLive launches one (profile, platform) runtime, publishes it, and runs its
// engine goroutine. A launch failure is logged and leaves the runtime absent:
// /api/state then reads enabled + not up, which the UI renders as "failed to
// start". Nothing else is disturbed.
func (a *App) startLive(ctx context.Context, binder Binder, pr *ProfileRuntime, plat domain.Platform, stagger time.Duration) {
	if ctx.Err() != nil {
		return // shutting down mid-pass: do not open a Chrome nobody will close
	}
	log := pr.Log.With().Str("platform", string(plat)).Logger()
	rt, err := a.startPlatformRuntime(ctx, pr, plat, stagger)
	if err != nil {
		log.Error().Err(err).Msg("app: runtime failed to start")
		return
	}
	rctx, cancel := context.WithCancel(ctx)
	rt.cancel = cancel
	rt.done = make(chan struct{})
	a.mu.Lock()
	pr.setPlatform(plat, rt)
	a.mu.Unlock()
	if binder != nil {
		binder.Attach(pr.Name, plat, rt.Engine, rt.Disco, rt.Session, rt.NeedsLogin)
	}
	go func() {
		defer close(rt.done)
		if err := rt.Engine.Run(rctx); err != nil && rctx.Err() == nil {
			log.Error().Err(err).Msg("app: engine loop exited")
		}
	}()
	log.Info().Bool("needs_login", rt.NeedsLogin).Msg("app: runtime up")
}

// teardown stops one runtime: cancel its engine context, wait (bounded) for the
// Run goroutine to return, detach it from the service layer, then release its
// browser.
//
// What "release its browser" means depends on the window layout. In
// separate_window mode it closes that platform's own Chrome
// (browser.Managed.Close flips its closed flag before closing the underlying
// session, so its supervisor sees the event stream end and returns instead of
// relaunching). In the shared layout it drops the platform's TAB: stopping Kick
// leaves the window and the Twitch tab running, while stopping Twitch - which
// owns the window - closes the whole profile browser, because no platform can
// outlive the tab the window was opened on.
func (a *App) teardown(pr *ProfileRuntime, plat domain.Platform, rt *PlatformRuntime) {
	if rt.cancel != nil {
		rt.cancel()
	}
	if rt.done != nil {
		select {
		case <-rt.done:
		case <-time.After(engineStopTimeout):
			pr.Log.Warn().Str("platform", string(plat)).
				Msg("app: engine did not exit in time; closing its browser anyway")
		}
	}
	if _, binder := a.live(); binder != nil {
		binder.Detach(pr.Name, plat)
	}
	ctx, cancel := context.WithTimeout(context.Background(), engineStopTimeout)
	rt.window.Close(ctx)
	cancel()

	if rt.group != nil {
		_ = rt.group.CloseSession(string(plat.OrTwitch()))
		a.mu.Lock()
		if pr.browser != nil && pr.browser.Closed() {
			pr.browser, pr.window = nil, nil
		}
		a.mu.Unlock()
		return
	}
	if rt.Session != nil {
		_ = rt.Session.Close()
	}
}

// ensureProfile returns the named profile's runtime, opening its store and
// registering it when this process has not seen it before (a profile added or
// re-enabled while the app runs).
func (a *App) ensureProfile(cfg *config.Config, name string) (*ProfileRuntime, error) {
	a.mu.Lock()
	pr := a.profileLocked(name)
	a.mu.Unlock()
	if pr != nil {
		return pr, nil
	}
	st, err := openProfileStore(cfg, name)
	if err != nil {
		return nil, err
	}
	pr = &ProfileRuntime{Name: name, Log: a.Log.With().Str("profile", name).Logger(), Store: st}
	a.mu.Lock()
	a.Profiles = append(a.Profiles, pr)
	a.mu.Unlock()
	if _, binder := a.live(); binder != nil {
		binder.AddProfile(name, st)
	}
	pr.Log.Info().Msg("app: profile store opened")
	return pr, nil
}

// forgetProfileLocked drops a profile from the runtime list. Callers hold a.mu.
func (a *App) forgetProfileLocked(name string) {
	out := a.Profiles[:0]
	for _, p := range a.Profiles {
		if p.Name != name {
			out = append(out, p)
		}
	}
	a.Profiles = out
}

// live returns the runtime context and binder installed by StartLive.
func (a *App) live() (context.Context, Binder) {
	a.cfgMu.RLock()
	defer a.cfgMu.RUnlock()
	return a.liveCtx, a.binder
}

// emitter returns the engine event sink installed by StartRuntime/StartLive.
func (a *App) emitter() watcher.Emitter {
	a.cfgMu.RLock()
	defer a.cfgMu.RUnlock()
	return a.emit
}

func hasPlatform(list []domain.Platform, p domain.Platform) bool {
	for _, v := range list {
		if v.OrTwitch() == p.OrTwitch() {
			return true
		}
	}
	return false
}
