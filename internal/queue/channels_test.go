package queue

import (
	"testing"
	"time"

	"github.com/rake-pro/dropwarden/internal/domain"
)

func noStats(string) (int, int) { return 0, 0 }
func noBlacklist(string) bool   { return false }

func loginsOf(chs []domain.Channel) []string {
	out := make([]string, len(chs))
	for i, c := range chs {
		out[i] = c.Login
	}
	return out
}

func TestRankChannels_AnyChannelUsesLive(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	live := []domain.Channel{
		{Login: "alice", Live: true, DropsEnabled: true, FirstSeenLiveAt: now.Add(-1 * time.Hour)},
		{Login: "bob", Live: true, DropsEnabled: true, FirstSeenLiveAt: now.Add(-1 * time.Hour)},
	}
	campaign := domain.Campaign{} // empty ACL -> AnyChannel
	got := RankChannels(campaign, live, "", noStats, noBlacklist, 0, now)
	if len(got) != 2 {
		t.Fatalf("expected 2 candidates, got %v", got)
	}
}

func TestRankChannels_ACLIntersect(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	live := []domain.Channel{
		{Login: "alice", Live: true, FirstSeenLiveAt: now.Add(-1 * time.Hour)},
		{Login: "bob", Live: true, FirstSeenLiveAt: now.Add(-1 * time.Hour)},
	}
	campaign := domain.Campaign{AllowedChannels: []domain.Channel{{Login: "bob"}, {Login: "carol"}}}
	got := RankChannels(campaign, live, "", noStats, noBlacklist, 0, now)
	logins := loginsOf(got)
	if len(logins) != 1 || logins[0] != "bob" {
		t.Fatalf("expected only bob (ACL intersect live), got %v", logins)
	}
}

func TestRankChannels_BlacklistExcluded(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	live := []domain.Channel{
		{Login: "alice", Live: true, FirstSeenLiveAt: now.Add(-1 * time.Hour)},
		{Login: "bob", Live: true, FirstSeenLiveAt: now.Add(-1 * time.Hour)},
	}
	campaign := domain.Campaign{}
	blacklist := func(login string) bool { return login == "alice" }
	got := RankChannels(campaign, live, "", noStats, blacklist, 0, now)
	logins := loginsOf(got)
	if len(logins) != 1 || logins[0] != "bob" {
		t.Fatalf("expected alice excluded, got %v", logins)
	}
}

func TestRankChannels_StreamUpDebounce(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	delay := 10 * time.Minute
	live := []domain.Channel{
		{Login: "fresh", Live: true, FirstSeenLiveAt: now.Add(-1 * time.Minute)}, // within debounce
		{Login: "stable", Live: true, FirstSeenLiveAt: now.Add(-1 * time.Hour)},  // past debounce
	}
	campaign := domain.Campaign{}
	got := RankChannels(campaign, live, "", noStats, noBlacklist, delay, now)
	logins := loginsOf(got)
	if len(logins) != 1 || logins[0] != "stable" {
		t.Fatalf("expected only stable channel past debounce, got %v", logins)
	}
}

func TestRankChannels_StreamUpDebounceCurrentExempt(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	delay := 10 * time.Minute
	live := []domain.Channel{
		{Login: "fresh", Live: true, FirstSeenLiveAt: now.Add(-1 * time.Minute)}, // within debounce but current
	}
	campaign := domain.Campaign{}
	got := RankChannels(campaign, live, "fresh", noStats, noBlacklist, delay, now)
	logins := loginsOf(got)
	if len(logins) != 1 || logins[0] != "fresh" {
		t.Fatalf("expected current channel exempt from debounce, got %v", logins)
	}
}

func TestRankChannels_RankByStatsThenViewers(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	live := []domain.Channel{
		{Login: "low-score", Live: true, ViewerCount: 1000, FirstSeenLiveAt: now.Add(-1 * time.Hour)},
		{Login: "high-score", Live: true, ViewerCount: 10, FirstSeenLiveAt: now.Add(-1 * time.Hour)},
		{Login: "tied-more-viewers", Live: true, ViewerCount: 500, FirstSeenLiveAt: now.Add(-1 * time.Hour)},
		{Login: "tied-fewer-viewers", Live: true, ViewerCount: 50, FirstSeenLiveAt: now.Add(-1 * time.Hour)},
	}
	stats := func(login string) (int, int) {
		switch login {
		case "high-score":
			return 10, 0 // net +10
		case "low-score":
			return 0, 10 // net -10
		default:
			return 5, 0 // net +5, tied against each other -> viewers decide
		}
	}
	campaign := domain.Campaign{}
	got := RankChannels(campaign, live, "", stats, noBlacklist, 0, now)
	logins := loginsOf(got)
	want := []string{"high-score", "tied-more-viewers", "tied-fewer-viewers", "low-score"}
	if !equalSlices(logins, want) {
		t.Fatalf("got %v, want %v", logins, want)
	}
}

func TestRankChannels_CurrentFloatsToTop(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	live := []domain.Channel{
		{Login: "top-scorer", Live: true, FirstSeenLiveAt: now.Add(-1 * time.Hour)},
		{Login: "current", Live: true, FirstSeenLiveAt: now.Add(-1 * time.Hour)},
	}
	stats := func(login string) (int, int) {
		if login == "top-scorer" {
			return 100, 0
		}
		return 0, 0
	}
	campaign := domain.Campaign{}
	got := RankChannels(campaign, live, "current", stats, noBlacklist, 0, now)
	logins := loginsOf(got)
	if len(logins) == 0 || logins[0] != "current" {
		t.Fatalf("expected current channel first despite lower score, got %v", logins)
	}
}

func TestRankChannels_Empty(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	campaign := domain.Campaign{}
	got := RankChannels(campaign, nil, "", noStats, noBlacklist, 0, now)
	if len(got) != 0 {
		t.Fatalf("expected empty result, got %v", got)
	}
}
