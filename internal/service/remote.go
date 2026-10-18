package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"sync"

	"github.com/rs/zerolog/log"
	"golang.org/x/net/websocket"

	"github.com/rake-pro/dropwarden/internal/browser"
	"github.com/rake-pro/dropwarden/internal/domain"
)

// Remote login: GET /api/remote/{profile}/{platform}/ws mirrors that
// runtime's watcher tab to a dashboard in another browser, so the headless
// service's Chrome (no screen, no VNC) can still be logged in by hand.
//
// Server -> client: a text JSON {type:"meta", width, height} whenever the
// viewport size changes, then binary JPEG frames. Client -> server: text JSON
// browser.InputEvent values, replayed on the tab over CDP.

// remoteMeta is the frame-size message sent ahead of the first frame and on
// every size change.
type remoteMeta struct {
	Type   string `json:"type"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

// newRemoteHandler serves the mirror WebSocket. One mirror per (profile,
// platform) at a time: a second client gets 409 until the first disconnects.
func newRemoteHandler(b *Backend) http.Handler {
	var (
		mu   sync.Mutex
		busy = map[string]bool{}
	)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !b.Remote() {
			http.NotFound(w, r)
			return
		}
		profile, plat := r.PathValue("profile"), domain.Platform(r.PathValue("platform"))
		if plat != domain.PlatformTwitch && plat != domain.PlatformKick {
			http.Error(w, fmt.Sprintf("unknown platform %q", plat), http.StatusBadRequest)
			return
		}
		pb := b.Profile(profile)
		if pb == nil {
			http.Error(w, fmt.Sprintf("unknown profile %q", profile), http.StatusNotFound)
			return
		}
		// A nil session (runtime down) fails the assertion too.
		m, ok := pb.Session(plat).(browser.Mirror)
		if !ok {
			http.Error(w, "this browser session cannot be mirrored (is it running?)", http.StatusNotFound)
			return
		}
		key := profile + "/" + string(plat)
		mu.Lock()
		if busy[key] {
			mu.Unlock()
			http.Error(w, "this window is already open in another remote login view", http.StatusConflict)
			return
		}
		busy[key] = true
		mu.Unlock()
		defer func() {
			mu.Lock()
			delete(busy, key)
			mu.Unlock()
		}()

		opts := browser.MirrorOptions{
			MaxWidth:  queryInt(r, "maxWidth", 1280, 160, 3840),
			MaxHeight: queryInt(r, "maxHeight", 800, 120, 2160),
		}
		srv := websocket.Server{
			Handshake: sameOrigin,
			Handler:   func(ws *websocket.Conn) { serveMirror(ws, m, opts, key) },
		}
		srv.ServeHTTP(w, r)
	})
}

// sameOrigin rejects a cross-site WebSocket: the Origin header must name the
// host the dashboard was served from.
func sameOrigin(cfg *websocket.Config, r *http.Request) error {
	o := r.Header.Get("Origin")
	if o == "" {
		return errors.New("remote: missing Origin header")
	}
	u, err := url.Parse(o)
	if err != nil || u.Host != r.Host {
		return fmt.Errorf("remote: cross-origin request from %q", o)
	}
	cfg.Origin = u
	return nil
}

// serveMirror runs one mirror session until the socket closes or the
// screencast ends (tab gone, Chrome relaunched). It returns only after the
// screencast has stopped, so the next session's start can never race this
// one's stop.
func serveMirror(ws *websocket.Conn, m browser.Mirror, opts browser.MirrorOptions, key string) {
	l := log.With().Str("component", "remote").Str("session", key).Logger()
	ctx, cancel := context.WithCancel(ws.Request().Context())
	defer cancel()

	// Latest frame wins: a slow client skips frames instead of stalling the
	// CDP event loop that delivers them.
	frames := make(chan browser.Frame, 1)
	push := func(f browser.Frame) {
		select {
		case frames <- f:
			return
		default:
		}
		select {
		case <-frames:
		default:
		}
		select {
		case frames <- f:
		default:
		}
	}

	castDone := make(chan struct{})
	go func() {
		defer close(castDone)
		defer cancel()
		if err := m.Screencast(ctx, opts, push); err != nil {
			l.Warn().Err(err).Msg("remote: screencast failed")
		}
	}()

	go func() {
		var w, h int
		for {
			select {
			case <-ctx.Done():
				return
			case f := <-frames:
				if f.Width != w || f.Height != h {
					w, h = f.Width, f.Height
					if err := websocket.JSON.Send(ws, remoteMeta{Type: "meta", Width: w, Height: h}); err != nil {
						cancel()
						return
					}
				}
				if err := websocket.Message.Send(ws, f.JPEG); err != nil {
					cancel()
					return
				}
			}
		}
	}()

	// Closing the socket unblocks the receive loop below once the session
	// ends from the other side (screencast over, write failed).
	go func() {
		<-ctx.Done()
		_ = ws.Close()
	}()

	l.Info().Msg("remote: mirror opened")
	for {
		var ev browser.InputEvent
		if err := websocket.JSON.Receive(ws, &ev); err != nil {
			break
		}
		if err := m.DispatchInput(ctx, ev); err != nil {
			l.Debug().Err(err).Str("type", ev.Type).Msg("remote: input not replayed")
		}
	}
	cancel()
	<-castDone
	l.Info().Msg("remote: mirror closed")
}

// queryInt reads an integer query parameter, defaulting when absent or bad
// and clamping to [lo, hi].
func queryInt(r *http.Request, name string, def, lo, hi int) int {
	v, err := strconv.Atoi(r.URL.Query().Get(name))
	if err != nil {
		return def
	}
	return min(max(v, lo), hi)
}
