package watcher

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/rake-pro/dropwarden/internal/browser"
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
	e := New(Deps{
		Store: st, Ctrl: ctrl, Pages: pager, Disco: disco, Cfg: cfg, Emit: emit,
		Log:  zerolog.Nop(),
		Now:  func() time.Time { return fixed },
		Rand: func() float64 { return 0.5 },
	})
	e.wait = func(context.Context, time.Duration) {}
	return e
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
	e.refreshReq = true // force the pass: discovery.interval would skip it otherwise
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

func TestValidationPassIgnoresDropsIndicator(t *testing.T) {
	// Live finding 2026-08-31: every healthy channel failed validation solely
	// on drops_indicator=false (stale selector), wedging a reselect loop. The
	// indicator must stay advisory: playback health alone passes the gate.
	if !validationPass(true, true, true, true) {
		t.Fatal("healthy playback must pass validation regardless of the drops indicator")
	}
	for i := 0; i < 4; i++ {
		gates := []bool{true, true, true, true}
		gates[i] = false
		if validationPass(gates[0], gates[1], gates[2], gates[3]) {
			t.Fatalf("gate %d false must fail validation", i)
		}
	}
}

func TestLoginFromPathRejectsNonChannelURLs(t *testing.T) {
	// Live 2026-08-31: the tab's buffered creation navigation to about:blank
	// parsed as login "about:blank", an any-channel campaign deemed it
	// "eligible", and the engine adopted it as a raid target off a healthy
	// channel. Anything that is not a plausible channel login must parse to "".
	for _, bad := range []string{
		"about:blank",
		"chrome-error://chromewebdata/",
		"data:text/html,hi",
		"https://example.com/somelogin",
		"https://www.youtube.com/watch?v=x",
	} {
		if got := loginFromPath("twitch.tv", bad); got != "" {
			t.Fatalf("loginFromPath(%q) = %q, want empty", bad, got)
		}
	}
	for in, want := range map[string]string{
		"https://www.twitch.tv/SomeLogin?x=1": "somelogin",
		"/another_login/videos":               "another_login",
		"twitch.tv/abc":                       "abc",
	} {
		if got := loginFromPath("twitch.tv", in); got != want {
			t.Fatalf("loginFromPath(%q) = %q, want %q", in, got, want)
		}
	}
	if got := loginFromPath("kick.com", "https://kick.com/some-streamer"); got != "some-streamer" {
		t.Fatalf("kick channel parse = %q", got)
	}
}

func TestDrainEventsEmptiesBacklog(t *testing.T) {
	ch := make(chan browser.Event, 8)
	ch <- browser.Event{Kind: browser.EventNavigated, URL: "about:blank"}
	ch <- browser.Event{Kind: browser.EventNavigated, URL: "https://www.twitch.tv/x"}
	if n := drainEvents(ch); n != 2 {
		t.Fatalf("expected 2 drained events, got %d", n)
	}
	if n := drainEvents(ch); n != 0 {
		t.Fatalf("a drained channel should yield 0, got %d", n)
	}
}

func TestHardStallNoCreditGivesUpOnDrop(t *testing.T) {
	// Live 2026-08-31: watching a participating channel that Twitch never opens
	// a crediting session for (creditChannel empty) with zero minutes gained
	// means the drop is un-earnable (reward already owned, etc.). The engine
	// must give up on the DROP (needs_action + skip it this session), not
	// blacklist channels forever.
	st, _ := store.OpenMemory()
	defer st.Close()
	emit := newRecordingEmitter()
	e := testEngine(t, st, newFakeController(), &fakePager{}, &fakeDisco{}, emit)
	now := e.deps.Now()
	e.cur = &activeWatch{
		drop:          domain.Drop{ID: "d1", Name: "DECAL", RequiredMinutes: 60},
		login:         "lilbit_007",
		creditChannel: "", // Twitch never opened a session for us
		startMinutes:  0,
		lastMinutes:   0, // zero credit for the whole watch
		lastProgress:  now.Add(-time.Hour),
		hardRefreshes: 99,
		session:       domain.WatchSession{ID: "s1", DropID: "d1"},
	}
	next, _, done := e.stallCheck(context.Background(), now, now.Add(time.Hour))
	if !done || next != StateCooldown {
		t.Fatalf("expected give-up to cooldown, got %s done=%v", next, done)
	}
	if !e.noCreditDrops["d1"] {
		t.Fatal("the un-earnable drop must be flagged no-credit so selection skips it")
	}
	if emit.count(EvNeedsAction) == 0 {
		t.Fatal("a non-crediting drop must surface as needs_action")
	}
	if bl, _ := st.IsBlacklisted(domain.PlatformTwitch, "lilbit_007", now); bl {
		t.Fatal("give-up is a DROP problem, not a channel one; the channel must not be blacklisted")
	}
}

