package queue

import (
	"reflect"
	"testing"
	"time"

	"github.com/rake-pro/dropwarden/internal/domain"
)

// orderFixture: two games. Rust has two single-streamer ACL campaigns and one
// any-channel campaign; Halo has one any-channel campaign.
func orderFixture(now time.Time) ([]domain.QueueItem, map[string]domain.Campaign) {
	end := now.Add(24 * time.Hour)
	mk := func(id, game string, acl []string, drops ...domain.Drop) domain.Campaign {
		c := baseCampaign(id, end, drops...)
		c.GameName = game
		for _, l := range acl {
			c.AllowedChannels = append(c.AllowedChannels, domain.Channel{Login: l})
		}
		return c
	}
	campaigns := map[string]domain.Campaign{
		"rgen":  mk("rgen", "Rust", nil, domain.Drop{ID: "r-gen", CampaignID: "rgen", RequiredMinutes: 30}),
		"racl1": mk("racl1", "Rust", []string{"streamer1"}, domain.Drop{ID: "r-acl1", CampaignID: "racl1", RequiredMinutes: 120}),
		"racl2": mk("racl2", "Rust", []string{"streamer2"}, domain.Drop{ID: "r-acl2", CampaignID: "racl2", RequiredMinutes: 60}),
		"halo":  mk("halo", "Halo Infinite", nil, domain.Drop{ID: "h-gen", CampaignID: "halo", RequiredMinutes: 15}),
	}
	items := []domain.QueueItem{
		{DropID: "h-gen", CampaignID: "halo", Position: 0, Enabled: true},
		{DropID: "r-gen", CampaignID: "rgen", Position: 1, Enabled: true},
		{DropID: "r-acl1", CampaignID: "racl1", Position: 2, Enabled: true},
		{DropID: "r-acl2", CampaignID: "racl2", Position: 3, Enabled: true},
	}
	return items, campaigns
}

func candIDs(cands []Candidate) []string {
	out := make([]string, len(cands))
	for i, c := range cands {
		out[i] = c.Drop.ID
	}
	return out
}

