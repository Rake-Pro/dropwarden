// Package power holds an OS "do not idle-sleep" assertion on the watcher's
// behalf. Display sleep is never blocked (monitors may turn off); only the
// system idle-sleep timer is, and only while the configured mode says so:
// while_watching holds it while any engine is mid-watch, always for the
// app's lifetime, never leaves the OS alone. Nothing here wakes a sleeping
// machine; that stays with the person at the desk.
package power

import (
	"sync"

	"github.com/rs/zerolog"

	"github.com/rake-pro/dropwarden/internal/config"
)

// Holder is one OS sleep inhibitor: Hold acquires it (idempotent), Release
// drops it (idempotent). Implementations live in hold_*.go.
type Holder interface {
	Hold() error
	Release()
}

// Controller decides from the mode and the watch state whether the holder
// should be held, and applies the difference. Refresh is cheap and safe to
// call from any goroutine on every engine state change and config save.
type Controller struct {
	cfg  func() *config.Config
	busy func() bool
	h    Holder
	log  zerolog.Logger

	mu     sync.Mutex
	active bool
	closed bool
}

// New builds a controller over the host OS holder. cfg supplies the live
// config; busy reports whether a watch is in progress.
func New(cfg func() *config.Config, busy func() bool, log zerolog.Logger) *Controller {
	return NewWith(cfg, busy, newHolder(), log)
}

// NewWith is New with an explicit holder (tests).
func NewWith(cfg func() *config.Config, busy func() bool, h Holder, log zerolog.Logger) *Controller {
	return &Controller{cfg: cfg, busy: busy, h: h, log: log}
}

// Active reports whether the sleep inhibitor is held right now.
func (c *Controller) Active() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.active
}

// Refresh re-evaluates the mode against the watch state and holds or
// releases the inhibitor to match.
func (c *Controller) Refresh() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	mode := c.cfg().KeepAwakeMode()
	want := mode == config.KeepAwakeAlways || (mode == config.KeepAwakeWhileWatching && c.busy())
	switch {
	case want && !c.active:
		if err := c.h.Hold(); err != nil {
			c.log.Warn().Err(err).Str("mode", mode).Msg("power: could not hold the sleep inhibitor")
			return
		}
		c.active = true
		c.log.Info().Str("mode", mode).Msg("power: keeping the system awake")
	case !want && c.active:
		c.h.Release()
		c.active = false
		c.log.Info().Str("mode", mode).Msg("power: sleep inhibitor released")
	}
}

// Close releases the inhibitor for good; later Refresh calls are no-ops.
func (c *Controller) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	if c.active {
		c.h.Release()
		c.active = false
		c.log.Info().Msg("power: sleep inhibitor released on shutdown")
	}
}
