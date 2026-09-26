package watcher

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/rake-pro/dropwarden/internal/discovery"
	"github.com/rake-pro/dropwarden/internal/domain"
	"github.com/rake-pro/dropwarden/internal/platform"
)

// cdpTimeout bounds one Reload/Navigate/Eval the engine issues. The engine is
// a single goroutine: a CDP call that never answers (a reload whose
// FrameNavigated is lost, a page fetch that hangs) would otherwise freeze
// every watchdog and command with it.
const cdpTimeout = 45 * time.Second

// bounded runs one browser call under the engine's CDP timeout.
func (e *Engine) bounded(ctx context.Context, f func(context.Context) error) error {
	cctx, cancel := context.WithTimeout(ctx, e.cdpTimeout)
	defer cancel()
	return f(cctx)
}

// reload hard-refreshes the tab under the CDP timeout.
func (e *Engine) reload(ctx context.Context) {
	_ = e.bounded(ctx, func(c context.Context) error { return e.deps.Ctrl.Session().Reload(c, true) })
}

// probeTimeout bounds a health probe. A timeout here is the hang signal: the
// renderer is not answering, distinct from a crash event.
const probeTimeout = 10 * time.Second

// probe runs a health probe under a hard timeout so a hung renderer surfaces as
// an error rather than blocking the engine loop.
func (e *Engine) probe(ctx context.Context) (platform.Health, error) {
	pctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	return e.deps.Ctrl.Probe(pctx)
}

// newSessionID builds a time-sortable session id: zero-padded unix millis plus a
// short random suffix, so history reads back in order without a UUID dependency.
func newSessionID(now time.Time, rnd func() float64) string {
	return fmt.Sprintf("%013d-%04d", now.UnixMilli(), int(rnd()*10000))
}

// updateProgressFromCapture drains passively observed progress (e.g. Twitch's
// GQL capture shim) and, for the active drop, updates the store and the
// in-memory baseline. It returns the current best-known minutes and whether
// they increased since last time.
func (e *Engine) updateProgressFromCapture(ctx context.Context) (minutes int, increased bool) {
	if e.cur == nil {
		return 0, false
	}
	var ps []domain.Progress
	err := e.bounded(ctx, func(c context.Context) (err error) { ps, err = e.deps.Pages.DrainProgress(c); return })
	if err != nil {
		return e.cur.lastMinutes, false
	}
	return e.applyProgress(ps)
}

