package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rake-pro/dropwarden/internal/update"
	"github.com/rake-pro/dropwarden/internal/watcher"
)

// feedServer serves a manifest for ver (404 when ver is "", 500 when "500")
// with this platform's asset carrying payload and the given checksum.
func feedServer(t *testing.T, ver string, payload []byte, sum string) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case ver == "":
			http.NotFound(w, r)
		case ver == "500":
			http.Error(w, "boom", http.StatusInternalServerError)
		case strings.HasSuffix(r.URL.Path, "/asset"):
			w.Write(payload)
		default:
			json.NewEncoder(w).Encode(update.Manifest{Version: ver, Notes: "## New\n- thing", Assets: []update.ManifestAsset{{
				Name: update.AssetName(ver), URL: srv.URL + "/asset", SHA256: sum, Size: int64(len(payload)),
			}}})
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

type ctlHarness struct {
	c      *UpdateController
	mu     sync.Mutex
	phases []string
	shows  int
	quit   chan struct{}
}

func (h *ctlHarness) seen() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.phases...)
}

func newCtl(t *testing.T, ver, feed string, apply func(string) (string, error)) *ctlHarness {
	t.Helper()
	b := testBackend(t)
	cfg := b.config()
	cfg.Update.FeedURL = &feed
	h := &ctlHarness{quit: make(chan struct{}, 1)}
	h.c = NewUpdateController(UpdateOptions{
		Ctx: context.Background(), Config: b.config, Backend: b, Version: ver,
		Apply: apply, RestartDelay: time.Millisecond,
		Changed: func() {
			p := h.c.UpdateState().Phase
			h.mu.Lock()
			if n := len(h.phases); n == 0 || h.phases[n-1] != p {
				h.phases = append(h.phases, p)
			}
			h.mu.Unlock()
		},
	})
	h.c.SetWindowHooks(func() { h.quit <- struct{}{} }, func() { h.mu.Lock(); h.shows++; h.mu.Unlock() })
	return h
}

func TestUpdateControllerDevBuild(t *testing.T) {
	h := newCtl(t, "0.0.0-dev", "https://example.com/latest.json", nil)
	if _, err := h.c.CheckNow(context.Background()); err == nil || err.Error() != "dev build, updates disabled" {
		t.Fatalf("CheckNow err = %v", err)
	}
	if err := h.c.InstallNow(); err == nil || err.Error() != "dev build, updates disabled" {
		t.Fatalf("InstallNow err = %v", err)
	}
	st := h.c.UpdateState()
	if !st.DevBuild || st.Status != UpdateStatusIdle || st.Phase != UpdatePhaseIdle || st.Percent != -1 {
		t.Fatalf("state = %+v", st)
	}
}

