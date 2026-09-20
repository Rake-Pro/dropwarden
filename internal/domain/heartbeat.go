package domain

// Heartbeat is what the platform page observed of its OWN watch-time
// reporting since the last drain: the player's minute-watched beacons and any
// drop-progress pushes. It answers "is the player telling the platform it is
// watching, and is the platform answering with credit" independently of the
// lagging inventory read.
type Heartbeat struct {
	// MinuteWatched counts minute-watched beacons the player sent.
	MinuteWatched int
	// Events counts every beacon event by name (minute-watched, video-play, ...).
	Events map[string]int
	// LastChannel, LastHidden, LastMuted describe the most recent minute-watched
	// beacon: the channel it reported and the player state it carried.
	LastChannel string
	LastHidden  bool
	LastMuted   bool
	// DropMessages counts drop-related WebSocket pushes seen.
	DropMessages int
	// Progress holds drop-progress pushes parsed into progress readings.
	Progress []Progress
	// Raw is the drained buffer verbatim, for debug dumps.
	Raw []byte
}
