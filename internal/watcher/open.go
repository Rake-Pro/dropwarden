package watcher

import (
	"context"
	"errors"
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
	// For an ANY-CHANNEL campaign, drops only credit on a PARTICIPATING stream
	// (Twitch: "go to a participating live channel"), so the drops indicator is
	// a real gate: healthy playback on a non-participating Once Human streamer
	// earns nothing forever (live 2026-08-31). For an ACL campaign the
	// allowlisted channels are known participating, so the indicator stays
	// advisory there (its selector can false-negative).
	requireDrops := e.cur.campaign.AnyChannel()
	// Server-side liveness first: an offline channel with "autoplay recent
	// broadcast" plays a VOD that passes every playback gate below and never
	// credits, which the stall ladder then misreads as an un-earnable drop.
	// Skipped when the platform has no status query or the call fails.
	if liveNow, err := e.deps.Pages.LiveChannels(ctx, []string{e.cur.login}); err == nil && len(liveNow) == 0 {
		return e.failChannel(domain.OutcomeAbortedOffline, "channel offline (no live stream)", e.deps.Cfg.Watcher.OfflineBlacklistTTL)
	} else if err != nil && !errors.Is(err, errors.ErrUnsupported) {
		e.deps.Log.Warn().Err(err).Str("login", e.cur.login).Msg("validating: live status read failed; relying on playback gates")
	}
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
			// Playback is healthy. For an any-channel campaign a missing drops
			// indicator now means "not a participating stream": fail fast and
			// reselect rather than watch it for nothing (there is no point
			// waiting the full timeout once playback is confirmed good). For an
			// ACL campaign it stays advisory - the channel is allowlisted, and
			// server-side progress plus the stall ladder are the real check.
			if requireDrops && !h.DropsIndicator {
				return e.failChannel(domain.OutcomeAbortedStall,
					"channel not participating in drops (playback ok, no drops indicator)",
					e.deps.Cfg.Watcher.OfflineBlacklistTTL)
			}
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
	// Keep the window VISIBLE while watching: a minimized tab reads
	// visibilityState "hidden" and Twitch pauses minute-watched heartbeats for
	// a hidden tab, so drops never credit (confirmed live 2026-08-31). Only
	// re-minimize when the user explicitly opts in and accepts that tradeoff.
	if e.deps.Cfg.Browser.Window.MinimizeWhileWatching {
		e.setWindowMinimized(ctx, true)
	}
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
