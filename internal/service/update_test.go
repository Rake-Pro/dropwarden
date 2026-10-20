package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rake-pro/dropwarden/internal/config"
	"github.com/rake-pro/dropwarden/internal/version"
)

func TestSettingsUpdateRoundTrip(t *testing.T) {
	b := testBackend(t)
	s := NewSettingsService(b)
	dto := s.Load()
	if dto.UpdateEnabled == nil || !*dto.UpdateEnabled || dto.UpdateIntervalHours == nil || *dto.UpdateIntervalHours != 24 {
		t.Fatalf("unexpected update defaults: %+v", dto)
	}
	dto.UpdateAutoInstall = ptr(false)
	dto.UpdateFeedURL = ptr("  https://example.com/dw/latest.json ")
	dto.UpdateIntervalHours = ptr(6)
	if err := s.Save(dto); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := config.Load(b.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if got.UpdateAutoInstall() || !got.UpdateEnabled() || got.UpdateFeedURL() != "https://example.com/dw/latest.json" || got.Update.IntervalHours != 6 {
		t.Fatalf("update block not persisted: %+v", got.Update)
	}

	bad := s.Load()
	bad.UpdateIntervalHours = ptr(0)
	if err := s.Save(bad); err == nil {
		t.Fatal("interval_hours 0 should be rejected on save")
	}
}

// TestSettingsSaveDoesNotBakeUpdateDefaults saves the loaded DTO unchanged
// (what any other settings pane does) and checks the optional update fields
// stay unset, so a later change of the defaults still reaches this install.
func TestSettingsSaveDoesNotBakeUpdateDefaults(t *testing.T) {
	b := testBackend(t)
	// The starter template pins every update field; model a config written
	// before the update block existed.
	b.cfg.Update = config.UpdateConfig{IntervalHours: 24}
	s := NewSettingsService(b)
	if err := s.Save(s.Load()); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := config.Load(b.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	u := got.Update
	if u.Enabled != nil || u.AutoInstall != nil || u.ShowChangelog != nil || u.FeedURL != nil {
		t.Fatalf("defaults baked into config: %+v", u)
	}

	// A field that is already pinned keeps following the form, even back to
	// the default value.
	dto := s.Load()
	dto.UpdateAutoInstall = ptr(false)
	if err := s.Save(dto); err != nil {
		t.Fatal(err)
	}
	dto = s.Load()
	dto.UpdateAutoInstall = ptr(true)
	if err := s.Save(dto); err != nil {
		t.Fatal(err)
	}
	if got, _ := config.Load(b.ConfigPath); got.Update.AutoInstall == nil || !*got.Update.AutoInstall {
		t.Fatalf("pinned auto_install not updated: %+v", got.Update)
	}
}

func TestSettingsPartialDTOKeepsUpdateBlock(t *testing.T) {
	b := testBackend(t)
	s := NewSettingsService(b)
	dto := s.Load()
	dto.UpdateEnabled, dto.UpdateAutoInstall, dto.UpdateShowChangelog, dto.UpdateFeedURL, dto.UpdateIntervalHours = nil, nil, nil, nil, nil
	if err := s.Save(dto); err != nil {
		t.Fatalf("save without update fields: %v", err)
	}
	cfg := b.config()
	if !cfg.UpdateEnabled() || cfg.UpdateFeedURL() != config.DefaultUpdateFeedURL || cfg.Update.IntervalHours != 24 {
		t.Fatalf("partial save changed the update block: %+v", cfg.Update)
	}
}

// fakeUpdater records calls for the /api/update routes.
type fakeUpdater struct {
	state      UpdateStateDTO
	checks     int
	installs   int
	installErr error
}

func (f *fakeUpdater) UpdateState() UpdateStateDTO { return f.state }
func (f *fakeUpdater) CheckNow(ctx context.Context) (UpdateStateDTO, error) {
	f.checks++
	f.state.Pending = &PendingUpdateDTO{Version: "9.9.9"}
	return f.state, nil
}
func (f *fakeUpdater) InstallNow() error { f.installs++; return f.installErr }

func TestHTTPUpdateWithoutUpdater(t *testing.T) {
	h := NewHTTPHandler(testBackend(t))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("GET", "/api/update", nil))
	var st UpdateStateDTO
	if err := json.Unmarshal(rr.Body.Bytes(), &st); err != nil || rr.Code != 200 {
		t.Fatalf("code=%d err=%v body=%s", rr.Code, err, rr.Body.String())
	}
	if st.Enabled || st.CurrentVersion != version.Version || st.Pending != nil || st.Status != UpdateStatusIdle || st.Percent != -1 {
		t.Errorf("state = %+v", st)
	}
	for _, path := range []string{"/api/update/check", "/api/update/install"} {
		rr = httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest("POST", path, nil))
		if rr.Code != 400 {
			t.Errorf("%s without an updater: code=%d, want 400", path, rr.Code)
		}
	}
}

