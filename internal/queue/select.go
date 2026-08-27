package queue

import (
	"sort"
	"time"

	"github.com/rake-pro/dropwarden/internal/domain"
)

// Candidate is a drop selected as the next one to watch, paired with its
// owning campaign.
type Candidate struct {
	Drop     domain.Drop
	Campaign domain.Campaign
}

// findDrop looks up a drop by id within a campaign.
func findDrop(c domain.Campaign, dropID string) (domain.Drop, bool) {
	for _, d := range c.Drops {
		if d.ID == dropID {
			return d, true
		}
	}
	return domain.Drop{}, false
}

// isExpired reports whether a campaign's watch window has closed. An explicit
// EXPIRED status or an end time in the past counts as expired.
func isExpired(c domain.Campaign, now time.Time) bool {
	if c.Status == domain.StatusExpired {
		return true
	}
	return !c.EndAt.IsZero() && !c.EndAt.After(now)
}

// hasStarted reports whether a campaign's watch window has opened. An explicit
// UPCOMING status or a start time in the future means it has not.
func hasStarted(c domain.Campaign, now time.Time) bool {
	if c.Status == domain.StatusUpcoming {
		return false
	}
	return c.StartAt.IsZero() || !c.StartAt.After(now)
}

// isWatchable reports whether a campaign is currently within its watch window.
// It derives this from the time window rather than trusting the status string,
// because Twitch's "open drop campaigns" list does not always tag entries with
// an explicit ACTIVE status.
func isWatchable(c domain.Campaign, now time.Time) bool {
	return !isExpired(c, now) && hasStarted(c, now)
}

// preconditionsMet reports whether every id in ids is present (true) in claimed.
func preconditionsMet(ids []string, claimed map[string]bool) bool {
	for _, id := range ids {
		if !claimed[id] {
			return false
		}
	}
	return true
}

// remainingMinutes returns the minutes left to complete a drop, using progress
// when available and falling back to the drop's full requirement otherwise.
func remainingMinutes(d domain.Drop, progress map[string]domain.Progress) int {
	if p, ok := progress[d.ID]; ok {
		effective, _ := p.Effective()
		remaining := d.RequiredMinutes - effective
		if remaining < 0 {
			remaining = 0
		}
		return remaining
	}
	return d.RequiredMinutes
}

// eligibleNow reports whether item currently qualifies for selection: enabled,
// pending or active, campaign live and account-link-free, drop present, not
// complete/claimed, and preconditions satisfied.
func eligibleNow(item domain.QueueItem, campaigns map[string]domain.Campaign, progress map[string]domain.Progress, claimed map[string]bool, now time.Time) (domain.Campaign, domain.Drop, bool) {
	if !item.Enabled {
		return domain.Campaign{}, domain.Drop{}, false
	}
	// Eligibility is fully determined by the campaign/drop/progress/precondition
	// checks below; we deliberately do NOT gate on item.Status here. Status is a
	// derived UI label that can lag behind (e.g. a stale "unavailable" from a
	// prior run), and gating on it would wrongly skip a now-eligible drop.
	campaign, ok := campaigns[item.CampaignID]
	if !ok || !isWatchable(campaign, now) {
		return domain.Campaign{}, domain.Drop{}, false
	}
	if campaign.RequiresAccountLink() {
		return domain.Campaign{}, domain.Drop{}, false
	}
	drop, ok := findDrop(campaign, item.DropID)
	if !ok {
		return domain.Campaign{}, domain.Drop{}, false
	}
	if claimed[item.DropID] {
		return domain.Campaign{}, domain.Drop{}, false
	}
	if p, ok := progress[item.DropID]; ok && p.Complete() {
		return domain.Campaign{}, domain.Drop{}, false
	}
	if !preconditionsMet(drop.PreconditionIDs, claimed) {
		return domain.Campaign{}, domain.Drop{}, false
	}
	return campaign, drop, true
}

