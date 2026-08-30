package service

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rake-pro/dropwarden/internal/config"
	"github.com/rake-pro/dropwarden/internal/domain"
)

// stateDTO is the /api/state shape the frontend consumes: shared config plus
// one entry per profile carrying that profile's platforms and data.
type stateDTO struct {
	Config   ConfigDTO `json:"config"`
	Profiles []struct {
		Name           string `json:"name"`
		DisplayName    string `json:"displayName"`
		Enabled        bool   `json:"enabled"`
		PendingRestart bool   `json:"pendingRestart"`
		Platforms      []struct {
			Platform       string `json:"platform"`
			Enabled        bool   `json:"enabled"`
			Up             bool   `json:"up"`
			PendingRestart bool   `json:"pendingRestart"`
		} `json:"platforms"`
		Queue  []QueueItemDTO `json:"queue"`
		Claims []ClaimDTO     `json:"claims"`
		Stats  StatsDTO       `json:"stats"`
		Spark  []int          `json:"spark"`
	} `json:"profiles"`
}

func getState(t *testing.T, h http.Handler) stateDTO {
	t.Helper()
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("GET", "/api/state", nil))
	if rr.Code != 200 {
		t.Fatalf("state code=%d body=%s", rr.Code, rr.Body.String())
	}
	var state stateDTO
	if err := json.Unmarshal(rr.Body.Bytes(), &state); err != nil {
		t.Fatalf("decode state: %v", err)
	}
	return state
}

func TestHTTPStateAndActions(t *testing.T) {
	b := testBackend(t)
	// Seed some data.
	c := domain.Campaign{ID: "c1", GameName: "Rust", Status: domain.StatusActive,
		Drops: []domain.Drop{{ID: "d1", CampaignID: "c1", Name: "Skin", RequiredMinutes: 60}}}
	b.FirstProfile().Store.UpsertCampaign(c)
	b.FirstProfile().Store.UpsertQueueItem(domain.QueueItem{DropID: "d1", CampaignID: "c1", Enabled: true, Status: domain.ItemPending})

	h := NewHTTPHandler(b)

	state := getState(t, h)
	if state.Config.Volume != 0.01 {
		t.Fatalf("bad config in state: %+v", state.Config)
	}
	if len(state.Profiles) != 1 || state.Profiles[0].Name != config.MainProfileName {
		t.Fatalf("expected one main profile: %+v", state.Profiles)
	}
	p := state.Profiles[0]
	if len(p.Queue) != 1 || p.Queue[0].DropName != "Skin" {
		t.Fatalf("bad queue in profile state: %+v", p.Queue)
	}
	if len(p.Platforms) != 2 || p.Platforms[0].Platform != "twitch" || p.Platforms[1].Platform != "kick" {
		t.Fatalf("bad platforms in profile state: %+v", p.Platforms)
	}
	if !p.Platforms[0].Enabled || p.Platforms[0].Up {
		t.Fatalf("twitch should be enabled but not up with nil engine: %+v", p.Platforms[0])
	}
	if p.Platforms[1].Enabled || p.Platforms[1].Up {
		t.Fatalf("kick should be disabled by default: %+v", p.Platforms[1])
	}
	if p.PendingRestart {
		t.Fatalf("an unchanged profile must not report pendingRestart: %+v", p)
	}

	// POST /api/queue/priority (valid) - persists config, engine nil is fine.
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, jsonReq("POST", "/api/queue/priority", `{"mode":"ending_soonest"}`))
	if rr.Code != 200 {
		t.Fatalf("priority code=%d body=%s", rr.Code, rr.Body.String())
	}

	// POST /api/queue/priority (invalid) - 400.
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, jsonReq("POST", "/api/queue/priority", `{"mode":"nonsense"}`))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("invalid priority should be 400, got %d", rr.Code)
	}

	// POST /api/engine/main/twitch/start with nil engine -> 400 "not running".
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("POST", "/api/engine/main/twitch/start", nil))
	if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "not running") {
		t.Fatalf("start with nil engine should be 400 not-running, got %d %s", rr.Code, rr.Body.String())
	}

	// Kick disabled -> 400 with a settings hint; bogus profile/platform/action -> 400.
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("POST", "/api/engine/main/kick/start", nil))
	if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "disabled") {
		t.Fatalf("kick-disabled start should be 400 with hint, got %d %s", rr.Code, rr.Body.String())
	}
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("POST", "/api/engine/nobody/twitch/start", nil))
	if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "unknown profile") {
		t.Fatalf("unknown profile should be 400, got %d %s", rr.Code, rr.Body.String())
	}
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("POST", "/api/engine/main/mixer/start", nil))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("unknown platform should be 400, got %d", rr.Code)
	}
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("POST", "/api/engine/main/twitch/dance", nil))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("unknown action should be 400, got %d", rr.Code)
	}

	// POST /api/queue/enabled toggles the item in the named profile.
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, jsonReq("POST", "/api/queue/enabled", `{"profile":"main","dropID":"d1","on":false}`))
	if rr.Code != 200 {
		t.Fatalf("enabled code=%d body=%s", rr.Code, rr.Body.String())
	}
	q, _ := b.FirstProfile().Store.GetQueue()
	if q[0].Enabled {
		t.Fatal("item should be disabled after toggle")
	}

	// An unknown profile in a queue action is rejected, not silently applied to
	// somebody else's queue.
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, jsonReq("POST", "/api/queue/enabled", `{"profile":"nobody","dropID":"d1","on":true}`))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("queue action for unknown profile should be 400, got %d", rr.Code)
	}
}

