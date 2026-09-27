package watcher

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/rake-pro/dropwarden/internal/domain"
	"github.com/rake-pro/dropwarden/internal/queue"
)

// loadState reads the queue, campaigns, progress, and claimed set from the store
// into the maps the queue package expects.
func (e *Engine) loadState() (items []domain.QueueItem, campaigns map[string]domain.Campaign, progress map[string]domain.Progress, claimed map[string]bool, err error) {
	all, err := e.deps.Store.GetQueue()
	if err != nil {
		return
	}
	// The queue is shared across platforms; this engine works only its own.
	for _, it := range all {
		if it.Platform.OrTwitch() == e.deps.Platform.OrTwitch() {
			items = append(items, it)
		}
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
		eligible, status, reason := queue.EligibilityReason(it, campaigns, progress, claimed, e.deps.Cfg.Claim.SkipAccountLink, now)
		if eligible {
			tally["eligible"]++
			// Preserve the item THIS engine is watching; any other "active"
			// row is a leftover (a previous run that quit mid-watch) and is
			// selectable now (live 2026-09-26: two rows read "watching
			// xchocobars" while the engine was on another channel).
			if it.Status == domain.ItemActive && e.cur != nil && e.cur.drop.ID == it.DropID {
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

	ord := queue.Ordering{
		Mode:                  domain.PriorityMode(e.deps.Cfg.Queue.PriorityMode),
		Games:                 e.deps.Cfg.Games,
		PreferChannelSpecific: e.deps.Cfg.PreferChannelSpecific(),
	}
	cands := queue.EligibleCandidates(items, campaigns, progress, claimed, ord, e.deps.Cfg.Claim.SkipAccountLink, now)
	// Drop the drops this session already proved un-earnable (watched a
	// participating channel to the full stall window with zero credit), so we
	// fall through to the next eligible drop instead of grinding this one.
	if len(e.noCreditDrops) > 0 {
		kept := cands[:0]
		for _, c := range cands {
			if !e.noCreditDrops[c.Drop.ID] {
				kept = append(kept, c)
			}
		}
		cands = kept
	}

	if len(items) > 0 {
		ev := e.deps.Log.Info().Int("queued", len(items)).Interface("eligibility", tally)
		// Include one queued campaign's raw window fields to diagnose filtering.
		if c, ok := campaigns[items[0].CampaignID]; ok {
			ev = ev.Str("sample_campaign", c.Name).Str("sample_status", string(c.Status)).
				Time("sample_start", c.StartAt).Time("sample_end", c.EndAt).
				Bool("sample_account_link", c.RequiresAccountLink()).
				Interface("sample_account_connected", c.AccountConnected)
		}
		// The top candidate's ranking inputs, to debug why it leads.
		if len(cands) > 0 {
			top := cands[0]
			ev = ev.Str("top_drop", top.Drop.Name).Str("top_game", top.Campaign.GameName).
				Int("top_pin", top.Pin).Int("top_game_rank", top.GameRank).Bool("top_alongside", top.Alongside)
		}
		ev.Msg("selecting: eligibility tally")
	}

	if len(cands) == 0 {
		return StateCooldown, "nothing eligible to watch"
	}
	// One batched status query so every allowlist row shows who is live,
	// not only the campaigns the loop below reaches.
	e.liveSweep(ctx, "select")

	// Try candidates in priority order and take the first with an available
	// channel. When wait_for_top is set, only the single top drop is tried, so
	// the watcher waits for its channel instead of falling through. Otherwise
	// every candidate is considered: an allowlist campaign costs one status
	// query, so it is always checked, while directory scrapes for any-channel
	// campaigns are bounded by max_select_attempts. Counting allowlist checks
	// against that cap stranded selection (live 2026-09-25: 19 eligible Rust
	// drops, the top 6 were single-streamer allowlists with the streamer
	// offline, and the any-channel campaign further down was never reached).
	n := len(cands)
	if e.deps.Cfg.Watcher.WaitForTop {
		n = 1
	}
	maxDir := e.deps.Cfg.Watcher.MaxSelectAttempts
	dirReads := 0
	var cand *queue.Candidate
	var login string
	for i := 0; i < n; i++ {
		if cands[i].Campaign.AnyChannel() {
			if maxDir > 0 && dirReads >= maxDir {
				continue
			}
			dirReads++
		}
		if l, ok := e.pickChannel(ctx, cands[i].Campaign, cands); ok {
			cand = &cands[i]
			login = l
			if i > 0 {
				e.deps.Log.Info().Int("skipped", i).Str("game", cands[i].Campaign.GameName).
					Str("drop", cands[i].Drop.Name).Int("pin", cands[i].Pin).Int("game_rank", cands[i].GameRank).
					Bool("alongside", cands[i].Alongside).
					Msg("selecting: fell through to next available drop")
			}
			break
		}
	}
	if cand == nil {
		return StateCooldown, "no live channel for any eligible drop"
	}

	drop, _ := dropByID(cand.Campaign, cand.Drop.ID)
	e.cur = &activeWatch{
		drop:         drop,
		campaign:     cand.Campaign,
		login:        login,
		sessionStart: now,
		lastProgress: now,
		// Computed once here, not per watchTick: see the maxSessionAt doc comment.
		maxSessionAt: now.Add(e.fuzzed(e.deps.Cfg.Watcher.MaxSession)),
	}
	_ = e.deps.Store.SetItemStatus(drop.ID, domain.ItemActive, "watching "+login)
	e.deps.Emit.Emit(EvChannel, ChannelPayload{Login: login, Game: cand.Campaign.GameName})
	e.setStatus(func(s *Status) {
		s.DropID = drop.ID
		s.DropName = drop.Name
		s.ChannelLogin = login
		s.Game = cand.Campaign.GameName
		s.Required = drop.RequiredMinutes
	})
	return StateOpening, "selected " + login
}

// pickChannel builds the candidate channel set and returns the top-ranked live
// login, or ok=false when none is available. cands (the full eligible-drop
// list) feeds the coverage ranking: among channels valid for this campaign,
// prefer the one that credits the most other eligible drops too, so drop SETS
// (e.g. Rivals global + team-specific) drain on one channel before moving on.
func (e *Engine) pickChannel(ctx context.Context, c domain.Campaign, cands []queue.Candidate) (string, bool) {
	now := e.deps.Now()
	var live []domain.Channel
	if c.AnyChannel() {
		var logins []string
		err := e.bounded(ctx, func(cc context.Context) (err error) {
			logins, err = e.deps.Pages.DirectoryChannels(cc, c.GameSlug)
			return
		})
		if err != nil {
			e.deps.Log.Warn().Err(err).Str("game", c.GameName).Msg("select: directory read failed")
			return "", false
		}
		for _, l := range logins {
			live = append(live, domain.Channel{Login: l, Live: true, DropsEnabled: true, GameName: c.GameName, FirstSeenLiveAt: now.Add(-time.Hour)})
		}
		e.setLive(c.ID, channelLogins(live))
	} else {
		// ACL campaign: candidates are the allowlisted channels that are live
		// NOW. The allowlist alone ranks an offline channel above a live one
		// (live 2026-09-20: europeanhalo picked over LVTHalo, its offline page
		// autoplayed a VOD that passed validation, and the drop was then
		// misfiled as "already owned"). Without a status query (Kick, or a
		// failed call) fall back to assuming every allowlisted channel is live
		// and let validation sort it out.
		logins := make([]string, 0, len(c.AllowedChannels))
		for _, ch := range c.AllowedChannels {
			logins = append(logins, ch.Login)
		}
		var liveNow []domain.Channel
		err := e.bounded(ctx, func(cc context.Context) (err error) { liveNow, err = e.deps.Pages.LiveChannels(cc, logins); return })
		switch {
		case errors.Is(err, errors.ErrUnsupported):
			for _, ch := range c.AllowedChannels {
				ch.Live = true
				ch.DropsEnabled = true
				ch.FirstSeenLiveAt = now.Add(-time.Hour)
				live = append(live, ch)
			}
		case err != nil:
			e.deps.Log.Warn().Err(err).Str("game", c.GameName).Msg("select: live status read failed; assuming allowlist live")
			for _, ch := range c.AllowedChannels {
				ch.Live = true
				ch.DropsEnabled = true
				ch.FirstSeenLiveAt = now.Add(-time.Hour)
				live = append(live, ch)
			}
		default:
			// Twitch only credits an allowlisted channel while it streams the
			// campaign's game (live 2026-09-20: eli_x was live and allowlisted
			// but earned nothing). Off-game streams are dropped here so the
			// no-credit ladder never has to discover it the slow way.
			var offGame []domain.Channel
			for _, ch := range liveNow {
				if !sameGame(c, ch) {
					offGame = append(offGame, ch)
					continue
				}
				ch.DropsEnabled = true
				ch.FirstSeenLiveAt = now.Add(-time.Hour)
				live = append(live, ch)
			}
			e.deps.Log.Info().Str("campaign", c.Name).Int("allowed", len(logins)).Int("live", len(live)).
				Strs("live_logins", channelLogins(live)).Strs("off_game", channelLogins(offGame)).
				Msg("select: ACL live status")
			e.setLive(c.ID, channelLogins(live))
		}
	}

	stats := func(login string) (int, int) {
		st, _ := e.deps.Store.ChannelStats(e.deps.Platform, login)
		return st.Successes, st.Failures
	}
	blacklisted := func(login string) bool {
		bl, _ := e.deps.Store.IsBlacklisted(e.deps.Platform, login, now)
		return bl
	}
	current := ""
	if e.cur != nil {
		current = e.cur.login
	}
	coverage := func(login string) int { return queue.ChannelCoverage(login, c, cands) }
	ranked := queue.RankChannels(c, live, current, coverage, stats, blacklisted, e.deps.Cfg.Watcher.StreamUpDelay, now)
	if len(ranked) == 0 {
		return "", false
	}
	if n := coverage(ranked[0].Login); n > 1 {
		e.deps.Log.Info().Str("login", ranked[0].Login).Int("covers_drops", n).
			Msg("selecting: channel covers multiple eligible drops")
	}
	return ranked[0].Login, true
}

// sameGame reports whether a live channel streams the campaign's game. IDs
// win when both sides have one; otherwise the display names are compared. A
// campaign or stream with no game information is not filtered.
func sameGame(c domain.Campaign, ch domain.Channel) bool {
	if c.GameID != "" && ch.GameID != "" {
		return c.GameID == ch.GameID
	}
	if c.GameName == "" || ch.GameName == "" {
		return true
	}
	return strings.EqualFold(c.GameName, ch.GameName)
}

func channelLogins(chs []domain.Channel) []string {
	out := make([]string, len(chs))
	for i, ch := range chs {
		out[i] = ch.Login
	}
	return out
}
