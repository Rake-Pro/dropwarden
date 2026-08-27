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
	DropName  string    `json:"dropName"`
	Game      string    `json:"game"`
	ClaimedAt time.Time `json:"claimedAt"`
}

type SessionDTO struct {
	ID           string    `json:"id"`
	DropID       string    `json:"dropID"`
	ChannelLogin string    `json:"channelLogin"`
	StartedAt    time.Time `json:"startedAt"`
	EndedAt      time.Time `json:"endedAt"`
	Minutes      int       `json:"minutes"`
	Outcome      string    `json:"outcome"`
}

type StatsDTO struct {
	DropsClaimed   int            `json:"dropsClaimed"`
	MinutesWatched int            `json:"minutesWatched"`
	Sessions       int            `json:"sessions"`
	SuccessRate    int            `json:"successRate"` // percent
	OutcomeCounts  map[string]int `json:"outcomeCounts"`
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
	DimOpacity         float64  `json:"dimOpacity"`
	ChromePath         string   `json:"chromePath"`
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