// pollProgress actively fetches server-side progress at most once per
// (fuzzed) watcher.progress_poll: the viewer's current drop session plus the
// full inventory. It exists because the channel page does not fire progress GQL over
// fetch/XHR on its own (live updates ride a WebSocket the capture shim cannot
// see), so passive capture alone leaves the UI frozen at 0. The inventory read
// matters for simultaneous multi-tier campaigns: dropCurrentSession names only
// ONE drop, which need not be the tier we queued, while the inventory carries
// per-drop minutes for everything in progress (up-next rows included).
func (e *Engine) pollProgress(ctx context.Context) (minutes int, increased bool) {
	if e.cur == nil {
		return 0, false
	}
	interval := e.deps.Cfg.Watcher.ProgressPoll
	if interval <= 0 {
		interval = time.Minute
	}
	now := e.deps.Now()
	if now.Before(e.cur.nextPollAt) {
		return e.cur.lastMinutes, false
	}
	// Pick the NEXT poll's due time now, fuzzed: this is a page-context GQL
	// fetch, the most network-visible fixed cadence in the app, so it should
	// not land on the exact interval every time.
	e.cur.nextPollAt = now.Add(e.fuzzed(interval))
	var ps []domain.Progress
	sessionFound := false
	sessionChannel := ""
	sessionMinutes := -1
	var p *domain.Progress
	if err := e.bounded(ctx, func(c context.Context) (err error) { p, err = e.deps.Pages.FetchCurrentDrop(c); return }); err != nil {
		e.cur.pollErrors++
		if errors.Is(err, domain.ErrLoggedOut) {
			e.cur.loggedOut = true
		}
		e.deps.Log.Warn().Err(err).Msg("current-drop poll failed")
	} else if p != nil {
		sessionFound = true
		sessionChannel = p.ChannelLogin
		sessionMinutes, _ = p.Effective()
		if p.ChannelLogin != "" {
			e.cur.creditChannel = p.ChannelLogin
		}
		ps = append(ps, *p)
	}
	invRows := 0
	var inv []domain.Progress
	if err := e.bounded(ctx, func(c context.Context) (err error) { inv, err = e.deps.Pages.FetchInventoryProgress(c); return }); err != nil {
		if !errors.Is(err, errors.ErrUnsupported) {
			e.cur.pollErrors++
			if errors.Is(err, domain.ErrLoggedOut) {
				e.cur.loggedOut = true
			}
			e.deps.Log.Warn().Err(err).Msg("inventory poll failed")
		}
	} else {
		invRows = len(inv)
		ps = append(ps, inv...)
		e.reconcileClaimedRows(inv)
	}
	// Diagnostics: does the drop we are ACTUALLY watching for appear in the
	// rows Twitch returned, and with what minutes? inventory_rows>0 while
	// session_found=false and minutes=0 means the replay works but Twitch is
	// crediting nothing for THIS drop - the question is whether our drop is
	// even in the set (wrong campaign selected) or present-but-frozen (Twitch
	// not registering the view, the minimized-tab-heartbeat suspect).
	activeInRows, activeRowMinutes := false, -1
	for _, p := range ps {
		if p.DropID == e.cur.drop.ID {
			activeInRows = true
			activeRowMinutes, _ = p.Effective()
			break
		}
	}
	// The player's own heartbeat since the last poll: minute-watched beacons
	// prove the client is reporting the watch; a drop-progress push proves the
	// server is crediting it. Zero beacons with healthy playback is a CLIENT
	// problem; beacons with no credit is Twitch's verdict on the drop/channel.
	var hb domain.Heartbeat
	err := e.bounded(ctx, func(c context.Context) (err error) { hb, err = e.deps.Pages.DrainHeartbeat(c); return })
	if err != nil {
		e.deps.Log.Warn().Err(err).Msg("heartbeat drain failed")
	} else {
		e.cur.heartbeats += hb.MinuteWatched
		if len(hb.Progress) > 0 {
			ps = append(ps, hb.Progress...)
			if e.cur.creditChannel == "" {
				e.cur.creditChannel = e.cur.login // a push while we watch here means we are the credited session
			}
		}
		if e.deps.Cfg.Logging.CaptureGQLBodies && (hb.MinuteWatched > 0 || hb.DropMessages > 0) {
			e.dumpDebug("heartbeat-"+e.cur.login+".json", hb.Raw)
		}
	}
	// Tab visibility at poll time: Twitch stops minute-watched heartbeats for a
	// hidden tab, so a "hidden" here while progress stays 0 points straight at
	// the re-minimize after validation. One extra probe/min is cheap.
	visibility := ""
	if h, err := e.probe(ctx); err == nil {
		visibility = h.Visibility
	}
	e.checkHidden(ctx, visibility)
	minutes, increased = e.cur.lastMinutes, false
	if len(ps) > 0 {
		minutes, increased = e.applyProgress(ps)
	}
	// Another drop (e.g. a simultaneous tier) finished while we watch: claim it
	// now rather than waiting for the next discovery pass.
	if e.sweepWorthy(ps) {
		e.claimSweep(ctx, "progress poll")
	}
	// One line per poll (~1/min): a poll that SUCCEEDS but sees nothing is
	// otherwise invisible in the log, indistinguishable from Twitch simply not
	// having credited the first minute yet - the same blind spot the validation
	// gate breakdown fixed.
	e.deps.Log.Info().Bool("session_found", sessionFound).Str("session_channel", sessionChannel).
		Int("session_minutes", sessionMinutes).Int("inventory_rows", invRows).
		Str("active_drop", e.cur.drop.ID).Bool("active_in_rows", activeInRows).
		Int("active_row_minutes", activeRowMinutes).Str("visibility", visibility).
		Int("mw_beacons", hb.MinuteWatched).Int("mw_total", e.cur.heartbeats).Str("mw_channel", hb.LastChannel).
		Bool("mw_hidden", hb.LastHidden).Bool("mw_muted", hb.LastMuted).Interface("beacon_events", hb.Events).
		Int("ws_drop_msgs", hb.DropMessages).Int("ws_progress", len(hb.Progress)).
		Int("minutes", minutes).Bool("increased", increased).
		Msg("progress poll")
	return minutes, increased
}

// applyProgress stores every progress observation (other drops' rows keep the
// queue/up-next display current) and advances the in-memory baseline for the
// active drop, emitting an event when its minutes moved.
func (e *Engine) applyProgress(ps []domain.Progress) (minutes int, increased bool) {
	best := e.cur.lastMinutes
	var src domain.ProgressSource
	for _, p := range ps {
		if p.RequiredMinutes == 0 && p.DropID == e.cur.drop.ID {
			p.RequiredMinutes = e.cur.drop.RequiredMinutes
		}
		_ = e.deps.Store.SaveProgress(p)
		if p.DropID != e.cur.drop.ID {
			continue
		}
		if m, _ := p.Effective(); m > best {
			best = m
			src = p.Source
		}
	}
	if best > e.cur.lastMinutes {
		e.cur.lastMinutes = best
		e.cur.lastProgress = e.deps.Now()
		e.cur.pollErrors = 0 // a new no-credit window starts
		e.deps.Emit.Emit(EvProgress, ProgressPayload{
			DropID: e.cur.drop.ID, Current: best, Required: e.cur.drop.RequiredMinutes,
			Source: string(src), At: e.deps.Now(),
		})
		e.setStatus(func(s *Status) { s.Current = best })
		return best, true
	}
	return best, false
}

// hiddenWindowReason is the Status.Reason while a minimized/hidden watch tab
// is being restored.
const hiddenWindowReason = "window is minimized - restoring"

