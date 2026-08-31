package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/rake-pro/dropwarden/internal/browser"
	"github.com/rake-pro/dropwarden/internal/config"
	"github.com/rake-pro/dropwarden/internal/domain"
	"github.com/rake-pro/dropwarden/internal/kick"
	"github.com/rake-pro/dropwarden/internal/twitch"
)

// stateDTO is the /api/state shape the frontend consumes: shared config plus
// one entry per profile carrying that profile's platforms and data.
type stateDTO struct {
	Config   ConfigDTO `json:"config"`
	Profiles []struct {
		Name        string `json:"name"`
		DisplayName string `json:"displayName"`
		Enabled     bool   `json:"enabled"`
		Platforms   []struct {
			Platform   string `json:"platform"`
			Enabled    bool   `json:"enabled"`
			Up         bool   `json:"up"`
			NeedsLogin bool   `json:"needsLogin"`
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
	if p.Platforms[0].NeedsLogin {
		t.Fatalf("a profile with no runtime up cannot need a login: %+v", p.Platforms[0])
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

	// POST /api/engine/main/twitch/start LAUNCHES the runtime, so it no longer
	// needs a live engine: an enabled platform is accepted (200) and the
	// reconciler brings Chrome up. Skip/refresh still need a live engine (below).
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("POST", "/api/engine/main/twitch/start", nil))
	if rr.Code != 200 {
		t.Fatalf("start on an enabled platform should be accepted (200), got %d %s", rr.Code, rr.Body.String())
	}
	// Skip on a down engine keeps the honest "not running" error.
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("POST", "/api/engine/main/twitch/skip", nil))
	if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "not running") {
		t.Fatalf("skip with nil engine should be 400 not-running, got %d %s", rr.Code, rr.Body.String())
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
	// Enabled + not up is the whole story now: the reconciler (absent in this
	// test backend) is what brings the runtime up, and the UI reads the pair.
	if !greg.Enabled || greg.Platforms[0].Up {
		t.Fatalf("an added profile must read enabled and not yet up: %+v", greg)
	}
	if len(greg.Queue) != 0 {
		t.Fatalf("a profile with no store open must report no data: %+v", greg)
	}

	// Its directories exist, so the next start finds them.
	root, _ := config.Dir()
	for _, d := range []string{"chrome-profile", "chrome-profile-kick", "debug"} {
		if _, err := os.Stat(filepath.Join(root, "profiles", "greg", d)); err != nil {
			t.Fatalf("expected profile dir %s: %v", d, err)
		}
	}

	// Start LAUNCHES: on an added-but-unstarted profile it is accepted (200) and
	// the reconciler opens the store and brings Chrome up. Skip, which needs a
	// live engine, is the one that stays honest about the missing store.
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("POST", "/api/engine/greg/twitch/start", nil))
	if rr.Code != 200 {
		t.Fatalf("start on an added profile should be accepted (200), got %d %s", rr.Code, rr.Body.String())
	}
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("POST", "/api/engine/greg/twitch/skip", nil))
	if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "no store open") {
		t.Fatalf("skip on a profile with no store should be 400 and say so, got %d %s", rr.Code, rr.Body.String())
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

	// Disabling is live: the state reports it off, and the reconciler (wired in
	// the real app) stops its runtime.
	p := getState(t, h).Profiles[0]
	if p.Enabled || p.Platforms[0].Enabled {
		t.Fatalf("disabled profile should read disabled: %+v", p)
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

// recordingReconciler stands in for internal/app: it only records that a
// commit asked for a reconcile pass.
type recordingReconciler struct {
	mu      sync.Mutex
	calls   []*config.Config
	started []string
}

// StartWatching records the run-control call a confirmed login triggers.
func (r *recordingReconciler) StartWatching(profile string, plat domain.Platform) {
	r.mu.Lock()
	r.started = append(r.started, profile+"/"+string(plat.OrTwitch()))
	r.mu.Unlock()
}

func (r *recordingReconciler) startedList() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.started...)
}

func (r *recordingReconciler) Reconcile(cfg *config.Config) {
	r.mu.Lock()
	r.calls = append(r.calls, cfg)
	r.mu.Unlock()
}

func (r *recordingReconciler) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

