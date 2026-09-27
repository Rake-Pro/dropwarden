package watcher

import (
	"context"
	"regexp"
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

	// ProactiveRefresh <= 0 disables the periodic refresh entirely (Kick: the
	// platform's own FAQ says frequent refreshes hurt watch-credit tracking).
	proactiveAt := now.Add(100 * 365 * 24 * time.Hour)
	if w.ProactiveRefresh > 0 {
		proactiveAt = now.Add(e.jittered(w.ProactiveRefresh, w.ProactiveJitter))
	}
	// Computed once at session start (select.go), not here, so re-entering
	// runWatching after a mid-watch refresh does not redraw a new target.
	maxSessionAt := e.cur.maxSessionAt
	nextReassertAt := now.Add(e.fuzzed(e.deps.Cfg.Browser.Player.ReassertInterval))
	frozenTicks := 0
	var lastTime float64
	lastTimeSet := false

	tick := time.NewTimer(e.healthInterval())
	defer tick.Stop()
	events := e.deps.Ctrl.Session().Events()
	// Drain the backlog before reacting to anything: the engine only reads
	// events while watching, so navigations from tab creation, discovery, and
	// channel opening sit buffered until now - and they are history (validation
	// just confirmed the live state). Live 2026-08-31: the tab's own CREATION
	// navigation to about:blank sat buffered and was adopted as a "raid" the
	// moment watching began, burning a healthy channel.
	if n := drainEvents(events); n > 0 {
		e.deps.Log.Debug().Int("dropped", n).Msg("watching: discarded stale pre-watch browser events")
	}

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
			if next, reason, done := e.watchTick(ctx, &lastTime, &lastTimeSet, &frozenTicks, &nextReassertAt, proactiveAt, maxSessionAt); done {
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
func (e *Engine) watchTick(ctx context.Context, lastTime *float64, lastTimeSet *bool, frozenTicks *int, nextReassertAt *time.Time, proactiveAt, maxSessionAt time.Time) (State, string, bool) {
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
		e.reload(ctx)
		e.cur.hardRefreshes++
		return StateValidating, "player error; refreshed", true
	}
	// Reactive raid via pathname mismatch (SPA nav that did not fire an event).
	if !strings.Contains(strings.ToLower(h.Pathname), strings.ToLower(e.cur.login)) && h.Pathname != "" && h.Pathname != "/" {
		return e.handleNavigation(ctx, h.Pathname)
	}
	// Preemptive raid: cancel the banner to keep our credited channel.
	if h.RaidBanner {
		var ok bool
		_ = e.bounded(ctx, func(c context.Context) (err error) { ok, err = e.deps.Ctrl.CancelRaid(c); return })
		if ok {
			e.deps.Log.Info().Str("login", e.cur.login).Msg("cancelled raid to stay on channel")
		}
	}

	// Frozen video: currentTime not advancing.
	if *lastTimeSet && h.CurrentTime <= *lastTime+0.5 {
		*frozenTicks++
		if *frozenTicks >= 3 {
			_ = e.bounded(ctx, e.deps.Ctrl.ClickPlay)
			e.reload(ctx)
			e.cur.hardRefreshes++
			*frozenTicks = 0
			return StateValidating, "video frozen; refreshed", true
		}
	} else {
		*frozenTicks = 0
	}
	*lastTime, *lastTimeSet = h.CurrentTime, true

	// Volume drift. The out-of-band/muted case reasserts immediately (untouched);
	// the routine periodic reassert's cadence is fuzzed one interval at a time.
	if h.Volume <= 0 || h.Volume > 0.05 || h.Muted {
		e.reassertVolume(ctx)
	} else if now.After(*nextReassertAt) {
		e.reassertVolume(ctx)
		*nextReassertAt = now.Add(e.fuzzed(e.deps.Cfg.Browser.Player.ReassertInterval))
	}
	e.keepWindowOnBottom(ctx)
	if now.After(e.nextLiveAt) {
		e.liveSweep(ctx, "watch")
	}

	// Progress + completion: passive capture first (free), then the rate-limited
	// active poll, which is the only reliable source mid-watch.
	minutes, _ := e.updateProgressFromCapture(ctx)
	if m, _ := e.pollProgress(ctx); m > minutes {
		minutes = m
	}
	if e.cur.loggedOut {
		e.endSession(domain.OutcomeError, "platform session logged out")
		s, r := e.handleLoggedOut()
		return s, r, true
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

// noFirstCreditTimeout is how long a fully-validated watch may run with ZERO
// server-side credit and no crediting session before the drop is judged
// un-earnable. A genuinely earnable drop credits its first minute within a
// poll or two, so 5 minutes with nothing is a confident "this will never
// credit" - fast enough to keep an unattended box from wasting a full stall
// window on a reward the account already owns.
const noFirstCreditTimeout = 5 * time.Minute

// noCredit routes a zero-credit watch to the right culprit. A drop that has
// ALREADY credited minutes (on some other channel, in an earlier session or by
// hand) is plainly earnable, so the channel is at fault - blacklist it briefly
// and reselect (live 2026-09-20: 6 minutes banked on LVTHalo, eli_x credited
// nothing, and the drop was misfiled as owned). Only a drop with no progress
// at all is judged un-earnable.
func (e *Engine) noCredit() (State, string, bool) {
	if e.cur.lastMinutes > 0 {
		e.deps.Log.Warn().Str("drop", e.cur.drop.Name).Str("login", e.cur.login).Int("minutes", e.cur.lastMinutes).
			Msg("channel earned no credit for a drop that has progress elsewhere; trying another channel")
		e.endSession(domain.OutcomeAbortedStall, "no credit on this channel")
		s, r := e.failChannel(domain.OutcomeAbortedStall, "no credit on this channel", e.deps.Cfg.Watcher.OfflineBlacklistTTL)
		return s, r, true
	}
	// A window in which a progress poll failed proves nothing: the zero may
	// be ours (poll errors), not Twitch's. Start a fresh window instead of
	// filing the drop as un-earnable.
	if e.cur.pollErrors > 0 {
		e.deps.Log.Warn().Str("drop", e.cur.drop.Name).Str("login", e.cur.login).Int("poll_errors", e.cur.pollErrors).
			Msg("no credit, but progress polls failed in this window; not judging the drop un-earnable")
		e.cur.pollErrors = 0
		e.cur.lastProgress = e.deps.Now()
		return "", "", false
	}
	return e.giveUpNoCredit()
}

// reasonNoCredit is the queue/needs-action reason for a drop given up after
// the no-first-credit window.
const reasonNoCredit = "not crediting after 5 min (already-owned reward, logged out, hidden window, or region lock)"

// giveUpNoCredit abandons a drop that watched a participating channel but
// earned no credit: flag it (session-scoped) so selection skips it and falls
// through to the next eligible drop, mark it needs_action, and surface it.
func (e *Engine) giveUpNoCredit() (State, string, bool) {
	e.noCreditDrops[e.cur.drop.ID] = true
	_ = e.deps.Store.SetItemStatus(e.cur.drop.ID, domain.ItemNeedsAction, reasonNoCredit)
	e.deps.Emit.Emit(EvNeedsAction, NeedsActionPayload{DropID: e.cur.drop.ID, Reason: reasonNoCredit})
	e.deps.Log.Warn().Str("drop", e.cur.drop.Name).Str("login", e.cur.login).
		Msg("drop earned no credit on a participating channel; skipping it this session")
	e.endSession(domain.OutcomeAbortedStall, "no credit after 5 min")
	s, r := e.abandon(domain.OutcomeAbortedStall, reasonNoCredit)
	return s, r, true
}

// stallCheck applies the soft/hard stall thresholds and the proactive refresh.
func (e *Engine) stallCheck(ctx context.Context, now, proactiveAt time.Time) (State, string, bool) {
	w := e.deps.Cfg.Watcher
	sinceProgress := now.Sub(e.cur.lastProgress)

	// Fast give-up: Twitch opened no crediting session (creditChannel empty)
	// and not a minute has landed after a few polls on a validated
	// participating channel -> the drop is un-earnable for this account (a
	// reward already owned, a region block). Skip it now instead of grinding
	// out the full stall window and then every other channel.
	if e.cur.creditChannel == "" && e.cur.lastMinutes <= e.cur.startMinutes && sinceProgress >= noFirstCreditTimeout {
		return e.noCredit()
	}

	// Proactive refresh even when healthy, to beat the +15m client estimate cap.
	if now.After(proactiveAt) {
		e.reload(ctx)
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
			// Zero credit and no crediting session: the drop is un-earnable
			// (the fast path above usually catches this first; this is the
			// backstop when progress moved once then stopped without a session).
			if e.cur.creditChannel == "" && e.cur.lastMinutes <= e.cur.startMinutes {
				return e.noCredit()
			}
			e.endSession(domain.OutcomeAbortedStall, "repeated hard stall")
			s, r := e.failChannel(domain.OutcomeAbortedStall, "progress would not advance", w.ChannelBlacklistTTL)
			return s, r, true
		}
		e.reload(ctx)
		e.cur.lastProgress = now // give the refresh a chance before counting another stall
		e.cur.pollErrors = 0
		return StateValidating, "hard stall; refreshed", true
	}
	if sinceProgress >= w.SoftStall {
		e.cur.stallEvents++
		// Once per stall episode, ask the platform whether the channel is
		// still live on the campaign's game: an offline or off-game stream
		// fails fast here instead of waiting out the hard stall.
		if !e.cur.softChecked.Equal(e.cur.lastProgress) {
			e.cur.softChecked = e.cur.lastProgress
			if problem := e.streamProblem(ctx); problem != "" {
				e.endSession(domain.OutcomeAbortedOffline, problem)
				s, r := e.failChannel(domain.OutcomeAbortedOffline, problem, w.OfflineBlacklistTTL)
				return s, r, true
			}
		}
		_ = e.bounded(ctx, e.deps.Ctrl.ToggleDropsPanel)
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
		e.reload(ctx)
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
// drainEvents empties a buffered browser-event channel without blocking and
// returns how many events were discarded.
func drainEvents(ch <-chan browser.Event) (n int) {
	for {
		select {
		case <-ch:
			n++
		default:
			return n
		}
	}
}

// loginValidRe is the conservative channel-login shape shared by the
// platforms we drive; anything else (about:blank, chrome-error://..., data:
// URLs) is NOT a channel and must never be treated as a raid target.
var loginValidRe = regexp.MustCompile(`^[a-z0-9_-]{2,30}$`)

func loginFromPath(host, p string) string {
	if host == "" {
		host = "twitch.tv"
	}
	s := p
	if i := strings.Index(s, host+"/"); i >= 0 {
		s = s[i+len(host)+1:]
	} else if strings.Contains(s, "://") {
		// A full URL that is NOT on this platform's channel host (about:blank
		// counts once schemes like chrome-error:// are considered too) can
		// never name one of its channels.
		return ""
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
	case "", "directory", "drops", "settings", "videos", "downloads", "store", "subscriptions",
		"login", "search", "popout", "moderator", "embed", "u", "p", "team", "event", "inventory":
		return ""
	}
	// Shape check last: "about:blank" (single colon, no ://) and every other
	// non-channel URL survives the extraction above but can never pass this.
	if !loginValidRe.MatchString(s) {
		return ""
	}
	return s
}
