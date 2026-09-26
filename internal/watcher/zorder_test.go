package watcher

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rake-pro/dropwarden/internal/browser"
	"github.com/rake-pro/dropwarden/internal/domain"
	"github.com/rake-pro/dropwarden/internal/store"
	"github.com/rake-pro/dropwarden/internal/twitch"
	"github.com/rs/zerolog"
)

// bottomSession is a fakeSession that records always_on_bottom pushes.
type bottomSession struct {
	*fakeSession
	pushes atomic.Int32
	err    error
}

func (b *bottomSession) SendToBottom(context.Context) error {
	b.pushes.Add(1)
	return b.err
}

// zorderEngine builds a watching engine whose Window is a REAL coordinator (so
// the always_on_bottom gate is exercised end to end) and returns the clock
// setter plus a function running one watch tick.
func zorderEngine(t *testing.T, enabled bool, sess *bottomSession) (*Engine, func(time.Duration), func()) {
	t.Helper()
	st, _ := store.OpenMemory()
	t.Cleanup(func() { st.Close() })
	ctrl := newFakeController()
	ctrl.health = twitch.Health{Pathname: "/somechan", Volume: 0.01, CurrentTime: 1}
	e := testEngine(t, st, ctrl, &fakePager{}, &fakeDisco{}, newRecordingEmitter())

	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	e.deps.Now = func() time.Time { return now }
	advance := func(d time.Duration) { now = now.Add(d) }

	coord := browser.NewWindowCoordinator(false, zerolog.Nop())
	coord.SetAlwaysOnBottom(enabled)
	e.deps.Window = coord.Register("twitch", sess)

	e.cur = &activeWatch{
		drop:         domain.Drop{ID: "d1", RequiredMinutes: 60},
		login:        "somechan",
		lastProgress: now,
		sessionStart: now,
		maxSessionAt: now.Add(24 * time.Hour),
		nextPollAt:   now.Add(24 * time.Hour),
	}
	far := now.Add(24 * time.Hour)
	var lastTime float64
	var lastSet bool
	var frozen int
	nextReassert := now.Add(time.Hour)
	tick := func() {
		ctrl.health.CurrentTime += 10 // keep the frozen-video watchdog quiet
		e.cur.lastProgress = e.deps.Now()
		e.watchTick(context.Background(), &lastTime, &lastSet, &frozen, &nextReassert, far, far)
	}
	return e, advance, tick
}

func TestWatchTickReassertsBottomOnCadence(t *testing.T) {
	sess := &bottomSession{fakeSession: newFakeSession()}
	e, advance, tick := zorderEngine(t, true, sess)

	tick()
	if n := sess.pushes.Load(); n != 1 {
		t.Fatalf("first watching tick must push to bottom, got %d", n)
	}
	advance(time.Second)
	tick()
	if n := sess.pushes.Load(); n != 1 {
		t.Fatalf("a tick inside the reassert interval must not push again, got %d", n)
	}
	advance(2 * e.deps.Cfg.Browser.Player.ReassertInterval)
	tick()
	if n := sess.pushes.Load(); n != 2 {
		t.Fatalf("the next reassert-interval tick must push again, got %d", n)
	}
}

func TestWatchTickSkipsBottomWhenDisabled(t *testing.T) {
	sess := &bottomSession{fakeSession: newFakeSession()}
	e, advance, tick := zorderEngine(t, false, sess)
	for i := 0; i < 3; i++ {
		tick()
		advance(2 * e.deps.Cfg.Browser.Player.ReassertInterval)
	}
	if n := sess.pushes.Load(); n != 0 {
		t.Fatalf("always_on_bottom off must never push, got %d", n)
	}
}

func TestWatchTickToleratesUnsupportedBottom(t *testing.T) {
	sess := &bottomSession{fakeSession: newFakeSession(), err: errors.ErrUnsupported}
	e, advance, tick := zorderEngine(t, true, sess)
	for i := 0; i < 3; i++ {
		tick()
		advance(2 * e.deps.Cfg.Browser.Player.ReassertInterval)
	}
	if n := sess.pushes.Load(); n != 1 {
		t.Fatalf("ErrUnsupported must be tried once and never retried, got %d", n)
	}
	if e.cur == nil {
		t.Fatal("an unsupported z-order must not end the watch")
	}
}
