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
	sess, err := browser.Launch(ctx, a.Cfg, a.Log)
	if err != nil {
		return fmt.Errorf("app: launch browser: %w", err)
	}
	a.Session = sess
	if a.Cfg.Browser.Window.StartMinimized {
		if err := sess.SetMinimized(ctx, true); err != nil {
			a.Log.Warn().Err(err).Msg("app: minimize watcher window failed")
		} else {
			a.Log.Info().Msg("app: watcher window minimized")
		}
	}
	if err := twitch.InstallCapture(ctx, sess); err != nil {
		a.Log.Warn().Err(err).Msg("app: install capture shim failed")
	}
	a.Ctrl = twitch.NewController(sess)
	a.Pages = twitch.NewPages(a.Ctrl)
	a.Disco = discovery.New(a.Store, a.Pages, a.Cfg, a.Log)
	a.Engine = watcher.New(watcher.Deps{
		Store: a.Store, Ctrl: a.Ctrl, Pages: a.Pages, Disco: a.Disco,
		Cfg: a.Cfg, Emit: emit, Log: a.Log,
	})
	return nil
}

// Close releases runtime and eager resources.
func (a *App) Close() {
	if a.Session != nil {
		_ = a.Session.Close()
	}
	if a.Store != nil {
		_ = a.Store.Close()
	}
}
