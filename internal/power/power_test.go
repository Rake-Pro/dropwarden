package power

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/rs/zerolog"

	"github.com/rake-pro/dropwarden/internal/config"
)

type fakeHolder struct{ held bool }

func (f *fakeHolder) Hold() error { f.held = true; return nil }
func (f *fakeHolder) Release()    { f.held = false }

func TestControllerFollowsModeAndWatchState(t *testing.T) {
	cfg := &config.Config{}
	busy := false
	h := &fakeHolder{}
	c := NewWith(func() *config.Config { return cfg }, func() bool { return busy }, h, zerolog.Nop())

	c.Refresh()
	if h.held {
		t.Fatal("while_watching (default) with no watch must not hold")
	}
	busy = true
	c.Refresh()
	if !h.held || !c.Active() {
		t.Fatal("a watch in progress must hold")
	}
	busy = false
	c.Refresh()
	if h.held {
		t.Fatal("watch ended: must release")
	}

	cfg.Power.KeepAwake = config.KeepAwakeAlways
	c.Refresh()
	if !h.held {
		t.Fatal("always must hold with no watch")
	}
	cfg.Power.KeepAwake = config.KeepAwakeNever
	busy = true
	c.Refresh()
	if h.held {
		t.Fatal("never must release even mid-watch")
	}

	cfg.Power.KeepAwake = config.KeepAwakeAlways
	c.Refresh()
	c.Close()
	if h.held || c.Active() {
		t.Fatal("Close must release")
	}
	c.Refresh()
	if h.held {
		t.Fatal("Refresh after Close must stay released")
	}
}

type failHolder struct{ calls int }

func (f *failHolder) Hold() error { f.calls++; return errors.New("systemd-inhibit not found") }
func (f *failHolder) Release()    {}

func TestControllerWarnsOnceWhenHoldFails(t *testing.T) {
	cfg := &config.Config{}
	cfg.Power.KeepAwake = config.KeepAwakeAlways
	var buf bytes.Buffer
	h := &failHolder{}
	c := NewWith(func() *config.Config { return cfg }, func() bool { return true }, h, zerolog.New(&buf))
	for range 5 {
		c.Refresh()
	}
	if c.Active() {
		t.Fatal("a failed hold must not report active")
	}
	if h.calls != 5 {
		t.Fatalf("Hold calls = %d, want 5 (a later inhibitor may appear)", h.calls)
	}
	if n := strings.Count(buf.String(), "could not hold"); n != 1 {
		t.Fatalf("warned %d times, want once:\n%s", n, buf.String())
	}
}
