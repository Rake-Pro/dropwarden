package watcher

import (
	"context"
	"time"
)

// processCommand applies one command to engine flags. It returns true if the
// command was a shutdown.
func (e *Engine) processCommand(c command) {
	switch c.kind {
	case cmdStart:
		e.paused = false
	case cmdStop:
		e.paused = true
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

func (e *Engine) runDiscovering(ctx context.Context) (State, string) {
	e.drainCommands()
	if e.shutdown {
		return StateIdle, "shutdown"
	}
	e.refreshReq = false
	res, err := e.deps.Disco.RunOnce(ctx)
	if err != nil {
		return e.enterBackoff("discovery: " + err.Error())
	}
	e.deps.Emit.Emit(EvCampaigns, map[string]any{"count": res.Campaigns + res.Details, "at": e.deps.Now()})
	e.backoff = 0
	if e.paused {
		return StateIdle, "paused"
	}
	return StateSelectingChannel, "discovered"
}

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
	// reset must never wipe Platform (stats attribution) or Paused.
	e.setStatus(func(s *Status) {
		*s = Status{Platform: s.Platform, Paused: s.Paused, State: StateCooldown, Since: e.deps.Now()}
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
