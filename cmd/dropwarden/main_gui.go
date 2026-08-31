//go:build windows || darwin || linux

// Command dropwarden is the tray-first desktop app. It opens the store and
// config immediately, brings up the dashboard window and system tray, and
// launches the browser + watcher engine in the background so the UI is
// responsive even before Chrome is up.
package main

import (
	"net/http"

	"github.com/wailsapp/wails/v3/pkg/application"

	webui "github.com/rake-pro/dropwarden/frontend"
	"github.com/rake-pro/dropwarden/internal/app"
	"github.com/rake-pro/dropwarden/internal/service"
)

func main() {
	a, err := app.Open(app.Options{Console: false})
	if err != nil {
		panic(err)
	}
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

	wapp := application.New(application.Options{
		Name:        "dropwarden",
		Description: "Twitch and Kick drops collector",
		Assets:      application.AssetOptions{Handler: assets},
		SingleInstance: &application.SingleInstanceOptions{
			UniqueID: "pro.rake.dropwarden",
		},
	})

	win := newDashboardWindow(wapp, a.Cfg.UI.StartMinimized)
	buildTray(wapp, win, backend)

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
