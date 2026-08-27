package watcher

import (
	"context"
	"time"

	"github.com/rake-pro/dropwarden/internal/domain"
	"github.com/rake-pro/dropwarden/internal/queue"
)

// loadState reads the queue, campaigns, progress, and claimed set from the store
// into the maps the queue package expects.
func (e *Engine) loadState() (items []domain.QueueItem, campaigns map[string]domain.Campaign, progress map[string]domain.Progress, claimed map[string]bool, err error) {
	items, err = e.deps.Store.GetQueue()
	if err != nil {
		return
	}
	cs, err := e.deps.Store.ListCampaigns()
	if err != nil {
		return
	}
	campaigns = make(map[string]domain.Campaign, len(cs))
	for _, c := range cs {
		campaigns[c.ID] = c
	}
	progress = make(map[string]domain.Progress)
	claimed = make(map[string]bool)
	for _, it := range items {
		if p, e2 := e.deps.Store.GetProgress(it.DropID); e2 == nil && p != nil {
			progress[it.DropID] = *p
		}
		if has, e2 := e.deps.Store.HasClaim(it.DropID); e2 == nil && has {
			claimed[it.DropID] = true
		}
	}
	return
}

// dropByID finds a drop within a campaign.
func dropByID(c domain.Campaign, dropID string) (domain.Drop, bool) {
	for _, d := range c.Drops {
		if d.ID == dropID {
			return d, true
		}
	}
	return domain.Drop{}, false
}

func (e *Engine) runSelecting(ctx context.Context) (State, string) {
	e.skipReq = false
	e.drainCommands()
	if e.shutdown {
		return StateIdle, "shutdown"
	}
	if e.paused {
		return StateIdle, "paused"
	}

	items, campaigns, progress, claimed, err := e.loadState()
	if err != nil {
		return e.enterBackoff("select: load state: " + err.Error())
	}

	// Reconcile queue item statuses so the UI reflects why items are not running,
	// and tally the reasons so a "nothing eligible" outcome is diagnosable.
	now := e.deps.Now()
	tally := map[string]int{}
	for i := range items {
		it := items[i]
		eligible, status, reason := queue.EligibilityReason(it, campaigns, progress, claimed, now)
		if eligible {
			tally["eligible"]++
			// Preserve an actively-watched item; otherwise it is selectable now.
			if it.Status == domain.ItemActive {
				status = domain.ItemActive
			} else {
				status = domain.ItemPending
			}
		} else {
			tally[string(status)]++
		}
		// Always sync the stored/in-memory status so a stale label (e.g. a prior
		// run's "unavailable") self-heals and the UI reflects reality.
		if it.Status != status {
			_ = e.deps.Store.SetItemStatus(it.DropID, status, reason)
			items[i].Status = status
			items[i].Reason = reason
			if status == domain.ItemNeedsAction {
				c := campaigns[it.CampaignID]
				e.deps.Emit.Emit(EvNeedsAction, NeedsActionPayload{DropID: it.DropID, Reason: reason, URL: c.AccountLinkURL})
			}
		}
	}
	e.setEligibility(tally)
	if len(items) > 0 {
		ev := e.deps.Log.Info().Int("queued", len(items)).Interface("eligibility", tally)
		// Include one queued campaign's raw window fields to diagnose filtering.
		if c, ok := campaigns[items[0].CampaignID]; ok {
			ev = ev.Str("sample_campaign", c.Name).Str("sample_status", string(c.Status)).
				Time("sample_start", c.StartAt).Time("sample_end", c.EndAt).
				Bool("sample_account_link", c.RequiresAccountLink())
		}
		ev.Msg("selecting: eligibility tally")
	}

	cand, err := queue.NextDrop(items, campaigns, progress, claimed, domain.PriorityMode(e.deps.Cfg.Queue.PriorityMode), now)
	if err != nil {
		return e.enterBackoff("select: next drop: " + err.Error())
	}
	if cand == nil {
		return StateCooldown, "nothing eligible to watch"
	}

	login, ok := e.pickChannel(ctx, cand.Campaign)
	if !ok {
		// No live channel right now: brief cooldown then re-discover.
		return StateCooldown, "no live channel for " + cand.Campaign.GameName
	}

	drop, _ := dropByID(cand.Campaign, cand.Drop.ID)
	e.cur = &activeWatch{
		drop:         drop,
		campaign:     cand.Campaign,
		login:        login,
		sessionStart: now,
		lastProgress: now,
	}
	_ = e.deps.Store.SetItemStatus(drop.ID, domain.ItemActive, "watching "+login)
	e.deps.Emit.Emit(EvChannel, ChannelPayload{Login: login, Game: cand.Campaign.GameName})
	e.setStatus(func(s *Status) {
		s.DropID = drop.ID
		s.DropName = drop.Name
		s.ChannelLogin = login
		s.Required = drop.RequiredMinutes
	})
	return StateOpening, "selected " + login
}

// pickChannel builds the candidate channel set and returns the top-ranked live
// login, or ok=false when none is available.
func (e *Engine) pickChannel(ctx context.Context, c domain.Campaign) (string, bool) {
	now := e.deps.Now()
	var live []domain.Channel
	if c.AnyChannel() {
		logins, err := e.deps.Pages.DirectoryChannels(ctx, c.GameSlug)
		if err != nil {
			e.deps.Log.Warn().Err(err).Str("game", c.GameName).Msg("select: directory read failed")
			return "", false
		}
		for _, l := range logins {
			live = append(live, domain.Channel{Login: l, Live: true, DropsEnabled: true, GameName: c.GameName, FirstSeenLiveAt: now.Add(-time.Hour)})
		}
	} else {
		// ACL campaign: candidates are the allowlisted channels. Liveness is
		// confirmed during ValidatingPlayback, which reselects on failure.
		for _, ch := range c.AllowedChannels {
			ch.Live = true
			ch.DropsEnabled = true
			ch.FirstSeenLiveAt = now.Add(-time.Hour)
			live = append(live, ch)
		}
	}

	stats := func(login string) (int, int) {
		st, _ := e.deps.Store.ChannelStats(login)
		return st.Successes, st.Failures
	}
	blacklisted := func(login string) bool {
		bl, _ := e.deps.Store.IsBlacklisted(login, now)
		return bl
	}
	current := ""
	if e.cur != nil {
		current = e.cur.login
	}
	ranked := queue.RankChannels(c, live, current, stats, blacklisted, e.deps.Cfg.Watcher.StreamUpDelay, now)
	if len(ranked) == 0 {
		return "", false
	}
	return ranked[0].Login, true
}