// An ACL campaign must pick the allowlisted channel that is live NOW, not the
// first name in the allowlist (live 2026-09-20: an offline channel's VOD
// autoplay passed validation and the drop was misfiled as already owned).
func TestSelectingACLPrefersLiveChannel(t *testing.T) {
	st, _ := store.OpenMemory()
	defer st.Close()
	c := seedCampaign(t, st, false)
	c.AllowedChannels = []domain.Channel{{Login: "europeanhalo"}, {Login: "lvthalo"}}
	if err := st.UpsertCampaign(c); err != nil {
		t.Fatal(err)
	}
	pager := &fakePager{live: []domain.Channel{{Login: "lvthalo", Live: true, ViewerCount: 900}}}
	e := testEngine(t, st, newFakeController(), pager, &fakeDisco{}, newRecordingEmitter())

	next, _ := e.runSelecting(context.Background())
	if next != StateOpening {
		t.Fatalf("expected Opening, got %s", next)
	}
	if e.cur == nil || e.cur.login != "lvthalo" {
		t.Fatalf("expected live lvthalo selected, got %+v", e.cur)
	}
	if pager.liveCalls == 0 {
		t.Fatal("expected LiveChannels to be consulted for an ACL campaign")
	}
}

// With no allowlisted channel live, selection cools down instead of opening an
// offline page.
func TestSelectingACLNoneLiveGoesCooldown(t *testing.T) {
	st, _ := store.OpenMemory()
	defer st.Close()
	c := seedCampaign(t, st, false)
	c.AllowedChannels = []domain.Channel{{Login: "europeanhalo"}, {Login: "lvthalo"}}
	if err := st.UpsertCampaign(c); err != nil {
		t.Fatal(err)
	}
	e := testEngine(t, st, newFakeController(), &fakePager{}, &fakeDisco{}, newRecordingEmitter())
	if next, _ := e.runSelecting(context.Background()); next != StateCooldown {
		t.Fatalf("no live ACL channel should cooldown, got %s", next)
	}
}

// Platforms without a status query (Kick) keep the old assume-live behavior.
func TestSelectingACLUnsupportedLiveFallsBack(t *testing.T) {
	st, _ := store.OpenMemory()
	defer st.Close()
	c := seedCampaign(t, st, false)
	c.AllowedChannels = []domain.Channel{{Login: "europeanhalo"}}
	if err := st.UpsertCampaign(c); err != nil {
		t.Fatal(err)
	}
	e := testEngine(t, st, newFakeController(), &fakePager{liveErr: errors.ErrUnsupported}, &fakeDisco{}, newRecordingEmitter())
	next, _ := e.runSelecting(context.Background())
	if next != StateOpening || e.cur == nil || e.cur.login != "europeanhalo" {
		t.Fatalf("expected assume-live fallback to open europeanhalo, got %s %+v", next, e.cur)
	}
}

// A live allowlisted channel streaming a different game cannot credit the
// campaign, so it is filtered out of the ACL pool.
func TestSelectingACLSkipsOffGameChannel(t *testing.T) {
	st, _ := store.OpenMemory()
	defer st.Close()
	c := seedCampaign(t, st, false)
	c.GameID = "g-rust"
	c.AllowedChannels = []domain.Channel{{Login: "eli_x"}, {Login: "lvthalo"}}
	if err := st.UpsertCampaign(c); err != nil {
		t.Fatal(err)
	}
	pager := &fakePager{live: []domain.Channel{
		{Login: "eli_x", Live: true, ViewerCount: 5000, GameID: "g-other", GameName: "Other Game"},
		{Login: "lvthalo", Live: true, ViewerCount: 900, GameID: "g-rust", GameName: "Rust"},
	}}
	e := testEngine(t, st, newFakeController(), pager, &fakeDisco{}, newRecordingEmitter())
	next, _ := e.runSelecting(context.Background())
	if next != StateOpening || e.cur == nil || e.cur.login != "lvthalo" {
		t.Fatalf("expected on-game lvthalo, got %s %+v", next, e.cur)
	}
}

