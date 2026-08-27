package domain

import "time"

// SessionOutcome records how a watch session ended. It is the backbone of the
// reporting view.
type SessionOutcome string

const (
	OutcomeClaimed        SessionOutcome = "claimed"         // drop completed and claimed
	OutcomeCompleted      SessionOutcome = "completed"       // minutes met, claim deferred
	OutcomeAbortedRaid    SessionOutcome = "aborted_raid"    // raided to an ineligible channel
	OutcomeAbortedOffline SessionOutcome = "aborted_offline" // channel went offline
	OutcomeAbortedStall   SessionOutcome = "aborted_stall"   // progress would not advance
	OutcomeChannelEnded   SessionOutcome = "aborted_channel_ended"
	OutcomeCrashed        SessionOutcome = "crashed"   // tab/renderer crash
	OutcomeUserStop       SessionOutcome = "user_stop" // user paused/stopped
	OutcomeError          SessionOutcome = "error"     // unclassified failure
)

// WatchSession is a single continuous attempt to watch one channel toward one
// drop. Its ID is time-sortable so history reads back in order.
type WatchSession struct {
	ID           string
	DropID       string
	CampaignID   string
	ChannelLogin string

	StartedAt time.Time
	EndedAt   time.Time

	StartMinutes int
	EndMinutes   int

	HardRefreshes int
	StallEvents   int

	Outcome SessionOutcome
	Note    string
}

// MinutesWatched is the credited watch time accrued during the session.
func (s WatchSession) MinutesWatched() int {
	d := s.EndMinutes - s.StartMinutes
	if d < 0 {
		return 0
	}
	return d
}

// Claim is a claimed drop reward, recorded for the history/report view.
type Claim struct {
	DropID     string
	CampaignID string
	DropName   string
	GameName   string
	Benefits   []Benefit
	ClaimedAt  time.Time
}
