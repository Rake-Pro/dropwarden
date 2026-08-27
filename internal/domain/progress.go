package domain

import "time"

// ProgressSource records where a progress reading came from, in descending
// order of trustworthiness. Server-confirmed inventory minutes beat a client
// estimate.
type ProgressSource string

const (
	SourceInventory   ProgressSource = "gql_inventory"    // server-confirmed minutes
	SourceCurrentDrop ProgressSource = "gql_current_drop" // GQL poll, authoritative-ish
	SourceWebSocket   ProgressSource = "ws"               // pubsub push
	SourceDOM         ProgressSource = "dom"              // scraped progress bar
	SourceEstimate    ProgressSource = "estimate"         // client-side, capped
)

// authoritative reports whether a source reflects server-confirmed state rather
// than a client-side guess.
func (s ProgressSource) authoritative() bool {
	return s == SourceInventory || s == SourceCurrentDrop
}

// Progress is the best-known watch progress for a single drop.
type Progress struct {
	DropID  string
	ClaimID string // "UserID#CampaignID#DropID" once known

	// RealMinutes is the server-confirmed watch time (from the inventory query).
	RealMinutes int
	// ExtraMinutes is a client-side estimate accrued while the server has not
	// yet confirmed; Twitch caps this at +15 before a resync is required.
	ExtraMinutes int

	RequiredMinutes int
	Claimable       bool
	ClaimedAt       *time.Time

	Source     ProgressSource
	ObservedAt time.Time
}

// Effective returns the best-known minute count and whether that count is
// server-authoritative. When the source is only an estimate, RealMinutes plus
// the capped estimate is returned but flagged non-authoritative so callers know
// a resync (hard refresh) may reveal a different value.
func (p Progress) Effective() (minutes int, authoritative bool) {
	if p.Source.authoritative() {
		return p.RealMinutes, true
	}
	extra := p.ExtraMinutes
	if extra > 15 {
		extra = 15
	}
	return p.RealMinutes + extra, false
}

// Complete reports whether the drop has met its required watch time by the best
// available reading.
func (p Progress) Complete() bool {
	if p.RequiredMinutes <= 0 {
		return false
	}
	m, _ := p.Effective()
	return m >= p.RequiredMinutes
}
