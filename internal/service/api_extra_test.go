package service

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rake-pro/dropwarden/internal/browser"
	"github.com/rake-pro/dropwarden/internal/domain"
	"github.com/rake-pro/dropwarden/internal/watcher"
)

func TestHTTPGames(t *testing.T) {
	b := testBackend(t)
	h := NewHTTPHandler(b)
	get := func() []GameDTO {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest("GET", "/api/games?profile=main", nil))
		if rr.Code != 200 {
			t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
		}
		var out []GameDTO
		if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	if got := get(); len(got) != 0 {
		t.Fatalf("before discovery: %+v", got)
	}

	cfg := b.config()
	cfg.Profiles[0].Games = []string{"rust"} // the profile's own list is what Tracked reads
	st := b.Profile("main").Store
	end1 := time.Now().Add(48 * time.Hour).UTC().Truncate(time.Second)
	end2 := time.Now().Add(96 * time.Hour).UTC().Truncate(time.Second)
	for _, c := range []domain.Campaign{
		{ID: "t1", Platform: domain.PlatformTwitch, Name: "a", GameName: "Rust", Status: domain.StatusActive, EndAt: end1},
		{ID: "k1", Platform: domain.PlatformKick, Name: "b", GameName: "Rust", Status: domain.StatusActive, EndAt: end2},
		{ID: "t2", Platform: domain.PlatformTwitch, Name: "c", GameName: "Apex Legends", Status: domain.StatusActive, EndAt: end1},
		{ID: "old", Platform: domain.PlatformTwitch, Name: "d", GameName: "Gone", Status: domain.StatusActive, EndAt: time.Now().Add(-time.Hour)},
	} {
		if err := st.UpsertCampaign(c); err != nil {
			t.Fatal(err)
		}
	}
	got := get()
	if len(got) != 2 || got[0].Game != "Apex Legends" || got[1].Game != "Rust" {
		t.Fatalf("games = %+v", got)
	}
	rust := got[1]
	if rust.Campaigns != 2 || strings.Join(rust.Platforms, ",") != "twitch,kick" || !rust.Tracked || rust.EndsAt != end2.Format(time.RFC3339) {
		t.Errorf("rust = %+v", rust)
	}
	if got[0].Tracked || strings.Join(got[0].Platforms, ",") != "twitch" {
		t.Errorf("apex = %+v", got[0])
	}
}

func TestAllowedOpenURL(t *testing.T) {
	for raw, ok := range map[string]bool{
		"https://www.twitch.tv/drops/inventory": true,
		"https://twitch.tv/login":               true,
		"https://kick.com/xqc":                  true,
		"https://dashboard.kick.com/x":          true,
		"http://www.twitch.tv/":                 false,
		"https://twitch.tv.evil.com/":           false,
		"https://eviltwitch.tv/":                false,
		"https://example.com/":                  false,
		"javascript:alert(1)":                   false,
		"file:///etc/passwd":                    false,
		"https://user@twitch.tv/":               false,
		"":                                      false,
	} {
		if _, err := allowedOpenURL(raw); (err == nil) != ok {
			t.Errorf("allowedOpenURL(%q) err=%v, want ok=%v", raw, err, ok)
		}
	}
}

func TestHTTPOpenFallsBackToSystemBrowser(t *testing.T) {
	var opened []string
	oldSys, oldSess := openSystem, openInSession
	openSystem = func(u string) error { opened = append(opened, u); return nil }
	openInSession = func(context.Context, browser.Session, string) error { return errors.New("no window") }
	t.Cleanup(func() { openSystem, openInSession = oldSys, oldSess })

	h := NewHTTPHandler(testBackend(t))
	post := func(body string) (int, string) {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest("POST", "/api/open", strings.NewReader(body)))
		return rr.Code, strings.TrimSpace(rr.Body.String())
	}
	if code, body := post(`{"profile":"main","platform":"twitch","url":"https://www.twitch.tv/drops/inventory"}`); code != 200 || body != "{}" {
		t.Fatalf("allowed: code=%d body=%s", code, body)
	}
	if code, _ := post(`{"profile":"main","platform":"twitch","url":"https://example.com/"}`); code != 400 {
		t.Fatalf("disallowed host: code=%d", code)
	}
	if len(opened) != 1 || opened[0] != "https://www.twitch.tv/drops/inventory" {
		t.Errorf("opened = %v", opened)
	}
}

