package domain

import "time"

// Channel is a Twitch channel, as seen either in a campaign ACL or in the
// game directory while hunting for a live channel to watch.
type Channel struct {
	ID           string
	Login        string // lowercase URL segment, e.g. "shroud"
	DisplayName  string
	GameName     string
	Live         bool
	DropsEnabled bool
	ViewerCount  int

	// FirstSeenLiveAt anchors the stream-up debounce: a channel that only just
	// went live is skipped until it has been live long enough to actually be
	// watchable, because Twitch's stream-up signal fires before playback is
	// reliably available.
	FirstSeenLiveAt time.Time
	LastSeenAt      time.Time
}
