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

func TestClaimFailureFlagsForManualReconcile(t *testing.T) {
	st, _ := store.OpenMemory()
	defer st.Close()
	c := seedCampaign(t, st, false)
	ctrl := newFakeController() // claimVisible=false, claimInv=false
	emit := newRecordingEmitter()
	e := testEngine(t, st, ctrl, &fakePager{}, &fakeDisco{}, emit)
	e.cur = &activeWatch{drop: c.Drops[0], campaign: c, login: "s", session: domain.WatchSession{ID: "s1", DropID: "d1"}}
	next, _ := e.runClaiming(context.Background())
	if next != StateCooldown {
		t.Fatalf("expected Cooldown, got %s", next)
	}
	if has, _ := st.HasClaim("d1"); has {
		t.Fatal("no claim should be recorded on failure")
	}
	q, _ := st.GetQueue()
	if q[0].Status != domain.ItemNeedsAction {
		t.Fatalf("failed claim should mark needs_action for manual reconcile, got %s", q[0].Status)
	}
	if emit.count(EvNeedsAction) != 1 {
		t.Fatal("failed claim should emit a needs_action event for the UI")
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

func TestPollProgressUpdatesAndRateLimits(t *testing.T) {
	st, _ := store.OpenMemory()
	defer st.Close()
	pager := &fakePager{currentDrop: &domain.Progress{
		DropID: "d1", RealMinutes: 7, RequiredMinutes: 60, Source: domain.SourceCurrentDrop,
	}}
	emit := newRecordingEmitter()
	e := testEngine(t, st, newFakeController(), pager, &fakeDisco{}, emit)
	e.cur = &activeWatch{drop: domain.Drop{ID: "d1", RequiredMinutes: 60}}

	m, inc := e.pollProgress(context.Background())
	if !inc || m != 7 {
		t.Fatalf("pollProgress = %d,%v want 7,true", m, inc)
	}
	if got := e.Status().Current; got != 7 {
		t.Fatalf("status current = %d want 7", got)
	}
	if emit.count(EvProgress) != 1 {
		t.Fatalf("expected 1 progress event, got %d", emit.count(EvProgress))
	}

	// Same instant: the interval gate must skip the fetch entirely.
	pager.currentDrop.RealMinutes = 20
	m, inc = e.pollProgress(context.Background())
	if inc || m != 7 || pager.pollCalls != 1 {
		t.Fatalf("expected rate-limited poll (m=7, 1 call), got m=%d inc=%v calls=%d", m, inc, pager.pollCalls)
	}
}

func TestPollProgressStoresButDoesNotCreditOtherDrop(t *testing.T) {
	st, _ := store.OpenMemory()
	defer st.Close()
	pager := &fakePager{currentDrop: &domain.Progress{
		DropID: "other", RealMinutes: 30, RequiredMinutes: 60, Source: domain.SourceCurrentDrop,
	}}
	e := testEngine(t, st, newFakeController(), pager, &fakeDisco{}, newRecordingEmitter())
	e.cur = &activeWatch{drop: domain.Drop{ID: "d1", RequiredMinutes: 60}}

	if m, inc := e.pollProgress(context.Background()); inc || m != 0 {
		t.Fatalf("progress for another drop must not credit ours; got %d,%v", m, inc)
	}
	if p, _ := st.GetProgress("other"); p == nil || p.RealMinutes != 30 {
		t.Fatalf("other drop's progress should still be stored, got %+v", p)
	}
}

func TestPollProgressCreditsActiveDropFromInventory(t *testing.T) {
	// Simultaneous multi-tier campaigns: dropCurrentSession names another tier,
	// but the inventory carries per-drop minutes including the one we watch.
	st, _ := store.OpenMemory()
	defer st.Close()
	pager := &fakePager{
		currentDrop: &domain.Progress{DropID: "tier2", RealMinutes: 18, RequiredMinutes: 120, Source: domain.SourceCurrentDrop},
		inventory: []domain.Progress{
			{DropID: "d1", RealMinutes: 18, RequiredMinutes: 60, Source: domain.SourceInventory},
			{DropID: "tier2", RealMinutes: 18, RequiredMinutes: 120, Source: domain.SourceInventory},
		},
	}
	e := testEngine(t, st, newFakeController(), pager, &fakeDisco{}, newRecordingEmitter())
	e.cur = &activeWatch{drop: domain.Drop{ID: "d1", RequiredMinutes: 60}}

	m, inc := e.pollProgress(context.Background())
	if !inc || m != 18 {
		t.Fatalf("inventory minutes should credit the active drop; got %d,%v", m, inc)
	}
	if got := e.Status().Current; got != 18 {
		t.Fatalf("status current = %d want 18", got)
	}
}

func TestHardStallCreditedElsewhereDoesNotBlacklist(t *testing.T) {
	st, _ := store.OpenMemory()
	defer st.Close()
	e := testEngine(t, st, newFakeController(), &fakePager{}, &fakeDisco{}, newRecordingEmitter())
	now := e.deps.Now()
	e.cur = &activeWatch{
		drop:          domain.Drop{ID: "d1", RequiredMinutes: 60},
		login:         "grabbys",
		creditChannel: "phone_session_channel",
		lastProgress:  now.Add(-time.Hour),
		hardRefreshes: 99, // past max retries
		session:       domain.WatchSession{ID: "s1", DropID: "d1"},
	}
	next, _, done := e.stallCheck(context.Background(), now, now.Add(time.Hour))
	if !done || next != StateCooldown {
		t.Fatalf("expected cooldown stand-down, got %s done=%v", next, done)
	}
	if bl, _ := st.IsBlacklisted("grabbys", now); bl {
		t.Fatal("channel must not be blacklisted when another session holds the drop slot")
	}
}

func TestHardStallSameChannelStillBlacklists(t *testing.T) {
	st, _ := store.OpenMemory()
	defer st.Close()
	e := testEngine(t, st, newFakeController(), &fakePager{}, &fakeDisco{}, newRecordingEmitter())
	now := e.deps.Now()
	e.cur = &activeWatch{
		drop:          domain.Drop{ID: "d1", RequiredMinutes: 60},
		login:         "grabbys",
		creditChannel: "grabbys", // Twitch credits us, yet minutes never moved
		lastProgress:  now.Add(-time.Hour),
		hardRefreshes: 99,
		session:       domain.WatchSession{ID: "s1", DropID: "d1"},
	}
	next, _, done := e.stallCheck(context.Background(), now, now.Add(time.Hour))
	if !done || next != StateSelectingChannel {
		t.Fatalf("expected reselect after blacklist, got %s done=%v", next, done)
	}
	if bl, _ := st.IsBlacklisted("grabbys", now); !bl {
		t.Fatal("stalled channel that IS our credited session should be blacklisted")
	}
}

func TestBeginWatchingContinuesSessionAcrossRevalidation(t *testing.T) {
	st, _ := store.OpenMemory()
	defer st.Close()
	c := seedCampaign(t, st, false)
	e := testEngine(t, st, newFakeController(), &fakePager{}, &fakeDisco{}, newRecordingEmitter())
	e.cur = &activeWatch{drop: c.Drops[0], campaign: c, login: "streamer_a"}

	if next, _ := e.beginWatching(context.Background()); next != StateWatching {
		t.Fatalf("expected Watching, got %s", next)
	}
	first := e.cur.session
	if first.ID == "" || e.Status().SessionStartedAt.IsZero() {
		t.Fatal("fresh watch must mint a session and stamp SessionStartedAt")
	}

	// Mid-watch refresh re-validates and re-enters; the session must survive.
	if next, _ := e.beginWatching(context.Background()); next != StateWatching {
		t.Fatalf("expected Watching, got %s", next)
	}
	if e.cur.session.ID != first.ID || !e.cur.session.StartedAt.Equal(first.StartedAt) {
		t.Fatalf("revalidation must not mint a new session: %+v vs %+v", e.cur.session, first)
	}
}
