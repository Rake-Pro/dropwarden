package service

import (
	"testing"
	"time"

	"github.com/rake-pro/dropwarden/internal/config"
	"github.com/rake-pro/dropwarden/internal/domain"
	"github.com/rake-pro/dropwarden/internal/store"
)

func testBackend(t *testing.T) *Backend {
	t.Helper()
	// Keep every path resolution (profile dirs, DB) inside the test tree.
	t.Setenv("DROPWARDEN_HOME", t.TempDir())
	st, err := store.Open(t.TempDir() + "/svc.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	cfg, _ := config.StarterDefaults()
	return NewBackend(cfg, t.TempDir()+"/config.yaml", NewProfileBackend(cfg, config.MainProfileName, st))
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
	// The synthesized main profile is written back on the first save, so the
	// file stops relying on legacy synthesis.
	if len(reloaded.Profiles) != 1 || reloaded.Profiles[0].Name != config.MainProfileName {
		t.Fatalf("expected the main profile block written back: %+v", reloaded.Profiles)
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
	if err := b.FirstProfile().Store.UpsertCampaign(c); err != nil {
		t.Fatal(err)
	}
	b.FirstProfile().Store.UpsertQueueItem(domain.QueueItem{DropID: "d1", CampaignID: "c1", Enabled: true, Status: domain.ItemPending})
	b.FirstProfile().Store.AppendClaim(domain.Claim{DropID: "d0", DropName: "Old", GameName: "Rust"})
	// MinutesWatched totals are wall-clock (StartedAt..EndedAt), not the
	// per-drop credited delta - simultaneous drops credit in parallel.
	started := time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC)
	b.FirstProfile().Store.AppendSession(domain.WatchSession{
		ID: "s1", DropID: "d0", StartedAt: started, EndedAt: started.Add(60 * time.Minute),
		StartMinutes: 0, EndMinutes: 45, Outcome: domain.OutcomeClaimed,
	})

	q, err := NewQueueService(b).Get(config.MainProfileName)
	if err != nil || len(q) != 1 || q[0].DropName != "Skin" || q[0].Game != "Rust" {
		t.Fatalf("queue dto wrong: %+v %v", q, err)
	}

	hs := NewHistoryService(b)
	claims, _ := hs.Claims(config.MainProfileName)
	if len(claims) != 1 {
		t.Fatalf("claims: %+v", claims)
	}
	stats, _ := hs.Stats(config.MainProfileName)
	if stats.DropsClaimed != 1 || stats.MinutesWatched != 60 || stats.SuccessRate != 100 {
		t.Fatalf("stats wrong: %+v", stats)
	}
}

func TestWatcherServiceNilEngine(t *testing.T) {
	b := testBackend(t)
	ws := NewWatcherService(b)
	if ws.Status(config.MainProfileName, domain.PlatformTwitch).State != "idle" {
		t.Fatal("nil engine should report idle")
	}
	if err := ws.Start(config.MainProfileName, domain.PlatformTwitch); err == nil {
		t.Fatal("start with nil engine should error")
	}
}

func TestUpdateProfileKeepsKickInheritance(t *testing.T) {
	b := testBackend(t)
	ss := NewSettingsService(b)
	// The pane shows the RESOLVED kick value; saving it back unchanged (off,
	// matching the root default) must not pin an override, or the shared
	// Platforms-pane toggle silently stops affecting the profile.
	if err := ss.UpdateProfile(config.MainProfileName, "Main", false, nil, true); err != nil {
		t.Fatalf("update: %v", err)
	}
	if p := b.config().Profile(config.MainProfileName); p.Kick.Enabled != nil {
		t.Fatalf("expected kick to stay inherited, got override %v", *p.Kick.Enabled)
	}
	// A value differing from the root default pins an override.
	if err := ss.UpdateProfile(config.MainProfileName, "Main", true, nil, true); err != nil {
		t.Fatalf("update: %v", err)
	}
	p := b.config().Profile(config.MainProfileName)
	if p.Kick.Enabled == nil || !*p.Kick.Enabled {
		t.Fatalf("expected pinned kick override true, got %+v", p.Kick.Enabled)
	}
}
