package service

import (
	"testing"

	"github.com/rake-pro/dropwarden/internal/config"
	"github.com/rake-pro/dropwarden/internal/domain"
	"github.com/rake-pro/dropwarden/internal/store"
)

func testBackend(t *testing.T) *Backend {
	t.Helper()
	st, err := store.Open(t.TempDir() + "/svc.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	cfg, _ := config.StarterDefaults()
	return NewBackend(st, nil, cfg, t.TempDir()+"/config.yaml")
}

func TestSettingsRoundTrip(t *testing.T) {
	b := testBackend(t)
	s := NewSettingsService(b)
	dto := s.Load()
	if dto.Volume != 0.01 || dto.AuthMode != "manual" {
		t.Fatalf("unexpected defaults: %+v", dto)
	}
	dto.Theme = "system"
	dto.Games = []string{"Rust"}
	dto.PriorityMode = "ending_soonest"
	if err := s.Save(dto); err != nil {
		t.Fatalf("save: %v", err)
	}
	reloaded, err := config.Load(b.ConfigPath)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded.Theme != "system" || reloaded.Queue.PriorityMode != "ending_soonest" || len(reloaded.Games) != 1 {
		t.Fatalf("not persisted: %+v", reloaded)
	}
}

func TestSettingsSaveRejectsInvalid(t *testing.T) {
	b := testBackend(t)
	s := NewSettingsService(b)
	dto := s.Load()
	dto.Volume = 0 // muted -> must be rejected by config validation
	if err := s.Save(dto); err == nil {
		t.Fatal("muted volume should be rejected on save")
	}
}

func TestQueueAndHistoryServices(t *testing.T) {
	b := testBackend(t)
	// Seed a campaign, queue item, claim, session.
	c := domain.Campaign{ID: "c1", GameName: "Rust", Status: domain.StatusActive,
		Drops: []domain.Drop{{ID: "d1", CampaignID: "c1", Name: "Skin", RequiredMinutes: 60}}}
	if err := b.Store.UpsertCampaign(c); err != nil {
		t.Fatal(err)
	}
	b.Store.UpsertQueueItem(domain.QueueItem{DropID: "d1", CampaignID: "c1", Enabled: true, Status: domain.ItemPending})
	b.Store.AppendClaim(domain.Claim{DropID: "d0", DropName: "Old", GameName: "Rust"})
	b.Store.AppendSession(domain.WatchSession{ID: "s1", DropID: "d0", StartMinutes: 0, EndMinutes: 60, Outcome: domain.OutcomeClaimed})

	q, err := NewQueueService(b).Get()
	if err != nil || len(q) != 1 || q[0].DropName != "Skin" || q[0].Game != "Rust" {
		t.Fatalf("queue dto wrong: %+v %v", q, err)
	}

	hs := NewHistoryService(b)
	claims, _ := hs.Claims()
	if len(claims) != 1 {
		t.Fatalf("claims: %+v", claims)
	}
	stats, _ := hs.Stats()
	if stats.DropsClaimed != 1 || stats.MinutesWatched != 60 || stats.SuccessRate != 100 {
		t.Fatalf("stats wrong: %+v", stats)
	}
}

func TestWatcherServiceNilEngine(t *testing.T) {
	b := testBackend(t)
	ws := NewWatcherService(b)
	if ws.Status().State != "idle" {
		t.Fatal("nil engine should report idle")
	}
	if err := ws.Start(); err == nil {
		t.Fatal("start with nil engine should error")
	}
}
