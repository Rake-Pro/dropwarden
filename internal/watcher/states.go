package watcher

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/rake-pro/dropwarden/internal/domain"
)

// processCommand applies one command to engine flags. It returns true if the
// command was a shutdown.
func (e *Engine) processCommand(c command) {
	switch c.kind {
	case cmdStart:
		e.paused = false
		// Mirror the flag into the published status right away: a Start that
		// lands mid-cycle (cooldown, discovery) otherwise keeps reporting
		// Paused until the engine next passes through Idle, and the UI shows a
		// running engine as paused.
		e.setStatus(func(s *Status) { s.Paused = false })
	case cmdStop:
		e.paused = true
		e.setStatus(func(s *Status) { s.Paused = true })
	case cmdSkip:
		e.skipReq = true
	case cmdRefresh:
		e.refreshReq = true
	case cmdReloadConfig:
		if c.cfg != nil {
			e.deps.Cfg = c.cfg
		}
	case cmdShutdown:
		e.shutdown = true
	case cmdStatus:
		if c.reply != nil {
			c.reply <- e.Status()
		}
	}
}

// drainCommands applies every currently-pending command without blocking.
func (e *Engine) drainCommands() {
	for {
		select {
		case c := <-e.cmds:
			e.processCommand(c)
		default:
			return
		}
	}
}

// waitIdle blocks until a command arrives (or ctx is done). Returns true on
// shutdown. Leaving Idle is driven by a Start command or a periodic nudge to
// re-discover.
func (e *Engine) waitIdle(ctx context.Context) (shutdown bool) {
	e.setStatus(func(s *Status) { s.Paused = e.paused })
	// If not paused, idle only briefly before re-discovering.
	var timer <-chan time.Time
	if !e.paused {
		timer = time.After(2 * time.Second)
	}
	select {
	case <-ctx.Done():
		return true
	case c := <-e.cmds:
		e.processCommand(c)
		if e.shutdown {
			return true
		}
		if !e.paused {
			e.transition(StateDiscovering, "resumed")
		}
		return false
	case <-timer:
		e.transition(StateDiscovering, "idle re-discovery")
		return false
	}
}

// sleepInterruptible sleeps for d, returning early if a command arrives or ctx
// is cancelled. Commands are applied. Returns true if the engine should abandon
// the current wait (skip/stop/shutdown/refresh requested).
func (e *Engine) sleepInterruptible(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return true
		case c := <-e.cmds:
			e.processCommand(c)
			if e.shutdown || e.paused || e.skipReq || e.refreshReq {
				return true
			}
		case <-timer.C:
			return false
		}
	}
}

// jittered returns base plus a random fraction of jitter.
func (e *Engine) jittered(base, jitter time.Duration) time.Duration {
	if jitter <= 0 {
		return base
	}
	return base + time.Duration(e.deps.Rand()*float64(jitter))
}

// fuzzed applies watcher.timing_jitter to a delay so the cadence stays human:
// the result is d * (1 + timing_jitter * (2*rand-1)), i.e. +/- that fraction,
// never an exact periodic interval. A non-positive d passes
// through unchanged (nothing to desync), and timing_jitter <= 0 (the default
// legacy behavior) returns d exactly.
func (e *Engine) fuzzed(d time.Duration) time.Duration {
	if d <= 0 {
		return d
	}
	f := e.deps.Cfg.Watcher.TimingJitter
	if f <= 0 {
		return d
	}
	delta := f * (2*e.deps.Rand() - 1)
	return time.Duration(float64(d) * (1 + delta))
}

func (e *Engine) runDiscovering(ctx context.Context) (State, string) {
	e.drainCommands()
	if e.shutdown {
		return StateIdle, "shutdown"
	}
	if !e.staggered {
		e.staggered = true
		if e.deps.DiscoveryStagger > 0 && e.sleepInterruptible(ctx, e.deps.DiscoveryStagger) {
			e.drainCommands()
			if e.shutdown {
				return StateIdle, "shutdown"
			}
			if e.paused {
				return StateIdle, "paused"
			}
		}
	}
	// discovery.interval/jitter gate the pass: cooldowns, idle nudges and
	// backoff retries re-enter Discovering far more often than the campaign
	// list changes. A dashboard refresh (refreshReq) always forces one.
	if !e.refreshReq && !e.discoveryDue() {
		if e.paused {
			return StateIdle, "paused"
		}
		return StateSelectingChannel, "campaigns fresh (discovery not due)"
	}
	e.refreshReq = false
	res, err := e.deps.Disco.RunOnce(ctx)
	if err != nil {
		if errors.Is(err, domain.ErrLoggedOut) {
			return e.handleLoggedOut()
		}
		return e.enterBackoff("discovery: " + err.Error())
	}
	e.lastDiscoveryAt = e.deps.Now()
	e.discoveryGap = e.jittered(e.deps.Cfg.Discovery.Interval, e.deps.Cfg.Discovery.Jitter)
	e.deps.Emit.Emit(EvCampaigns, map[string]any{"count": res.Campaigns + res.Details, "at": e.deps.Now()})
	e.backoff = 0
	// Discovery just saved fresh inventory progress: book drops Twitch already
	// reports claimed, then claim anything completed.
	if rows, err := e.deps.Store.ListProgress(); err == nil {
		e.reconcileClaimedRows(rows)
	}
	e.claimSweep(ctx, "discovery")
	if e.paused {
		return StateIdle, "paused"
	}
	return StateSelectingChannel, "discovered"
}

