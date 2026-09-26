package watcher

// State is a watcher engine state. The engine is a single goroutine; state is
// only ever mutated inside its loop, so no locking is required.
type State string

const (
	StateIdle             State = "idle"
	StateDiscovering      State = "discovering"
	StateSelectingChannel State = "selecting_channel"
	StateOpening          State = "opening"
	StateValidating       State = "validating_playback"
	StateWatching         State = "watching"
	StateClaiming         State = "claiming"
	StateCooldown         State = "cooldown"
	StateNeedsUserAction  State = "needs_user_action"
	StateBackoff          State = "backoff"
)

// String implements fmt.Stringer.
func (s State) String() string { return string(s) }

// WatchInProgress reports whether an engine in this state is mid-watch:
// opening a channel, validating its playback, watching it, or claiming the
// drop it earned. The self-updater defers an automatic install while any
// engine is in one of these states, since a restart would cut the watch short.
func (s State) WatchInProgress() bool {
	switch s {
	case StateOpening, StateValidating, StateWatching, StateClaiming:
		return true
	}
	return false
}
