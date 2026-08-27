// Package domain holds the core types for dropwarden. It imports nothing
// outside the standard library so it stays trivially testable and free of any
// coupling to browser, storage, or UI concerns.
package domain

import "time"

// CampaignStatus mirrors the states Twitch exposes for a drop campaign.
type CampaignStatus string

const (
	StatusActive   CampaignStatus = "ACTIVE"
	StatusUpcoming CampaignStatus = "UPCOMING"
	StatusExpired  CampaignStatus = "EXPIRED"
)

// Campaign is a Twitch drop campaign for a single game over a time window.
type Campaign struct {
	ID       string
	Name     string
	GameID   string
	GameName string
	GameSlug string // used to build /directory/category/<slug>
	Status   CampaignStatus
	StartAt  time.Time
	EndAt    time.Time

	// AccountLinkURL is non-empty when the campaign requires the viewer to have
	// linked their Twitch account to an external game account. Such drops cannot
	// be claimed by watching alone and are surfaced to the user rather than
	// attempted.
	AccountLinkURL string

	// AllowedChannels is the campaign ACL. An empty slice means "any channel
	// streaming the game with drops enabled" (up to Twitch's own limits).
	AllowedChannels []Channel

	Drops []Drop

	// DetailsFetched records when CampaignDetails was last read for this
	// campaign, so discovery can skip re-expanding fresh entries.
	DetailsFetched time.Time
}

// AnyChannel reports whether the campaign credits progress on any drops-enabled
// channel for the game (an empty ACL), rather than a fixed allowlist.
func (c Campaign) AnyChannel() bool { return len(c.AllowedChannels) == 0 }

// RequiresAccountLink reports whether the campaign needs an external linked
// account before its drops can be claimed.
func (c Campaign) RequiresAccountLink() bool { return c.AccountLinkURL != "" }

// Benefit is a single reward granted by a drop.
type Benefit struct {
	ID       string
	Name     string
	ImageURL string
}

// Drop is one time-based reward tier within a campaign.
type Drop struct {
	ID              string
	CampaignID      string
	Name            string
	RequiredMinutes int
	Benefits        []Benefit

	// PreconditionIDs lists drops (within the same campaign) that must be
	// claimed before this one becomes attainable. Twitch uses this for
	// sequential drop tiers.
	PreconditionIDs []string

	StartAt time.Time
	EndAt   time.Time
}