// TestLiveTogglesReconcile pins the whole point of the lifecycle work: every
// config commit that changes desired runtime state asks the reconciler to
// follow, instead of reporting a pending restart.
func TestLiveTogglesReconcile(t *testing.T) {
	b := testBackend(t)
	rec := &recordingReconciler{}
	b.SetReconciler(rec)
	h := NewHTTPHandler(b)

	cases := []struct {
		name string
		path string
		body string
	}{
		{"root kick toggle", "/api/config", `{"theme":"dark","games":["Rust"],"priorityMode":"top_down","volume":0.01,"kickEnabled":true,"claimStrategy":"in_page","authMode":"manual","logLevel":"info"}`},
		{"add profile", "/api/profiles", `{"name":"greg","displayName":"Greg"}`},
		{"profile kick toggle", "/api/profiles/update", `{"name":"greg","displayName":"Greg","kickEnabled":false,"inheritGames":true}`},
		{"profile enable toggle", "/api/profiles/enabled", `{"name":"greg","on":false}`},
		{"profile remove", "/api/profiles/remove", `{"name":"greg"}`},
	}
	for i, tc := range cases {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, jsonReq("POST", tc.path, tc.body))
		if rr.Code != 200 {
			t.Fatalf("%s: code=%d body=%s", tc.name, rr.Code, rr.Body.String())
		}
		if rec.count() != i+1 {
			t.Fatalf("%s must trigger a reconcile (calls=%d)", tc.name, rec.count())
		}
	}
}

