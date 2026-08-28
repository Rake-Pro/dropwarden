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
	Platform     Platform
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

// MinutesWatched is the drop-credited watch time accrued during the session
// for its TARGET drop only. It is not wall-clock time: simultaneous drops
// accrue in parallel, so use DurationMinutes for "time spent watching".
func (s WatchSession) MinutesWatched() int {
	d := s.EndMinutes - s.StartMinutes
	if d < 0 {
		return 0
	}
	return d
}

// DurationMinutes is the wall-clock length of the session in whole minutes.
func (s WatchSession) DurationMinutes() int {
	if s.StartedAt.IsZero() || s.EndedAt.IsZero() || s.EndedAt.Before(s.StartedAt) {
		return 0
	}
	return int(s.EndedAt.Sub(s.StartedAt).Minutes())
}

// Claim is a claimed drop reward, recorded for the history/report view.
type Claim struct {
	DropID     string
	CampaignID string
	Platform   Platform
	DropName   string
	GameName   string
	Benefits   []Benefit
	ClaimedAt  time.Time
}
