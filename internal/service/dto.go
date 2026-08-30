package service

import "time"

// DTOs are the JSON-friendly shapes the frontend consumes. They are kept
// separate from internal/domain so binding regeneration never churns the core
// model.

type CampaignDTO struct {
	ID             string    `json:"id"`
	Name           string    `json:"name"`
	Game           string    `json:"game"`
	Status         string    `json:"status"`
	EndAt          time.Time `json:"endAt"`
	AnyChannel     bool      `json:"anyChannel"`
	ChannelCount   int       `json:"channelCount"`
	AccountLinkURL string    `json:"accountLinkURL"`
	Drops          []DropDTO `json:"drops"`
}

type DropDTO struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	RequiredMinutes int    `json:"requiredMinutes"`
	CurrentMinutes  int    `json:"currentMinutes"`
	Claimed         bool   `json:"claimed"`
}

type QueueItemDTO struct {
	DropID          string `json:"dropID"`
	CampaignID      string `json:"campaignID"`
	Platform        string `json:"platform"`
	DropName        string `json:"dropName"`
	Game            string `json:"game"`
	Position        int    `json:"position"`
	Enabled         bool   `json:"enabled"`
	Status          string `json:"status"`
	Reason          string `json:"reason"`
	CurrentMinutes  int    `json:"currentMinutes"`
	RequiredMinutes int    `json:"requiredMinutes"`
}

type ClaimDTO struct {
	DropID    string    `json:"dropID"`
	Platform  string    `json:"platform"`
	DropName  string    `json:"dropName"`
	Game      string    `json:"game"`
	ClaimedAt time.Time `json:"claimedAt"`
}

type SessionDTO struct {
	ID           string    `json:"id"`
	Platform     string    `json:"platform"`
	DropID       string    `json:"dropID"`
	ChannelLogin string    `json:"channelLogin"`
	StartedAt    time.Time `json:"startedAt"`
	EndedAt      time.Time `json:"endedAt"`
	Minutes      int       `json:"minutes"`
	Outcome      string    `json:"outcome"`
}

type StatsDTO struct {
	// DropsEarned counts drops whose watch requirement is met, whether or not
	// the claim is confirmed; DropsClaimed counts confirmed claims only. All
	// totals are lifetime (read from the store), not per-session.
	DropsEarned    int            `json:"dropsEarned"`
	DropsClaimed   int            `json:"dropsClaimed"`
	MinutesWatched int            `json:"minutesWatched"`
	Sessions       int            `json:"sessions"`
	SuccessRate    int            `json:"successRate"` // percent
	OutcomeCounts  map[string]int `json:"outcomeCounts"`

	// ByPlatform splits the headline totals per platform so a multi-platform
	// setup can see where the time and claims went. Keys are platform names
	// ("twitch", "kick"); single-platform setups just get one entry.
	ByPlatform map[string]PlatformTotalsDTO `json:"byPlatform"`
}

type PlatformTotalsDTO struct {
	DropsEarned    int `json:"dropsEarned"`
	DropsClaimed   int `json:"dropsClaimed"`
	MinutesWatched int `json:"minutesWatched"`
	Sessions       int `json:"sessions"`
}

// ConfigDTO is the subset of config the settings UI edits. It is deliberately
// flat and string-friendly for form binding.
type ConfigDTO struct {
	Theme              string   `json:"theme"`
	Games              []string `json:"games"`
	AnyGame            bool     `json:"anyGame"`
	PriorityMode       string   `json:"priorityMode"`
	AutoEnqueueNew     bool     `json:"autoEnqueueNew"`
	FallThrough        bool     `json:"fallThrough"`
	Volume             float64  `json:"volume"`
	ForceLowestQuality bool     `json:"forceLowestQuality"`
	NeverFocus         bool     `json:"neverFocus"`
	StartMinimized     bool     `json:"startMinimized"`
	DimOpacity         float64  `json:"dimOpacity"`
	ChromePath         string   `json:"chromePath"`
	MuteAudio          bool     `json:"muteAudio"`
	KickEnabled        bool     `json:"kickEnabled"`
	ClaimStrategy      string   `json:"claimStrategy"`
	AllowGQLFallback   bool     `json:"allowGqlFallback"`
	SkipAccountLink    bool     `json:"skipAccountLink"`
	AuthMode           string   `json:"authMode"`
	StoredEnabled      bool     `json:"storedEnabled"`
	LogLevel           string   `json:"logLevel"`
	CaptureGQLBodies   bool     `json:"captureGqlBodies"`
}

type LoginDTO struct {
	LoggedIn bool `json:"loggedIn"`
}
