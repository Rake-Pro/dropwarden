package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/websocket"

	"github.com/rake-pro/dropwarden/internal/browser"
	"github.com/rake-pro/dropwarden/internal/config"
	"github.com/rake-pro/dropwarden/internal/domain"
)

// mirrorSession is a browser.Session that also implements browser.Mirror: it
// sends the scripted frames, then holds the screencast open until ctx ends.
type mirrorSession struct {
	browser.Session
	frames  []browser.Frame
	inputs  chan browser.InputEvent
	stopped chan struct{}
}

func newMirrorSession(frames ...browser.Frame) *mirrorSession {
	return &mirrorSession{frames: frames, inputs: make(chan browser.InputEvent, 8), stopped: make(chan struct{}, 4)}
}

func (m *mirrorSession) Screencast(ctx context.Context, _ browser.MirrorOptions, onFrame func(browser.Frame)) error {
	for _, f := range m.frames {
		onFrame(f)
		time.Sleep(20 * time.Millisecond)
	}
	<-ctx.Done()
	m.stopped <- struct{}{}
	return nil
}

func (m *mirrorSession) DispatchInput(_ context.Context, ev browser.InputEvent) error {
	m.inputs <- ev
	return nil
}

func remoteServer(t *testing.T, sess browser.Session, remote bool) *httptest.Server {
	t.Helper()
	b := testBackend(t)
	b.SetRemote(remote)
	if sess != nil {
		b.FirstProfile().Attach(domain.PlatformTwitch, nil, nil, sess, true)
	}
	srv := httptest.NewServer(NewHTTPHandler(b))
	t.Cleanup(srv.Close)
	return srv
}

const remotePath = "/api/remote/" + config.MainProfileName + "/twitch/ws"

func dialMirror(t *testing.T, srv *httptest.Server) *websocket.Conn {
	t.Helper()
	ws, err := websocket.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+remotePath, "", srv.URL)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	return ws
}

func getStatus(t *testing.T, url string) int {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func TestRemoteMirrorUnavailable(t *testing.T) {
	cases := []struct {
		name   string
		sess   browser.Session
		remote bool
		path   string
		want   int
	}{
		{"not the headless service", newMirrorSession(), false, remotePath, http.StatusNotFound},
		{"runtime down", nil, true, remotePath, http.StatusNotFound},
		{"session cannot mirror", &probeSession{}, true, remotePath, http.StatusNotFound},
		{"unknown profile", newMirrorSession(), true, "/api/remote/nobody/twitch/ws", http.StatusNotFound},
		{"unknown platform", newMirrorSession(), true, "/api/remote/" + config.MainProfileName + "/youtube/ws", http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := remoteServer(t, tc.sess, tc.remote)
			if got := getStatus(t, srv.URL+tc.path); got != tc.want {
				t.Fatalf("status = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestRemoteMirrorStreamsFramesAndForwardsInput(t *testing.T) {
	sess := newMirrorSession(
		browser.Frame{JPEG: []byte("jpeg-1"), Width: 1280, Height: 720},
		browser.Frame{JPEG: []byte("jpeg-2"), Width: 1280, Height: 720},
		browser.Frame{JPEG: []byte("jpeg-3"), Width: 800, Height: 600},
	)
	srv := remoteServer(t, sess, true)
	ws := dialMirror(t, srv)
	_ = ws.SetDeadline(time.Now().Add(5 * time.Second))

	// Expected stream: meta 1280x720, jpeg-1, jpeg-2, meta 800x600, jpeg-3.
	// Latest-frame-wins may drop a frame under load, so check order and the
	// meta placement rather than an exact transcript.
	var msgs []string
	for len(msgs) == 0 || msgs[len(msgs)-1] != "jpeg-3" {
		var b []byte
		if err := websocket.Message.Receive(ws, &b); err != nil {
			t.Fatalf("receive after %q: %v", msgs, err)
		}
		if strings.HasPrefix(string(b), "{") {
			var meta remoteMeta
			if err := json.Unmarshal(b, &meta); err != nil || meta.Type != "meta" {
				t.Fatalf("bad meta %q: %v", b, err)
			}
			msgs = append(msgs, fmt.Sprintf("meta %dx%d", meta.Width, meta.Height))
			continue
		}
		msgs = append(msgs, string(b))
	}
	if msgs[0] != "meta 1280x720" || len(msgs) < 3 || msgs[len(msgs)-2] != "meta 800x600" {
		t.Fatalf("stream = %q", msgs)
	}
	for i, m := range msgs[1 : len(msgs)-2] {
		if !strings.HasPrefix(m, "jpeg-") || (i > 0 && m <= msgs[i]) {
			t.Fatalf("frames out of order or meta misplaced: %q", msgs)
		}
	}

	in := browser.InputEvent{Type: "mousedown", X: 0.5, Y: 0.5, Button: 0, Buttons: 1, Modifiers: 8}
	if err := websocket.JSON.Send(ws, in); err != nil {
		t.Fatal(err)
	}
	select {
	case ev := <-sess.inputs:
		if ev != in {
			t.Fatalf("forwarded %+v, want %+v", ev, in)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("input not forwarded")
	}

	// A second client is turned away while the first is connected.
	if got := getStatus(t, srv.URL+remotePath); got != http.StatusConflict {
		t.Fatalf("second client status = %d, want 409", got)
	}

	// Closing the socket stops the screencast and frees the slot.
	ws.Close()
	select {
	case <-sess.stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("screencast not stopped after the socket closed")
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		ws2, err := websocket.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+remotePath, "", srv.URL)
		if err == nil {
			ws2.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("reconnect after close: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestRemoteMirrorRejectsCrossOrigin(t *testing.T) {
	srv := remoteServer(t, newMirrorSession(), true)
	if _, err := websocket.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+remotePath, "", "http://evil.example"); err == nil {
		t.Fatal("cross-origin dial must fail")
	}
}
