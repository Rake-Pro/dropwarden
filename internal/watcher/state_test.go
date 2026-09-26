package watcher

import "testing"

func TestWatchInProgress(t *testing.T) {
	busy := map[State]bool{
		StateIdle: false, StateDiscovering: false, StateSelectingChannel: false,
		StateOpening: true, StateValidating: true, StateWatching: true, StateClaiming: true,
		StateCooldown: false, StateNeedsUserAction: false, StateBackoff: false,
	}
	for s, want := range busy {
		if got := s.WatchInProgress(); got != want {
			t.Errorf("%s.WatchInProgress() = %v, want %v", s, got, want)
		}
	}
}