func TestHTTPOpenRefusedInRemoteMode(t *testing.T) {
	opened := 0
	oldSys, oldSess := openSystem, openInSession
	openSystem = func(string) error { opened++; return nil }
	openInSession = func(context.Context, browser.Session, string) error { opened++; return nil }
	t.Cleanup(func() { openSystem, openInSession = oldSys, oldSess })

	b := testBackend(t)
	b.SetRemote(true)
	h := NewHTTPHandler(b)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("POST", "/api/open", strings.NewReader(`{"profile":"main","platform":"twitch","url":"https://www.twitch.tv/drops/inventory"}`)))
	if rr.Code != 400 || !strings.Contains(rr.Body.String(), "own browser") || opened != 0 {
		t.Fatalf("code=%d body=%s opened=%d", rr.Code, strings.TrimSpace(rr.Body.String()), opened)
	}
}

func TestQueueAction(t *testing.T) {
	c := domain.Campaign{AccountLinkURL: "https://www.twitch.tv/link/game"}
	for _, tc := range []struct {
		it        domain.QueueItem
		url, text string
	}{
		{domain.QueueItem{Status: domain.ItemNeedsAction, Reason: "account link required"}, c.AccountLinkURL, "Link account"},
		{domain.QueueItem{Status: domain.ItemNeedsAction, Reason: "completed - claim it manually in the Twitch inventory"}, watcher.URLInventoryClaim, "Claim on Twitch"},
		{domain.QueueItem{Status: domain.ItemNeedsAction, Reason: "Twitch session expired - log in again"}, "https://www.twitch.tv/login", "Log in"},
		{domain.QueueItem{Platform: domain.PlatformKick, Status: domain.ItemNeedsAction, Reason: "watch complete - claim it in the Kick inventory"}, "https://kick.com/drops/inventory", "Claim on Kick"},
		{domain.QueueItem{Status: domain.ItemNeedsAction, Reason: "reward already owned"}, "", ""},
		{domain.QueueItem{Status: domain.ItemPending, Reason: "account link required"}, "", ""},
	} {
		u, l := queueAction(tc.it, c)
		if u != tc.url || l != tc.text {
			t.Errorf("%q: got (%q, %q), want (%q, %q)", tc.it.Reason, u, l, tc.url, tc.text)
		}
	}
}

func TestDiagnosticsRedaction(t *testing.T) {
	b := testBackend(t)
	cfg := b.config()
	cfg.Auth.Stored.Username = "secretuser42"
	cfg.Auth.Stored.PasswordFile = "/very/private/pw.txt"
	cfg.Browser.ExtraFlags = []string{"oauth:abcdefghijklmnop", "abcdefghijklmnopqrstuvwxyz0123456789"}
	h := NewHTTPHandler(b)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("GET", "/api/diagnostics", nil))
	body := rr.Body.String()
	if rr.Code != 200 || !strings.HasPrefix(rr.Header().Get("Content-Type"), "text/plain") {
		t.Fatalf("code=%d type=%s", rr.Code, rr.Header().Get("Content-Type"))
	}
	for _, leak := range []string{"secretuser42", "/very/private/pw.txt", "oauth:abcdefghijklmnop", "abcdefghijklmnopqrstuvwxyz0123456789"} {
		if strings.Contains(body, leak) {
			t.Errorf("diagnostics leaks %q", leak)
		}
	}
	for _, want := range []string{"version:", "chrome: found=", "==== update ====", "==== config (redacted) ====", "username: \"\"", "==== profile main", "==== log (last 400 lines) ===="} {
		if !strings.Contains(body, want) {
			t.Errorf("diagnostics missing %q", want)
		}
	}
}

func TestConfigErrorField(t *testing.T) {
	b := testBackend(t)
	h := NewHTTPHandler(b)
	post := func(dto ConfigDTO) map[string]string {
		raw, _ := json.Marshal(dto)
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest("POST", "/api/config", strings.NewReader(string(raw))))
		out := map[string]string{}
		json.Unmarshal(rr.Body.Bytes(), &out)
		if rr.Code != 400 {
			t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
		}
		return out
	}
	s := NewSettingsService(b)
	d := s.Load()
	d.UpdateIntervalHours = ptr(0)
	if got := post(d); got["field"] != "updateIntervalHours" || got["error"] == "" {
		t.Errorf("interval: %v", got)
	}
	d = s.Load()
	d.Volume = 0
	if got := post(d); got["field"] != "volume" {
		t.Errorf("volume: %v", got)
	}
	d = s.Load()
	d.UpdateFeedURL = ptr("ftp://x")
	if got := post(d); got["field"] != "updateFeedURL" {
		t.Errorf("feed: %v", got)
	}
	d = s.Load()
	d.Theme = "neon"
	if got := post(d); got["field"] != "theme" {
		t.Errorf("theme: %v", got)
	}
	if f := configErrorField(errors.New("config: watcher.timing_jitter 2 must be a fraction")); f != "" {
		t.Errorf("unmapped key got field %q", f)
	}
	if f := configErrorField(errors.New("config: validate temp: config: logging.level \"x\" must be")); f != "logLevel" {
		t.Errorf("wrapped key got field %q", f)
	}
}

