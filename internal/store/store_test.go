package store

import (
	"testing"
	"time"

	"github.com/rake-pro/dropwarden/internal/domain"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestCampaignRoundTrip(t *testing.T) {
	s := newTestStore(t)
	c := domain.Campaign{
		ID: "camp1", Name: "Rust Twitch Drops", GameName: "Rust", GameSlug: "rust",
		Status: domain.StatusActive, EndAt: time.Now().Add(48 * time.Hour),
		AllowedChannels: []domain.Channel{{Login: "shroud"}},
		Drops: []domain.Drop{
			{ID: "d1", CampaignID: "camp1", Name: "Tier 1", RequiredMinutes: 60,
				Benefits: []domain.Benefit{{ID: "b1", Name: "Skin"}}},
			{ID: "d2", CampaignID: "camp1", Name: "Tier 2", RequiredMinutes: 120, PreconditionIDs: []string{"d1"}},
		},
	}
	if err := s.UpsertCampaign(c); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	got, err := s.GetCampaign("camp1")
	if err != nil || got == nil {
		t.Fatalf("get: %v %v", got, err)
	}
	if got.GameName != "Rust" || len(got.Drops) != 2 {
		t.Fatalf("bad campaign: %+v", got)
	}
	if len(got.AllowedChannels) != 1 || got.AllowedChannels[0].Login != "shroud" {
		t.Fatalf("bad ACL: %+v", got.AllowedChannels)
	}
	// drops ordered by required_minutes
	if got.Drops[1].ID != "d2" || len(got.Drops[1].PreconditionIDs) != 1 {
		t.Fatalf("bad drop/preconditions: %+v", got.Drops[1])
	}
}

func TestListUpsertPreservesDetails(t *testing.T) {
	s := newTestStore(t)
	// Detailed campaign: has slug, ACL, drops.
	detailed := domain.Campaign{
		ID: "c1", Name: "NBPL", GameName: "NARAKA", GameSlug: "naraka",
		Status: domain.StatusActive, AccountLinkURL: "https://link",
		AllowedChannels: []domain.Channel{{Login: "chan1"}, {Login: "chan2"}},
		Drops:           []domain.Drop{{ID: "d1", CampaignID: "c1", RequiredMinutes: 60}},
	}
	if err := s.UpsertCampaign(detailed); err != nil {
		t.Fatal(err)
	}
	// List-style re-upsert: same id, no slug/ACL/link/drops.
	listOnly := domain.Campaign{ID: "c1", Name: "NBPL", GameName: "NARAKA", Status: domain.StatusActive}
	if err := s.UpsertCampaign(listOnly); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetCampaign("c1")
	if got == nil {
		t.Fatal("campaign missing")
	}
	if got.GameSlug != "naraka" {
		t.Fatalf("slug clobbered: %q", got.GameSlug)
	}
	if len(got.AllowedChannels) != 2 {
		t.Fatalf("ACL clobbered: %+v", got.AllowedChannels)
	}
	if got.AccountLinkURL != "https://link" {
		t.Fatalf("account link clobbered: %q", got.AccountLinkURL)
	}
	if len(got.Drops) != 1 {
		t.Fatalf("drops clobbered: %+v", got.Drops)
	}
}

func TestProgressRoundTrip(t *testing.T) {
	s := newTestStore(t)
	p := domain.Progress{DropID: "d1", RealMinutes: 30, RequiredMinutes: 60, Source: domain.SourceInventory, ObservedAt: time.Now()}
	if err := s.SaveProgress(p); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := s.GetProgress("d1")
	if err != nil || got == nil {
		t.Fatalf("get: %v %v", got, err)
	}
	if got.RealMinutes != 30 || got.Source != domain.SourceInventory {
		t.Fatalf("bad progress: %+v", got)
	}
	if none, _ := s.GetProgress("nope"); none != nil {
		t.Fatal("expected nil for missing progress")
	}
}

func TestQueueOrderAndStatus(t *testing.T) {
	s := newTestStore(t)
	for i, id := range []string{"a", "b", "c"} {
		if err := s.UpsertQueueItem(domain.QueueItem{DropID: id, Position: i, Enabled: true, Status: domain.ItemPending}); err != nil {
			t.Fatalf("upsert %s: %v", id, err)
		}
	}
	if err := s.SetQueueOrder([]string{"c", "a", "b"}); err != nil {
		t.Fatalf("reorder: %v", err)
	}
	q, err := s.GetQueue()
	if err != nil {
		t.Fatalf("get queue: %v", err)
	}
	if q[0].DropID != "c" || q[1].DropID != "a" || q[2].DropID != "b" {
		t.Fatalf("bad order: %v %v %v", q[0].DropID, q[1].DropID, q[2].DropID)
	}
	if err := s.SetItemStatus("c", domain.ItemNeedsAction, "account link"); err != nil {
		t.Fatalf("status: %v", err)
	}
	q, _ = s.GetQueue()
	if q[0].Status != domain.ItemNeedsAction || q[0].Reason != "account link" {
		t.Fatalf("status not applied: %+v", q[0])
	}
}

func TestPruneQueue(t *testing.T) {
	s := newTestStore(t)
	for _, id := range []string{"a", "b", "c"} {
		s.UpsertQueueItem(domain.QueueItem{DropID: id, Enabled: true, Status: domain.ItemPending})
	}
	removed, err := s.PruneQueue(map[string]bool{"b": true})
	if err != nil {
		t.Fatal(err)
	}
	if removed != 2 {
		t.Fatalf("expected 2 removed, got %d", removed)
	}
	q, _ := s.GetQueue()
	if len(q) != 1 || q[0].DropID != "b" {
		t.Fatalf("expected only b to remain: %+v", q)
	}
}

func TestBlacklistExpiry(t *testing.T) {
	s := newTestStore(t)
	now := time.Now()
	if err := s.BlacklistChannel("shroud", "stall", now.Add(10*time.Minute)); err != nil {
		t.Fatalf("blacklist: %v", err)
	}
	if bl, _ := s.IsBlacklisted("shroud", now); !bl {
		t.Fatal("should be blacklisted now")
	}
	if bl, _ := s.IsBlacklisted("shroud", now.Add(11*time.Minute)); bl {
		t.Fatal("should have expired")
	}
	if bl, _ := s.IsBlacklisted("other", now); bl {
		t.Fatal("unknown channel not blacklisted")
	}
}

func TestClaimsAndSessions(t *testing.T) {
	s := newTestStore(t)
	if err := s.AppendClaim(domain.Claim{DropID: "d1", DropName: "Skin", GameName: "Rust", ClaimedAt: time.Now()}); err != nil {
		t.Fatalf("claim: %v", err)
	}
	has, _ := s.HasClaim("d1")
	if !has {
		t.Fatal("expected claim recorded")
	}
	claims, _ := s.ListClaims(time.Time{}, time.Time{})
	if len(claims) != 1 || claims[0].DropName != "Skin" {
		t.Fatalf("bad claims: %+v", claims)
	}
	ws := domain.WatchSession{ID: "s1", DropID: "d1", ChannelLogin: "shroud",
		StartedAt: time.Now(), EndedAt: time.Now(), StartMinutes: 0, EndMinutes: 60, Outcome: domain.OutcomeClaimed}
	if err := s.AppendSession(ws); err != nil {
		t.Fatalf("session: %v", err)
	}
	sessions, _ := s.ListSessions(10)
	if len(sessions) != 1 || sessions[0].MinutesWatched() != 60 {
		t.Fatalf("bad sessions: %+v", sessions)
	}
}
