package domain

import "time"

// ItemStatus is the lifecycle state of a queued drop.
type ItemStatus string

const (
	ItemPending     ItemStatus = "pending"              // eligible, waiting its turn
	ItemActive      ItemStatus = "active"               // currently being watched
	ItemBlocked     ItemStatus = "blocked_precondition" // a prerequisite drop is unclaimed
	ItemNeedsAction ItemStatus = "needs_action"         // account link etc. required
	ItemClaimed     ItemStatus = "claimed"              // reward obtained
	ItemExpired     ItemStatus = "expired"              // campaign window passed
	ItemUnavailable ItemStatus = "unavailable"          // no live channel / claim failed
	ItemDisabled    ItemStatus = "disabled"             // user turned it off
)

// PriorityMode determines the order in which pending drops are selected.
type PriorityMode string

const (
	// PriorityTopDown follows the user's explicit queue order (default).
	PriorityTopDown PriorityMode = "top_down"
	// PriorityEndingSoonest prefers drops whose campaign ends first.
	PriorityEndingSoonest PriorityMode = "ending_soonest"
	// PriorityClosestToComplete prefers drops nearest their required minutes.
	PriorityClosestToComplete PriorityMode = "closest_to_complete"
	// PriorityShortestFirst prefers drops with the least required minutes.
	PriorityShortestFirst PriorityMode = "shortest_first"
)

// ValidPriorityMode reports whether m is a recognized mode.
func ValidPriorityMode(m PriorityMode) bool {
	switch m {
	case PriorityTopDown, PriorityEndingSoonest, PriorityClosestToComplete, PriorityShortestFirst:
		return true
	}
	return false
}

// QueueItem is one drop the user has placed in the watch queue.
type QueueItem struct {
	DropID        string
	CampaignID    string
	Platform      Platform
	Position      int
	Enabled       bool
	Status        ItemStatus
	Reason        string // human-readable detail for needs_action / unavailable
	LastAttemptAt time.Time

	// ForceWatch is a manual user override that makes eligibility ignore the
	// owned-reward skip for THIS drop (only): the user insists on watching a drop
	// whose reward the account already holds, e.g. to test crediting end to end.
	// It bypasses ONLY the Owned gate - a genuinely claimed or complete drop is
	// still excluded - and the reactive no-credit give-up still bounds the grind.
	ForceWatch bool
}