func TestExportDiagnosticsZip(t *testing.T) {
	b := testBackend(t)
	b.config().Auth.Stored.Username = "secretuser42"
	pp, err := b.config().ProfilePaths("main")
	if err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(pp.Debug, 0o700)
	os.WriteFile(filepath.Join(pp.Debug, "inventory-buttons.json"), []byte(`{}`), 0o600)
	os.WriteFile(filepath.Join(pp.Debug, "page.html"), []byte(`<html>`), 0o600)
	os.WriteFile(filepath.Join(pp.Debug, "huge.json"), make([]byte, diagMaxFile+1), 0o600)
	path := filepath.Join(t.TempDir(), "d.zip")
	if err := b.writeDiagnosticsZip(path); err != nil {
		t.Fatal(err)
	}
	zr, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	names := map[string]bool{}
	for _, f := range zr.File {
		names[f.Name] = true
		if f.Name == "config.yaml" {
			rc, _ := f.Open()
			raw, _ := io.ReadAll(rc)
			rc.Close()
			if strings.Contains(string(raw), "secretuser42") {
				t.Error("config.yaml leaks the username")
			}
		}
	}
	for _, want := range []string{"diagnostics.txt", "config.yaml", "debug/main/inventory-buttons.json"} {
		if !names[want] {
			t.Errorf("zip missing %s: %v", want, names)
		}
	}
	for _, bad := range []string{"debug/main/page.html", "debug/main/huge.json"} {
		if names[bad] {
			t.Errorf("zip must skip %s", bad)
		}
	}
}

func TestProfileAnyGameKickRoundTrip(t *testing.T) {
	b := testBackend(t)
	s := NewSettingsService(b)
	d := s.Profiles()[0]
	d.AnyGame, d.KickEnabled = true, true
	if err := s.UpdateProfile(d); err != nil {
		t.Fatal(err)
	}
	if got := s.Profiles()[0]; !got.AnyGame || !got.KickEnabled {
		t.Fatalf("anyGame/kickEnabled not saved: %+v", got)
	}
}

// The catalogue routes reject bad input before any network call.
func TestHTTPGamesCatalogSearchValidation(t *testing.T) {
	h := NewHTTPHandler(testBackend(t))
	for _, path := range []string{
		"/api/games/catalog?profile=nope",
		"/api/games/search?q=a",
		"/api/games/search?q=%20%20",
		"/api/games/search?q=" + strings.Repeat("x", 61),
	} {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest("GET", path, nil))
		if rr.Code != 400 || !strings.Contains(rr.Body.String(), `"error"`) {
			t.Errorf("%s: code=%d body=%s", path, rr.Code, rr.Body.String())
		}
	}
}

