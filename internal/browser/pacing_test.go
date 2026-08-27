package browser

import (
	"context"
	"testing"
	"time"
)

// fakeClock advances only when something sleeps on it, and records every sleep
// so a test can assert the exact spacing the Pacer asked for.
type fakeClock struct {
	now   time.Time
	slept []time.Duration
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time { return c.now }

func (c *fakeClock) Sleep(d time.Duration) {
	c.slept = append(c.slept, d)
	c.now = c.now.Add(d)
}

func fixedRnd(v float64) func() float64 { return func() float64 { return v } }

func TestPacerFirstActionDoesNotWait(t *testing.T) {
	clock := newFakeClock()
	p := NewPacer(clock, fixedRnd(0))

	if err := p.Wait(context.Background(), Navigation); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if len(clock.slept) != 0 {
		t.Fatalf("first navigation slept %v, want no sleep", clock.slept)
	}
}

func TestPacerEnforcesMinimumSpacing(t *testing.T) {
	cases := []struct {
		class ActionClass
		want  time.Duration
	}{
		{Navigation, 8 * time.Second},
		{Click, 3 * time.Second},
		{HardRefresh, 4 * time.Minute},
		{DiscoveryCycle, 30 * time.Minute},
		{DirectoryPoll, 4 * time.Minute},
	}
	for _, tc := range cases {
		t.Run(tc.class.String(), func(t *testing.T) {
			clock := newFakeClock()
			p := NewPacer(clock, fixedRnd(0))
			ctx := context.Background()

			if err := p.Wait(ctx, tc.class); err != nil {
				t.Fatalf("first Wait: %v", err)
			}
			if err := p.Wait(ctx, tc.class); err != nil {
				t.Fatalf("second Wait: %v", err)
			}

			if len(clock.slept) != 1 {
				t.Fatalf("got %d sleeps (%v), want exactly 1", len(clock.slept), clock.slept)
			}
			if clock.slept[0] != tc.want {
				t.Fatalf("spacing = %v, want %v", clock.slept[0], tc.want)
			}
		})
	}
}

func TestPacerAddsJitter(t *testing.T) {
	clock := newFakeClock()
	// Half of the Navigation jitter budget (12s) on top of the 8s minimum.
	p := NewPacer(clock, fixedRnd(0.5))
	ctx := context.Background()

	if err := p.Wait(ctx, Navigation); err != nil {
		t.Fatalf("first Wait: %v", err)
	}
	if len(clock.slept) != 1 || clock.slept[0] != 6*time.Second {
		t.Fatalf("first sleep = %v, want [6s] (jitter only)", clock.slept)
	}
	if err := p.Wait(ctx, Navigation); err != nil {
		t.Fatalf("second Wait: %v", err)
	}
	if got := clock.slept[1]; got != 14*time.Second {
		t.Fatalf("second sleep = %v, want 14s (8s minimum + 6s jitter)", got)
	}
}

func TestPacerClassesAreIndependent(t *testing.T) {
	clock := newFakeClock()
	p := NewPacer(clock, fixedRnd(0))
	ctx := context.Background()

	if err := p.Wait(ctx, Navigation); err != nil {
		t.Fatalf("Wait(Navigation): %v", err)
	}
	if err := p.Wait(ctx, Click); err != nil {
		t.Fatalf("Wait(Click): %v", err)
	}
	if len(clock.slept) != 0 {
		t.Fatalf("a click was paced against a navigation: %v", clock.slept)
	}
}

func TestPacerNavigationCeiling(t *testing.T) {
	clock := newFakeClock()
	p := NewPacer(clock, fixedRnd(0))
	ctx := context.Background()

	// Burn the whole budget at the minimum spacing: navigations land at
	// t=0s, 8s, ... 88s, which all sit inside the 10 minute window.
	for i := 0; i < navCeiling; i++ {
		if err := p.Wait(ctx, Navigation); err != nil {
			t.Fatalf("Wait %d: %v", i, err)
		}
	}
	if got := len(clock.slept); got != navCeiling-1 {
		t.Fatalf("got %d sleeps filling the budget, want %d", got, navCeiling-1)
	}

	// The next one may not run until the oldest navigation (t=0s) ages out of
	// the window at t=600s: 8s of ordinary spacing plus 504s of ceiling delay.
	if err := p.Wait(ctx, Navigation); err != nil {
		t.Fatalf("Wait past ceiling: %v", err)
	}
	got := clock.slept[len(clock.slept)-1]
	want := 8*time.Second + 504*time.Second
	if got != want {
		t.Fatalf("ceiling delay = %v, want %v", got, want)
	}
	if clock.now.Sub(newFakeClock().now) != 600*time.Second {
		t.Fatalf("navigation landed at t=%v, want t=600s (exactly when the window frees up)",
			clock.now.Sub(newFakeClock().now))
	}
}

func TestPacerCeilingReleasesAfterWindow(t *testing.T) {
	clock := newFakeClock()
	p := NewPacer(clock, fixedRnd(0))
	ctx := context.Background()

	for i := 0; i < navCeiling; i++ {
		if err := p.Wait(ctx, Navigation); err != nil {
			t.Fatalf("Wait %d: %v", i, err)
		}
	}
	// Idle well past the window: every recorded navigation should fall out of
	// the budget, so the next one pays no ceiling delay.
	clock.now = clock.now.Add(2 * navWindow)
	before := len(clock.slept)

	if err := p.Wait(ctx, Navigation); err != nil {
		t.Fatalf("Wait after idle: %v", err)
	}
	if len(clock.slept) != before {
		t.Fatalf("slept %v after the window emptied, want no sleep", clock.slept[before:])
	}
}

func TestPacerRespectsCancelledContext(t *testing.T) {
	clock := newFakeClock()
	p := NewPacer(clock, fixedRnd(0))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := p.Wait(ctx, Navigation); err == nil {
		t.Fatal("Wait returned nil for a cancelled context")
	}
	if len(clock.slept) != 0 {
		t.Fatalf("Wait slept despite a cancelled context: %v", clock.slept)
	}
}

func TestPacerUnknownClass(t *testing.T) {
	p := NewPacer(newFakeClock(), fixedRnd(0))
	if err := p.Wait(context.Background(), ActionClass(99)); err == nil {
		t.Fatal("Wait returned nil for an unknown action class")
	}
}
