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
	// Owned reports that the drop's reward is already held (skipped unless forced);
	// ForceWatch reports the user's manual override of that skip. The UI shows a
	// "Watch anyway" toggle when Owned is set.
	Owned      bool `json:"owned"`
	ForceWatch bool `json:"forceWatch"`
	// Pin is the focus rank (0 = not pinned, 1 = selected first). Alongside
	// marks an any-channel drop that an eligible channel-specific drop of the
	// same game and platform also credits (ranked after those drops).
	Pin       int  `json:"pin"`
	Alongside bool `json:"alongside"`
	// NeedsUser is true only for a needs_action row a human must act on: an
	// account link, a claim that has to be made by hand, or an expired session.
	// Informational needs_action rows (owned-reward skip, not crediting) are
	// false, so the dashboard banner can ignore them.
	NeedsUser bool `json:"needsUser"`
	// ActionURL/ActionLabel are the one-click fix for a needs_action row that
	// needs the user (account link, manual claim, expired session); "" when
	// there is none. The UI opens ActionURL through POST /api/open.
	ActionURL   string `json:"actionURL"`
	ActionLabel string `json:"actionLabel"`
}

type ClaimDTO struct {
	DropID    string    `json:"dropID"`
	Platform  string    `json:"platform"`
	DropName  string    `json:"dropName"`
	Game      string    `json:"game"`
	ClaimedAt time.Time `json:"claimedAt"`
	Source    string    `json:"source,omitempty"` // "" = claimed by dropwarden, "detected" = found claimed in the inventory
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
	AlwaysOnBottom     bool     `json:"alwaysOnBottom"` // Windows only; the UI disables it elsewhere
	StartMinimized     bool     `json:"startMinimized"`
	DimOpacity         float64  `json:"dimOpacity"`
	ChromePath         string   `json:"chromePath"`
	MuteAudio          bool     `json:"muteAudio"`
	KickEnabled        bool     `json:"kickEnabled"`
	ClaimStrategy      string   `json:"claimStrategy"`
	AllowGQLFallback   bool     `json:"allowGqlFallback"`
	SkipAccountLink    bool     `json:"skipAccountLink"`
	// DOM claim fallback (claim.inventory_button_*). Pointers so a partial POST
	// that omits them leaves the config alone; "" selector = capture only.
	ClaimButtonSelector *string `json:"claimButtonSelector,omitempty"`
	ClaimButtonText     *string `json:"claimButtonText,omitempty"`
	AuthMode            string  `json:"authMode"`
	StoredEnabled       bool    `json:"storedEnabled"`
	LogLevel            string  `json:"logLevel"`
	CaptureGQLBodies    bool    `json:"captureGqlBodies"`
	// PreferChannelSpecific is read-only here (config file only; Save ignores
	// it): the Queue view uses it to describe the selection order accurately.
	PreferChannelSpecific bool `json:"preferChannelSpecific"`

	// Updates pane. Pointers so a partial POST that omits them (an older
	// client, a scripted call) leaves the update block untouched instead of
	// zeroing it; the settings UI always sends all five.
	UpdateEnabled       *bool   `json:"updateEnabled,omitempty"`
	UpdateAutoInstall   *bool   `json:"updateAutoInstall,omitempty"`
	UpdateShowChangelog *bool   `json:"updateShowChangelog,omitempty"`
	UpdateFeedURL       *string `json:"updateFeedURL,omitempty"`
	UpdateIntervalHours *int    `json:"updateIntervalHours,omitempty"`

	// KeepAwake is power.keep_awake (while_watching|always|never); "" in a
	// POST leaves it alone.
	KeepAwake string `json:"keepAwake,omitempty"`
}

type LoginDTO struct {
	LoggedIn bool `json:"loggedIn"`
}

// ProfileDTO is one profile's editable overlay for the Profiles settings
// pane - the raw overlay values (what the profile itself sets vs inherits),
// not the resolved Effective ones the dashboard uses.
type ProfileDTO struct {
	Name         string   `json:"name"`
	DisplayName  string   `json:"displayName"`
	Enabled      bool     `json:"enabled"`
	Games        []string `json:"games"`
	InheritGames bool     `json:"inheritGames"`
	KickEnabled  bool     `json:"kickEnabled"` // effective (resolved), for display
}
