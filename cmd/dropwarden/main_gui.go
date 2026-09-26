//go:build windows || darwin || linux

// Command dropwarden is the tray-first desktop app. It opens the store and
// config immediately, brings up the dashboard window and system tray, and
// launches the browser + watcher engine in the background so the UI is
// responsive even before Chrome is up.
package main

import (
	"context"
	"net/http"

	"github.com/wailsapp/wails/v3/pkg/application"

	webui "github.com/rake-pro/dropwarden/frontend"
	"github.com/rake-pro/dropwarden/internal/app"
	"github.com/rake-pro/dropwarden/internal/service"
	"github.com/rake-pro/dropwarden/internal/update"
	"github.com/rake-pro/dropwarden/internal/version"
)

func main() {
	a, err := app.Open(app.Options{Console: false})
	if err != nil {
		panic(err)
	}
	// Close tears down every engine and Chrome. It is ALSO registered as the
	// Wails OnShutdown hook below: on macOS a quit ends in [NSApp terminate:],
	// which exits the process without wapp.Run ever returning, so this defer
	// alone would never run there. Close is idempotent.
	defer a.Close()

	profiles := make([]*service.ProfileBackend, 0, len(a.Profiles))
	for _, pr := range a.Profiles {
		profiles = append(profiles, service.NewProfileBackend(a.Cfg, pr.Name, pr.Store))
	}
	backend := service.NewBackend(a.Cfg, a.ConfigPath, profiles...)
	// Every settings commit reconciles the running runtimes against the new
	// config, so enabling a profile or a platform starts its Chrome live.
	backend.SetReconciler(a)

	// The webview talks to Go over a small JSON API on the same asset origin
	// (no generated bindings needed): /api/* is the service layer, everything
	// else is the embedded frontend.
	assets := http.NewServeMux()
	assets.Handle("/api/", service.NewHTTPHandler(backend))
	assets.Handle("/", webui.Handler())

	// The self-updater follows the live config (a.Config), so the Updates
	// settings pane applies without a restart. Its lifetime is the app's:
	// cancelled when Wails starts shutting down.
	updCtx, updCancel := context.WithCancel(context.Background())
	updates := newUpdateController(updCtx, a.Config, backend)
	backend.SetUpdater(updates)

	wapp := application.New(application.Options{
		Name:        "dropwarden",
		Description: "Twitch and Kick drops collector",
		Assets:      application.AssetOptions{Handler: assets},
		SingleInstance: &application.SingleInstanceOptions{
			UniqueID: "pro.rake.dropwarden",
		},
		OnShutdown: func() {
			updCancel()
			a.Close()
		},
		// Runs after the single-instance lock is released, so an in-process
		// update can start the new binary without it being turned away.
		PostShutdown: updates.postShutdown,
	})
	a.Log.Info().Str("version", version.Version).Msg("dropwarden starting")

	// application.New acquires the single-instance lock synchronously (a
	// second instance notifies the first and os.Exits inside New), so from
	// here on this is the only instance: drop what a previous self-update
	// left behind (*.old binaries, stage dirs) and pick up its failure marker.
	update.CleanupOld()
	updates.loadFailure()

	win := newDashboardWindow(wapp, a.Cfg.UI.StartMinimized)
	updates.quit = wapp.Quit
	updates.show = func() { win.Show() }
	buildTray(wapp, win, backend, updates)
	updates.start()

	// Launch the browsers + engines in the background so the window is
	// responsive immediately. If Chrome cannot start (not installed, profile
	// locked), the UI stays up in a disconnected state and surfaces the error
	// via the log. StartLive keeps following the config from here on.
	emit := newWailsEmitter(wapp)
	go a.StartLive(wapp.Context(), emit, backend)

	if err := wapp.Run(); err != nil {
		a.Log.Error().Err(err).Msg("wails run failed")
	}
}

// newDashboardWindow creates the main window, hidden at start when configured to
// live in the tray. Closing it hides rather than quits; quit is from the tray.
func newDashboardWindow(wapp *application.App, startHidden bool) *application.WebviewWindow {
	win := wapp.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:             "dashboard",
		Title:            "dropwarden",
		Width:            1000,
		Height:           700,
		MinWidth:         720,
		MinHeight:        480,
		URL:              "/",
		Hidden:           startHidden,
		BackgroundColour: application.RGBA{Red: 0x1e, Green: 0x1e, Blue: 0x1e, Alpha: 0xff},
		Windows: application.WindowsWindow{
			Theme: application.Dark,
		},
	})
	return win
}
