package watcher

import (
	"context"
	"fmt"
	"time"

	"github.com/rake-pro/dropwarden/internal/domain"
	"github.com/rake-pro/dropwarden/internal/twitch"
)

// probeTimeout bounds a health probe. A timeout here is the hang signal: the
// renderer is not answering, distinct from a crash event.
const probeTimeout = 10 * time.Second

// probe runs a health probe under a hard timeout so a hung renderer surfaces as
// an error rather than blocking the engine loop.
func (e *Engine) probe(ctx context.Context) (twitch.Health, error) {
	pctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	return e.deps.Ctrl.Probe(pctx)
}

// newSessionID builds a time-sortable session id: zero-padded unix millis plus a
// short random suffix, so history reads back in order without a UUID dependency.
func newSessionID(now time.Time, rnd func() float64) string {
	return fmt.Sprintf("%013d-%04d", now.UnixMilli(), int(rnd()*10000))
}

// updateProgressFromCapture drains the page's own captured GQL traffic and, for
// the active drop, updates the store and the in-memory baseline. It returns the
// current best-known minutes and whether they increased since last time.
func (e *Engine) updateProgressFromCapture(ctx context.Context) (minutes int, increased bool) {
	if e.cur == nil {
		return 0, false
	}
	res, err := twitch.Drain(ctx, e.deps.Ctrl.Session())
	if err != nil {
		return e.cur.lastMinutes, false
	}
	return e.applyProgress(res.Progress)
}

// pollProgress actively fetches server-side progress at most once per
// watcher.progress_poll: the viewer's current drop session plus the full
// inventory. It exists because the channel page does not fire progress GQL over
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
	if now.Sub(e.cur.lastPoll) < interval {
		return e.cur.lastMinutes, false
	}
	e.cur.lastPoll = now
	var ps []domain.Progress
	if p, err := e.deps.Pages.FetchCurrentDrop(ctx); err != nil {
		e.deps.Log.Warn().Err(err).Msg("current-drop poll failed")
	} else if p != nil {
		if p.ChannelLogin != "" {
			e.cur.creditChannel = p.ChannelLogin
		}
		ps = append(ps, *p)
	}
	if inv, err := e.deps.Pages.FetchInventoryProgress(ctx); err != nil {
		e.deps.Log.Warn().Err(err).Msg("inventory poll failed")
	} else {
		ps = append(ps, inv...)
	}
	if len(ps) == 0 {
		return e.cur.lastMinutes, false
	}
	return e.applyProgress(ps)
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
		e.deps.Emit.Emit(EvProgress, ProgressPayload{
			DropID: e.cur.drop.ID, Current: best, Required: e.cur.drop.RequiredMinutes,
			Source: string(src), At: e.deps.Now(),
		})
		e.setStatus(func(s *Status) { s.Current = best })
		return best, true
	}
	return best, false
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
	_ = e.deps.Store.RecordChannelResult(e.cur.login, success)
}
