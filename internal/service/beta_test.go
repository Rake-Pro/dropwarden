package service

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/rs/zerolog"

	"github.com/rake-pro/dropwarden/internal/config"
	"github.com/rake-pro/dropwarden/internal/discovery"
	"github.com/rake-pro/dropwarden/internal/domain"
	"github.com/rake-pro/dropwarden/internal/watcher"
)

func TestNeedsUserOnlyForHumanActions(t *testing.T) {
	for reason, want := range map[string]bool{
		"account link required":                                                                       true,
		"completed - claim it manually in the Twitch inventory":                                       true,
		"watch complete - claim it in the Twitch inventory":                                           true,
		"Twitch session expired - log in again":                                                       true,
		"reward already owned - Twitch will not credit it again":                                      false,
		"not crediting after 5 min (already-owned reward, logged out, hidden window, or region lock)": false,
	} {
		if got := needsUser(domain.QueueItem{Status: domain.ItemNeedsAction, Reason: reason}); got != want {
			t.Errorf("%q: needsUser=%v want %v", reason, got, want)
		}
	}
	if needsUser(domain.QueueItem{Status: domain.ItemPending, Reason: "account link required"}) {
		t.Error("only needs_action rows can need the user")
	}
}

// launchErrReconciler reports a launch failure the way internal/app does.
type launchErrReconciler struct{ recordingReconciler }

func (*launchErrReconciler) LaunchError(string, domain.Platform) string { return "chrome not found" }

func TestStateCarriesReasonLaunchErrorAndHost(t *testing.T) {
	b := testBackend(t)
	b.SetReconciler(&launchErrReconciler{})
	rr := httptest.NewRecorder()
	NewHTTPHandler(b).ServeHTTP(rr, httptest.NewRequest("GET", "/api/state", nil))
	var raw struct {
		Host     map[string]any `json:"host"`
		Profiles []struct {
			Platforms []map[string]any `json:"platforms"`
		} `json:"profiles"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw.Host["chromeFound"].(bool); !ok {
		t.Fatalf("host.chromeFound missing: %v", raw.Host)
	}
	if _, ok := raw.Host["chromePath"].(string); !ok {
		t.Fatalf("host.chromePath missing: %v", raw.Host)
	}
	tw := raw.Profiles[0].Platforms[0]
	if tw["launchError"] != "chrome not found" {
		t.Fatalf("launchError = %v", tw["launchError"])
	}
	if _, ok := tw["reason"].(string); !ok {
		t.Fatalf("reason missing: %v", tw)
	}
}

// cfgDisco records SetConfig.
type cfgDisco struct{ got *config.Config }

func (d *cfgDisco) RunOnce(context.Context) (discovery.Result, error) { return discovery.Result{}, nil }
func (d *cfgDisco) SetConfig(c *config.Config)                        { d.got = c }

func TestReloadEnginesPushesDiscovererConfig(t *testing.T) {
	b := testBackend(t)
	cfg := b.config()
	e := watcher.New(watcher.Deps{Cfg: cfg, Log: zerolog.Nop()})
	d := &cfgDisco{}
	b.Attach(config.MainProfileName, domain.PlatformTwitch, e, d, nil, false)
	b.ReloadEngines()
	if d.got == nil {
		t.Fatal("ReloadEngines must push the profile config into the discoverer")
	}
}