func TestHTTPUpdateRoutes(t *testing.T) {
	b := testBackend(t)
	f := &fakeUpdater{state: UpdateStateDTO{CurrentVersion: "1.0.0", Enabled: true, Status: UpdateStatusIdle}}
	b.SetUpdater(f)
	h := NewHTTPHandler(b)

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("POST", "/api/update/check", nil))
	var st UpdateStateDTO
	json.Unmarshal(rr.Body.Bytes(), &st)
	if rr.Code != 200 || f.checks != 1 || st.Pending == nil || st.Pending.Version != "9.9.9" {
		t.Fatalf("check: code=%d checks=%d state=%+v", rr.Code, f.checks, st)
	}

	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("POST", "/api/update/install", nil))
	st = UpdateStateDTO{}
	json.Unmarshal(rr.Body.Bytes(), &st)
	if rr.Code != 200 || f.installs != 1 || st.CurrentVersion != "1.0.0" {
		t.Fatalf("install: code=%d installs=%d body=%s", rr.Code, f.installs, rr.Body.String())
	}
	f.installErr = errors.New("no update pending")
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("POST", "/api/update/install", nil))
	if rr.Code != 400 || !strings.Contains(rr.Body.String(), "no update pending") {
		t.Fatalf("install error: code=%d body=%s", rr.Code, rr.Body.String())
	}

	// /api/state carries the version and the same update snapshot.
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("GET", "/api/state", nil))
	var state struct {
		Version string         `json:"version"`
		Update  UpdateStateDTO `json:"update"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if state.Version != version.Version || state.Update.CurrentVersion != "1.0.0" || state.Update.Pending == nil {
		t.Errorf("state = %+v", state)
	}
}

func TestWatchInProgressNoEngines(t *testing.T) {
	if testBackend(t).WatchInProgress() {
		t.Error("a backend with no engines is never busy")
	}
}

func TestSettingsUpdateChannelSwitchesTheFeed(t *testing.T) {
	b := testBackend(t)
	s := NewSettingsService(b)
	dto := s.Load()
	// The pane sends the resolved stable feed back together with the new
	// channel: that must not pin the stable URL.
	dto.UpdateChannel = ptr(config.UpdateChannelDev)
	if err := s.Save(dto); err != nil {
		t.Fatal(err)
	}
	got := b.config()
	if got.UpdateChannel() != config.UpdateChannelDev || got.Update.FeedURL != nil || got.UpdateFeedURL() != config.DefaultDevUpdateFeedURL {
		t.Fatalf("dev channel: channel=%q pinned=%v feed=%q", got.UpdateChannel(), got.Update.FeedURL, got.UpdateFeedURL())
	}
	// And back, with the dev feed echoed.
	dto = s.Load()
	dto.UpdateChannel = ptr(config.UpdateChannelStable)
	if err := s.Save(dto); err != nil {
		t.Fatal(err)
	}
	got = b.config()
	if got.UpdateChannel() != config.UpdateChannelStable || got.Update.FeedURL != nil || got.UpdateFeedURL() != config.DefaultUpdateFeedURL {
		t.Fatalf("stable channel: channel=%q pinned=%v feed=%q", got.UpdateChannel(), got.Update.FeedURL, got.UpdateFeedURL())
	}
	// A stale echo: the pane kept the dev feed from before the switch and
	// saves another pane without reloading. It must not become a pin.
	dto = s.Load()
	dto.UpdateFeedURL = ptr(config.DefaultDevUpdateFeedURL)
	if err := s.Save(dto); err != nil {
		t.Fatal(err)
	}
	got = b.config()
	if got.Update.FeedURL != nil || got.UpdateFeedURL() != config.DefaultUpdateFeedURL {
		t.Fatalf("stale dev echo pinned: pinned=%v feed=%q", got.Update.FeedURL, got.UpdateFeedURL())
	}
	// A real custom feed still pins.
	dto = s.Load()
	dto.UpdateFeedURL = ptr("https://example.com/dw/latest.json")
	if err := s.Save(dto); err != nil {
		t.Fatal(err)
	}
	if b.config().UpdateFeedURL() != "https://example.com/dw/latest.json" {
		t.Fatalf("custom feed lost: %q", b.config().UpdateFeedURL())
	}
}
