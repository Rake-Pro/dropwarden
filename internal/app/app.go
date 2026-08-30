// Package app assembles the dropwarden object graph from configuration: config,
// logging, and every enabled profile's SQLite store are opened eagerly; the
// browsers, controllers, discoverers, and watcher engines are built on demand by
// StartRuntime (which launches Chrome). Both the CLI and the Wails app build on
// this.
package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
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
// driver, discoverer, and watcher engine.
type PlatformRuntime struct {
	Session browser.Session
	Pages   watcher.Pager
	Disco   watcher.Discoverer
	Engine  *watcher.Engine
}

// ProfileRuntime is one person's runtime: their own store (opened eagerly) and
// one PlatformRuntime per platform they run. Kick is nil unless kick is enabled
// for that profile; Twitch is nil until StartRuntime brings Chrome up.
type ProfileRuntime struct {
	Name  string
	Log   zerolog.Logger
	Store *store.Store

	Twitch *PlatformRuntime
	Kick   *PlatformRuntime
}

// App holds the eagerly-opened resources.
type App struct {
	Cfg        *config.Config
	ConfigPath string
	Log        zerolog.Logger

	// Profiles are the enabled profiles this process manages, in config order.
	Profiles []*ProfileRuntime
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
	a := &App{Cfg: cfg, ConfigPath: path, Log: logger}
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
	if len(a.Profiles) == 0 {
		return nil
	}
	return a.Profiles[0]
}

