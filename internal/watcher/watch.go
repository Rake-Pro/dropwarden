package watcher

import (
	"context"
	"strings"
	"time"

	"github.com/rake-pro/dropwarden/internal/browser"
	"github.com/rake-pro/dropwarden/internal/domain"
)

// runWatching is the steady state. A single jittered ticker drives the health
// probe, progress capture, and the stall/playback/session watchdogs; the crash
// watchdog is event-driven off the browser event channel. It returns the next
// state when any watchdog trips or the drop completes.
func (e *Engine) runWatching(ctx context.Context) (State, string) {
	if e.cur == nil {
		return StateSelectingChannel, "no active watch"
	}
	e.skipReq = false
	w := e.deps.Cfg.Watcher
	now := e.deps.Now()

	proactiveAt := now.Add(e.jittered(w.ProactiveRefresh, w.ProactiveJitter))
	maxSessionAt := e.cur.sessionStart.Add(w.MaxSession)
	lastReassert := now
	frozenTicks := 0
	var lastTime float64
	lastTimeSet := false

	tick := time.NewTimer(e.healthInterval())
	defer tick.Stop()
	events := e.deps.Ctrl.Session().Events()

	for {
		select {
		case <-ctx.Done():
			e.endSession(domain.OutcomeUserStop, "context cancelled")
			return StateIdle, "cancelled"

		case c := <-e.cmds:
			e.processCommand(c)
			if e.shutdown {
				e.endSession(domain.OutcomeUserStop, "shutdown")
				return StateIdle, "shutdown"
			}
			if e.paused {
				e.endSession(domain.OutcomeUserStop, "stopped")
				return StateIdle, "stopped"
			}
			if e.skipReq {
				e.endSession(domain.OutcomeUserStop, "skipped")
				return e.abandon(domain.OutcomeUserStop, "skip requested")
			}

		case ev := <-events:
			if next, reason, done := e.handleBrowserEvent(ctx, ev); done {
				return next, reason
			}

		case <-tick.C:
			if next, reason, done := e.watchTick(ctx, &lastTime, &lastTimeSet, &frozenTicks, &lastReassert, proactiveAt, maxSessionAt); done {
				return next, reason
			}
			tick.Reset(e.healthInterval())
		}
	}
}

// healthInterval applies the configured jitter percent to the base health tick.
func (e *Engine) healthInterval() time.Duration {
	base := e.deps.Cfg.Watcher.HealthTick
	pct := e.deps.Cfg.Watcher.HealthTickJitter
	if pct <= 0 {
		return base
	}
	// +/- pct percent.
	delta := (e.deps.Rand()*2 - 1) * float64(pct) / 100.0
	d := time.Duration(float64(base) * (1 + delta))
	if d < time.Second {
		d = time.Second
	}
	return d
}

// handleBrowserEvent reacts to async crash/disconnect/navigation events.
func (e *Engine) handleBrowserEvent(ctx context.Context, ev browser.Event) (State, string, bool) {
	switch ev.Kind {
	case browser.EventCrash, browser.EventDisconnect:
		e.endSession(domain.OutcomeCrashed, string(ev.Kind))
		return e.enterBackoffDone("browser " + string(ev.Kind))
	case browser.EventNavigated:
		return e.handleNavigation(ctx, ev.URL)
	}
	return "", "", false
}

func (e *Engine) enterBackoffDone(reason string) (State, string, bool) {
	s, r := e.enterBackoff(reason)
	return s, r, true
}

