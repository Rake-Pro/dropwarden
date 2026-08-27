package watcher

import (
	"context"
	"testing"
	"time"

	"github.com/rake-pro/dropwarden/internal/config"
	"github.com/rake-pro/dropwarden/internal/discovery"
	"github.com/rake-pro/dropwarden/internal/domain"
	"github.com/rake-pro/dropwarden/internal/store"
	"github.com/rs/zerolog"
)

func testEngine(t *testing.T, st *store.Store, ctrl *fakeController, pager *fakePager, disco *fakeDisco, emit *recordingEmitter) *Engine {
	t.Helper()
	cfg, _ := config.StarterDefaults()
	fixed := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	return New(Deps{
		Store: st, Ctrl: ctrl, Pages: pager, Disco: disco, Cfg: cfg, Emit: emit,
		Log:  zerolog.Nop(),
		Now:  func() time.Time { return fixed },
		Rand: func() float64 { return 0.5 },
	})
}

func seedCampaign(t *testing.T, st *store.Store, accountLink bool) domain.Campaign {
	t.Helper()
	c := domain.Campaign{
		ID: "camp1", Name: "Rust Drops", GameName: "Rust", GameSlug: "rust",
		Status: domain.StatusActive, EndAt: time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC),
		Drops: []domain.Drop{{ID: "d1", CampaignID: "camp1", Name: "Tier 1", RequiredMinutes: 60}},
	}
	if accountLink {
		c.AccountLinkURL = "https://twitch.tv/settings/connections"
	}
	if err := st.UpsertCampaign(c); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertQueueItem(domain.QueueItem{DropID: "d1", CampaignID: "camp1", Position: 0, Enabled: true, Status: domain.ItemPending}); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestSelectingPicksChannel(t *testing.T) {
	st, _ := store.OpenMemory()
	defer st.Close()
	seedCampaign(t, st, false)
	ctrl := newFakeController()
	pager := &fakePager{channels: []string{"streamer_a", "streamer_b"}}
	e := testEngine(t, st, ctrl, pager, &fakeDisco{}, newRecordingEmitter())

	next, _ := e.runSelecting(context.Background())
	if next != StateOpening {
		t.Fatalf("expected Opening, got %s", next)
	}
	if e.cur == nil || e.cur.login != "streamer_a" {
		t.Fatalf("expected streamer_a selected, got %+v", e.cur)
	}
}

func TestSelectingAccountLinkSkipped(t *testing.T) {
	st, _ := store.OpenMemory()
	defer st.Close()
	seedCampaign(t, st, true) // requires account link
	ctrl := newFakeController()
	emit := newRecordingEmitter()
	e := testEngine(t, st, ctrl, &fakePager{channels: []string{"x"}}, &fakeDisco{}, emit)

	next, _ := e.runSelecting(context.Background())
	if next != StateCooldown {
		t.Fatalf("account-link drop should not be selected; got %s", next)
	}
	if emit.count(EvNeedsAction) == 0 {
		t.Fatal("expected needs_action event for account-link drop")
	}
	q, _ := st.GetQueue()
	if q[0].Status != domain.ItemNeedsAction {
		t.Fatalf("queue item should be needs_action, got %s", q[0].Status)
	}
}

func TestSelectingNoChannelGoesCooldown(t *testing.T) {
	st, _ := store.OpenMemory()
	defer st.Close()
	seedCampaign(t, st, false)
	e := testEngine(t, st, newFakeController(), &fakePager{channels: nil}, &fakeDisco{}, newRecordingEmitter())
	next, _ := e.runSelecting(context.Background())
	if next != StateCooldown {
		t.Fatalf("no live channel should cooldown, got %s", next)
	}
}

func TestDiscoveringEmitsAndAdvances(t *testing.T) {
	st, _ := store.OpenMemory()
	defer st.Close()
	emit := newRecordingEmitter()
	e := testEngine(t, st, newFakeController(), &fakePager{}, &fakeDisco{res: discovery.Result{Campaigns: 3}}, emit)
	e.paused = false
	next, _ := e.runDiscovering(context.Background())
	if next != StateSelectingChannel {
		t.Fatalf("expected SelectingChannel, got %s", next)
	}
	if emit.count(EvCampaigns) != 1 {
		t.Fatal("expected campaigns:updated event")
	}
}

func TestClaimVisibleRecordsClaim(t *testing.T) {
	st, _ := store.OpenMemory()
	defer st.Close()
	c := seedCampaign(t, st, false)
	ctrl := newFakeController()
	ctrl.claimVisible = true
	emit := newRecordingEmitter()
	e := testEngine(t, st, ctrl, &fakePager{}, &fakeDisco{}, emit)
	e.cur = &activeWatch{
		drop: c.Drops[0], campaign: c, login: "streamer_a",
		session:     domain.WatchSession{ID: "s1", DropID: "d1", ChannelLogin: "streamer_a", StartedAt: e.deps.Now()},
		lastMinutes: 60,
	}
	next, _ := e.runClaiming(context.Background())
	if next != StateCooldown {
		t.Fatalf("expected Cooldown after claim, got %s", next)
	}
	has, _ := st.HasClaim("d1")
	if !has {
		t.Fatal("expected claim recorded in store")
	}
	if emit.count(EvClaimed) != 1 {
		t.Fatal("expected drop:claimed event")
	}
	q, _ := st.GetQueue()
	if q[0].Status != domain.ItemClaimed {
		t.Fatalf("queue item should be claimed, got %s", q[0].Status)
	}
}

func TestClaimFailureDefers(t *testing.T) {
	st, _ := store.OpenMemory()
	defer st.Close()
	c := seedCampaign(t, st, false)
	ctrl := newFakeController() // claimVisible=false, claimInv=false
	e := testEngine(t, st, ctrl, &fakePager{}, &fakeDisco{}, newRecordingEmitter())
	e.cur = &activeWatch{drop: c.Drops[0], campaign: c, login: "s", session: domain.WatchSession{ID: "s1", DropID: "d1"}}
	next, _ := e.runClaiming(context.Background())
	if next != StateCooldown {
		t.Fatalf("expected Cooldown, got %s", next)
	}
	if has, _ := st.HasClaim("d1"); has {
		t.Fatal("no claim should be recorded on failure")
	}
	q, _ := st.GetQueue()
	if q[0].Status != domain.ItemUnavailable {
		t.Fatalf("failed claim should mark unavailable, got %s", q[0].Status)
	}
}

func TestFailChannelBlacklists(t *testing.T) {
	st, _ := store.OpenMemory()
	defer st.Close()
	c := seedCampaign(t, st, false)
	e := testEngine(t, st, newFakeController(), &fakePager{}, &fakeDisco{}, newRecordingEmitter())
	e.cur = &activeWatch{drop: c.Drops[0], campaign: c, login: "deadchannel"}
	next, _ := e.failChannel(domain.OutcomeAbortedOffline, "offline", 15*time.Minute)
	if next != StateSelectingChannel {
		t.Fatalf("expected reselect, got %s", next)
	}
	bl, _ := st.IsBlacklisted("deadchannel", e.deps.Now())
	if !bl {
		t.Fatal("expected channel blacklisted")
	}
}
