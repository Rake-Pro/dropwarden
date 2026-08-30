// Package app assembles the dropwarden object graph from configuration: config,
// logging, and the SQLite store are opened eagerly; the browser, Twitch
// controller, discovery, and watcher engine are built on demand by StartRuntime
// (which launches Chrome). Both the CLI and the Wails app build on this.
package app

import (
	"context"
	"fmt"

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

// App holds the eagerly-opened resources.
type App struct {
	Cfg        *config.Config
	ConfigPath string
	Log        zerolog.Logger
	Store      *store.Store

	// Runtime resources (nil until StartRuntime).
	Session browser.Session
	Ctrl    twitch.Controller
	Pages   *twitch.Pages
	Disco   *discovery.Discoverer
	Engine  *watcher.Engine

	// Kick runtime (nil unless kick.enabled).
	KickSession browser.Session
	KickDisco   *kick.Discoverer
	KickEngine  *watcher.Engine
}

// Options tune assembly.
type Options struct {
	Console bool // pretty console logging (CLI)
}

// Open loads config, sets up logging, and opens the store. It does NOT launch
// Chrome, so it is safe for commands like doctor and report.
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
	dbPath, err := config.DBPath()
	if err != nil {
		return nil, err
	}
	st, err := store.Open(dbPath)
	if err != nil {
		return nil, err
	}
	return &App{Cfg: cfg, ConfigPath: path, Log: logger, Store: st}, nil
}

// StartRuntime launches Chrome and builds the Twitch controller, page driver,
// discoverer, and watcher engine. emit receives engine events.
func (a *App) StartRuntime(ctx context.Context, emit watcher.Emitter) error {
	// The launch closure fully initializes a Twitch-ready session; the Managed
	// wrapper re-invokes it whenever Chrome dies (CDP disconnect, crash), so an
	// overnight browser crash recovers instead of retry-looping on a dead
	// socket forever.
	launch := func(lctx context.Context) (browser.Session, error) {
		sess, err := browser.Launch(lctx, a.Cfg, a.Log)
		if err != nil {
			return nil, err
		}
		if err := twitch.InstallCapture(lctx, sess); err != nil {
			a.Log.Warn().Err(err).Msg("app: install capture shim failed")
		}
		if a.Cfg.Browser.Window.StartMinimized {
			if err := sess.SetMinimized(lctx, true); err != nil {
				a.Log.Warn().Err(err).Msg("app: minimize watcher window failed")
			} else {
				a.Log.Info().Msg("app: watcher window minimized")
			}
		}
		return sess, nil
	}
	sess, err := launch(ctx)
	if err != nil {
		return fmt.Errorf("app: launch browser: %w", err)
	}
	a.Session = browser.NewManaged(sess, launch, a.Log)
	a.Ctrl = twitch.NewController(a.Session)
	a.Pages = twitch.NewPages(a.Ctrl)
	a.Disco = discovery.New(a.Store, a.Pages, a.Cfg, a.Log)
	a.Engine = watcher.New(watcher.Deps{
		Store: a.Store, Ctrl: a.Ctrl, Pages: a.Pages, Disco: a.Disco,
		Cfg: a.Cfg, Emit: emit, Log: a.Log,
		Platform: domain.PlatformTwitch, ChannelHost: "twitch.tv",
	})

	if a.Cfg.Kick.Enabled {
		if err := a.startKickRuntime(ctx, emit); err != nil {
			// Kick is additive: a Kick launch failure must never take down the
			// working Twitch runtime.
			a.Log.Error().Err(err).Msg("app: kick runtime failed to start; continuing twitch-only")
		}
	}
	return nil
}

// startKickRuntime launches the second Chrome instance and the Kick engine.
func (a *App) startKickRuntime(ctx context.Context, emit watcher.Emitter) error {
	kcfg := a.Cfg.KickRuntimeConfig()
	profileDir, err := a.Cfg.KickProfileDir()
	if err != nil {
		return err
	}
	klog := a.Log.With().Str("platform", "kick").Logger()
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
	a.KickSession = browser.NewManaged(sess, launch, klog)
	ctrl := kick.NewController(a.KickSession)
	pages := kick.NewPages(ctrl)
	a.KickDisco = kick.NewDiscoverer(a.Store, pages, kcfg, klog)
	a.KickEngine = watcher.New(watcher.Deps{
		Store: a.Store, Ctrl: ctrl, Pages: pages, Disco: a.KickDisco,
		Cfg: kcfg, Emit: emit, Log: klog,
		Platform: domain.PlatformKick, ChannelHost: "kick.com",
	})
	a.Log.Info().Msg("app: kick runtime up")
	return nil
}

// Engines returns every engine that should run (twitch first, then kick).
func (a *App) Engines() []*watcher.Engine {
	var out []*watcher.Engine
	if a.Engine != nil {
		out = append(out, a.Engine)
	}
	if a.KickEngine != nil {
		out = append(out, a.KickEngine)
	}
	return out
}

// Close releases runtime and eager resources.
func (a *App) Close() {
	if a.Session != nil {
		_ = a.Session.Close()
	}
	if a.KickSession != nil {
		_ = a.KickSession.Close()
	}
	if a.Store != nil {
		_ = a.Store.Close()
	}
}