func TestUpdateControllerStatuses(t *testing.T) {
	for _, tc := range []struct {
		feedVer, want string
		wantErr       bool
	}{
		{"", UpdateStatusNonePublished, false},
		{"500", UpdateStatusError, true},
		{"1.0.0", UpdateStatusUpToDate, false},
		{"1.1.0", UpdateStatusAvailable, false},
	} {
		srv := feedServer(t, tc.feedVer, nil, "")
		h := newCtl(t, "1.0.0", srv.URL+"/latest.json", nil)
		st, err := h.c.CheckNow(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if st.Status != tc.want || (st.LastError != "") != tc.wantErr || st.LastCheck == nil || st.Phase != UpdatePhaseIdle {
			t.Errorf("feed %q: state = %+v", tc.feedVer, st)
		}
		if st.InstalledNotes == nil || (len(st.InstalledNotes) > 0) != (tc.feedVer == "1.0.0") {
			t.Errorf("feed %q: installedNotes = %+v", tc.feedVer, st.InstalledNotes)
		}
		if tc.want == UpdateStatusAvailable {
			if st.Pending == nil || st.Pending.Version != "1.1.0" || len(st.Pending.Notes) == 0 || st.PromptSeq != 1 || h.shows != 1 {
				t.Errorf("available: state = %+v shows=%d", st, h.shows)
			}
		} else if st.Pending != nil || st.PromptSeq != 0 {
			t.Errorf("feed %q: pending=%v promptSeq=%d", tc.feedVer, st.Pending, st.PromptSeq)
		}
	}
}

func TestUpdateControllerTrayClickPrompts(t *testing.T) {
	srv := feedServer(t, "1.1.0", nil, "")
	installed := false
	h := newCtl(t, "1.0.0", srv.URL+"/latest.json", func(string) (string, error) { installed = true; return "", nil })
	// No pending yet: the tray runs a manual check, which prompts.
	h.c.TrayClick()
	if st := h.c.UpdateState(); st.Status != UpdateStatusAvailable || st.PromptSeq != 1 {
		t.Fatalf("after first click: %+v", st)
	}
	h.c.TrayClick()
	st := h.c.UpdateState()
	if st.PromptSeq != 2 || st.Phase != UpdatePhaseIdle || installed || h.shows != 2 {
		t.Fatalf("tray click must prompt, not install: %+v shows=%d installed=%v", st, h.shows, installed)
	}
	if title, ok := h.c.TrayLabel(); !ok || !strings.Contains(title, "1.1.0") {
		t.Errorf("tray label = %q, %v", title, ok)
	}
}

func TestUpdateControllerInstallPhases(t *testing.T) {
	payload := []byte("new build")
	sum := sha256.Sum256(payload)
	srv := feedServer(t, "1.1.0", payload, hex.EncodeToString(sum[:]))
	applied := make(chan string, 1)
	h := newCtl(t, "1.0.0", srv.URL+"/latest.json", func(a string) (string, error) { applied <- a; return "/new/dropwarden", nil })
	if _, err := h.c.CheckNow(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := h.c.InstallNow(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-h.quit:
	case <-time.After(5 * time.Second):
		t.Fatalf("never quit; phases %v state %+v", h.seen(), h.c.UpdateState())
	}
	<-applied
	got := strings.Join(h.seen(), ",")
	if !strings.Contains(got, "downloading,verifying,installing,restarting") {
		t.Fatalf("phases = %s", got)
	}
	st := h.c.UpdateState()
	if st.Phase != UpdatePhaseRestarting || st.Percent != 100 || st.BytesTotal != int64(len(payload)) {
		t.Errorf("final state = %+v", st)
	}
	if err := h.c.InstallNow(); err == nil {
		t.Error("second install while one is running must be refused")
	}
}

func TestUpdateControllerInstallFailure(t *testing.T) {
	payload := []byte("new build")
	srv := feedServer(t, "1.1.0", payload, strings.Repeat("ab", 32))
	h := newCtl(t, "1.0.0", srv.URL+"/latest.json", func(string) (string, error) { return "", errors.New("unreachable") })
	h.c.CheckNow(context.Background())
	if err := h.c.InstallNow(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for h.c.UpdateState().FailedVersion == "" && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	st := h.c.UpdateState()
	if st.FailedVersion != "1.1.0" || st.FailMessage != "The download did not verify (checksum mismatch)" || st.Phase != UpdatePhaseIdle || st.Status != UpdateStatusAvailable {
		t.Fatalf("state = %+v", st)
	}
	if st.SkippedVersion != "" {
		t.Errorf("a failed manual install must not skip the version: %+v", st)
	}
	// A failed automatic install skips the version (manual install only).
	h.c.mu.Lock()
	rel := h.c.pending
	h.c.mu.Unlock()
	if err := h.c.install(rel, false); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(5 * time.Second)
	for h.c.UpdateState().SkippedVersion == "" && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if st := h.c.UpdateState(); st.SkippedVersion != "1.1.0" || st.Phase != UpdatePhaseIdle {
		t.Fatalf("after auto failure: %+v", st)
	}
}

func TestUpdateControllerLoadFailureChecks(t *testing.T) {
	srv := feedServer(t, "1.1.0", nil, "")
	h := newCtl(t, "1.0.0", srv.URL+"/latest.json", nil)
	dir := t.TempDir()
	if err := update.WriteFailure(dir, "1.1.0", errors.New("install location is not writable: permission denied")); err != nil {
		t.Fatal(err)
	}
	h.c.loadFailureFrom(dir)
	deadline := time.Now().Add(5 * time.Second)
	for h.c.UpdateState().Pending == nil && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	st := h.c.UpdateState()
	if st.Pending == nil || st.SkippedVersion != "1.1.0" || st.FailedVersion != "1.1.0" || st.FailMessage != "The install folder is not writable" || st.Phase != UpdatePhaseIdle {
		t.Fatalf("state = %+v", st)
	}
	if st.PromptSeq != 0 {
		t.Errorf("a background check must not prompt: %+v", st)
	}
}

func TestFriendlyUpdateError(t *testing.T) {
	for raw, want := range map[string]string{
		"install location is not writable: x":                             "The install folder is not writable",
		"update: checksum mismatch for a: got b want c":                   "The download did not verify (checksum mismatch)",
		"update: download a: context deadline exceeded":                   "The download timed out",
		"update: release v1 has no asset \"x\"":                           "update: release v1 has no asset \"x\"",
		"open C:\\x: Access is denied.":                                   "The install folder is not writable",
		"Get \"u\": net/http: request canceled (Client.Timeout exceeded)": "The download timed out",
	} {
		if got := friendlyUpdateError(raw); got != want {
			t.Errorf("friendlyUpdateError(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestWrapEmitterWakesOnWatchEnd(t *testing.T) {
	h := newCtl(t, "1.0.0", "https://example.com/latest.json", nil)
	var got []string
	em := h.c.WrapEmitter(emitFunc(func(ev string, _ any) { got = append(got, ev) }))
	em.Emit(watcher.EvState, watcher.StatePayload{From: watcher.StateIdle, To: watcher.StateOpening})
	select {
	case <-h.c.wake:
		t.Fatal("entering a watch must not wake the deferral")
	default:
	}
	em.Emit(watcher.EvState, watcher.StatePayload{From: watcher.StateWatching, To: watcher.StateCooldown})
	select {
	case <-h.c.wake:
	default:
		t.Fatal("leaving a watch must wake the deferral")
	}
	if len(got) != 2 {
		t.Errorf("events not forwarded: %v", got)
	}
}

type emitFunc func(string, any)

func (f emitFunc) Emit(ev string, p any) { f(ev, p) }