// Zero credit on a drop that already has minutes banked is the CHANNEL's
// fault: blacklist it briefly and reselect; the drop stays in play.
func TestNoCreditWithPriorProgressBlamesChannel(t *testing.T) {
	st, _ := store.OpenMemory()
	defer st.Close()
	emit := newRecordingEmitter()
	e := testEngine(t, st, newFakeController(), &fakePager{}, &fakeDisco{}, emit)
	now := e.deps.Now()
	e.cur = &activeWatch{
		drop:          domain.Drop{ID: "d1", Name: "Desert Tea", RequiredMinutes: 60},
		login:         "eli_x",
		creditChannel: "",
		startMinutes:  6,
		lastMinutes:   6, // banked elsewhere, nothing gained here
		lastProgress:  now.Add(-noFirstCreditTimeout),
		session:       domain.WatchSession{ID: "s1", DropID: "d1"},
	}
	next, _, done := e.stallCheck(context.Background(), now, now.Add(time.Hour))
	if !done || next != StateSelectingChannel {
		t.Fatalf("expected reselect, got %s done=%v", next, done)
	}
	if e.noCreditDrops["d1"] {
		t.Fatal("a drop with prior progress must not be flagged un-earnable")
	}
	if bl, _ := st.IsBlacklisted(domain.PlatformTwitch, "eli_x", now); !bl {
		t.Fatal("the non-crediting channel must be blacklisted")
	}
	if emit.count(EvNeedsAction) != 0 {
		t.Fatal("no needs_action for a channel problem")
	}
}

// Offline single-streamer allowlist campaigns must not eat the fall-through
// budget (live 2026-09-25: 19 eligible Rust drops, the top 6 were allowlists
// with the streamer offline, and the any-channel campaign below them was
// never tried, so the watcher sat in cooldown all morning).
func TestSelectingOfflineACLsDoNotConsumeFallThroughBudget(t *testing.T) {
	st, _ := store.OpenMemory()
	defer st.Close()
	e := testEngine(t, st, newFakeController(), &fakePager{channels: []string{"rust_any"}}, &fakeDisco{}, newRecordingEmitter())
	n := e.deps.Cfg.Watcher.MaxSelectAttempts + 1
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("acl%d", i)
		c := domain.Campaign{
			ID: id, Name: id, GameName: "Rust", GameSlug: "rust", Status: domain.StatusActive,
			EndAt:           time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC),
			AllowedChannels: []domain.Channel{{Login: "offline_" + id}},
			Drops:           []domain.Drop{{ID: id + "-d", CampaignID: id, Name: "Tier 1", RequiredMinutes: 60}},
		}
		if err := st.UpsertCampaign(c); err != nil {
			t.Fatal(err)
		}
		if err := st.UpsertQueueItem(domain.QueueItem{DropID: id + "-d", CampaignID: id, Position: i, Enabled: true, Status: domain.ItemPending}); err != nil {
			t.Fatal(err)
		}
	}
	any := domain.Campaign{
		ID: "general", Name: "Rust Isles General Drops", GameName: "Rust", GameSlug: "rust", Status: domain.StatusActive,
		EndAt: time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC),
		Drops: []domain.Drop{{ID: "gen-d", CampaignID: "general", Name: "Tier 1", RequiredMinutes: 60}},
	}
	if err := st.UpsertCampaign(any); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertQueueItem(domain.QueueItem{DropID: "gen-d", CampaignID: "general", Position: n, Enabled: true, Status: domain.ItemPending}); err != nil {
		t.Fatal(err)
	}

	next, _ := e.runSelecting(context.Background())
	if next != StateOpening || e.cur == nil || e.cur.login != "rust_any" || e.cur.campaign.ID != "general" {
		t.Fatalf("expected the any-channel campaign below %d offline allowlists to be selected, got %s %+v", n, next, e.cur)
	}
}
