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
	if err := s.Save(dto); err != nil {
		t.Fatalf("save: %v", err)
	}
	pd := s.Profiles()[0]
	pd.Games = []string{"Rust"}
	pd.PriorityMode = "ending_soonest"
	if err := s.UpdateProfile(pd); err != nil {
		t.Fatalf("update profile: %v", err)
	}
	reloaded, err := config.Load(b.ConfigPath)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	eff, _ := reloaded.Effective(config.MainProfileName)
	if reloaded.Theme != "system" || eff.PriorityMode != "ending_soonest" || len(eff.Games) != 1 {
		t.Fatalf("not persisted: %+v / %+v", reloaded, eff)
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
	// Start/Stop/Login are lifecycle intents handed to the reconciler, not
	// engine calls: with none wired (this test backend) they are accepted
	// no-ops rather than errors - the runtime is what launches.
	if err := ws.Start(config.MainProfileName, domain.PlatformTwitch); err != nil {
		t.Fatalf("start should be accepted without a reconciler: %v", err)
	}
	if err := ws.Stop(config.MainProfileName, domain.PlatformTwitch); err != nil {
		t.Fatalf("stop should be accepted without a reconciler: %v", err)
	}
	if err := ws.Login(config.MainProfileName, domain.PlatformTwitch); err != nil {
		t.Fatalf("login should be accepted without a reconciler: %v", err)
	}
	// Skip still operates on a live engine, so it stays honest when there is none.
	if err := ws.SkipCurrent(config.MainProfileName, domain.PlatformTwitch); err == nil {
		t.Fatal("skip with nil engine should error")
	}
}

func TestUpdateProfileWritesTheWholeBlock(t *testing.T) {
	b := testBackend(t)
	ss := NewSettingsService(b)
	d := ss.Profiles()[0]
	d.DisplayName = "Main"
	d.Games = []string{"Rust", " Halo Infinite ", ""}
	d.AnyGame = false
	d.PriorityMode = "shortest_first"
	d.AutoEnqueueNew = false
	d.PreferChannelSpecific = false
	d.FallThrough = false
	d.SkipAccountLink = false
	d.KickEnabled = true
	if err := ss.UpdateProfile(d); err != nil {
		t.Fatalf("update: %v", err)
	}
	got := ss.Profiles()[0]
	if got.DisplayName != "Main" || len(got.Games) != 2 || got.Games[1] != "Halo Infinite" || got.PriorityMode != "shortest_first" ||
		got.AutoEnqueueNew || got.PreferChannelSpecific || got.FallThrough || got.SkipAccountLink || !got.KickEnabled {
		t.Fatalf("profile block not saved in full: %+v", got)
	}
	// The engines' copy carries it.
	rc, err := b.config().ProfileRuntimeConfig(config.MainProfileName)
	if err != nil {
		t.Fatal(err)
	}
	if rc.Queue.PriorityMode != "shortest_first" || !rc.Watcher.WaitForTop || rc.Claim.SkipAccountLink || !rc.Kick.Enabled {
		t.Fatalf("runtime copy missed the update: %+v wait=%v skip=%v kick=%v", rc.Queue, rc.Watcher.WaitForTop, rc.Claim.SkipAccountLink, rc.Kick.Enabled)
	}
	// Unknown profile is refused, nothing changes.
	d.Name = "nobody"
	if err := ss.UpdateProfile(d); err == nil {
		t.Fatal("unknown profile must be refused")
	}
}
