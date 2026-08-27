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
		Connection map[string]bool `json:"connection"`
		Queue      []QueueItemDTO  `json:"queue"`
		Config     ConfigDTO       `json:"config"`
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
	if state.Connection["chrome"] {
		t.Fatal("chrome should be false with nil engine")
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

	// POST /api/watch/start with nil engine -> 400 "watcher not running".
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("POST", "/api/watch/start", nil))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("start with nil engine should be 400, got %d", rr.Code)
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

func jsonReq(method, path, body string) *http.Request {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	return r
}