func TestHTTPProfileRemove(t *testing.T) {
	b := testBackend(t)
	h := NewHTTPHandler(b)

	// The only profile cannot be removed.
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, jsonReq("POST", "/api/profiles/remove", `{"name":"main"}`))
	if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "only profile") {
		t.Fatalf("removing the last profile must be refused, got %d %s", rr.Code, rr.Body.String())
	}

	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, jsonReq("POST", "/api/profiles", `{"name":"greg","displayName":"Greg"}`))
	if rr.Code != 200 {
		t.Fatalf("add: %d %s", rr.Code, rr.Body.String())
	}

	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, jsonReq("POST", "/api/profiles/remove", `{"name":"nobody"}`))
	if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "unknown profile") {
		t.Fatalf("removing an unknown profile must be 400, got %d %s", rr.Code, rr.Body.String())
	}

	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, jsonReq("POST", "/api/profiles/remove", `{"name":"greg"}`))
	if rr.Code != 200 {
		t.Fatalf("remove: %d %s", rr.Code, rr.Body.String())
	}
	var out struct {
		OK      bool   `json:"ok"`
		DataDir string `json:"dataDir"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode remove: %v", err)
	}
	root, _ := config.Dir()
	want := filepath.Join(root, "profiles", "greg")
	if !out.OK || out.DataDir != want {
		t.Fatalf("remove must return the untouched data dir %q, got %+v", want, out)
	}
	// The directory itself is NEVER deleted.
	if _, err := os.Stat(filepath.Join(want, "chrome-profile")); err != nil {
		t.Fatalf("a removed profile's data must stay on disk: %v", err)
	}
	if b.config().Profile("greg") != nil {
		t.Fatal("removed profile must be gone from the live config")
	}
	reloaded, err := config.Load(b.ConfigPath)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded.Profile("greg") != nil {
		t.Fatal("removed profile must be gone from the saved config")
	}
	if len(getState(t, h).Profiles) != 1 {
		t.Fatal("state must stop listing the removed profile")
	}
}

// probeSession is a browser.Session that answers only the login probe.
type probeSession struct {
	browser.Session
	loggedIn  bool
	kickState string // kick.LoginProbe state: "in" | "out" | "unknown" | ""
}

func (p *probeSession) Navigate(context.Context, string) error { return nil }
func (p *probeSession) Eval(_ context.Context, _ string, out any) error {
	if st, ok := out.(*twitch.LoginState); ok {
		st.LoggedIn = p.loggedIn
	}
	if lp, ok := out.(*kick.LoginProbe); ok {
		lp.State = p.kickState
	}
	return nil
}

func TestHTTPLoginCheck(t *testing.T) {
	b := testBackend(t)
	rec := &recordingReconciler{}
	b.SetReconciler(rec)
	h := NewHTTPHandler(b)

	loginCheck := func(body string) (int, string) {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, jsonReq("POST", "/api/profiles/login-check", body))
		return rr.Code, strings.TrimSpace(rr.Body.String())
	}

	// No session up for that pair: unknowable, not an error.
	code, body := loginCheck(`{"profile":"main","platform":"twitch"}`)
	if code != 200 || !strings.Contains(body, `"loggedIn":null`) {
		t.Fatalf("no session should report null, got %d %s", code, body)
	}
	if code, body := loginCheck(`{"profile":"main","platform":"mixer"}`); code != http.StatusBadRequest {
		t.Fatalf("unknown platform should be 400, got %d %s", code, body)
	}
	if code, _ := loginCheck(`{"profile":"nobody","platform":"twitch"}`); code != http.StatusBadRequest {
		t.Fatalf("unknown profile should be 400, got %d", code)
	}

	pb := b.FirstProfile()
	// Kick probe: an API status check (kick/auth.go). Unknown stays null and
	// leaves needsLogin alone - never guessed.
	pb.Attach(domain.PlatformKick, nil, nil, &probeSession{kickState: "unknown"}, true)
	if code, body := loginCheck(`{"profile":"main","platform":"kick"}`); code != 200 || !strings.Contains(body, `"loggedIn":null`) {
		t.Fatalf("an unknowable kick probe must report null, got %d %s", code, body)
	}
	if !pb.NeedsLogin(domain.PlatformKick) {
		t.Fatal("an unknowable kick probe must leave needsLogin alone")
	}
	// A definite logged-out answer reports false and keeps the prompt.
	pb.Attach(domain.PlatformKick, nil, nil, &probeSession{kickState: "out"}, true)
	if code, body := loginCheck(`{"profile":"main","platform":"kick"}`); code != 200 || !strings.Contains(body, `"loggedIn":false`) {
		t.Fatalf("a logged-out kick probe must report false, got %d %s", code, body)
	}
	if !pb.NeedsLogin(domain.PlatformKick) {
		t.Fatal("a logged-out kick probe must keep needsLogin set")
	}
	// A confirmed kick login clears the prompt and flows into watching, same
	// as twitch.
	pb.Attach(domain.PlatformKick, nil, nil, &probeSession{kickState: "in"}, true)
	if code, body := loginCheck(`{"profile":"main","platform":"kick"}`); code != 200 || !strings.Contains(body, `"loggedIn":true`) {
		t.Fatalf("a logged-in kick probe must report true, got %d %s", code, body)
	}
	if pb.NeedsLogin(domain.PlatformKick) {
		t.Fatal("a confirmed kick login must clear needsLogin")
	}
	if got := rec.startedList(); len(got) != 1 || got[0] != "main/kick" {
		t.Fatalf("a confirmed kick login must trigger StartWatching, got %v", got)
	}

	pb.Attach(domain.PlatformTwitch, nil, nil, &probeSession{loggedIn: false}, true)
	if code, body := loginCheck(`{"profile":"main","platform":"twitch"}`); code != 200 || !strings.Contains(body, `"loggedIn":false`) {
		t.Fatalf("a logged-out session should report false, got %d %s", code, body)
	}
	if !pb.NeedsLogin(domain.PlatformTwitch) {
		t.Fatal("a logged-out probe must keep needsLogin set")
	}

	pb.Attach(domain.PlatformTwitch, nil, nil, &probeSession{loggedIn: true}, true)
	if code, body := loginCheck(`{"profile":"main","platform":"twitch"}`); code != 200 || !strings.Contains(body, `"loggedIn":true`) {
		t.Fatalf("a logged-in session should report true, got %d %s", code, body)
	}
	if pb.NeedsLogin(domain.PlatformTwitch) {
		t.Fatal("a successful login check must clear needsLogin")
	}
	if getState(t, h).Profiles[0].Platforms[0].NeedsLogin {
		t.Fatal("state must mirror the cleared needsLogin flag")
	}
	// A confirmed first login flows straight into watching (launch-on-demand:
	// a logged-in window parked with no watcher must not linger open).
	if got := rec.startedList(); len(got) != 2 || got[1] != "main/twitch" {
		t.Fatalf("a confirmed login must trigger StartWatching, got %v", got)
	}
}
