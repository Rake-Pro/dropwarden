package watcher

import "time"

// Emitter receives engine events. The CLI implements it as a log sink; the
// Wails app implements it by forwarding to the frontend event bus. Keeping the
// engine behind this interface is what lets it run head-less in tests and in the
// CLI without importing any UI code.
type Emitter interface {
	Emit(event string, payload any)
}

// Event names. These are the wire identifiers the UI subscribes to.
const (
	EvState       = "watcher:state"
	EvProgress    = "watcher:progress"
	EvChannel     = "watcher:channel"
	EvSession     = "watcher:session"
	EvCampaigns   = "campaigns:updated"
	EvClaimed     = "drop:claimed"
	EvNeedsAction = "drop:needs_action"
	EvError       = "watcher:error"
)

// StatePayload is emitted on every transition.
type StatePayload struct {
	From   State     `json:"from"`
	To     State     `json:"to"`
	At     time.Time `json:"at"`
	Reason string    `json:"reason"`
}

// ProgressPayload is emitted as watch minutes advance.
type ProgressPayload struct {
	DropID   string    `json:"dropID"`
	Current  int       `json:"current"`
	Required int       `json:"required"`
	Source   string    `json:"source"`
	At       time.Time `json:"at"`
}

// ChannelPayload is emitted when the active channel changes.
type ChannelPayload struct {
	Login       string `json:"login"`
	DisplayName string `json:"displayName"`
	Game        string `json:"game"`
}

// SessionPayload is emitted when a watch session ends.
type SessionPayload struct {
	SessionID string    `json:"sessionID"`
	DropID    string    `json:"dropID"`
	Outcome   string    `json:"outcome"`
	StartedAt time.Time `json:"startedAt"`
	EndedAt   time.Time `json:"endedAt"`
}

// ClaimedPayload is emitted when a drop is claimed.
type ClaimedPayload struct {
	DropID     string `json:"dropID"`
	CampaignID string `json:"campaignID"`
	Name       string `json:"name"`
}

// NeedsActionPayload is emitted when a drop needs the user (account link).
type NeedsActionPayload struct {
	DropID string `json:"dropID"`
	Reason string `json:"reason"`
	URL    string `json:"url"`
}

// ErrorPayload is emitted on recoverable and unrecoverable errors.
type ErrorPayload struct {
	Message      string    `json:"message"`
	Recoverable  bool      `json:"recoverable"`
	BackoffUntil time.Time `json:"backoffUntil,omitempty"`
}