// checkHidden restores the window when two consecutive progress polls read
// the watching tab "hidden": Twitch stops minute-watched heartbeats for a
// hidden tab, so a user-minimized window silently stops crediting. Skipped
// when the user opted into minimize_while_watching and accepted that.
func (e *Engine) checkHidden(ctx context.Context, visibility string) {
	if visibility != "hidden" {
		e.cur.hiddenPolls = 0
		return
	}
	e.cur.hiddenPolls++
	if e.cur.hiddenPolls < 2 || e.deps.Cfg.Browser.Window.MinimizeWhileWatching {
		return
	}
	e.cur.hiddenPolls = 0
	e.deps.Log.Warn().Str("login", e.cur.login).Msg("watching: tab hidden for two polls (window minimized?); restoring it")
	e.setStatus(func(s *Status) { s.Reason = hiddenWindowReason })
	e.deps.Emit.Emit(EvState, StatePayload{From: e.state, To: e.state, At: e.deps.Now(), Reason: hiddenWindowReason})
	if w := e.deps.Window; w != nil {
		w.Restore(ctx) // brings this tab to the front of a shared window
	}
	// The coordinator only restores what it minimized itself; un-minimize a
	// window the user minimized by hand too.
	if err := e.bounded(ctx, func(c context.Context) error { return e.deps.Ctrl.Session().SetMinimized(c, false) }); err != nil {
		e.deps.Log.Warn().Err(err).Msg("watching: restore hidden window failed")
	}
}

// setWindowMinimized asks this engine's window coordinator for a visible,
// foregrounded tab (minimized=false) or gives that claim up (minimized=true).
// The engine needs the window while a channel is opened and validated (a
// minimized or backgrounded tab can suppress the player's autoplay) and gives it
// back once playback is confirmed.
//
// The coordinator owns the policy: it applies start_minimized, refcounts holds
// across the platforms sharing the window, and brings the holding tab to the
// front. Without one (tests, and any runtime built before the coordinator was
// wired) this falls back to driving the session's window state directly.
func (e *Engine) setWindowMinimized(ctx context.Context, minimized bool) {
	if w := e.deps.Window; w != nil {
		if minimized {
			w.Release(ctx)
		} else {
			w.Restore(ctx)
		}
		return
	}
	if !e.deps.Cfg.Browser.Window.StartMinimized {
		return
	}
	if err := e.bounded(ctx, func(c context.Context) error { return e.deps.Ctrl.Session().SetMinimized(c, minimized) }); err != nil {
		e.deps.Log.Warn().Err(err).Bool("minimized", minimized).Msg("set window state failed")
	}
}

// keepWindowOnBottom re-asserts always_on_bottom while watching, on the player
// reassert interval (fuzzed by watcher.timing_jitter like the volume reassert).
// Chrome re-raises its window on some internal events, so setting the z-order
// once is not enough. The coordinator decides whether the policy is on.
func (e *Engine) keepWindowOnBottom(ctx context.Context) {
	w := e.deps.Window
	if w == nil || e.cur == nil {
		return
	}
	now := e.deps.Now()
	if now.Before(e.cur.nextZOrderAt) {
		return
	}
	e.cur.nextZOrderAt = now.Add(e.fuzzed(e.deps.Cfg.Browser.Player.ReassertInterval))
	w.SendToBottom(ctx)
}

// endSession records the current watch session with an outcome.
func (e *Engine) endSession(outcome domain.SessionOutcome, note string) {
	if e.cur == nil {
		return
	}
	ws := e.cur.session
	ws.EndedAt = e.deps.Now()
	ws.EndMinutes = e.cur.lastMinutes
	ws.HardRefreshes = e.cur.hardRefreshes
	ws.StallEvents = e.cur.stallEvents
	ws.Outcome = outcome
	ws.Note = note
	if err := e.deps.Store.AppendSession(ws); err != nil {
		e.deps.Log.Warn().Err(err).Msg("record session failed")
	}
	e.deps.Emit.Emit(EvSession, SessionPayload{
		SessionID: ws.ID, DropID: ws.DropID, Outcome: string(outcome),
		StartedAt: ws.StartedAt, EndedAt: ws.EndedAt,
	})
	success := outcome == domain.OutcomeClaimed || outcome == domain.OutcomeCompleted
	_ = e.deps.Store.RecordChannelResult(e.deps.Platform, e.cur.login, success)
	// The session is in the store now; stop overlaying it live.
	e.setStatus(func(s *Status) { s.SessionStartedAt = time.Time{} })
}

// dumpDebug writes a raw capture to the profile's debug dir (debug only; the
// heartbeat buffer carries player state and drop ids, never auth material) and
// returns the file path ("" when the write failed).
func (e *Engine) dumpDebug(name string, body []byte) string {
	ddir := discovery.DebugDirOr(e.deps.DebugDir)
	if ddir == "" {
		return ""
	}
	if err := os.MkdirAll(ddir, 0o700); err != nil {
		return ""
	}
	path := filepath.Join(ddir, name)
	if err := os.WriteFile(path, body, 0o600); err != nil {
		e.deps.Log.Warn().Err(err).Msg("watcher: write debug dump failed")
		return ""
	}
	e.deps.Log.Info().Str("file", path).Int("bytes", len(body)).Msg("watcher: wrote debug dump")
	return path
}
