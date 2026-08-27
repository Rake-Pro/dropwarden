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
	got := RankChannels(campaign, live, "", nil, noStats, noBlacklist, 0, now)
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
	got := RankChannels(campaign, live, "", nil, noStats, noBlacklist, 0, now)
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
	got := RankChannels(campaign, live, "", nil, noStats, blacklist, 0, now)
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
	got := RankChannels(campaign, live, "", nil, noStats, noBlacklist, delay, now)
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
	got := RankChannels(campaign, live, "fresh", nil, noStats, noBlacklist, delay, now)
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
	got := RankChannels(campaign, live, "", nil, stats, noBlacklist, 0, now)
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
	got := RankChannels(campaign, live, "current", nil, stats, noBlacklist, 0, now)
	logins := loginsOf(got)
	if len(logins) == 0 || logins[0] != "current" {
		t.Fatalf("expected current channel first despite lower score, got %v", logins)
	}
}

func TestRankChannels_Empty(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	campaign := domain.Campaign{}
	got := RankChannels(campaign, nil, "", nil, noStats, noBlacklist, 0, now)
	if len(got) != 0 {
		t.Fatalf("expected empty result, got %v", got)
	}
}

// Twitch Rivals template: a "global" any-channel campaign creditable on every
// participating channel, plus team campaigns locked to channel subsets. A team
// channel covers global + its team set; coverage must prefer it.
func rivalsCandidates() (anchor domain.Campaign, cands []Candidate) {
	anchor = domain.Campaign{ID: "team12", GameID: "rust", AllowedChannels: []domain.Channel{{Login: "team1"}, {Login: "team2"}}}
	global := domain.Campaign{ID: "global", GameID: "rust"} // empty ACL = any channel
	team2Only := domain.Campaign{ID: "team2only", GameID: "rust", AllowedChannels: []domain.Channel{{Login: "team2"}}}
	otherGame := domain.Campaign{ID: "othergame", GameID: "halo"} // any-channel, different game
	cands = []Candidate{
		{Drop: domain.Drop{ID: "dA"}, Campaign: anchor},
		{Drop: domain.Drop{ID: "dB"}, Campaign: global},
		{Drop: domain.Drop{ID: "dC"}, Campaign: team2Only},
		{Drop: domain.Drop{ID: "dD"}, Campaign: otherGame},
	}
	return anchor, cands
}

func TestChannelCoverage_RivalsTemplate(t *testing.T) {
	anchor, cands := rivalsCandidates()
	if got := ChannelCoverage("team1", anchor, cands); got != 2 {
		t.Fatalf("team1 coverage = %d, want 2 (anchor + global)", got)
	}
	if got := ChannelCoverage("team2", anchor, cands); got != 3 {
		t.Fatalf("team2 coverage = %d, want 3 (anchor + global + team2-only)", got)
	}
}

func TestRankChannels_CoveragePrefersSetChannel(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	anchor, cands := rivalsCandidates()
	live := []domain.Channel{
		{Login: "team1", Live: true, FirstSeenLiveAt: now.Add(-time.Hour)},
		{Login: "team2", Live: true, FirstSeenLiveAt: now.Add(-time.Hour)},
	}
	coverage := func(login string) int { return ChannelCoverage(login, anchor, cands) }
	got := RankChannels(anchor, live, "", coverage, noStats, noBlacklist, 0, now)
	if len(got) != 2 || got[0].Login != "team2" {
		t.Fatalf("expected team2 (covers 3 drops) ranked first, got %v", loginsOf(got))
	}
}

func TestRankChannels_CurrentStillBeatsCoverage(t *testing.T) {
	// Stickiness wins: never hop channels mid-set just because another channel
	// covers more - the current channel keeps draining what it can credit.
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	anchor, cands := rivalsCandidates()
	live := []domain.Channel{
		{Login: "team1", Live: true, FirstSeenLiveAt: now.Add(-time.Hour)},
		{Login: "team2", Live: true, FirstSeenLiveAt: now.Add(-time.Hour)},
	}
	coverage := func(login string) int { return ChannelCoverage(login, anchor, cands) }
	got := RankChannels(anchor, live, "team1", coverage, noStats, noBlacklist, 0, now)
	if len(got) != 2 || got[0].Login != "team1" {
		t.Fatalf("expected current team1 to stay first, got %v", loginsOf(got))
	}
}