// watchTick runs the per-tick watchdogs. Returns done=true with the next state
// when a transition is required.
func (e *Engine) watchTick(ctx context.Context, lastTime *float64, lastTimeSet *bool, frozenTicks *int, lastReassert *time.Time, proactiveAt, maxSessionAt time.Time) (State, string, bool) {
	now := e.deps.Now()

	// Session-level guards first.
	if now.After(maxSessionAt) {
		e.endSession(domain.OutcomeCompleted, "max session duration")
		return StateCooldown, "session rotation", true
	}
	if !e.cur.campaign.EndAt.IsZero() && now.After(e.cur.campaign.EndAt) {
		e.endSession(domain.OutcomeChannelEnded, "campaign ended")
		return StateDiscovering, "campaign ended", true
	}

	h, err := e.probe(ctx)
	if err != nil {
		// Hang: two consecutive unanswered probes => treat as crash/reopen.
		*frozenTicks++
		if *frozenTicks >= 2 {
			e.endSession(domain.OutcomeCrashed, "renderer hang")
			return e.enterBackoffDone("renderer not responding")
		}
		return "", "", false
	}

	// Playback health.
	if h.OfflineGate {
		e.endSession(domain.OutcomeAbortedOffline, "channel offline")
		s, r := e.failChannel(domain.OutcomeAbortedOffline, "channel went offline", e.deps.Cfg.Watcher.OfflineBlacklistTTL)
		return s, r, true
	}
	if h.PlayerError {
		_ = e.deps.Ctrl.Session().Reload(ctx, true)
		e.cur.hardRefreshes++
		return StateValidating, "player error; refreshed", true
	}
	// Reactive raid via pathname mismatch (SPA nav that did not fire an event).
	if !strings.Contains(strings.ToLower(h.Pathname), strings.ToLower(e.cur.login)) && h.Pathname != "" && h.Pathname != "/" {
		return e.handleNavigation(ctx, h.Pathname)
	}
	// Preemptive raid: cancel the banner to keep our credited channel.
	if h.RaidBanner {
		if ok, _ := e.deps.Ctrl.CancelRaid(ctx); ok {
			e.deps.Log.Info().Str("login", e.cur.login).Msg("cancelled raid to stay on channel")
		}
	}

	// Frozen video: currentTime not advancing.
	if *lastTimeSet && h.CurrentTime <= *lastTime+0.5 {
		*frozenTicks++
		if *frozenTicks >= 3 {
			_ = e.deps.Ctrl.ClickPlay(ctx)
			_ = e.deps.Ctrl.Session().Reload(ctx, true)
			e.cur.hardRefreshes++
			*frozenTicks = 0
			return StateValidating, "video frozen; refreshed", true
		}
	} else {
		*frozenTicks = 0
	}
	*lastTime, *lastTimeSet = h.CurrentTime, true

	// Volume drift.
	if h.Volume <= 0 || h.Volume > 0.05 || h.Muted {
		_ = e.deps.Ctrl.ReassertVolume(ctx, e.playerPolicy())
	} else if now.Sub(*lastReassert) >= e.deps.Cfg.Browser.Player.ReassertInterval {
		_ = e.deps.Ctrl.ReassertVolume(ctx, e.playerPolicy())
		*lastReassert = now
	}

	// Progress + completion: passive capture first (free), then the rate-limited
	// active poll, which is the only reliable source mid-watch.
	minutes, _ := e.updateProgressFromCapture(ctx)
	if m, _ := e.pollProgress(ctx); m > minutes {
		minutes = m
	}
	if e.cur.drop.RequiredMinutes > 0 && minutes >= e.cur.drop.RequiredMinutes {
		e.endSession(domain.OutcomeCompleted, "required minutes met")
		return StateClaiming, "drop complete", true
	}

	// Stall watchdog.
	if next, reason, done := e.stallCheck(ctx, now, proactiveAt); done {
		return next, reason, true
	}
	return "", "", false
}

