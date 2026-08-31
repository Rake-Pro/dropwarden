package browser

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rs/zerolog"
)

// fakeHost is a WindowHost that hands out stub tabs instead of launching Chrome.
// The keeper (owner) holds the window open until the first platform tab is
// created and then CloseInitialTab drops it; every platform tab is an equal
// NewTab, matching the reference-counted group model.
type fakeHost struct {
	name string

	mu        sync.Mutex
	owner     *stubSession
	keeperOut bool
	tabs      []*stubSession

	done      chan struct{}
	closed    atomic.Bool
	closeOnce sync.Once
	newTabErr error
}

func newFakeHost(name string) *fakeHost {
	return &fakeHost{name: name, owner: newStubSession(name + "-owner"), done: make(chan struct{})}
}

func (h *fakeHost) NewTab(context.Context) (Session, error) {
	if h.newTabErr != nil {
		return nil, h.newTabErr
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	s := newStubSession(fmt.Sprintf("%s-tab%d", h.name, len(h.tabs)+1))
	h.tabs = append(h.tabs, s)
	return s, nil
}

// CloseInitialTab drops the keeper, leaving the window on its platform tabs.
func (h *fakeHost) CloseInitialTab() {
	h.mu.Lock()
	h.keeperOut = true
	h.mu.Unlock()
	h.owner.closed.Store(true)
}

func (h *fakeHost) SetMinimized(context.Context, bool) error { return nil }

func (h *fakeHost) Alive() bool               { return !h.closed.Load() }
func (h *fakeHost) Done() <-chan struct{}     { return h.done }
func (h *fakeHost) extraTabs() []*stubSession { h.mu.Lock(); defer h.mu.Unlock(); return h.tabs }

// keeperDropped reports whether CloseInitialTab has run.
func (h *fakeHost) keeperDropped() bool { h.mu.Lock(); defer h.mu.Unlock(); return h.keeperOut }

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
	if urlOf(t, tw) != "h1-tab1" {
		t.Fatalf("the first platform must get its own tab, got %q", urlOf(t, tw))
	}
	// The keeper is dropped once a platform tab holds the window open.
	if !host.keeperDropped() {
		t.Fatal("the first platform tab must drop the window's keeper page")
	}

	kk, err := g.Session(ctx, "kick", nil)
	if err != nil {
		t.Fatalf("kick session: %v", err)
	}
	if urlOf(t, kk) != "h1-tab2" {
		t.Fatalf("the second platform must get its own new tab, got %q", urlOf(t, kk))
	}
	if launches != 1 {
		t.Fatalf("a second platform must not relaunch chrome: %d launches", launches)
	}
	if n := len(host.extraTabs()); n != 2 {
		t.Fatalf("two platforms must be two tabs, got %d", n)
	}

	// Asking again returns the same handle instead of opening a duplicate tab.
	again, err := g.Session(ctx, "kick", nil)
	if err != nil || again != kk {
		t.Fatalf("re-requesting a live platform must reuse its handle (%v)", err)
	}
	if n := len(host.extraTabs()); n != 2 {
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
	twTab, kkTab := host.extraTabs()[0], host.extraTabs()[1]

	if err := g.CloseSession("kick"); err != nil {
		t.Fatalf("close kick: %v", err)
	}
	if host.closed.Load() {
		t.Fatal("stopping kick must not close the shared window")
	}
	if !kkTab.closed.Load() {
		t.Fatal("stopping kick must close its tab")
	}
	if twTab.closed.Load() || !tw.Alive() {
		t.Fatal("stopping kick must leave the twitch session running")
	}

	// Kick can come back on the same live window, as a fresh tab.
	if _, err := g.Session(ctx, "kick", nil); err != nil {
		t.Fatalf("restarting kick on a live window: %v", err)
	}
	if n := len(host.extraTabs()); n != 3 {
		t.Fatalf("restarting kick should open one more tab, got %d", n)
	}
}

// TestGroupWindowClosesOnlyWithLastTab is the point of the reference-counted
// window: stopping either platform closes just its tab and leaves the sibling
// (and the window) running; only closing the LAST tab tears the window down.
func TestGroupWindowClosesOnlyWithLastTab(t *testing.T) {
	ctx := context.Background()
	host := newFakeHost("h1")
	g, _ := NewGroup(ctx, func(context.Context) (WindowHost, error) { return host, nil }, zerolog.Nop())

	_, _ = g.Session(ctx, "twitch", nil)
	kk, _ := g.Session(ctx, "kick", nil)
	twTab := host.extraTabs()[0]

	// Stop Twitch first: the window and Kick must survive.
	if err := g.CloseSession("twitch"); err != nil {
		t.Fatalf("close twitch: %v", err)
	}
	if host.closed.Load() || g.Closed() {
		t.Fatal("stopping one of two platforms must not close the shared window")
	}
	if !twTab.closed.Load() {
		t.Fatal("stopping twitch must close its tab")
	}
	if !kk.Alive() {
		t.Fatal("kick must keep running after twitch stops")
	}

	// Stop the last platform: now the window goes.
	if err := g.CloseSession("kick"); err != nil {
		t.Fatalf("close kick: %v", err)
	}
	if !host.closed.Load() {
		t.Fatal("closing the last tab must close the window")
	}
	if !g.Closed() {
		t.Fatal("the group must be closed for good once its last tab is gone")
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
	// Both stable handles must now point at the REBUILT window's tabs, recreated
	// as equal tabs in registration order (twitch first, then kick).
	if got := urlOf(t, tw); got != "h2-tab1" {
		t.Fatalf("twitch handle not swapped to the rebuilt window: %q", got)
	}
	if got := urlOf(t, kk); got != "h2-tab2" {
		t.Fatalf("kick handle not swapped to the rebuilt window: %q", got)
	}
	if n := len(h2.extraTabs()); n != 2 {
		t.Fatalf("the rebuilt window should carry both platform tabs, got %d", n)
	}
	if !h2.keeperDropped() {
		t.Fatal("the rebuilt window must drop its keeper once tabs are restored")
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
