package queue

import (
	"sort"
	"strings"
	"time"

	"github.com/rake-pro/dropwarden/internal/domain"
)

// ChannelCoverage reports how many of the eligible drops a channel can credit,
// used to prefer channels that serve whole SETS of drops (e.g. Twitch Rivals:
// "global" drops creditable on any participating channel plus team-specific
// drops locked to a channel subset - a team channel covers both). A drop
// counts when the login is in its campaign's ACL, or when its campaign is
// any-channel for the same game as the anchor campaign (the channel candidates
// under consideration stream the anchor's game, so a same-game any-channel
// campaign credits them; the drops-indicator validation remains the hard gate).
func ChannelCoverage(login string, anchor domain.Campaign, cands []Candidate) int {
	n := 0
	for _, c := range cands {
		camp := c.Campaign
		if camp.AnyChannel() {
			if camp.GameID != "" && camp.GameID == anchor.GameID {
				n++
			}
			continue
		}
		for _, ch := range camp.AllowedChannels {
			if strings.EqualFold(ch.Login, login) {
				n++
				break
			}
		}
	}
	return n
}

// ChannelScore pairs a channel with its computed ranking score, for callers
// that want to inspect why RankChannels ordered things the way it did.
type ChannelScore struct {
	Channel domain.Channel
	Score   int
}

// RankChannels orders candidate channels for a campaign from most to least
// preferred. The candidate set is `live` itself when the campaign has no ACL
// (AnyChannel), or the intersection of the campaign's AllowedChannels with
// `live` otherwise. Blacklisted logins are excluded, as are channels still
// inside the stream-up debounce window, unless that channel is the current
// one. Ordering: the current channel first (stay on a channel until its drop
// set is exhausted), then eligible-drop coverage (see ChannelCoverage), then
// success/failure stats, then viewers. All external state is supplied via
// closures so the function stays pure; coverage may be nil to skip that tier.
func RankChannels(campaign domain.Campaign, live []domain.Channel, current string, coverage func(login string) int, stats func(login string) (successes, failures int), blacklisted func(login string) bool, streamUpDelay time.Duration, now time.Time) []domain.Channel {
	liveByLogin := make(map[string]domain.Channel, len(live))
	for _, ch := range live {
		liveByLogin[ch.Login] = ch
	}

	var pool []domain.Channel
	if campaign.AnyChannel() {
		pool = live
	} else {
		for _, allowed := range campaign.AllowedChannels {
			if ch, ok := liveByLogin[allowed.Login]; ok {
				pool = append(pool, ch)
			}
		}
	}

	type ranked struct {
		ch        domain.Channel
		isCurrent bool
		covered   int
		netScore  int
		viewers   int
		origIdx   int
	}

	var candidates []ranked
	for i, ch := range pool {
		isCurrent := ch.Login == current
		if blacklisted != nil && blacklisted(ch.Login) {
			continue
		}
		if !isCurrent && streamUpDelay > 0 && !ch.FirstSeenLiveAt.IsZero() && now.Sub(ch.FirstSeenLiveAt) < streamUpDelay {
			continue
		}
		successes, failures := 0, 0
		if stats != nil {
			successes, failures = stats(ch.Login)
		}
		covered := 0
		if coverage != nil {
			covered = coverage(ch.Login)
		}
		candidates = append(candidates, ranked{
			ch:        ch,
			isCurrent: isCurrent,
			covered:   covered,
			netScore:  successes - failures,
			viewers:   ch.ViewerCount,
			origIdx:   i,
		})
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]
		if a.isCurrent != b.isCurrent {
			return a.isCurrent
		}
		if a.covered != b.covered {
			return a.covered > b.covered
		}
		if a.netScore != b.netScore {
			return a.netScore > b.netScore
		}
		if a.viewers != b.viewers {
			return a.viewers > b.viewers
		}
		return a.origIdx < b.origIdx
	})

	result := make([]domain.Channel, len(candidates))
	for i, c := range candidates {
		result[i] = c.ch
	}
	return result
}
