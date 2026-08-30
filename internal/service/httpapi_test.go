package service

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rake-pro/dropwarden/internal/domain"
)

func TestHTTPStateAndActions(t *testing.T) {
	b := testBackend(t)
	// Seed some data.
	c := domain.Campaign{ID: "c1", GameName: "Rust", Status: domain.StatusActive,
		Drops: []domain.Drop{{ID: "d1", CampaignID: "c1", Name: "Skin", RequiredMinutes: 60}}}
	b.Store.UpsertCampaign(c)
	b.Store.UpsertQueueItem(domain.QueueItem{DropID: "d1", CampaignID: "c1", Enabled: true, Status: domain.ItemPending})

	h := NewHTTPHandler(b)

	// GET /api/state
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("GET", "/api/state", nil))
	if rr.Code != 200 {
		t.Fatalf("state code=%d", rr.Code)
	}
	var state struct {
		Platforms []struct {
			Platform string `json:"platform"`
			Enabled  bool   `json:"enabled"`
			Up       bool   `json:"up"`
		} `json:"platforms"`
		Queue  []QueueItemDTO `json:"queue"`
		Config ConfigDTO      `json:"config"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &state); err != nil {
		t.Fatalf("decode state: %v", err)
	}
	if len(state.Queue) != 1 || state.Queue[0].DropName != "Skin" {
		t.Fatalf("bad queue in state: %+v", state.Queue)
	}
	if state.Config.Volume != 0.01 {
		t.Fatalf("bad config in state: %+v", state.Config)
	}
	if len(state.Platforms) != 2 || state.Platforms[0].Platform != "twitch" || state.Platforms[1].Platform != "kick" {
		t.Fatalf("bad platforms in state: %+v", state.Platforms)
	}
	if !state.Platforms[0].Enabled || state.Platforms[0].Up {
		t.Fatalf("twitch should be enabled but not up with nil engine: %+v", state.Platforms[0])
	}
	if state.Platforms[1].Enabled || state.Platforms[1].Up {
		t.Fatalf("kick should be disabled by default: %+v", state.Platforms[1])
	}

	// POST /api/queue/priority (valid) - persists config, engine nil is fine.
	rr = httptest.NewRecorder()
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

	// POST /api/engine/twitch/start with nil engine -> 400 "not running".
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("POST", "/api/engine/twitch/start", nil))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("start with nil engine should be 400, got %d", rr.Code)
	}

	// Kick disabled -> 400 with a settings hint; bogus platform/action -> 400.
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("POST", "/api/engine/kick/start", nil))
	if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "disabled") {
		t.Fatalf("kick-disabled start should be 400 with hint, got %d %s", rr.Code, rr.Body.String())
	}
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("POST", "/api/engine/mixer/start", nil))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("unknown platform should be 400, got %d", rr.Code)
	}
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("POST", "/api/engine/twitch/dance", nil))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("unknown action should be 400, got %d", rr.Code)
	}

	// POST /api/queue/enabled toggles the item.
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, jsonReq("POST", "/api/queue/enabled", `{"dropID":"d1","on":false}`))
	if rr.Code != 200 {
		t.Fatalf("enabled code=%d", rr.Code)
	}
	q, _ := b.Store.GetQueue()
	if q[0].Enabled {
		t.Fatal("item should be disabled after toggle")
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
		Platforms map[string]struct {
			Enabled bool `json:"enabled"`
			Up      bool `json:"up"`
		} `json:"platforms"`
		Paths   map[string]string `json:"paths"`
		LogTail []string          `json:"logTail"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &dbg); err != nil {
		t.Fatalf("decode debug: %v", err)
	}
	tw, ok := dbg.Platforms["twitch"]
	if !ok || tw.Up {
		t.Fatalf("twitch debug section missing or up with nil engine: %+v", dbg.Platforms)
	}
	if _, ok := dbg.Platforms["kick"]; !ok {
		t.Fatalf("kick debug section missing: %+v", dbg.Platforms)
	}
	if dbg.Paths["config"] == "" || dbg.Paths["kickChromeProfile"] == "" {
		t.Fatalf("expected config + kick profile paths in debug paths: %+v", dbg.Paths)
	}
}

func jsonReq(method, path, body string) *http.Request {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	return r
}
