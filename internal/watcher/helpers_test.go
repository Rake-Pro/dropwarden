package watcher

import (
	"testing"
	"time"
)

func TestLoginFromPath(t *testing.T) {
	cases := map[string]string{
		"https://www.twitch.tv/shroud":           "shroud",
		"/shroud":                                "shroud",
		"shroud":                                 "shroud",
		"https://www.twitch.tv/shroud/videos":    "shroud",
		"/directory/category/rust":               "",
		"/drops/inventory":                       "",
		"https://www.twitch.tv/Streamer_A?foo=1": "streamer_a",
		"/":                                      "",
	}
	for in, want := range cases {
		if got := loginFromPath(in); got != want {
			t.Errorf("loginFromPath(%q)=%q want %q", in, got, want)
		}
	}
}

func TestBackoffDuration(t *testing.T) {
	if backoffDuration(1) != 30*time.Second {
		t.Fatal("level 1 should be 30s")
	}
	if backoffDuration(99) != 15*time.Minute {
		t.Fatal("high level should cap at 15m")
	}
	// monotonic non-decreasing
	prev := time.Duration(0)
	for i := 1; i <= 6; i++ {
		d := backoffDuration(i)
		if d < prev {
			t.Fatalf("backoff decreased at level %d", i)
		}
		prev = d
	}
}

func TestNewSessionIDSortable(t *testing.T) {
	early := newSessionID(time.UnixMilli(1000), func() float64 { return 0.1 })
	late := newSessionID(time.UnixMilli(2000), func() float64 { return 0.1 })
	if !(early < late) {
		t.Fatalf("session ids should sort by time: %q vs %q", early, late)
	}
}