// Profile returns the named profile runtime, or nil.
func (a *App) Profile(name string) *ProfileRuntime {
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
func (a *App) StartRuntime(ctx context.Context, emit watcher.Emitter) error {
	started := 0
	for idx, pr := range a.Profiles {
		if err := a.startProfileRuntime(ctx, pr, idx, emit); err != nil {
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
func (a *App) startProfileRuntime(ctx context.Context, pr *ProfileRuntime, idx int, emit watcher.Emitter) error {
	if err := a.startTwitchRuntime(ctx, pr, idx, emit); err != nil {
		return err
	}
	eff, err := a.Cfg.Effective(pr.Name)
	if err != nil {
		return err
	}
	if eff.KickEnabled {
		if err := a.startKickRuntime(ctx, pr, idx, emit); err != nil {
			// Kick is additive: a Kick launch failure must never take down the
			// working Twitch runtime.
			pr.Log.Error().Err(err).Msg("app: kick runtime failed to start; continuing twitch-only")
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

// startTwitchRuntime launches the profile's Twitch Chrome and engine.
func (a *App) startTwitchRuntime(ctx context.Context, pr *ProfileRuntime, idx int, emit watcher.Emitter) error {
	pcfg, err := a.Cfg.ProfileRuntimeConfig(pr.Name)
	if err != nil {
		return err
	}
	profileDir, _, err := a.chromeDirs(pr.Name)
	if err != nil {
		return err
	}
	log := pr.Log.With().Str("platform", "twitch").Logger()
	// The launch closure fully initializes a Twitch-ready session; the Managed
	// wrapper re-invokes it whenever Chrome dies (CDP disconnect, crash), so an
	// overnight browser crash recovers instead of retry-looping on a dead
	// socket forever.
	launch := func(lctx context.Context) (browser.Session, error) {
		sess, err := browser.LaunchWith(lctx, pcfg.Browser, profileDir, pcfg.Browser.Burnin.DimOpacity, log)
		if err != nil {
			return nil, err
		}
		if err := twitch.InstallCapture(lctx, sess); err != nil {
			log.Warn().Err(err).Msg("app: install capture shim failed")
		}
		if pcfg.Browser.Window.StartMinimized {
			if err := sess.SetMinimized(lctx, true); err != nil {
				log.Warn().Err(err).Msg("app: minimize watcher window failed")
			} else {
				log.Info().Msg("app: watcher window minimized")
			}
		}
		return sess, nil
	}
	sess, err := launch(ctx)
	if err != nil {
		return fmt.Errorf("app: launch browser: %w", err)
	}
	managed := browser.NewManaged(sess, launch, log)
	ctrl := twitch.NewController(managed)
	pages := twitch.NewPages(ctrl)
	disco := discovery.New(pr.Store, pages, pcfg, log)
	pr.Twitch = &PlatformRuntime{
		Session: managed,
		Pages:   pages,
		Disco:   disco,
		Engine: watcher.New(watcher.Deps{
			Store: pr.Store, Ctrl: ctrl, Pages: pages, Disco: disco,
			Cfg: pcfg, Emit: emit, Log: log,
			Platform: domain.PlatformTwitch, ChannelHost: "twitch.tv", Profile: pr.Name,
			DiscoveryStagger: discoveryStagger(idx),
		}),
	}
	return nil
}

// startKickRuntime launches the profile's second Chrome instance and its Kick
// engine. The Kick engine always runs on the profile's derived kick config,
// never the root config.
func (a *App) startKickRuntime(ctx context.Context, pr *ProfileRuntime, idx int, emit watcher.Emitter) error {
	kcfg, err := a.Cfg.ProfileKickRuntimeConfig(pr.Name)
	if err != nil {
		return err
	}
	_, profileDir, err := a.chromeDirs(pr.Name)
	if err != nil {
		return err
	}
	klog := pr.Log.With().Str("platform", "kick").Logger()
	launch := func(lctx context.Context) (browser.Session, error) {
		sess, err := browser.LaunchWith(lctx, kcfg.Browser, profileDir, kcfg.Browser.Burnin.DimOpacity, klog)
		if err != nil {
			return nil, err
		}
		if kcfg.Browser.Window.StartMinimized {
			if err := sess.SetMinimized(lctx, true); err != nil {
				klog.Warn().Err(err).Msg("app: minimize kick window failed")
			}
		}
		return sess, nil
	}
	sess, err := launch(ctx)
	if err != nil {
		return fmt.Errorf("app: launch kick browser: %w", err)
	}
	managed := browser.NewManaged(sess, launch, klog)
	ctrl := kick.NewController(managed)
	pages := kick.NewPages(ctrl)
	disco := kick.NewDiscoverer(pr.Store, pages, kcfg, klog)
	pr.Kick = &PlatformRuntime{
		Session: managed,
		Pages:   pages,
		Disco:   disco,
		Engine: watcher.New(watcher.Deps{
			Store: pr.Store, Ctrl: ctrl, Pages: pages, Disco: disco,
			Cfg: kcfg, Emit: emit, Log: klog,
			Platform: domain.PlatformKick, ChannelHost: "kick.com", Profile: pr.Name,
			DiscoveryStagger: discoveryStagger(idx),
		}),
	}
	pr.Log.Info().Msg("app: kick runtime up")
	return nil
}

// chromeDirs resolves a profile's two Chrome user-data dirs. The browser
// profile_dir overrides are single-instance settings, so they apply to the main
// profile only; every other profile uses its own directory layout.
func (a *App) chromeDirs(name string) (string, string, error) {
	if name == config.MainProfileName {
		twitchDir, err := a.Cfg.ProfileDir()
		if err != nil {
			return "", "", err
		}
		kickDir, err := a.Cfg.KickProfileDir()
		if err != nil {
			return "", "", err
		}
		return twitchDir, kickDir, nil
	}
	pp, err := a.Cfg.ProfilePaths(name)
	if err != nil {
		return "", "", err
	}
	return pp.ChromeProfile, pp.ChromeProfileKick, nil
}

// Engines returns every engine that should run, profile by profile (twitch
// first, then kick).
func (a *App) Engines() []*watcher.Engine {
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

// Close releases runtime and eager resources.
func (a *App) Close() {
	for _, pr := range a.Profiles {
		for _, plat := range []*PlatformRuntime{pr.Twitch, pr.Kick} {
			if plat != nil && plat.Session != nil {
				_ = plat.Session.Close()
			}
		}
		if pr.Store != nil {
			_ = pr.Store.Close()
		}
	}
}
