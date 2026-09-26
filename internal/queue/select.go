package queue

import (
	"sort"
	"strings"
	"time"

	"github.com/rake-pro/dropwarden/internal/domain"
)

// Candidate is a drop selected as the next one to watch, paired with its
// owning campaign.
type Candidate struct {
	Drop     domain.Drop
	Campaign domain.Campaign

	// Ranking inputs, carried for logging: the item's pin rank (0 = not
	// pinned), the game's rank in the games list, and whether the drop credits
	// alongside a channel-specific drop of the same game (see Alongside).
	Pin       int
	GameRank  int
	Alongside bool
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

// linkBlocked reports whether the account-link gate should exclude this item.
// Twitch's flags cannot distinguish "link required to EARN" from "link page
// exists but the drop itself earns fine" (Special Events watch-to-redeem
// campaigns report isAccountConnected=false while crediting normally), so
// server-confirmed progress overrides the gate: if Twitch is already crediting
// minutes for the drop, it is watchable regardless of link status. skipLink
// false (the skip_account_link_drops setting) disables the gate entirely.
func linkBlocked(skipLink bool, campaign domain.Campaign, dropID string, progress map[string]domain.Progress) bool {
	if !skipLink || !campaign.RequiresAccountLink() {
		return false
	}
	if p, ok := progress[dropID]; ok && (p.RealMinutes > 0 || p.Claimable) {
		return false // the server is demonstrably crediting this drop
	}
	return true
}

// eligibleNow reports whether item currently qualifies for selection: enabled,
// pending or active, campaign live and account-link-free, drop present, not
// complete/claimed, and preconditions satisfied.
func eligibleNow(item domain.QueueItem, campaigns map[string]domain.Campaign, progress map[string]domain.Progress, claimed map[string]bool, skipLink bool, now time.Time) (domain.Campaign, domain.Drop, bool) {
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
	if linkBlocked(skipLink, campaign, item.DropID, progress) {
		return domain.Campaign{}, domain.Drop{}, false
	}
	drop, ok := findDrop(campaign, item.DropID)
	if !ok {
		return domain.Campaign{}, domain.Drop{}, false
	}
	if claimed[item.DropID] {
		return domain.Campaign{}, domain.Drop{}, false
	}
	// A zero-minute drop is event-based (subs, gifts, chat), not earned by
	// watching: selecting it would watch a channel for nothing.
	if drop.RequiredMinutes <= 0 {
		return domain.Campaign{}, domain.Drop{}, false
	}
	// Exclude an already-claimed drop even when Twitch reports its minutes as 0
	// (a claimed drop from a fully-earned campaign can come back with
	// currentMinutesWatched 0, so Complete() alone would let it back in). Owned
	// excludes a drop whose reward the account already holds (matched at
	// discovery) - Twitch never re-credits it, so watching would grind forever -
	// unless the user set ForceWatch to override the owned skip for this drop.
	if p, ok := progress[item.DropID]; ok && (p.Complete() || p.ClaimedAt != nil || (p.Owned && !item.ForceWatch)) {
		return domain.Campaign{}, domain.Drop{}, false
	}
	if !preconditionsMet(drop.PreconditionIDs, claimed) {
		return domain.Campaign{}, domain.Drop{}, false
	}
	return campaign, drop, true
}

// Ordering configures how eligible drops are ranked. The sort keys, in order:
//
//  0. Pin rank ascending: pinned (focus) drops first, rank 1 before rank 2.
//  1. Game rank: the index of the campaign's game in Games (case-insensitive,
//     trimmed); games not in the list sort after every listed game.
//  2. When PreferChannelSpecific is set, drops that credit alongside a
//     channel-specific drop of the same game and platform (see Alongside)
//     sort after the rest, so within a game the ACL campaigns' drops come
//     before the any-channel ones: watching an allowlisted streamer credits
//     the any-channel drops too, so those should not take a slot of their own
//     while ACL drops of that game are still eligible. Selection falls through
//     past drops with no live channel, so the any-channel drops are still
//     watched when no allowlisted streamer of the game is live.
//  3. Mode (the PriorityMode) as the final tiebreaker, then input order.
type Ordering struct {
	Mode                  domain.PriorityMode
	Games                 []string
	PreferChannelSpecific bool
}

// GameRank returns the priority rank of a game name within games: its index
// (case-insensitive, trimmed), or len(games) when it is not listed.
func GameRank(games []string, name string) int {
	n := strings.ToLower(strings.TrimSpace(name))
	for i, g := range games {
		if strings.ToLower(strings.TrimSpace(g)) == n {
			return i
		}
	}
	return len(games)
}

// groupKey identifies "the same game on the same platform" for the alongside
// rule: the normalized game name, falling back to the game id.
type groupKey struct {
	platform domain.Platform
	game     string
}

func gameGroup(pf domain.Platform, c domain.Campaign) groupKey {
	g := strings.ToLower(strings.TrimSpace(c.GameName))
	if g == "" {
		g = "id:" + c.GameID
	}
	return groupKey{platform: pf.OrTwitch(), game: g}
}

// Alongside reports, per drop id, which currently-eligible queued drops are any-channel AND share
// their game and platform with at least one currently-eligible channel-specific
// (ACL) drop in items. Such a drop is credited by watching that ACL drop's
// streamer, so Ordering (with PreferChannelSpecific) ranks it after the ACL
// drops and the UI marks it "credits alongside channel drops".
func Alongside(items []domain.QueueItem, campaigns map[string]domain.Campaign, progress map[string]domain.Progress, claimed map[string]bool, skipLink bool, now time.Time) map[string]bool {
	acl := map[groupKey]bool{}
	for _, it := range items {
		if c, _, ok := eligibleNow(it, campaigns, progress, claimed, skipLink, now); ok && !c.AnyChannel() {
			acl[gameGroup(it.Platform, c)] = true
		}
	}
	out := map[string]bool{}
	if len(acl) == 0 {
		return out
	}
	for _, it := range items {
		// Only eligible any-channel drops: a completed/owned one waits on
		// nothing, so the UI must not badge it.
		if c, _, ok := eligibleNow(it, campaigns, progress, claimed, skipLink, now); ok && c.AnyChannel() && acl[gameGroup(it.Platform, c)] {
			out[it.DropID] = true
		}
	}
	return out
}

// pinLess orders pinned before unpinned, then by ascending rank. ok=false
// means the two tie on this key.
func pinLess(a, b int) (less, ok bool) {
	switch {
	case a == b:
		return false, false
	case a == 0:
		return false, true
	case b == 0:
		return true, true
	}
	return a < b, true
}

// modeLess applies the PriorityMode key. ok=false means a tie on this key.
func modeLess(mode domain.PriorityMode, a, b Candidate, pa, pb int, progress map[string]domain.Progress) (less, ok bool) {
	switch mode {
	case domain.PriorityEndingSoonest:
		aZero, bZero := a.Campaign.EndAt.IsZero(), b.Campaign.EndAt.IsZero()
		if aZero != bZero {
			return bZero, true // zero EndAt sorts last, so a before b when b is zero
		}
		if !aZero && !a.Campaign.EndAt.Equal(b.Campaign.EndAt) {
			return a.Campaign.EndAt.Before(b.Campaign.EndAt), true
		}
	case domain.PriorityClosestToComplete:
		ra, rb := remainingMinutes(a.Drop, progress), remainingMinutes(b.Drop, progress)
		if ra != rb {
			return ra < rb, true
		}
	case domain.PriorityShortestFirst:
		if a.Drop.RequiredMinutes != b.Drop.RequiredMinutes {
			return a.Drop.RequiredMinutes < b.Drop.RequiredMinutes, true
		}
	default: // domain.PriorityTopDown and anything unrecognized
		if pa != pb {
			return pa < pb, true
		}
	}
	return false, false
}

// EligibleCandidates returns every currently-eligible drop, ordered per ord
// (highest priority first; see Ordering for the keys). The watcher uses this
// to fall through to the next available drop when the top one has no live
// channel.
func EligibleCandidates(items []domain.QueueItem, campaigns map[string]domain.Campaign, progress map[string]domain.Progress, claimed map[string]bool, ord Ordering, skipLink bool, now time.Time) []Candidate {
	type entry struct {
		cand     Candidate
		position int
		origIdx  int
	}

	var survivors []entry
	var eligibleItems []domain.QueueItem
	for i, item := range items {
		campaign, drop, ok := eligibleNow(item, campaigns, progress, claimed, skipLink, now)
		if !ok {
			continue
		}
		eligibleItems = append(eligibleItems, item)
		survivors = append(survivors, entry{
			cand: Candidate{
				Drop: drop, Campaign: campaign, Pin: item.Pin,
				GameRank: GameRank(ord.Games, campaign.GameName),
			},
			position: item.Position,
			origIdx:  i,
		})
	}
	if len(survivors) == 0 {
		return nil
	}
	alongside := Alongside(eligibleItems, campaigns, progress, claimed, skipLink, now)
	for i := range survivors {
		survivors[i].cand.Alongside = alongside[survivors[i].cand.Drop.ID]
	}

	less := func(i, j int) bool {
		a, b := survivors[i], survivors[j]
		if l, ok := pinLess(a.cand.Pin, b.cand.Pin); ok {
			return l
		}
		if a.cand.GameRank != b.cand.GameRank {
			return a.cand.GameRank < b.cand.GameRank
		}
		if ord.PreferChannelSpecific && a.cand.Alongside != b.cand.Alongside {
			return b.cand.Alongside
		}
		if l, ok := modeLess(ord.Mode, a.cand, b.cand, a.position, b.position, progress); ok {
			return l
		}
		return a.origIdx < b.origIdx
	}
	sort.SliceStable(survivors, less)

	out := make([]Candidate, len(survivors))
	for i, s := range survivors {
		out[i] = s.cand
	}
	return out
}

// NextDrop returns the single highest-priority eligible drop, or (nil, nil) when
// nothing is currently eligible.
func NextDrop(items []domain.QueueItem, campaigns map[string]domain.Campaign, progress map[string]domain.Progress, claimed map[string]bool, ord Ordering, skipLink bool, now time.Time) (*Candidate, error) {
	cands := EligibleCandidates(items, campaigns, progress, claimed, ord, skipLink, now)
	if len(cands) == 0 {
		return nil, nil
	}
	return &cands[0], nil
}

// ReasonNotWatchTime is the queue reason for a zero-minute (event-based) drop.
const ReasonNotWatchTime = "not a watch-time drop"

// EligibilityReason classifies why item is or is not currently selectable,
// returning the status the engine should record on the queue item along with
// a human-readable reason. eligible=true, status=domain.ItemPending means the
// item is selectable right now.
func EligibilityReason(item domain.QueueItem, campaigns map[string]domain.Campaign, progress map[string]domain.Progress, claimed map[string]bool, skipLink bool, now time.Time) (eligible bool, status domain.ItemStatus, reason string) {
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
	if linkBlocked(skipLink, campaign, item.DropID, progress) {
		return false, domain.ItemNeedsAction, "account link required"
	}

	drop, ok := findDrop(campaign, item.DropID)
	if !ok {
		return false, domain.ItemUnavailable, "drop not found in campaign"
	}

	if claimed[item.DropID] {
		return false, domain.ItemClaimed, "already claimed"
	}
	if drop.RequiredMinutes <= 0 {
		return false, domain.ItemUnavailable, ReasonNotWatchTime
	}
	// Reward already owned (matched at discovery against the earned inventory):
	// Twitch reports isClaimed=false but will never credit a reward the account
	// already holds, so surface it instead of watching a channel for nothing -
	// unless the user forced it (Watch anyway), which falls through to eligible.
	if p, ok := progress[item.DropID]; ok && p.Owned && !item.ForceWatch {
		return false, domain.ItemNeedsAction, "reward already owned - Twitch will not credit it again"
	}
	// Complete but never confirmed claimed: the reward is sitting unclaimed on
	// Twitch (a failed auto-claim, or a tier that finished in the background of
	// a simultaneous multi-drop campaign). Surface it instead of lying "claimed".
	if p, ok := progress[item.DropID]; ok && p.Complete() {
		if item.Platform.OrTwitch() == domain.PlatformKick {
			return false, domain.ItemNeedsAction, "watch complete - claim it in the Kick inventory"
		}
		return false, domain.ItemNeedsAction, "watch complete - claim it in the Twitch inventory"
	}

	if !preconditionsMet(drop.PreconditionIDs, claimed) {
		return false, domain.ItemBlocked, "waiting on prerequisite drop"
	}

	return true, domain.ItemPending, ""
}
