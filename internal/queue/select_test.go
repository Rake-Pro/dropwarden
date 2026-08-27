package queue

import (
	"testing"
	"time"

	"github.com/rake-pro/dropwarden/internal/domain"
)

func baseCampaign(id string, endAt time.Time, drops ...domain.Drop) domain.Campaign {
	return domain.Campaign{
		ID:       id,
		GameName: "Some Game",
		Status:   domain.StatusActive,
		EndAt:    endAt,
		Drops:    drops,
	}
}

func TestNextDrop_TopDown(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	future := now.Add(24 * time.Hour)
	camp := baseCampaign("c1", future,
		domain.Drop{ID: "d1", CampaignID: "c1", RequiredMinutes: 60},
		domain.Drop{ID: "d2", CampaignID: "c1", RequiredMinutes: 30},
	)
	items := []domain.QueueItem{
		{DropID: "d2", CampaignID: "c1", Position: 2, Enabled: true, Status: domain.ItemPending},
		{DropID: "d1", CampaignID: "c1", Position: 1, Enabled: true, Status: domain.ItemPending},
	}
	campaigns := map[string]domain.Campaign{"c1": camp}
	got, err := NextDrop(items, campaigns, nil, nil, domain.PriorityTopDown, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got == nil || got.Drop.ID != "d1" {
		t.Fatalf("expected d1 (position 1) first, got %+v", got)
	}
}

func TestNextDrop_EndingSoonest(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	soon := now.Add(1 * time.Hour)
	later := now.Add(48 * time.Hour)
	campSoon := baseCampaign("c1", soon, domain.Drop{ID: "d1", CampaignID: "c1", RequiredMinutes: 60})
	campLater := baseCampaign("c2", later, domain.Drop{ID: "d2", CampaignID: "c2", RequiredMinutes: 60})
	campNoEnd := baseCampaign("c3", time.Time{}, domain.Drop{ID: "d3", CampaignID: "c3", RequiredMinutes: 60})

	items := []domain.QueueItem{
		{DropID: "d3", CampaignID: "c3", Position: 1, Enabled: true, Status: domain.ItemPending},
		{DropID: "d2", CampaignID: "c2", Position: 2, Enabled: true, Status: domain.ItemPending},
		{DropID: "d1", CampaignID: "c1", Position: 3, Enabled: true, Status: domain.ItemPending},
	}
	campaigns := map[string]domain.Campaign{"c1": campSoon, "c2": campLater, "c3": campNoEnd}
	got, err := NextDrop(items, campaigns, nil, nil, domain.PriorityEndingSoonest, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got == nil || got.Drop.ID != "d1" {
		t.Fatalf("expected d1 (ends soonest), got %+v", got)
	}

	// Zero EndAt should sort last: remove c1/c2 to confirm d3 selectable at all,
	// but with all three present d3 (no end) must not win.
	if got.Drop.ID == "d3" {
		t.Fatalf("zero EndAt campaign should not win ending_soonest ordering")
	}
}

func TestNextDrop_ClosestToComplete(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	future := now.Add(24 * time.Hour)
	camp := baseCampaign("c1", future,
		domain.Drop{ID: "d1", CampaignID: "c1", RequiredMinutes: 100},
		domain.Drop{ID: "d2", CampaignID: "c1", RequiredMinutes: 100},
	)
	items := []domain.QueueItem{
		{DropID: "d1", CampaignID: "c1", Position: 1, Enabled: true, Status: domain.ItemPending},
		{DropID: "d2", CampaignID: "c1", Position: 2, Enabled: true, Status: domain.ItemPending},
	}
	campaigns := map[string]domain.Campaign{"c1": camp}
	progress := map[string]domain.Progress{
		// d1 has 90/100 done (10 remaining), d2 has no progress (100 remaining)
		"d1": {DropID: "d1", RealMinutes: 90, RequiredMinutes: 100, Source: domain.SourceInventory},
	}
	got, err := NextDrop(items, campaigns, progress, nil, domain.PriorityClosestToComplete, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got == nil || got.Drop.ID != "d1" {
		t.Fatalf("expected d1 (closest to complete), got %+v", got)
	}
}

func TestNextDrop_ShortestFirst(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	future := now.Add(24 * time.Hour)
	camp := baseCampaign("c1", future,
		domain.Drop{ID: "d1", CampaignID: "c1", RequiredMinutes: 120},
		domain.Drop{ID: "d2", CampaignID: "c1", RequiredMinutes: 30},
	)
	items := []domain.QueueItem{
		{DropID: "d1", CampaignID: "c1", Position: 1, Enabled: true, Status: domain.ItemPending},
		{DropID: "d2", CampaignID: "c1", Position: 2, Enabled: true, Status: domain.ItemPending},
	}
	campaigns := map[string]domain.Campaign{"c1": camp}
	got, err := NextDrop(items, campaigns, nil, nil, domain.PriorityShortestFirst, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got == nil || got.Drop.ID != "d2" {
		t.Fatalf("expected d2 (shortest), got %+v", got)
	}
}

func TestNextDrop_AccountLinkExcluded(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	future := now.Add(24 * time.Hour)
	camp := baseCampaign("c1", future, domain.Drop{ID: "d1", CampaignID: "c1", RequiredMinutes: 60})
	camp.AccountLinkURL = "https://example.com/link"
	items := []domain.QueueItem{{DropID: "d1", CampaignID: "c1", Position: 1, Enabled: true, Status: domain.ItemPending}}
	campaigns := map[string]domain.Campaign{"c1": camp}
	got, err := NextDrop(items, campaigns, nil, nil, domain.PriorityTopDown, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil candidate for account-link campaign, got %+v", got)
	}
}

func TestNextDrop_ExpiredCampaignExcluded(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	past := now.Add(-1 * time.Hour)
	camp := baseCampaign("c1", past, domain.Drop{ID: "d1", CampaignID: "c1", RequiredMinutes: 60})
	items := []domain.QueueItem{{DropID: "d1", CampaignID: "c1", Position: 1, Enabled: true, Status: domain.ItemPending}}
	campaigns := map[string]domain.Campaign{"c1": camp}
	got, err := NextDrop(items, campaigns, nil, nil, domain.PriorityTopDown, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil candidate for expired campaign, got %+v", got)
	}
}

func TestNextDrop_PreconditionUnmetSkipped(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	future := now.Add(24 * time.Hour)
	camp := baseCampaign("c1", future,
		domain.Drop{ID: "d1", CampaignID: "c1", RequiredMinutes: 60},
		domain.Drop{ID: "d2", CampaignID: "c1", RequiredMinutes: 30, PreconditionIDs: []string{"d1"}},
	)
	items := []domain.QueueItem{
		{DropID: "d2", CampaignID: "c1", Position: 1, Enabled: true, Status: domain.ItemPending},
	}
	campaigns := map[string]domain.Campaign{"c1": camp}
	got, err := NextDrop(items, campaigns, nil, map[string]bool{}, domain.PriorityTopDown, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil candidate for unmet precondition, got %+v", got)
	}

	// Once claimed, it should become eligible.
	got, err = NextDrop(items, campaigns, nil, map[string]bool{"d1": true}, domain.PriorityTopDown, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got == nil || got.Drop.ID != "d2" {
		t.Fatalf("expected d2 eligible once precondition claimed, got %+v", got)
	}
}

func TestNextDrop_CompletedOrClaimedSkipped(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	future := now.Add(24 * time.Hour)
	camp := baseCampaign("c1", future,
		domain.Drop{ID: "d1", CampaignID: "c1", RequiredMinutes: 60},
		domain.Drop{ID: "d2", CampaignID: "c1", RequiredMinutes: 60},
	)
	items := []domain.QueueItem{
		{DropID: "d1", CampaignID: "c1", Position: 1, Enabled: true, Status: domain.ItemPending},
		{DropID: "d2", CampaignID: "c1", Position: 2, Enabled: true, Status: domain.ItemPending},
	}
	campaigns := map[string]domain.Campaign{"c1": camp}
	progress := map[string]domain.Progress{
		"d1": {DropID: "d1", RealMinutes: 60, RequiredMinutes: 60, Source: domain.SourceInventory},
	}
	claimed := map[string]bool{"d2": true}
	got, err := NextDrop(items, campaigns, progress, claimed, domain.PriorityTopDown, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil candidate, both complete/claimed, got %+v", got)
	}
}

func TestNextDrop_DisabledSkipped(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	future := now.Add(24 * time.Hour)
	camp := baseCampaign("c1", future, domain.Drop{ID: "d1", CampaignID: "c1", RequiredMinutes: 60})
	items := []domain.QueueItem{
		{DropID: "d1", CampaignID: "c1", Position: 1, Enabled: false, Status: domain.ItemPending},
	}
	campaigns := map[string]domain.Campaign{"c1": camp}
	got, err := NextDrop(items, campaigns, nil, nil, domain.PriorityTopDown, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil candidate for disabled item, got %+v", got)
	}
}

func TestNextDrop_Empty(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	got, err := NextDrop(nil, nil, nil, nil, domain.PriorityTopDown, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil candidate for empty input, got %+v", got)
	}
}

func TestEligibilityReason(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	future := now.Add(24 * time.Hour)
	past := now.Add(-1 * time.Hour)

	drop1 := domain.Drop{ID: "d1", CampaignID: "c1", RequiredMinutes: 60}
	dropWithPre := domain.Drop{ID: "d2", CampaignID: "c1", RequiredMinutes: 60, PreconditionIDs: []string{"d1"}}

	tests := []struct {
		name       string
		item       domain.QueueItem
		campaigns  map[string]domain.Campaign
		progress   map[string]domain.Progress
		claimed    map[string]bool
		wantElig   bool
		wantStatus domain.ItemStatus
	}{
		{
			name:       "disabled",
			item:       domain.QueueItem{DropID: "d1", CampaignID: "c1", Enabled: false},
			campaigns:  map[string]domain.Campaign{"c1": baseCampaign("c1", future, drop1)},
			wantElig:   false,
			wantStatus: domain.ItemDisabled,
		},
		{
			name:       "expired campaign",
			item:       domain.QueueItem{DropID: "d1", CampaignID: "c1", Enabled: true},
			campaigns:  map[string]domain.Campaign{"c1": baseCampaign("c1", past, drop1)},
			wantElig:   false,
			wantStatus: domain.ItemExpired,
		},
		{
			name: "account link required",
			item: domain.QueueItem{DropID: "d1", CampaignID: "c1", Enabled: true},
			campaigns: map[string]domain.Campaign{"c1": func() domain.Campaign {
				c := baseCampaign("c1", future, drop1)
				c.AccountLinkURL = "https://x"
				return c
			}()},
			wantElig:   false,
			wantStatus: domain.ItemNeedsAction,
		},
		{
			name:       "precondition unmet",
			item:       domain.QueueItem{DropID: "d2", CampaignID: "c1", Enabled: true},
			campaigns:  map[string]domain.Campaign{"c1": baseCampaign("c1", future, drop1, dropWithPre)},
			claimed:    map[string]bool{},
			wantElig:   false,
			wantStatus: domain.ItemBlocked,
		},
		{
			name:       "already claimed",
			item:       domain.QueueItem{DropID: "d1", CampaignID: "c1", Enabled: true},
			campaigns:  map[string]domain.Campaign{"c1": baseCampaign("c1", future, drop1)},
			claimed:    map[string]bool{"d1": true},
			wantElig:   false,
			wantStatus: domain.ItemClaimed,
		},
		{
			name:       "complete via progress",
			item:       domain.QueueItem{DropID: "d1", CampaignID: "c1", Enabled: true},
			campaigns:  map[string]domain.Campaign{"c1": baseCampaign("c1", future, drop1)},
			progress:   map[string]domain.Progress{"d1": {DropID: "d1", RealMinutes: 60, RequiredMinutes: 60, Source: domain.SourceInventory}},
			wantElig:   false,
			wantStatus: domain.ItemClaimed,
		},
		{
			name:       "eligible",
			item:       domain.QueueItem{DropID: "d1", CampaignID: "c1", Enabled: true},
			campaigns:  map[string]domain.Campaign{"c1": baseCampaign("c1", future, drop1)},
			wantElig:   true,
			wantStatus: domain.ItemPending,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			elig, status, reason := EligibilityReason(tt.item, tt.campaigns, tt.progress, tt.claimed, now)
			if elig != tt.wantElig {
				t.Errorf("eligible: got %v, want %v", elig, tt.wantElig)
			}
			if status != tt.wantStatus {
				t.Errorf("status: got %v, want %v", status, tt.wantStatus)
			}
			if tt.wantElig && reason != "" {
				t.Errorf("expected empty reason when eligible, got %q", reason)
			}
			if !tt.wantElig && reason == "" {
				t.Errorf("expected non-empty reason when not eligible")
			}
		})
	}
}