// discoveryDue reports whether a discovery pass should run: none has run yet,
// discovery.interval is off (<= 0), or the jittered gap drawn after the last
// pass has elapsed.
func (e *Engine) discoveryDue() bool {
	if e.lastDiscoveryAt.IsZero() || e.deps.Cfg.Discovery.Interval <= 0 {
		return true
	}
	return e.deps.Now().Sub(e.lastDiscoveryAt) >= e.discoveryGap
}

// handleLoggedOut stops the engine on an expired platform session: nothing
// credits while logged out, and every watchdog downstream would misread it
// (live: a five-minute "not crediting" give-up per drop). The engine pauses
// until the user logs in and starts it again; the app's OnLoggedOut hook sets
// the needs-login flag and pending-login marker the first-run flow uses.
func (e *Engine) handleLoggedOut() (State, string) {
	if e.cur != nil {
		_ = e.deps.Store.SetItemStatus(e.cur.drop.ID, domain.ItemPending, "")
		e.cur = nil
	}
	e.paused = true
	e.setStatus(func(s *Status) { s.Paused = true })
	name := string(e.deps.Platform.OrTwitch())
	name = strings.ToUpper(name[:1]) + name[1:]
	reason := name + " session expired - log in again"
	url := URLTwitchLogin
	if e.deps.Platform.OrTwitch() == domain.PlatformKick {
		url = URLKickLogin
	}
	e.deps.Emit.Emit(EvNeedsAction, NeedsActionPayload{Reason: reason, URL: url})
	e.deps.Log.Warn().Msg("platform session is logged out; pausing until the user logs in again")
	if e.deps.OnLoggedOut != nil {
		e.deps.OnLoggedOut()
	}
	return StateIdle, reason
}

// Login pages the needs-action event for an expired session points at.
const (
	URLTwitchLogin = "https://www.twitch.tv/login"
	URLKickLogin   = "https://kick.com/login"
)

func (e *Engine) runNeedsAction(ctx context.Context) (State, string) {
	// Surface handled at the point of detection; here we simply idle until the
	// user acts (a refresh/start re-evaluates the queue).
	if e.sleepInterruptible(ctx, 30*time.Second) {
		e.drainCommands()
	}
	if e.shutdown {
		return StateIdle, "shutdown"
	}
	return StateIdle, "awaiting user action"
}

func (e *Engine) runBackoff(ctx context.Context) (State, string) {
	e.backoff++
	d := backoffDuration(e.backoff)
	until := e.deps.Now().Add(d)
	e.deps.Emit.Emit(EvError, ErrorPayload{Message: "backing off", Recoverable: true, BackoffUntil: until})
	e.deps.Log.Warn().Int("level", e.backoff).Dur("wait", d).Msg("backoff")
	if e.backoff >= 6 {
		e.backoff = 0
		return StateNeedsUserAction, "repeated failures; browser may need attention"
	}
	if e.sleepInterruptible(ctx, d) {
		e.drainCommands()
		if e.shutdown {
			return StateIdle, "shutdown"
		}
		if e.paused {
			return StateIdle, "paused"
		}
	}
	return StateDiscovering, "retry after backoff"
}

// enterBackoff is a helper to jump to backoff with a reason.
func (e *Engine) enterBackoff(reason string) (State, string) {
	e.deps.Log.Warn().Str("reason", reason).Msg("entering backoff")
	return StateBackoff, reason
}

// backoffDuration is the exponential schedule capped at 15m.
func backoffDuration(level int) time.Duration {
	switch {
	case level <= 1:
		return 30 * time.Second
	case level == 2:
		return time.Minute
	case level == 3:
		return 2 * time.Minute
	case level == 4:
		return 5 * time.Minute
	case level == 5:
		return 10 * time.Minute
	default:
		return 15 * time.Minute
	}
}

// runCooldown pauses between watch sessions and is the window in which a
// refresh may run. It always returns to Discovering.
func (e *Engine) runCooldown(ctx context.Context) (State, string) {
	e.cur = nil
	// Reset the watch fields but keep the engine identity/flags: this wholesale
	// reset must never wipe Profile or Platform (stats attribution) or Paused.
	e.setStatus(func(s *Status) {
		*s = Status{Profile: s.Profile, Platform: s.Platform, Paused: s.Paused, State: StateCooldown, Since: e.deps.Now(), Reason: s.Reason}
	})
	d := e.jittered(e.deps.Cfg.Watcher.Cooldown, e.deps.Cfg.Watcher.CooldownJitter)
	e.sleepInterruptible(ctx, d)
	e.drainCommands()
	if e.shutdown {
		return StateIdle, "shutdown"
	}
	if e.paused {
		return StateIdle, "paused"
	}
	return StateDiscovering, "cooldown complete"
}
