package power

import (
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
