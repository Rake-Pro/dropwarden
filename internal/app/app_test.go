package app

import (
	"testing"
	"time"
)

func TestDiscoveryStagger(t *testing.T) {
	cases := []struct {
		idx  int
		want time.Duration
	}{
		{0, 0}, // first-started profile: today's un-staggered timing
		{1, 90 * time.Second},
		{2, 180 * time.Second},
	}
	for _, tc := range cases {
		if got := discoveryStagger(tc.idx); got != tc.want {
			t.Fatalf("discoveryStagger(%d) = %v, want %v", tc.idx, got, tc.want)
		}
	}
}