// TestStateCarriesFrontendFields pins the /api/state and /api/update shapes
// to the fields frontend/app.js reads, so a renamed JSON tag fails here
// instead of rendering blanks.
func TestStateCarriesFrontendFields(t *testing.T) {
	b := testBackend(t)
	st := b.Profile("main").Store
	c := domain.Campaign{ID: "c1", GameName: "Rust", Status: domain.StatusActive,
		Drops: []domain.Drop{{ID: "d1", CampaignID: "c1", Name: "Skin", RequiredMinutes: 60}}}
	if err := st.UpsertCampaign(c); err != nil {
		t.Fatal(err)
	}
	st.UpsertQueueItem(domain.QueueItem{DropID: "d1", CampaignID: "c1", Enabled: true, Status: domain.ItemPending})
	st.AppendClaim(domain.Claim{DropID: "d0", DropName: "Old", GameName: "Rust"})

	h := NewHTTPHandler(b)
	get := func(path string) map[string]any {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest("GET", path, nil))
		if rr.Code != 200 {
			t.Fatalf("%s: code=%d", path, rr.Code)
		}
		var m map[string]any
		if err := json.Unmarshal(rr.Body.Bytes(), &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	need := func(where string, m map[string]any, keys ...string) {
		t.Helper()
		for _, k := range keys {
			if _, ok := m[k]; !ok {
				t.Errorf("%s: missing %q", where, k)
			}
		}
	}
	updateKeys := []string{"currentVersion", "promptSeq", "phase", "pending", "bytesTotal", "bytesDone", "percent",
		"failedVersion", "showChangelog", "devBuild", "failMessage", "deferredReason", "status", "lastError", "lastCheck", "installedNotes"}

	state := get("/api/state")
	need("state", state, "config", "host", "profiles", "update", "version")
	need("config", state["config"].(map[string]any), "theme", "volume", "forceLowestQuality", "muteAudio", "startMinimized", "alwaysOnBottom",
		"chromePath", "claimButtonSelector", "claimButtonText", "captureGqlBodies",
		"updateEnabled", "updateAutoInstall", "updateShowChangelog", "updateFeedURL", "updateIntervalHours")
	// Per-profile settings ride on the profile entry, not on config.
	need("profiles[0]", state["profiles"].([]any)[0].(map[string]any), "name", "displayName", "enabled", "games", "anyGame",
		"priorityMode", "preferChannelSpecific", "autoEnqueueNew", "fallThrough", "skipAccountLink", "platforms", "queue")
	need("host", state["host"].(map[string]any), "os", "chromeFound", "chromePath", "remote", "updates")
	u := state["update"].(map[string]any)
	need("state.update", u, updateKeys...)
	if notes, ok := u["installedNotes"].([]any); !ok || notes == nil {
		t.Errorf("installedNotes must be an array, got %#v", u["installedNotes"])
	}
	need("/api/update", get("/api/update"), updateKeys...)

	prof := state["profiles"].([]any)[0].(map[string]any)
	need("profile", prof, "name", "displayName", "enabled", "platforms", "queue", "claims", "stats", "spark")
	for _, p := range prof["platforms"].([]any) {
		need("platform", p.(map[string]any), "platform", "enabled", "up", "needsLogin", "wanted", "launchError", "reason", "status")
	}
	need("queue", prof["queue"].([]any)[0].(map[string]any), "dropID", "platform", "dropName", "game", "position", "status",
		"enabled", "pin", "alongside", "currentMinutes", "requiredMinutes", "reason", "needsUser", "actionURL", "actionLabel", "owned", "forceWatch")
	need("claim", prof["claims"].([]any)[0].(map[string]any), "platform", "dropName", "game", "claimedAt")
	stats := prof["stats"].(map[string]any)
	need("stats", stats, "dropsEarned", "dropsClaimed", "minutesWatched", "sessions", "successRate", "outcomeCounts", "byPlatform")

	// status is null with no engine up; its shape is watcher.Status.
	raw, _ := json.Marshal(watcher.Status{})
	var sm map[string]any
	_ = json.Unmarshal(raw, &sm)
	need("status", sm, "state", "paused", "channelLogin", "dropName", "current", "required", "game", "reason")
	raw, _ = json.Marshal(PlatformTotalsDTO{})
	var pm map[string]any
	_ = json.Unmarshal(raw, &pm)
	need("byPlatform", pm, "minutesWatched", "dropsClaimed")
}

func TestOpenAllowsCampaignAccountLink(t *testing.T) {
	var opened []string
	oldSys := openSystem
	openSystem = func(u string) error { opened = append(opened, u); return nil }
	t.Cleanup(func() { openSystem = oldSys })

	b := testBackend(t)
	link := "https://example-game.com/link?src=twitch"
	if err := b.Profile("main").Store.UpsertCampaign(domain.Campaign{ID: "c1", GameName: "G", Status: domain.StatusActive, AccountLinkURL: link}); err != nil {
		t.Fatal(err)
	}
	if err := b.OpenURL(context.Background(), "main", "twitch", link); err != nil {
		t.Fatalf("campaign link refused: %v", err)
	}
	for _, bad := range []string{"https://example-game.com/other", "http://example-game.com/link?src=twitch", "https://evil.com/"} {
		if err := b.OpenURL(context.Background(), "main", "twitch", bad); err == nil {
			t.Errorf("%s: allowed", bad)
		}
	}
	if len(opened) != 1 || opened[0] != link {
		t.Errorf("opened = %v", opened)
	}
}

func TestSaveRejectsMissingChromePath(t *testing.T) {
	b := testBackend(t)
	h := NewHTTPHandler(b)
	d := NewSettingsService(b).Load()
	d.ChromePath = filepath.Join(t.TempDir(), "nope", "chrome.exe")
	body, _ := json.Marshal(d)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("POST", "/api/config", strings.NewReader(string(body))))
	var got map[string]string
	_ = json.Unmarshal(rr.Body.Bytes(), &got)
	if rr.Code != 400 || got["field"] != "chromePath" {
		t.Fatalf("code=%d body=%v", rr.Code, got)
	}
}
