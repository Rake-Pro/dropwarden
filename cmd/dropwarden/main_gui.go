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
)

func main() {
	a, err := app.Open(app.Options{Console: false})
	if err != nil {
		panic(err)
	}
	defer a.Close()

	backend := service.NewBackend(a.Store, nil, a.Cfg, a.ConfigPath)

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

	// Launch the browser + engine in the background so the window is responsive
	// immediately. If Chrome cannot start (not installed, profile locked), the
	// UI stays up in a disconnected state and surfaces the error via the log.
	emit := newWailsEmitter(wapp)
	go startRuntime(wapp.Context(), a, backend, emit)

	if err := wapp.Run(); err != nil {
		a.Log.Error().Err(err).Msg("wails run failed")
	}
}

// startRuntime launches Chrome, attaches the engine to the backend, and runs the
// engine loop until the app context is cancelled.
func startRuntime(ctx context.Context, a *app.App, backend *service.Backend, emit *wailsEmitter) {
	if err := a.StartRuntime(ctx, emit); err != nil {
		a.Log.Error().Err(err).Msg("runtime start failed; UI running disconnected")
		return
	}
	backend.SetEngine(a.Engine)
	backend.SetDiscoverer(a.Disco)
	if a.KickEngine != nil {
		backend.SetKickEngine(a.KickEngine)
		backend.SetKickDiscoverer(a.KickDisco)
	}
	a.Log.Info().Int("engines", len(a.Engines())).Msg("runtime up; watcher engine(s) attached")
	// Run every platform engine; block until all exit (ctx cancellation).
	done := make(chan struct{})
	for _, e := range a.Engines() {
		e := e
		go func() {
			if err := e.Run(ctx); err != nil && ctx.Err() == nil {
				a.Log.Error().Err(err).Msg("engine loop exited")
			}
			done <- struct{}{}
		}()
	}
	for range a.Engines() {
		<-done
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
