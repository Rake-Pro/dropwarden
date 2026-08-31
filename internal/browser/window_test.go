package browser

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/rs/zerolog"
)

// winStub is a Session that only records the window calls the coordinator makes.
type winStub struct {
	*stubSession
	minimizeCalls []bool
	activations   atomic.Int32
	activateErr   error
}

func newWinStub(name string) *winStub {
	return &winStub{stubSession: newStubSession(name)}
}

func (w *winStub) SetMinimized(_ context.Context, minimized bool) error {
	w.minimizeCalls = append(w.minimizeCalls, minimized)
	return nil
}

func (w *winStub) Activate(context.Context) error {
	w.activations.Add(1)
	return w.activateErr
}

func TestWindowCoordinatorRefcountsRestoreHolds(t *testing.T) {
	ctx := context.Background()
	tw, kk := newWinStub("twitch"), newWinStub("kick")
	c := NewWindowCoordinator(true, zerolog.Nop())
	twh := c.Register("twitch", tw)
	kkh := c.Register("kick", kk)

	twh.Restore(ctx)
	if len(tw.minimizeCalls) != 1 || tw.minimizeCalls[0] {
		t.Fatalf("the first hold must restore the window, got %v", tw.minimizeCalls)
	}

	// A second hold finds the window already restored: no redundant CDP call.
	kkh.Restore(ctx)
	if len(kk.minimizeCalls) != 0 {
		t.Fatalf("a second hold must not re-issue the window state, got %v", kk.minimizeCalls)
	}
	// Re-entering Validating after a refresh must not double-count either.
	twh.Restore(ctx)

	// Releasing one of two holds keeps the window up: the other platform is
	// still validating and must not be buried.
	twh.Release(ctx)
	if len(tw.minimizeCalls) != 1 {
		t.Fatalf("releasing one of two holds must not minimize, got %v", tw.minimizeCalls)
	}
	if len(kk.minimizeCalls) != 0 {
		t.Fatalf("releasing one of two holds must not minimize, got %v", kk.minimizeCalls)
	}

	// The last release tucks the window away again.
	kkh.Release(ctx)
	if len(kk.minimizeCalls) != 1 || !kk.minimizeCalls[0] {
		t.Fatalf("the last release must re-minimize the window, got %v", kk.minimizeCalls)
	}
}

func TestWindowCoordinatorActivatesOnlyTheHoldingTab(t *testing.T) {
	ctx := context.Background()
	tw, kk := newWinStub("twitch"), newWinStub("kick")
	c := NewWindowCoordinator(true, zerolog.Nop())
	twh := c.Register("twitch", tw)
	kkh := c.Register("kick", kk)

	twh.Restore(ctx)
	if tw.activations.Load() != 1 {
		t.Fatalf("the holding tab must be activated once, got %d", tw.activations.Load())
	}
	if kk.activations.Load() != 0 {
		t.Fatalf("a tab that took no hold must never be activated, got %d", kk.activations.Load())
	}

	kkh.Restore(ctx)
	if kk.activations.Load() != 1 || tw.activations.Load() != 1 {
		t.Fatalf("each holder activates its own tab only: twitch=%d kick=%d",
			tw.activations.Load(), kk.activations.Load())
	}

	// Releasing while the other platform still holds must put ITS tab back in
	// front - the released tab may be covering it mid-validation.
	twh.Release(ctx)
	if kk.activations.Load() != 2 {
		t.Fatalf("release must re-activate the remaining holder, got %d", kk.activations.Load())
	}
	if tw.activations.Load() != 1 {
		t.Fatalf("release must not activate the released tab, got %d", tw.activations.Load())
	}
	// The last release has nobody left to put in front.
	kkh.Release(ctx)
	if tw.activations.Load() != 1 || kk.activations.Load() != 2 {
		t.Fatalf("the last release must not activate: twitch=%d kick=%d", tw.activations.Load(), kk.activations.Load())
	}
}

func TestWindowCoordinatorSingleTabNeverActivates(t *testing.T) {
	// separate_window mode (and shared mode with Kick off) is one tab per
	// window: there is nothing to switch between, so the focus-touching call
	// must never fire.
	ctx := context.Background()
	tw := newWinStub("twitch")
	c := NewWindowCoordinator(true, zerolog.Nop())
	h := c.Register("twitch", tw)

	h.Restore(ctx)
	h.Release(ctx)
	if tw.activations.Load() != 0 {
		t.Fatalf("a single-tab window must never activate, got %d", tw.activations.Load())
	}
	if len(tw.minimizeCalls) != 2 || tw.minimizeCalls[0] || !tw.minimizeCalls[1] {
		t.Fatalf("single-tab restore/minimize sequence wrong: %v", tw.minimizeCalls)
	}
}

func TestWindowCoordinatorSkipsWindowStateWhenNotRunningMinimized(t *testing.T) {
	// start_minimized off: the window is always visible, so there is no window
	// state to drive - but a shared window still needs the right tab in front.
	ctx := context.Background()
	tw, kk := newWinStub("twitch"), newWinStub("kick")
	c := NewWindowCoordinator(false, zerolog.Nop())
	twh := c.Register("twitch", tw)
	c.Register("kick", kk)

	twh.Restore(ctx)
	twh.Release(ctx)
	if len(tw.minimizeCalls) != 0 {
		t.Fatalf("start_minimized off must not touch the window state, got %v", tw.minimizeCalls)
	}
	if tw.activations.Load() != 1 {
		t.Fatalf("a shared window still activates the validating tab, got %d", tw.activations.Load())
	}
}

func TestWindowCoordinatorUnregisterReleasesTheHold(t *testing.T) {
	ctx := context.Background()
	tw, kk := newWinStub("twitch"), newWinStub("kick")
	c := NewWindowCoordinator(true, zerolog.Nop())
	twh := c.Register("twitch", tw)
	kkh := c.Register("kick", kk)

	twh.Restore(ctx)
	kkh.Restore(ctx)
	// Kick's runtime is torn down mid-validation: its hold must not pin the
	// window open forever.
	kkh.Close(ctx)
	twh.Release(ctx)
	if len(tw.minimizeCalls) != 2 || !tw.minimizeCalls[1] {
		t.Fatalf("a torn-down tab must not keep the window restored: %v", tw.minimizeCalls)
	}
}

func TestWindowCoordinatorActivationErrorIsNotFatal(t *testing.T) {
	ctx := context.Background()
	tw, kk := newWinStub("twitch"), newWinStub("kick")
	tw.activateErr = errors.New("no such target")
	c := NewWindowCoordinator(true, zerolog.Nop())
	twh := c.Register("twitch", tw)
	c.Register("kick", kk)

	twh.Restore(ctx)
	twh.Release(ctx)
	if len(tw.minimizeCalls) != 2 {
		t.Fatalf("a failed activation must not stop the window state changes: %v", tw.minimizeCalls)
	}
}
