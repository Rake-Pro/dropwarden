package watcher

import (
	"context"
	"strings"
	"time"

	"github.com/rake-pro/dropwarden/internal/domain"
	"github.com/rake-pro/dropwarden/internal/platform"
)

func (e *Engine) playerPolicy() platform.PlayerPolicy {
	return platform.PlayerPolicy{
		Volume:        e.deps.Cfg.Browser.Player.Volume,
		LowestQuality: e.deps.Cfg.Browser.Player.ForceLowestQuality,
	}
}

func (e *Engine) runOpening(ctx context.Context) (State, string) {
	if e.cur == nil {
		return StateSelectingChannel, "no active watch"
	}
	e.setWindowMinimized(ctx, false)
	if err := e.deps.Pages.OpenChannel(ctx, e.cur.login, e.playerPolicy()); err != nil {
		return e.enterBackoff("open channel: " + err.Error())
	}
	return StateValidating, "opened " + e.cur.login
}

// validationPass is the playback gate: path, playing, volume, and time
// advancing. The drops indicator is deliberately NOT part of it - it is
// advisory (a stale DOM selector must never block watching; server-side
// progress plus the stall ladder are the real crediting check). Pinned by
// TestValidationPassIgnoresDropsIndicator.
func validationPass(pathOK, playing, volOK, advancing bool) bool {
	return pathOK && playing && volOK && advancing
}

// runValidating enforces the playback checklist within the configured
// timeout. It is also re-run after every hard refresh.
func (e *Engine) runValidating(ctx context.Context) (State, string) {
	if e.cur == nil {
		return StateSelectingChannel, "no active watch"
	}
	// Also covers mid-watch hard refreshes, which re-enter this state while the
	// window is minimized - the player must be visible to (re)start playback.
	e.setWindowMinimized(ctx, false)
	deadline := e.deps.Now().Add(e.deps.Cfg.Watcher.ValidateTimeout)
	var prevTime float64
	var prevSet bool

	// Diagnostics: a timeout must name the failing gate(s), not just "timed
	// out" - validation failures are otherwise undebuggable from the log.
	type gates struct {
		h                               platform.Health
		pathOK, playing, volOK, advance bool
	}
	var last gates
	var lastSet, firstLogged bool
	probeErrs := 0
	var lastErr error

	for e.deps.Now().Before(deadline) {
		if e.sleepInterruptible(ctx, 5*time.Second) {
			e.drainCommands()
			if e.shutdown || e.paused {
				return StateIdle, "interrupted"
			}
			if e.skipReq {
				return e.abandon(domain.OutcomeUserStop, "skip requested")
			}
		}
		h, err := e.probe(ctx)
		if err != nil {
			probeErrs++
			lastErr = err
			continue // transient; keep trying until deadline
		}
		if h.OfflineGate {
			return e.failChannel(domain.OutcomeAbortedOffline, "channel offline", e.deps.Cfg.Watcher.OfflineBlacklistTTL)
		}
		if h.PlayerError {
			// One reload attempt, then keep validating.
			_ = e.deps.Ctrl.Session().Reload(ctx, true)
			continue
		}
		pathOK := strings.Contains(strings.ToLower(h.Pathname), strings.ToLower(e.cur.login))
		playing := h.Playing()
		volOK := h.Volume > 0 && h.Volume <= 0.05 && !h.Muted
		if !playing {
			_ = e.deps.Ctrl.ClickPlay(ctx)
		}
		if !volOK {
			_ = e.deps.Ctrl.ReassertVolume(ctx, e.playerPolicy())
		}
		advancing := prevSet && h.CurrentTime > prevTime+1.0
		prevTime, prevSet = h.CurrentTime, true
		last, lastSet = gates{h: h, pathOK: pathOK, playing: playing, volOK: volOK, advance: advancing}, true
		if !firstLogged {
			firstLogged = true
			e.deps.Log.Info().Str("login", e.cur.login).
				Str("pathname", h.Pathname).Str("visibility", h.Visibility).
				Bool("path_ok", pathOK).Bool("playing", playing).Bool("vol_ok", volOK).
				Bool("drops_indicator", h.DropsIndicator).
				Int("ready_state", h.ReadyState).Bool("paused", h.Paused).
				Float64("current_time", h.CurrentTime).Float64("volume", h.Volume).Bool("muted", h.Muted).
				Msg("validating: first probe")
		}

		if validationPass(pathOK, playing, volOK, advancing) {
			// The drops indicator is ADVISORY, not a gate (live finding
			// 2026-08-31: healthy playback on every channel with the indicator
			// selector matching nothing, so requiring it wedged validation into
			// a reselect loop). The authoritative "is this crediting" signal is
			// server-side progress, which the watch phase already enforces via
			// the stall ladder and the credited-elsewhere handling; a stale DOM
			// selector must never be able to block watching.
			if !h.DropsIndicator {
				e.deps.Log.Info().Str("login", e.cur.login).
					Msg("validating: drops indicator not detected (selector may be stale); relying on server-side progress")
			}
			return e.beginWatching(ctx)
		}
	}
	ev := e.deps.Log.Warn().Str("login", e.cur.login).Int("probe_errors", probeErrs)
	if lastErr != nil {
		ev = ev.AnErr("last_probe_error", lastErr)
	}
	if lastSet {
		ev = ev.Str("pathname", last.h.Pathname).Str("visibility", last.h.Visibility).
			Bool("path_ok", last.pathOK).Bool("playing", last.playing).Bool("vol_ok", last.volOK).
			Bool("advancing", last.advance).Bool("drops_indicator", last.h.DropsIndicator).
			Int("ready_state", last.h.ReadyState).Bool("paused", last.h.Paused).
			Float64("current_time", last.h.CurrentTime).Float64("volume", last.h.Volume).Bool("muted", last.h.Muted)
	} else {
		ev = ev.Bool("any_probe_succeeded", false)
	}
	ev.Msg("validating: timed out - gate breakdown")
	return e.failChannel(domain.OutcomeAbortedStall, "playback validation timed out", e.deps.Cfg.Watcher.OfflineBlacklistTTL)
}

