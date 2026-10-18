//go:build production

// Command dropwarden-serve is the headless service: the same runtime as the
// desktop app (store, profiles, live reconcile, one Chrome per profile) with no
// tray and no windows. The dashboard is served over plain HTTP for a browser
// elsewhere, and logins happen through its remote-login view, which mirrors
// the watcher tab over CDP. Built for the container image, where the image
// itself is the update, so the self-updater never runs here.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	webui "github.com/rake-pro/dropwarden/frontend"
	"github.com/rake-pro/dropwarden/internal/app"
	"github.com/rake-pro/dropwarden/internal/browser"
	"github.com/rake-pro/dropwarden/internal/power"
	"github.com/rake-pro/dropwarden/internal/service"
	"github.com/rake-pro/dropwarden/internal/version"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:8080", "HTTP listen address for the dashboard and API")
	chromePath := flag.String("chrome-path", "", "Chrome to launch when the config's browser.chrome_path is empty")
	configPath := flag.String("config", "", "config file (default: DROPWARDEN_CONFIG, else <DROPWARDEN_HOME>/config.yaml)")
	flag.Parse()

	if *configPath != "" {
		// config.DefaultConfigPath resolves DROPWARDEN_CONFIG; the flag is
		// that override, spelled as a flag.
		if err := os.Setenv("DROPWARDEN_CONFIG", *configPath); err != nil {
			fatal(err)
		}
	}
	// Set before anything can look Chrome up; a configured chrome_path wins.
	browser.FallbackChromePath = *chromePath

	a, err := app.Open(app.Options{Console: false})
	if err != nil {
		fatal(err)
	}
	defer a.Close()

	profiles := make([]*service.ProfileBackend, 0, len(a.Profiles))
	for _, pr := range a.Profiles {
		profiles = append(profiles, service.NewProfileBackend(a.Cfg, pr.Name, pr.Store))
	}
	backend := service.NewBackend(a.Cfg, a.ConfigPath, profiles...)
	backend.SetReconciler(a)
	backend.SetRemote(true)
	// No SetUpdater: /api/state reports host.updates=false and /api/update*
	// refuse, so nothing ever checks for or installs a release here.
	pwr := power.New(a.Config, backend.WatchInProgress, a.Log.With().Str("component", "power").Logger())
	backend.SetPower(pwr)
	pwr.Refresh()

	mux := http.NewServeMux()
	mux.Handle("/api/", service.NewHTTPHandler(backend))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.Handle("/", webui.Handler())
	srv := &http.Server{Addr: *listen, Handler: mux, ReadHeaderTimeout: 10 * time.Second}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	a.Log.Info().Str("version", version.Version).Str("listen", *listen).Msg("dropwarden-serve starting")
	srvErr := make(chan error, 1)
	go func() { srvErr <- srv.ListenAndServe() }()

	// Same live runtime as the desktop app: every enabled (profile, platform)
	// comes up with its seeded intent and later settings saves reconcile live.
	emit := backend.WrapEmitterForPower(service.NewLogEmitter(a.Log))
	go a.StartLive(ctx, emit, backend)

	select {
	case <-ctx.Done():
		a.Log.Info().Msg("dropwarden-serve: shutting down")
	case err := <-srvErr:
		if !errors.Is(err, http.ErrServerClosed) {
			a.Log.Error().Err(err).Msg("dropwarden-serve: http server failed")
		}
	}
	shCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shCtx); err != nil {
		a.Log.Warn().Err(err).Msg("dropwarden-serve: http shutdown")
	}
	pwr.Close()
	a.Close()
}

func fatal(err error) {
	fmt.Fprintf(os.Stderr, "dropwarden-serve: %v\n", err)
	os.Exit(1)
}
