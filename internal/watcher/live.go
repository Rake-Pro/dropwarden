package watcher

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/rake-pro/dropwarden/internal/domain"
)

// CampaignLive is what the engine last learned about who is streaming for a
// campaign: the allowlisted logins live on the campaign's game (ACL), or the
// drops-enabled directory logins seen at selection (any-channel). The Queue
// view shows it as a live dot per drop.
type CampaignLive struct {
	Logins    []string  `json:"logins"`
	CheckedAt time.Time `json:"checkedAt"`
}

// Liveness cadence between watch-time refreshes: one batched status query
// per interval, no navigation, so it is safe while a stream plays.
const (
	liveSweepInterval = 5 * time.Minute
	liveSweepJitter   = 2 * time.Minute
)

// Liveness returns a copy of the per-campaign live snapshot.
func (e *Engine) Liveness() map[string]CampaignLive {
	e.liveMu.Lock()
	defer e.liveMu.Unlock()
	out := make(map[string]CampaignLive, len(e.live))
	for id, l := range e.live {
		// Non-nil even when empty: the UI reads null as "never checked".
		logins := make([]string, len(l.Logins))
		copy(logins, l.Logins)
		out[id] = CampaignLive{Logins: logins, CheckedAt: l.CheckedAt}
	}
	return out
}

func (e *Engine) setLive(campaignID string, logins []string) {
	if logins == nil {
		logins = []string{}
	}
	e.liveMu.Lock()
	if e.live == nil {
		e.live = map[string]CampaignLive{}
	}
	e.live[campaignID] = CampaignLive{Logins: logins, CheckedAt: e.deps.Now()}
	e.liveMu.Unlock()
}

// liveSweep refreshes the live snapshot of every enabled, unexpired ACL
// campaign of this platform in ONE batched status query (chunked by the
// platform driver). Any-channel campaigns need a directory page, so they are
// only refreshed by selection. Platforms without a status query are skipped.
func (e *Engine) liveSweep(ctx context.Context, trigger string) {
	now := e.deps.Now()
	e.nextLiveAt = now.Add(e.jittered(liveSweepInterval, liveSweepJitter))
	items, err := e.deps.Store.GetQueue()
	if err != nil {
		return
	}
	campaigns, err := e.deps.Store.ListCampaigns()
	if err != nil {
		return
	}
	byID := make(map[string]domain.Campaign, len(campaigns))
	for _, c := range campaigns {
		byID[c.ID] = c
	}
	var acl []domain.Campaign
	seen := map[string]bool{}
	var logins []string
	for _, it := range items {
		c, ok := byID[it.CampaignID]
		if !ok || !it.Enabled || seen[c.ID] || c.AnyChannel() || c.Platform.OrTwitch() != e.deps.Platform.OrTwitch() {
			continue
		}
		if !c.EndAt.IsZero() && !c.EndAt.After(now) {
			continue
		}
		seen[c.ID] = true
		acl = append(acl, c)
		for _, ch := range c.AllowedChannels {
			l := strings.ToLower(ch.Login)
			if !seen["@"+l] {
				seen["@"+l] = true
				logins = append(logins, l)
			}
		}
	}
	if len(logins) == 0 {
		return
	}
	var liveNow []domain.Channel
	err = e.bounded(ctx, func(cc context.Context) (err error) { liveNow, err = e.deps.Pages.LiveChannels(cc, logins); return })
	if errors.Is(err, errors.ErrUnsupported) {
		return
	}
	if err != nil {
		e.deps.Log.Warn().Err(err).Str("trigger", trigger).Msg("live sweep: status read failed")
		return
	}
	byLogin := make(map[string]domain.Channel, len(liveNow))
	for _, ch := range liveNow {
		byLogin[strings.ToLower(ch.Login)] = ch
	}
	liveCampaigns := 0
	for _, c := range acl {
		var on []string
		for _, ch := range c.AllowedChannels {
			if lv, ok := byLogin[strings.ToLower(ch.Login)]; ok && sameGame(c, lv) {
				on = append(on, lv.Login)
			}
		}
		if len(on) > 0 {
			liveCampaigns++
		}
		e.setLive(c.ID, on)
	}
	e.deps.Log.Debug().Str("trigger", trigger).Int("campaigns", len(acl)).Int("logins", len(logins)).
		Int("live_campaigns", liveCampaigns).Msg("live sweep")
}