// stallCheck applies the soft/hard stall thresholds and the proactive refresh.
func (e *Engine) stallCheck(ctx context.Context, now, proactiveAt time.Time) (State, string, bool) {
	w := e.deps.Cfg.Watcher
	sinceProgress := now.Sub(e.cur.lastProgress)

	// Proactive refresh even when healthy, to beat the +15m client estimate cap.
	if now.After(proactiveAt) {
		_ = e.deps.Ctrl.Session().Reload(ctx, true)
		e.cur.hardRefreshes++
		return StateValidating, "proactive refresh", true
	}

	if sinceProgress >= w.HardStall {
		e.cur.stallEvents++
		e.cur.hardRefreshes++
		if e.cur.hardRefreshes > w.HardStallMaxRetries {
			// When the poll shows Twitch crediting a DIFFERENT channel, the drop
			// slot is held by another session (e.g. the user watching on a phone).
			// The channel is fine - stand down to cooldown without blacklisting so
			// selection keeps retrying it on the normal cadence.
			if other := e.cur.creditChannel; other != "" && !strings.EqualFold(other, e.cur.login) {
				e.deps.Log.Warn().Str("watching", e.cur.login).Str("crediting", other).
					Msg("another session holds the drop slot; standing down without penalizing the channel")
				e.endSession(domain.OutcomeAbortedStall, "credited elsewhere: "+other)
				s, r := e.abandon(domain.OutcomeAbortedStall, "another session is being credited ("+other+")")
				return s, r, true
			}
			e.endSession(domain.OutcomeAbortedStall, "repeated hard stall")
			s, r := e.failChannel(domain.OutcomeAbortedStall, "progress would not advance", w.ChannelBlacklistTTL)
			return s, r, true
		}
		_ = e.deps.Ctrl.Session().Reload(ctx, true)
		e.cur.lastProgress = now // give the refresh a chance before counting another stall
		return StateValidating, "hard stall; refreshed", true
	}
	if sinceProgress >= w.SoftStall {
		e.cur.stallEvents++
		_ = e.deps.Ctrl.ToggleDropsPanel(ctx)
	}
	return "", "", false
}

// handleNavigation reacts to the active channel changing (a raid or redirect).
func (e *Engine) handleNavigation(ctx context.Context, urlOrPath string) (State, string, bool) {
	newLogin := loginFromPath(e.deps.ChannelHost, urlOrPath)
	if newLogin == "" || newLogin == e.cur.login {
		return "", "", false
	}
	e.cur.raids++
	if e.cur.raids > 2 {
		e.endSession(domain.OutcomeAbortedRaid, "raid chain")
		s, r := e.abandon(domain.OutcomeAbortedRaid, "too many raids this session")
		return s, r, true
	}
	if e.channelEligible(newLogin) {
		// Adopt the raid target, force a hard refresh, and re-validate.
		e.deps.Log.Info().Str("from", e.cur.login).Str("to", newLogin).Msg("adopting eligible raid target")
		e.cur.login = newLogin
		e.cur.session.Note = "adopted raid to " + newLogin
		_ = e.deps.Ctrl.Session().Reload(ctx, true)
		e.cur.hardRefreshes++
		return StateValidating, "adopted raid target", true
	}
	e.endSession(domain.OutcomeAbortedRaid, "raided to ineligible channel")
	s, r := e.abandon(domain.OutcomeAbortedRaid, "raided away from drop")
	return s, r, true
}

// channelEligible reports whether a login can credit the current campaign's drop.
func (e *Engine) channelEligible(login string) bool {
	c := e.cur.campaign
	if c.AnyChannel() {
		// For an any-channel campaign we cannot cheaply confirm the raid target
		// still streams the game with drops enabled, so re-validation on the new
		// channel (drops indicator check) is the real gate. Treat as eligible and
		// let ValidatingPlayback reject it if the drops indicator is absent.
		return true
	}
	for _, ch := range c.AllowedChannels {
		if strings.EqualFold(ch.Login, login) {
			return true
		}
	}
	return false
}

// loginFromPath extracts the channel login from a URL or pathname. host is the
// platform's channel host (e.g. "twitch.tv", "kick.com"); empty means Twitch.
func loginFromPath(host, p string) string {
	if host == "" {
		host = "twitch.tv"
	}
	s := p
	if i := strings.Index(s, host+"/"); i >= 0 {
		s = s[i+len(host)+1:]
	}
	s = strings.TrimPrefix(s, "/")
	// Cut at the next path/query separator.
	for _, sep := range []string{"/", "?", "#"} {
		if i := strings.Index(s, sep); i >= 0 {
			s = s[:i]
		}
	}
	s = strings.ToLower(strings.TrimSpace(s))
	// Reserved first-segment paths are not channels.
	switch s {
	case "", "directory", "drops", "settings", "videos", "downloads", "store", "subscriptions":
		return ""
	}
	return s
}
