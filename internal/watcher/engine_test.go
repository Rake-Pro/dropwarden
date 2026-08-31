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
	bl, _ := st.IsBlacklisted(domain.PlatformTwitch, "deadchannel", e.deps.Now())
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
	if bl, _ := st.IsBlacklisted(domain.PlatformTwitch, "grabbys", now); bl {
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
	if bl, _ := st.IsBlacklisted(domain.PlatformTwitch, "grabbys", now); !bl {
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

func TestRunDiscoveringZeroStaggerRunsImmediately(t *testing.T) {
	// Profile 0 (or any single-profile install, where Deps.DiscoveryStagger is
	// never set) must see today's exact behavior: discovery fires with no
	// delay at all.
	st, _ := store.OpenMemory()
	defer st.Close()
	cfg, _ := config.StarterDefaults()
	disco := &fakeDisco{}
	e := New(Deps{
		Store: st, Ctrl: newFakeController(), Pages: &fakePager{}, Disco: disco,
		Cfg: cfg, Emit: newRecordingEmitter(), Log: zerolog.Nop(),
		Rand: func() float64 { return 0.5 },
	})

	start := time.Now()
	next, _ := e.runDiscovering(context.Background())
	if elapsed := time.Since(start); elapsed > 50*time.Millisecond {
		t.Fatalf("zero stagger should not delay discovery, took %s", elapsed)
	}
	if next != StateSelectingChannel {
		t.Fatalf("expected SelectingChannel, got %s", next)
	}
	if disco.calls != 1 {
		t.Fatalf("expected 1 discovery call, got %d", disco.calls)
	}
}

func TestRunDiscoveringAppliesStaggerOnceOnly(t *testing.T) {
	// A later-started engine's FIRST discovery is delayed by its stagger
	// offset; every subsequent discovery pass (e.g. after a cooldown) runs on
	// its normal schedule with no further stagger.
	st, _ := store.OpenMemory()
	defer st.Close()
	cfg, _ := config.StarterDefaults()
	disco := &fakeDisco{}
	const stagger = 30 * time.Millisecond
	e := New(Deps{
		Store: st, Ctrl: newFakeController(), Pages: &fakePager{}, Disco: disco,
		Cfg: cfg, Emit: newRecordingEmitter(), Log: zerolog.Nop(),
		Rand: func() float64 { return 0.5 }, DiscoveryStagger: stagger,
	})

	start := time.Now()
	e.runDiscovering(context.Background())
	if elapsed := time.Since(start); elapsed < stagger {
		t.Fatalf("first discovery should wait out the stagger, took %s want >= %s", elapsed, stagger)
	}
	if disco.calls != 1 {
		t.Fatalf("expected 1 discovery call after stagger, got %d", disco.calls)
	}

	start = time.Now()
	e.runDiscovering(context.Background())
	if elapsed := time.Since(start); elapsed >= stagger {
		t.Fatalf("second discovery must not re-apply the stagger, took %s", elapsed)
	}
	if disco.calls != 2 {
		t.Fatalf("expected 2 discovery calls, got %d", disco.calls)
	}
}

func TestCooldownPreservesEngineIdentity(t *testing.T) {
	st, _ := store.OpenMemory()
	defer st.Close()
	cfg, _ := config.StarterDefaults()
	e := New(Deps{
		Store: st, Ctrl: newFakeController(), Pages: &fakePager{}, Disco: &fakeDisco{},
		Cfg: cfg, Emit: newRecordingEmitter(), Log: zerolog.Nop(),
		Platform: domain.PlatformKick, ChannelHost: "kick.com", Profile: "greg",
		Rand: func() float64 { return 0.5 },
	})
	if got := e.Status(); got.Profile != "greg" || got.Platform != "kick" {
		t.Fatalf("engine identity not stamped at New: %+v", got)
	}
	e.setStatus(func(s *Status) { s.DropName = "Tier 1"; s.Current = 42 })

	// The cooldown reset wipes the watch fields wholesale; a wiped Profile would
	// credit this profile's minutes to another one.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	e.runCooldown(ctx)
	got := e.Status()
	if got.Profile != "greg" || got.Platform != "kick" {
		t.Fatalf("cooldown reset wiped engine identity: %+v", got)
	}
	if got.DropName != "" || got.Current != 0 {
		t.Fatalf("cooldown reset should clear the watch fields: %+v", got)
	}
}

func TestFuzzedBounds(t *testing.T) {
	st, _ := store.OpenMemory()
	defer st.Close()
	cfg, _ := config.StarterDefaults()
	cfg.Watcher.TimingJitter = 0.2
	const base = 100 * time.Second

	e := New(Deps{
		Store: st, Ctrl: newFakeController(), Pages: &fakePager{}, Disco: &fakeDisco{},
		Cfg: cfg, Emit: newRecordingEmitter(), Log: zerolog.Nop(),
		Rand: func() float64 { return 0 },
	})
	if got, want := e.fuzzed(base), base*8/10; got != want {
		t.Fatalf("rand=0 should give D*(1-f): got %s want %s", got, want)
	}

	e.deps.Rand = func() float64 { return 1 }
	if got, want := e.fuzzed(base), base*12/10; got != want {
		t.Fatalf("rand=1 should give D*(1+f): got %s want %s", got, want)
	}

	unjittered, _ := config.StarterDefaults()
	unjittered.Watcher.TimingJitter = 0
	e.deps.Cfg = unjittered
	if got := e.fuzzed(base); got != base {
		t.Fatalf("timing_jitter=0 should return D exactly: got %s want %s", got, base)
	}

	e.deps.Cfg = cfg
	if got := e.fuzzed(0); got != 0 {
		t.Fatalf("d=0 should pass through unchanged: got %s", got)
	}
	if got := e.fuzzed(-5 * time.Second); got != -5*time.Second {
		t.Fatalf("negative d should pass through unchanged: got %s", got)
	}
}

func TestPollProgressIntervalVariesWithJitterOn(t *testing.T) {
	st, _ := store.OpenMemory()
	defer st.Close()
	cfg, _ := config.StarterDefaults()
	cfg.Watcher.ProgressPoll = 60 * time.Second
	cfg.Watcher.TimingJitter = 0.2
	pager := &fakePager{currentDrop: &domain.Progress{
		DropID: "d1", RealMinutes: 1, RequiredMinutes: 60, Source: domain.SourceCurrentDrop,
	}}
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	rnd := 0.0
	e := New(Deps{
		Store: st, Ctrl: newFakeController(), Pages: pager, Disco: &fakeDisco{},
		Cfg: cfg, Emit: newRecordingEmitter(), Log: zerolog.Nop(),
		Now: func() time.Time { return now }, Rand: func() float64 { return rnd },
	})
	e.cur = &activeWatch{drop: domain.Drop{ID: "d1", RequiredMinutes: 60}}

	// First poll fires immediately (nextPollAt is the zero value); rand=0 fuzzes
	// the next interval down to 60s*0.8=48s.
	e.pollProgress(context.Background())
	due1 := now.Add(48 * time.Second)
	if !e.cur.nextPollAt.Equal(due1) {
		t.Fatalf("nextPollAt after first poll = %s want %s", e.cur.nextPollAt, due1)
	}

	// Not yet due: must not fetch again.
	now = due1.Add(-time.Second)
	pager.pollCalls = 0
	e.pollProgress(context.Background())
	if pager.pollCalls != 0 {
		t.Fatalf("poll fired before its fuzzed due time, calls=%d", pager.pollCalls)
	}

	// Due, with a different rand draw: the SECOND interval must differ from the
	// first (60s*1.2=72s vs 48s), proving each poll picks its own next interval.
	now = due1
	rnd = 1
	e.pollProgress(context.Background())
	due2 := now.Add(72 * time.Second)
	if !e.cur.nextPollAt.Equal(due2) {
		t.Fatalf("nextPollAt after second poll = %s want %s", e.cur.nextPollAt, due2)
	}
}

func TestPollProgressIntervalExactWhenJitterZero(t *testing.T) {
	st, _ := store.OpenMemory()
	defer st.Close()
	cfg, _ := config.StarterDefaults()
	cfg.Watcher.ProgressPoll = 60 * time.Second
	cfg.Watcher.TimingJitter = 0
	pager := &fakePager{currentDrop: &domain.Progress{
		DropID: "d1", RealMinutes: 1, RequiredMinutes: 60, Source: domain.SourceCurrentDrop,
	}}
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	e := New(Deps{
		Store: st, Ctrl: newFakeController(), Pages: pager, Disco: &fakeDisco{},
		Cfg: cfg, Emit: newRecordingEmitter(), Log: zerolog.Nop(),
		Now: func() time.Time { return now }, Rand: func() float64 { return 0 },
	})
	e.cur = &activeWatch{drop: domain.Drop{ID: "d1", RequiredMinutes: 60}}

	e.pollProgress(context.Background())
	want := now.Add(60 * time.Second)
	if !e.cur.nextPollAt.Equal(want) {
		t.Fatalf("timing_jitter=0 must reproduce the exact interval: nextPollAt = %s want %s", e.cur.nextPollAt, want)
	}
}

func TestMaxSessionTargetFuzzedOncePerSession(t *testing.T) {
	st, _ := store.OpenMemory()
	defer st.Close()
	seedCampaign(t, st, false)
	cfg, _ := config.StarterDefaults()
	cfg.Watcher.TimingJitter = 0.2
	cfg.Watcher.MaxSession = 4 * time.Hour
	fixed := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	pager := &fakePager{channels: []string{"streamer_a"}}
	// rand=1 fuzzes MaxSession up to 4h*1.2=4h48m, so the target only matches if
	// it was actually computed through fuzzed(), not a plain sessionStart+MaxSession.
	e := New(Deps{
		Store: st, Ctrl: newFakeController(), Pages: pager, Disco: &fakeDisco{},
		Cfg: cfg, Emit: newRecordingEmitter(), Log: zerolog.Nop(),
		Now: func() time.Time { return fixed }, Rand: func() float64 { return 1 },
	})

	if _, _ = e.runSelecting(context.Background()); e.cur == nil {
		t.Fatal("expected a channel selected")
	}
	want := fixed.Add(4*time.Hour + 48*time.Minute)
	if !e.cur.maxSessionAt.Equal(want) {
		t.Fatalf("maxSessionAt = %s want %s", e.cur.maxSessionAt, want)
	}

	// A mid-watch re-entry (beginWatching again on the SAME session, as happens
	// after a hard-stall refresh) must not redraw the target even though Rand
	// would now give a very different fuzz.
	e.deps.Rand = func() float64 { return 0 }
	if next, _ := e.beginWatching(context.Background()); next != StateWatching {
		t.Fatalf("expected Watching, got %s", next)
	}
	if !e.cur.maxSessionAt.Equal(want) {
		t.Fatalf("maxSessionAt changed on re-entry: got %s want %s", e.cur.maxSessionAt, want)
	}
}
