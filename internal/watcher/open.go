package watcher

import (
	"context"
	"strings"
	"time"

	"github.com/rake-pro/dropwarden/internal/domain"
	"github.com/rake-pro/dropwarden/internal/twitch"
)

func (e *Engine) playerPolicy() twitch.PlayerPolicy {
	return twitch.PlayerPolicy{
		Volume:        e.deps.Cfg.Browser.Player.Volume,
		LowestQuality: e.deps.Cfg.Browser.Player.ForceLowestQuality,
	}
}

func (e *Engine) runOpening(ctx context.Context) (State, string) {
	if e.cur == nil {
		return StateSelectingChannel, "no active watch"
	}
	if err := e.deps.Pages.OpenChannel(ctx, e.cur.login, e.playerPolicy()); err != nil {
		return e.enterBackoff("open channel: " + err.Error())
	}
	return StateValidating, "opened " + e.cur.login
}

// runValidating enforces the six-point playback checklist within the configured
// timeout. It is also re-run after every hard refresh.
func (e *Engine) runValidating(ctx context.Context) (State, string) {
	if e.cur == nil {
		return StateSelectingChannel, "no active watch"
	}
	deadline := e.deps.Now().Add(e.deps.Cfg.Watcher.ValidateTimeout)
	var prevTime float64
	var prevSet bool

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

		if pathOK && playing && volOK && advancing && h.DropsIndicator {
			return e.beginWatching(ctx)
		}
	}
	return e.failChannel(domain.OutcomeAbortedStall, "playback validation timed out", e.deps.Cfg.Watcher.OfflineBlacklistTTL)
}

// beginWatching records the session baseline and enters Watching.
func (e *Engine) beginWatching(ctx context.Context) (State, string) {
	now := e.deps.Now()
	base := 0
	if p, err := e.deps.Store.GetProgress(e.cur.drop.ID); err == nil && p != nil {
		base, _ = p.Effective()
	}
	e.cur.startMinutes = base
	e.cur.lastMinutes = base
	e.cur.lastProgress = now
	e.cur.session = domain.WatchSession{
		ID:           newSessionID(now, e.deps.Rand),
		DropID:       e.cur.drop.ID,
		CampaignID:   e.cur.campaign.ID,
		ChannelLogin: e.cur.login,
		StartedAt:    now,
		StartMinutes: base,
	}
	_ = ctx
	return StateWatching, "playback validated"
}

// failChannel records a failed channel, blacklists it, and reselects.
func (e *Engine) failChannel(outcome domain.SessionOutcome, reason string, ttl time.Duration) (State, string) {
	if e.cur != nil {
		_ = e.deps.Store.RecordChannelResult(e.cur.login, false)
		_ = e.deps.Store.BlacklistChannel(e.cur.login, reason, e.deps.Now().Add(ttl))
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
