package queue

import (
	"sort"
	"time"

	"github.com/rake-pro/dropwarden/internal/domain"
)

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
// one. All external state (success/failure stats, blacklist membership) is
// supplied via closures so the function stays pure.
func RankChannels(campaign domain.Campaign, live []domain.Channel, current string, stats func(login string) (successes, failures int), blacklisted func(login string) bool, streamUpDelay time.Duration, now time.Time) []domain.Channel {
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
		candidates = append(candidates, ranked{
			ch:        ch,
			isCurrent: isCurrent,
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
