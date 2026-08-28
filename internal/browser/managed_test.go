package browser

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rs/zerolog"
)

// stubSession is a minimal Session whose events channel and liveness the test
// controls directly.
type stubSession struct {
	events chan Event
	alive  atomic.Bool
	closed atomic.Bool
	name   string
}

func newStubSession(name string) *stubSession {
	s := &stubSession{events: make(chan Event, 4), name: name}
	s.alive.Store(true)
	return s
}

func (s *stubSession) die() {
	s.alive.Store(false)
	s.events <- Event{Kind: EventDisconnect, At: time.Now()}
	close(s.events)
}

func (s *stubSession) Navigate(context.Context, string) error {
	if !s.alive.Load() {
		return errors.New("dead")
	}
	return nil
}
func (s *stubSession) Reload(context.Context, bool) error              { return nil }
func (s *stubSession) CurrentURL(context.Context) (string, error)      { return s.name, nil }
func (s *stubSession) Eval(context.Context, string, any) error         { return nil }
func (s *stubSession) Click(context.Context, string) (bool, error)     { return false, nil }
func (s *stubSession) SeedOnNewDocument(context.Context, string) error { return nil }
func (s *stubSession) Events() <-chan Event                            { return s.events }
func (s *stubSession) ShiftWindow(int, int) error                      { return nil }
func (s *stubSession) SetMinimized(context.Context, bool) error        { return nil }
func (s *stubSession) Alive() bool                                     { return s.alive.Load() }
func (s *stubSession) Close() error                                    { s.closed.Store(true); return nil }

func TestManagedRelaunchesOnDisconnect(t *testing.T) {
	first := newStubSession("first")
	second := newStubSession("second")
	relaunched := make(chan struct{})
	m := NewManaged(first, func(context.Context) (Session, error) {
		defer close(relaunched)
		return second, nil
	}, testLogger())

	first.die()

	// The disconnect event must reach the stable stream.
	select {
	case ev := <-m.Events():
		if ev.Kind != EventDisconnect {
			t.Fatalf("expected disconnect event, got %s", ev.Kind)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("disconnect event not forwarded")
	}
	select {
	case <-relaunched:
	case <-time.After(2 * time.Second):
		t.Fatal("relaunch was not attempted")
	}
	// Give the swap a moment, then verify calls hit the new session.
	deadline := time.Now().Add(2 * time.Second)
	for {
		if url, _ := m.CurrentURL(context.Background()); url == "second" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("managed session never swapped to the relaunched browser")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !first.closed.Load() {
		t.Fatal("dead session should have been closed/reaped")
	}
	if !m.Alive() {
		t.Fatal("managed session should be alive after recovery")
	}
}

func TestManagedCloseStopsRecovery(t *testing.T) {
	first := newStubSession("first")
	var relaunches atomic.Int32
	m := NewManaged(first, func(context.Context) (Session, error) {
		relaunches.Add(1)
		return nil, errors.New("nope")
	}, testLogger())
	_ = m.Close()
	first.die()
	time.Sleep(100 * time.Millisecond)
	if n := relaunches.Load(); n != 0 {
		t.Fatalf("closed managed session must not relaunch, got %d attempts", n)
	}
}

func testLogger() zerolog.Logger { return zerolog.Nop() }