// EligibleCandidates returns every currently-eligible drop, ordered according
// to mode (highest priority first). The watcher uses this to fall through to the
// next available drop when the top one has no live channel.
func EligibleCandidates(items []domain.QueueItem, campaigns map[string]domain.Campaign, progress map[string]domain.Progress, claimed map[string]bool, mode domain.PriorityMode, now time.Time) []Candidate {
	type entry struct {
		item     domain.QueueItem
		campaign domain.Campaign
		drop     domain.Drop
		origIdx  int
	}

	var survivors []entry
	for i, item := range items {
		campaign, drop, ok := eligibleNow(item, campaigns, progress, claimed, now)
		if !ok {
			continue
		}
		survivors = append(survivors, entry{item: item, campaign: campaign, drop: drop, origIdx: i})
	}
	if len(survivors) == 0 {
		return nil
	}

	less := func(i, j int) bool {
		a, b := survivors[i], survivors[j]
		switch mode {
		case domain.PriorityEndingSoonest:
			aZero, bZero := a.campaign.EndAt.IsZero(), b.campaign.EndAt.IsZero()
			if aZero != bZero {
				return bZero // zero EndAt sorts last, so a before b when b is zero
			}
			if !aZero && !bZero && !a.campaign.EndAt.Equal(b.campaign.EndAt) {
				return a.campaign.EndAt.Before(b.campaign.EndAt)
			}
		case domain.PriorityClosestToComplete:
			ra := remainingMinutes(a.drop, progress)
			rb := remainingMinutes(b.drop, progress)
			if ra != rb {
				return ra < rb
			}
		case domain.PriorityShortestFirst:
			if a.drop.RequiredMinutes != b.drop.RequiredMinutes {
				return a.drop.RequiredMinutes < b.drop.RequiredMinutes
			}
		default: // domain.PriorityTopDown and anything unrecognized
			if a.item.Position != b.item.Position {
				return a.item.Position < b.item.Position
			}
		}
		return a.origIdx < b.origIdx
	}
	sort.SliceStable(survivors, less)

	out := make([]Candidate, len(survivors))
	for i, s := range survivors {
		out[i] = Candidate{Drop: s.drop, Campaign: s.campaign}
	}
	return out
}

// NextDrop returns the single highest-priority eligible drop, or (nil, nil) when
// nothing is currently eligible.
func NextDrop(items []domain.QueueItem, campaigns map[string]domain.Campaign, progress map[string]domain.Progress, claimed map[string]bool, mode domain.PriorityMode, now time.Time) (*Candidate, error) {
	cands := EligibleCandidates(items, campaigns, progress, claimed, mode, now)
	if len(cands) == 0 {
		return nil, nil
	}
	return &cands[0], nil
}

// EligibilityReason classifies why item is or is not currently selectable,
// returning the status the engine should record on the queue item along with
// a human-readable reason. eligible=true, status=domain.ItemPending means the
// item is selectable right now.
func EligibilityReason(item domain.QueueItem, campaigns map[string]domain.Campaign, progress map[string]domain.Progress, claimed map[string]bool, now time.Time) (eligible bool, status domain.ItemStatus, reason string) {
	if !item.Enabled {
		return false, domain.ItemDisabled, "user disabled this drop"
	}

	campaign, ok := campaigns[item.CampaignID]
	if !ok {
		return false, domain.ItemUnavailable, "campaign not found"
	}
	if isExpired(campaign, now) {
		return false, domain.ItemExpired, "campaign window has passed"
	}
	if !hasStarted(campaign, now) {
		return false, domain.ItemUnavailable, "campaign has not started yet"
	}
	if campaign.RequiresAccountLink() {
		return false, domain.ItemNeedsAction, "account link required"
	}

	drop, ok := findDrop(campaign, item.DropID)
	if !ok {
		return false, domain.ItemUnavailable, "drop not found in campaign"
	}

	if claimed[item.DropID] {
		return false, domain.ItemClaimed, "already claimed"
	}
	if p, ok := progress[item.DropID]; ok && p.Complete() {
		return false, domain.ItemClaimed, "already claimed"
	}

	if !preconditionsMet(drop.PreconditionIDs, claimed) {
		return false, domain.ItemBlocked, "waiting on prerequisite drop"
	}

	return true, domain.ItemPending, ""
}