// beginWatching records the session baseline and enters Watching. Re-entry
// after a mid-watch hard/proactive refresh CONTINUES the existing session (one
// session = one continuous watch of a channel toward a drop; refreshes do not
// end it) - a fresh session is only minted when none is in flight.
func (e *Engine) beginWatching(ctx context.Context) (State, string) {
	now := e.deps.Now()
	base := 0
	if p, err := e.deps.Store.GetProgress(e.cur.drop.ID); err == nil && p != nil {
		base, _ = p.Effective()
	}
	if e.cur.session.ID == "" {
		e.cur.startMinutes = base
		e.cur.lastMinutes = base
		e.cur.session = domain.WatchSession{
			ID:           newSessionID(now, e.deps.Rand),
			DropID:       e.cur.drop.ID,
			CampaignID:   e.cur.campaign.ID,
			Platform:     e.deps.Platform,
			ChannelLogin: e.cur.login,
			StartedAt:    now,
			StartMinutes: base,
		}
		e.setStatus(func(s *Status) { s.SessionStartedAt = now })
	} else if base > e.cur.lastMinutes {
		// Continuation after a refresh: keep the session, adopt newer minutes.
		e.cur.lastMinutes = base
	}
	e.cur.lastProgress = now
	e.setStatus(func(s *Status) { s.Current = e.cur.lastMinutes })
	// Playback is confirmed; tuck the window away again if running minimized.
	e.setWindowMinimized(ctx, true)
	return StateWatching, "playback validated"
}

// failChannel records a failed channel, blacklists it, and reselects.
func (e *Engine) failChannel(outcome domain.SessionOutcome, reason string, ttl time.Duration) (State, string) {
	if e.cur != nil {
		_ = e.deps.Store.RecordChannelResult(e.deps.Platform, e.cur.login, false)
		_ = e.deps.Store.BlacklistChannel(e.deps.Platform, e.cur.login, reason, e.deps.Now().Add(ttl))
		e.deps.Log.Warn().Str("login", e.cur.login).Str("reason", reason).Msg("channel failed")
	}
	e.cur = nil
	return StateSelectingChannel, reason
}

// abandon ends the current attempt without penalizing the channel.
func (e *Engine) abandon(outcome domain.SessionOutcome, reason string) (State, string) {
	if e.cur != nil {
		_ = e.deps.Store.SetItemStatus(e.cur.drop.ID, domain.ItemPending, reason)
	}
	e.cur = nil
	if e.paused {
		return StateIdle, reason
	}
	return StateCooldown, reason
}