func TestOrdering_GameOrderThenChannelSpecificFirst(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	items, campaigns := orderFixture(now)
	ord := Ordering{Mode: domain.PriorityTopDown, Games: []string{"rust", "Halo Infinite"}, PreferChannelSpecific: true}
	got := candIDs(EligibleCandidates(items, campaigns, nil, nil, ord, true, now))
	want := []string{"r-acl1", "r-acl2", "r-gen", "h-gen"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
}

func TestOrdering_PreferChannelSpecificOff(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	items, campaigns := orderFixture(now)
	ord := Ordering{Mode: domain.PriorityTopDown, Games: []string{"Rust", "Halo Infinite"}}
	got := candIDs(EligibleCandidates(items, campaigns, nil, nil, ord, true, now))
	want := []string{"r-gen", "r-acl1", "r-acl2", "h-gen"} // game rank, then position
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
}

func TestOrdering_UnknownGamesLastAndEmptyListKeepsMode(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	items, campaigns := orderFixture(now)
	got := candIDs(EligibleCandidates(items, campaigns, nil, nil, Ordering{Mode: domain.PriorityTopDown, Games: []string{"Halo Infinite"}}, true, now))
	if got[0] != "h-gen" {
		t.Fatalf("listed game should lead, got %v", got)
	}
	got = candIDs(EligibleCandidates(items, campaigns, nil, nil, Ordering{Mode: domain.PriorityTopDown}, true, now))
	if want := []string{"h-gen", "r-gen", "r-acl1", "r-acl2"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("no games list: order = %v, want %v (plain position)", got, want)
	}
}

func TestOrdering_PinnedFirst(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	items, campaigns := orderFixture(now)
	items[0].Pin = 2 // h-gen: lowest game, pinned rank 2
	items[1].Pin = 1 // r-gen: any-channel that would otherwise yield to ACL drops
	ord := Ordering{Mode: domain.PriorityTopDown, Games: []string{"Rust", "Halo Infinite"}, PreferChannelSpecific: true}
	cands := EligibleCandidates(items, campaigns, nil, nil, ord, true, now)
	want := []string{"r-gen", "h-gen", "r-acl1", "r-acl2"}
	if got := candIDs(cands); !reflect.DeepEqual(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
	if cands[0].Pin != 1 || cands[0].GameRank != 0 || !cands[0].Alongside {
		t.Fatalf("top candidate ranking fields = %+v", cands[0])
	}
}

func TestOrdering_ModeTiebreakWithinGroups(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	items, campaigns := orderFixture(now)
	// shortest_first: within Rust's ACL group r-acl2 (60) beats r-acl1 (120);
	// the any-channel Rust drop (30) is still behind both, Halo (15) still last.
	ord := Ordering{Mode: domain.PriorityShortestFirst, Games: []string{"Rust", "Halo Infinite"}, PreferChannelSpecific: true}
	got := candIDs(EligibleCandidates(items, campaigns, nil, nil, ord, true, now))
	want := []string{"r-acl2", "r-acl1", "r-gen", "h-gen"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
}

func TestOrdering_AnyChannelNotPenalizedWithoutEligibleACL(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	items, campaigns := orderFixture(now)
	items[2].Enabled = false // r-acl1
	items[3].Enabled = false // r-acl2
	ord := Ordering{Mode: domain.PriorityTopDown, Games: []string{"Rust", "Halo Infinite"}, PreferChannelSpecific: true}
	cands := EligibleCandidates(items, campaigns, nil, nil, ord, true, now)
	if got := candIDs(cands); !reflect.DeepEqual(got, []string{"r-gen", "h-gen"}) {
		t.Fatalf("order = %v", got)
	}
	if cands[0].Alongside {
		t.Fatal("r-gen has no eligible ACL sibling, so it must not be marked alongside")
	}
}

func TestAlongside(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	items, campaigns := orderFixture(now)
	got := Alongside(items, campaigns, nil, nil, true, now)
	if !got["r-gen"] || got["h-gen"] || got["r-acl1"] || got["r-acl2"] {
		t.Fatalf("alongside = %v, want only r-gen", got)
	}
	// Same game on another platform does not count.
	items[1].Platform = domain.PlatformKick
	if got := Alongside(items, campaigns, nil, nil, true, now); got["r-gen"] {
		t.Fatalf("cross-platform ACL drop must not mark alongside: %v", got)
	}
}

func TestGameRank(t *testing.T) {
	games := []string{"Rust", " Halo Infinite "}
	if GameRank(games, "halo infinite") != 1 || GameRank(games, "RUST") != 0 || GameRank(games, "Other") != 2 {
		t.Fatal("GameRank mismatch")
	}
}

func TestOrdering_CompletedOrOwnedACLDoesNotPushAnyChannelDown(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	items, campaigns := orderFixture(now)
	progress := map[string]domain.Progress{
		"r-acl1": {DropID: "r-acl1", RealMinutes: 120, RequiredMinutes: 120, Source: domain.SourceInventory},
		"r-acl2": {DropID: "r-acl2", RequiredMinutes: 60, Owned: true},
	}
	ord := Ordering{Mode: domain.PriorityTopDown, Games: []string{"Rust", "Halo Infinite"}, PreferChannelSpecific: true}
	cands := EligibleCandidates(items, campaigns, progress, nil, ord, true, now)
	if got := candIDs(cands); !reflect.DeepEqual(got, []string{"r-gen", "h-gen"}) {
		t.Fatalf("order = %v", got)
	}
	if cands[0].Alongside || Alongside(items, campaigns, progress, nil, true, now)["r-gen"] {
		t.Fatal("a completed or owned ACL drop must not mark r-gen alongside")
	}
}