func TestHTTPStateListsUnstartedProfile(t *testing.T) {
	b := testBackend(t)
	h := NewHTTPHandler(b)
	// A profile added since startup: configured, but this process opened no
	// store or engine for it.
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, jsonReq("POST", "/api/profiles", `{"name":"greg","displayName":"Greg"}`))
	if rr.Code != 200 {
		t.Fatalf("add profile code=%d body=%s", rr.Code, rr.Body.String())
	}

	state := getState(t, h)
	if len(state.Profiles) != 2 {
		t.Fatalf("expected main + greg in state, got %+v", state.Profiles)
	}
	greg := state.Profiles[1]
	if greg.Name != "greg" || greg.DisplayName != "Greg" {
		t.Fatalf("unexpected added profile: %+v", greg)
	}
	if !greg.Enabled || !greg.PendingRestart {
		t.Fatalf("a profile added after startup must read enabled+pendingRestart: %+v", greg)
	}
	if greg.Platforms[0].Up || len(greg.Queue) != 0 {
		t.Fatalf("unstarted profile must report no engine and no data: %+v", greg)
	}

	// Its directories exist, so the next start finds them.
	root, _ := config.Dir()
	for _, d := range []string{"chrome-profile", "chrome-profile-kick", "debug"} {
		if _, err := os.Stat(filepath.Join(root, "profiles", "greg", d)); err != nil {
			t.Fatalf("expected profile dir %s: %v", d, err)
		}
	}

	// Engine actions against it are honest about needing a restart.
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("POST", "/api/engine/greg/twitch/start", nil))
	if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "restart") {
		t.Fatalf("start on an unstarted profile should be 400 with a restart hint, got %d %s", rr.Code, rr.Body.String())
	}

	// A bad slug is rejected by the config validator and never reaches the live
	// config.
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, jsonReq("POST", "/api/profiles", `{"name":"Greg Smith"}`))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("invalid profile name should be 400, got %d %s", rr.Code, rr.Body.String())
	}
	if len(b.config().Profiles) != 2 {
		t.Fatalf("rejected profile must not reach the live config: %+v", b.config().Profiles)
	}
}

func TestHTTPProfileEnabledToggle(t *testing.T) {
	b := testBackend(t)
	h := NewHTTPHandler(b)

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, jsonReq("POST", "/api/profiles/enabled", `{"name":"main","on":false}`))
	if rr.Code != 200 {
		t.Fatalf("disable code=%d body=%s", rr.Code, rr.Body.String())
	}
	reloaded, err := config.Load(b.ConfigPath)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded.Profile(config.MainProfileName).Enabled {
		t.Fatal("profile should be persisted as disabled")
	}

	// Disabling is effective on restart: the state must say so rather than
	// pretending the runtime already stopped.
	p := getState(t, h).Profiles[0]
	if p.Enabled || !p.PendingRestart {
		t.Fatalf("disabled profile should read disabled + pendingRestart: %+v", p)
	}

	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, jsonReq("POST", "/api/profiles/enabled", `{"name":"nobody","on":true}`))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("unknown profile toggle should be 400, got %d", rr.Code)
	}
}

func TestHTTPDebug(t *testing.T) {
	b := testBackend(t)
	h := NewHTTPHandler(b)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("GET", "/api/debug", nil))
	if rr.Code != 200 {
		t.Fatalf("debug code=%d", rr.Code)
	}
	var dbg struct {
		Profiles map[string]struct {
			DisplayName string `json:"displayName"`
			Enabled     bool   `json:"enabled"`
			Twitch      struct {
				Enabled bool `json:"enabled"`
				Up      bool `json:"up"`
			} `json:"twitch"`
			Kick struct {
				Enabled bool `json:"enabled"`
				Up      bool `json:"up"`
			} `json:"kick"`
		} `json:"profiles"`
		Paths struct {
			AppRoot  string                       `json:"appRoot"`
			Config   string                       `json:"config"`
			Log      string                       `json:"log"`
			Profiles map[string]map[string]string `json:"profiles"`
		} `json:"paths"`
		LogTail []string `json:"logTail"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &dbg); err != nil {
		t.Fatalf("decode debug: %v", err)
	}
	main, ok := dbg.Profiles[config.MainProfileName]
	if !ok || main.Twitch.Up {
		t.Fatalf("main profile debug section missing or up with nil engine: %+v", dbg.Profiles)
	}
	if main.Kick.Enabled {
		t.Fatalf("kick should read disabled by default: %+v", main)
	}
	if dbg.Paths.Config == "" {
		t.Fatalf("expected the shared config path: %+v", dbg.Paths)
	}
	mp := dbg.Paths.Profiles[config.MainProfileName]
	if mp["database"] == "" || mp["chromeProfile"] == "" || mp["kickChromeProfile"] == "" || mp["debugDir"] == "" {
		t.Fatalf("expected per-profile paths: %+v", dbg.Paths.Profiles)
	}
}

func jsonReq(method, path, body string) *http.Request {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	return r
}
