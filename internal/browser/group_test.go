package browser

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rs/zerolog"
)

// fakeHost is a WindowHost that hands out stub tabs instead of launching Chrome.
type fakeHost struct {
	name string

	mu    sync.Mutex
	owner *stubSession
	tabs  []*stubSession

	done      chan struct{}
	closed    atomic.Bool
	closeOnce sync.Once
	newTabErr error
}

func newFakeHost(name string) *fakeHost {
	return &fakeHost{name: name, owner: newStubSession(name + "-owner"), done: make(chan struct{})}
}

func (h *fakeHost) Owner() Session { return h.owner }

func (h *fakeHost) NewTab(context.Context) (Session, error) {
	if h.newTabErr != nil {
		return nil, h.newTabErr
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	s := newStubSession(h.name + "-tab")
	h.tabs = append(h.tabs, s)
	return s, nil
}

func (h *fakeHost) Alive() bool               { return !h.closed.Load() }
func (h *fakeHost) Done() <-chan struct{}     { return h.done }
func (h *fakeHost) extraTabs() []*stubSession { h.mu.Lock(); defer h.mu.Unlock(); return h.tabs }

// die simulates a CDP loss: the window is gone but nobody asked for it.
func (h *fakeHost) die() { h.closed.Store(true); h.closeOnce.Do(func() { close(h.done) }) }

func (h *fakeHost) Close() error {
	h.closed.Store(true)
	h.owner.closed.Store(true)
	for _, t := range h.extraTabs() {
		t.closed.Store(true)
	}
	h.closeOnce.Do(func() { close(h.done) })
	return nil
}

func urlOf(t *testing.T, s Session) string {
	t.Helper()
	u, err := s.CurrentURL(context.Background())
	if err != nil {
		t.Fatalf("current url: %v", err)
	}
	return u
}

func TestGroupSecondPlatformGetsATabOnTheLiveWindow(t *testing.T) {
	ctx := context.Background()
	host := newFakeHost("h1")
	launches := 0
	g, err := NewGroup(ctx, func(context.Context) (WindowHost, error) {
		launches++
		return host, nil
	}, zerolog.Nop())
	if err != nil {
		t.Fatalf("NewGroup: %v", err)
	}
	t.Cleanup(func() { _ = g.Close() })

	tw, err := g.Session(ctx, "twitch", nil)
	if err != nil {
		t.Fatalf("twitch session: %v", err)
	}
	if urlOf(t, tw) != "h1-owner" {
		t.Fatalf("the first platform must adopt the window's own tab, got %q", urlOf(t, tw))
	}

	kk, err := g.Session(ctx, "kick", nil)
	if err != nil {
		t.Fatalf("kick session: %v", err)
	}
	if urlOf(t, kk) != "h1-tab" {
		t.Fatalf("the second platform must get a new tab, got %q", urlOf(t, kk))
	}
	if launches != 1 {
		t.Fatalf("a second platform must not relaunch chrome: %d launches", launches)
	}
	if n := len(host.extraTabs()); n != 1 {
		t.Fatalf("expected exactly one extra tab, got %d", n)
	}

	// Asking again returns the same handle instead of opening a duplicate tab.
	again, err := g.Session(ctx, "kick", nil)
	if err != nil || again != kk {
		t.Fatalf("re-requesting a live platform must reuse its handle (%v)", err)
	}
	if n := len(host.extraTabs()); n != 1 {
		t.Fatalf("re-requesting a live platform must not open a tab, got %d", n)
	}
}

func TestGroupCloseSessionDropsOnlyThatTab(t *testing.T) {
	ctx := context.Background()
	host := newFakeHost("h1")
	g, _ := NewGroup(ctx, func(context.Context) (WindowHost, error) { return host, nil }, zerolog.Nop())
	t.Cleanup(func() { _ = g.Close() })

	tw, _ := g.Session(ctx, "twitch", nil)
	_, _ = g.Session(ctx, "kick", nil)

	if err := g.CloseSession("kick"); err != nil {
		t.Fatalf("close kick: %v", err)
	}
	if host.closed.Load() {
		t.Fatal("stopping kick must not close the shared window")
	}
	if !host.extraTabs()[0].closed.Load() {
		t.Fatal("stopping kick must close its tab")
	}
	if !tw.Alive() {
		t.Fatal("stopping kick must leave the twitch session running")
	}

	// Kick can come back on the same live window, as a fresh tab.
	if _, err := g.Session(ctx, "kick", nil); err != nil {
		t.Fatalf("restarting kick on a live window: %v", err)
	}
	if n := len(host.extraTabs()); n != 2 {
		t.Fatalf("restarting kick should open one more tab, got %d", n)
	}
}

func TestGroupClosingTheOwnerClosesTheWindow(t *testing.T) {
	ctx := context.Background()
	host := newFakeHost("h1")
	g, _ := NewGroup(ctx, func(context.Context) (WindowHost, error) { return host, nil }, zerolog.Nop())

	_, _ = g.Session(ctx, "twitch", nil)
	_, _ = g.Session(ctx, "kick", nil)

	if err := g.CloseSession("twitch"); err != nil {
		t.Fatalf("close twitch: %v", err)
	}
	if !host.closed.Load() {
		t.Fatal("twitch owns the shared window: stopping it must close the browser")
	}
	if !g.Closed() {
		t.Fatal("the group must be closed for good once its window is gone")
	}
	if !host.extraTabs()[0].closed.Load() {
		t.Fatal("kick cannot outlive the window it was a tab in")
	}
}

func TestGroupRelaunchRebuildsEveryTabAndItsInit(t *testing.T) {
	ctx := context.Background()
	h1, h2 := newFakeHost("h1"), newFakeHost("h2")
	hosts := []*fakeHost{h1, h2}
	var launched atomic.Int32
	g, err := NewGroup(ctx, func(context.Context) (WindowHost, error) {
		n := launched.Add(1)
		return hosts[n-1], nil
	}, zerolog.Nop())
	if err != nil {
		t.Fatalf("NewGroup: %v", err)
	}
	t.Cleanup(func() { _ = g.Close() })

	var twInits, kkInits atomic.Int32
	tw, _ := g.Session(ctx, "twitch", func(context.Context, Session) error { twInits.Add(1); return nil })
	kk, _ := g.Session(ctx, "kick", func(context.Context, Session) error { kkInits.Add(1); return nil })
	if twInits.Load() != 1 || kkInits.Load() != 1 {
		t.Fatalf("each tab is initialized once at start: twitch=%d kick=%d", twInits.Load(), kkInits.Load())
	}

	h1.die()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if twInits.Load() == 2 && kkInits.Load() == 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if twInits.Load() != 2 || kkInits.Load() != 2 {
		t.Fatalf("a relaunch must re-run every tab's init (capture shims): twitch=%d kick=%d",
			twInits.Load(), kkInits.Load())
	}
	// Both stable handles must now point at the REBUILT window's tabs.
	if got := urlOf(t, tw); got != "h2-owner" {
		t.Fatalf("twitch handle not swapped to the rebuilt window: %q", got)
	}
	if got := urlOf(t, kk); got != "h2-tab" {
		t.Fatalf("kick handle not swapped to the rebuilt window: %q", got)
	}
	if n := len(h2.extraTabs()); n != 1 {
		t.Fatalf("the rebuilt window should carry exactly one extra tab, got %d", n)
	}
}

func TestGroupTabOnAClosedWindowIsRefused(t *testing.T) {
	ctx := context.Background()
	host := newFakeHost("h1")
	g, _ := NewGroup(ctx, func(context.Context) (WindowHost, error) { return host, nil }, zerolog.Nop())
	_, _ = g.Session(ctx, "twitch", nil)
	_ = g.Close()

	if _, err := g.Session(ctx, "kick", nil); err == nil {
		t.Fatal("opening a tab on a closed window must fail so the caller retries later")
	}
}

func TestGroupLaunchFailurePropagates(t *testing.T) {
	_, err := NewGroup(context.Background(), func(context.Context) (WindowHost, error) {
		return nil, errors.New("no chrome")
	}, zerolog.Nop())
	if err == nil {
		t.Fatal("a failed launch must surface, not yield a half-built group")
	}
}
