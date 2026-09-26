package watcher

import (
	"context"
	"testing"
	"time"

	"github.com/rake-pro/dropwarden/internal/domain"
	"github.com/rake-pro/dropwarden/internal/store"
	"github.com/rake-pro/dropwarden/internal/twitch"
)

// clockEngine is testEngine with a movable clock.
func clockEngine(t *testing.T, pager *fakePager, disco *fakeDisco) (*Engine, *fakeController, *recordingEmitter, func(time.Duration)) {
	t.Helper()
	st, _ := store.OpenMemory()
	t.Cleanup(func() { st.Close() })
	ctrl := newFakeController()
	emit := newRecordingEmitter()
	e := testEngine(t, st, ctrl, pager, disco, emit)
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	e.deps.Now = func() time.Time { return now }
	return e, ctrl, emit, func(d time.Duration) { now = now.Add(d) }
}

func TestDiscoverySkippedWithinInterval(t *testing.T) {
	disco := &fakeDisco{}
	e, _, _, advance := clockEngine(t, &fakePager{}, disco)
	e.deps.Cfg.Watcher.Cooldown = time.Millisecond
	e.deps.Cfg.Watcher.CooldownJitter = 0
	e.deps.Cfg.Discovery.Interval = 45 * time.Minute
	e.deps.Cfg.Discovery.Jitter = 30 * time.Minute // Rand 0.5 -> gap 60m

	if next, _ := e.runDiscovering(context.Background()); next != StateSelectingChannel || disco.calls != 1 {
		t.Fatalf("first pass must run: next=%s calls=%d", next, disco.calls)
	}
	advance(2 * time.Minute)
	if next, _ := e.runCooldown(context.Background()); next != StateDiscovering {
		t.Fatalf("cooldown -> %s", next)
	}
	next, reason := e.runDiscovering(context.Background())
	if disco.calls != 1 || next != StateSelectingChannel {
		t.Fatalf("a cooldown inside the interval must not rediscover: calls=%d next=%s (%s)", disco.calls, next, reason)
	}
	// The dashboard refresh always forces a pass.
	e.refreshReq = true
	e.runDiscovering(context.Background())
	if disco.calls != 2 {
		t.Fatalf("refreshReq must force discovery, calls=%d", disco.calls)
	}
	advance(59 * time.Minute)
	e.runDiscovering(context.Background())
	if disco.calls != 2 {
		t.Fatalf("the gap restarts at the forced pass: calls=%d", disco.calls)
	}
	advance(2 * time.Minute)
	e.runDiscovering(context.Background())
	if disco.calls != 3 {
		t.Fatalf("a pass is due once the jittered interval elapsed: calls=%d", disco.calls)
	}
}

func TestStatusReasonTracksTransitions(t *testing.T) {
	e, _, _, _ := clockEngine(t, &fakePager{}, &fakeDisco{})
	e.deps.Cfg.Watcher.Cooldown = time.Millisecond
	e.deps.Cfg.Watcher.CooldownJitter = 0
	e.transition(StateCooldown, "nothing eligible to watch")
	if r := e.Status().Reason; r != "nothing eligible to watch" {
		t.Fatalf("Status.Reason = %q", r)
	}
	e.runCooldown(context.Background())
	if r := e.Status().Reason; r != "nothing eligible to watch" {
		t.Fatalf("the cooldown status reset must keep the reason, got %q", r)
	}
}

func aclWatch(now time.Time) *activeWatch {
	return &activeWatch{
		drop: domain.Drop{ID: "d1", Name: "Tier 1", RequiredMinutes: 60},
		campaign: domain.Campaign{ID: "c1", GameID: "g-rust", GameName: "Rust",
			AllowedChannels: []domain.Channel{{Login: "streamer"}}},
		login: "streamer", lastProgress: now, sessionStart: now, maxSessionAt: now.Add(24 * time.Hour),
	}
}

func TestValidatingFailsStreamerOnAnotherGame(t *testing.T) {
	pager := &fakePager{live: []domain.Channel{{Login: "streamer", Live: true, GameID: "g-other", GameName: "Just Chatting"}}}
	e, _, _, _ := clockEngine(t, pager, &fakeDisco{})
	e.cur = aclWatch(e.deps.Now())
	next, reason := e.runValidating(context.Background())
	if next != StateSelectingChannel || reason != reasonOtherGame {
		t.Fatalf("off-game stream must fail the channel: %s (%s)", next, reason)
	}
	if bl, _ := e.deps.Store.IsBlacklisted(e.deps.Platform, "streamer", e.deps.Now()); !bl {
		t.Fatal("off-game channel must be blacklisted for the offline TTL")
	}
}

func TestSoftStallChecksLivenessOncePerEpisode(t *testing.T) {
	pager := &fakePager{live: []domain.Channel{{Login: "streamer", Live: true, GameID: "g-rust"}}}
	e, _, _, _ := clockEngine(t, pager, &fakeDisco{})
	now := e.deps.Now()
	e.cur = aclWatch(now.Add(-7 * time.Minute)) // past soft (6m), before hard (12m)
	e.cur.creditChannel = "streamer"
	far := now.Add(time.Hour)

	if _, _, done := e.stallCheck(context.Background(), now, far); done {
		t.Fatal("a live on-game stream must keep watching")
	}
	calls := pager.liveCalls
	if _, _, done := e.stallCheck(context.Background(), now, far); done || pager.liveCalls != calls {
		t.Fatalf("the soft-stall live check runs once per stall episode (calls %d -> %d)", calls, pager.liveCalls)
	}

	pager.live = nil // streamer went offline; a new episode starts after progress
	e.cur.lastProgress = now.Add(-8 * time.Minute)
	next, reason, done := e.stallCheck(context.Background(), now, far)
	if !done || next != StateSelectingChannel || reason != reasonOffline {
		t.Fatalf("offline at the soft stall must fail fast: %v %s (%s)", done, next, reason)
	}
}

func TestLoggedOutPollPausesAndPrompts(t *testing.T) {
	pager := &fakePager{currentErr: domain.ErrLoggedOut}
	e, ctrl, emit, _ := clockEngine(t, pager, &fakeDisco{})
	ctrl.health = twitch.Health{Pathname: "/streamer", Volume: 0.01, CurrentTime: 1, ReadyState: 4}
	hooked := false
	e.deps.OnLoggedOut = func() { hooked = true }
	e.cur = aclWatch(e.deps.Now())
	far := e.deps.Now().Add(time.Hour)
	var lt float64
	var ls bool
	var fr int
	nr := far
	next, reason, done := e.watchTick(context.Background(), &lt, &ls, &fr, &nr, far, far)
	if !done || next != StateIdle || reason != "Twitch session expired - log in again" {
		t.Fatalf("logged out must stop watching: %v %s (%s)", done, next, reason)
	}
	if !e.paused || !hooked {
		t.Fatalf("engine must pause and run the needs-login hook: paused=%v hooked=%v", e.paused, hooked)
	}
	p, _ := emit.last[EvNeedsAction].(NeedsActionPayload)
	if p.Reason != reason || p.URL != URLTwitchLogin {
		t.Fatalf("needs-action event = %+v", p)
	}
	if len(e.noCreditDrops) != 0 {
		t.Fatal("a logged-out session must never file the drop as un-earnable")
	}
}

func TestLoggedOutDiscoveryPauses(t *testing.T) {
	e, _, _, _ := clockEngine(t, &fakePager{}, &fakeDisco{err: domain.ErrLoggedOut})
	if next, _ := e.runDiscovering(context.Background()); next != StateIdle || !e.paused {
		t.Fatalf("logged-out discovery must pause: %s paused=%v", next, e.paused)
	}
}

func TestNoCreditNotJudgedWhenPollsFailed(t *testing.T) {
	st, _ := store.OpenMemory()
	defer st.Close()
	seedCampaign(t, st, false)
	e := testEngine(t, st, newFakeController(), &fakePager{}, &fakeDisco{}, newRecordingEmitter())
	now := e.deps.Now()
	e.cur = &activeWatch{drop: domain.Drop{ID: "d1", Name: "Tier 1", RequiredMinutes: 60}, login: "chan",
		lastProgress: now.Add(-6 * time.Minute), maxSessionAt: now.Add(time.Hour), pollErrors: 1}
	far := now.Add(time.Hour)
	if _, _, done := e.stallCheck(context.Background(), now, far); done {
		t.Fatal("a window with a failed poll must not give up on the drop")
	}
	if e.noCreditDrops["d1"] || e.cur.pollErrors != 0 || !e.cur.lastProgress.Equal(now) {
		t.Fatalf("must start a fresh window: noCredit=%v pollErrors=%d", e.noCreditDrops["d1"], e.cur.pollErrors)
	}

	e.cur.lastProgress = now.Add(-6 * time.Minute)
	if _, reason, done := e.stallCheck(context.Background(), now, far); !done || reason != reasonNoCredit {
		t.Fatalf("a clean window gives up with the reworded reason: %v %q", done, reason)
	}
	q, _ := st.GetQueue()
	if len(q) != 1 || q[0].Reason != reasonNoCredit {
		t.Fatalf("queue reason = %+v", q)
	}
}

func TestHiddenTabTwicePollsRestoresWindow(t *testing.T) {
	e, ctrl, _, _ := clockEngine(t, &fakePager{}, &fakeDisco{})
	ctrl.health = twitch.Health{Visibility: "hidden"}
	e.cur = aclWatch(e.deps.Now())
	e.pollProgress(context.Background())
	if e.Status().Reason == hiddenWindowReason {
		t.Fatal("one hidden poll is not enough")
	}
	e.cur.nextPollAt = time.Time{}
	e.pollProgress(context.Background())
	if r := e.Status().Reason; r != hiddenWindowReason {
		t.Fatalf("two hidden polls must restore and say so, reason %q", r)
	}
}

// hangSession never answers a reload until its ctx ends.
type hangSession struct{ *fakeSession }

func (h hangSession) Reload(ctx context.Context, _ bool) error {
	<-ctx.Done()
	return ctx.Err()
}

func TestBoundedCDPCallGivesUp(t *testing.T) {
	st, _ := store.OpenMemory()
	defer st.Close()
	ctrl := newFakeController()
	ctrl.sess = hangSession{newFakeSession()}
	e := testEngine(t, st, ctrl, &fakePager{}, &fakeDisco{}, newRecordingEmitter())
	e.cdpTimeout = 50 * time.Millisecond
	done := make(chan struct{})
	go func() { e.reload(context.Background()); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("a hanging reload must be abandoned at the CDP timeout")
	}
}

func TestSweepSkipsPreconditionsUnmet(t *testing.T) {
	st, _ := store.OpenMemory()
	defer st.Close()
	seedCampaign(t, st, false)
	e := testEngine(t, st, newFakeController(), &fakePager{}, &fakeDisco{}, newRecordingEmitter())
	row := domain.Progress{DropID: "d1", ClaimID: "u#camp1#d1", RealMinutes: 60, RequiredMinutes: 60, Source: domain.SourceInventory}
	if got := e.sweepCandidates([]domain.Progress{row}); len(got) != 1 {
		t.Fatalf("a complete row is a sweep candidate, got %d", len(got))
	}
	row.PreconditionsUnmet = true
	if got := e.sweepCandidates([]domain.Progress{row}); len(got) != 0 {
		t.Fatalf("preconditions unmet must be skipped, got %d", len(got))
	}
}

// A needs_action "claim it manually" row whose campaign already left the
// in-progress inventory flips to claimed once discovery's re-detail stores
// isClaimed; the next pass changes nothing.
func TestRedetailedClaimFlipsNeedsActionRow(t *testing.T) {
	st, _ := store.OpenMemory()
	defer st.Close()
	seedCampaign(t, st, false)
	_ = st.SetItemStatus("d1", domain.ItemNeedsAction, "completed - claim it manually in the Twitch inventory")
	_ = st.SaveProgress(domain.Progress{DropID: "d1", RealMinutes: 60, RequiredMinutes: 60, Source: domain.SourceInventory})
	disco := &fakeDisco{run: func() {
		at := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
		_ = st.SaveProgress(domain.Progress{DropID: "d1", RealMinutes: 60, RequiredMinutes: 60, Source: domain.SourceInventory, ClaimedAt: &at})
	}}
	emit := newRecordingEmitter()
	e := testEngine(t, st, newFakeController(), &fakePager{}, disco, emit)
	e.runDiscovering(context.Background())
	q, _ := st.GetQueue()
	if len(q) != 1 || q[0].Status != domain.ItemClaimed {
		t.Fatalf("re-detailed isClaimed must flip the row to claimed: %+v", q)
	}
	claims := emit.count(EvClaimed)
	e.refreshReq = true
	e.runDiscovering(context.Background())
	if emit.count(EvClaimed) != claims {
		t.Fatal("second pass must be a no-op")
	}
}

func TestLoginFromPathReservedSegments(t *testing.T) {
	for _, p := range []string{"/login", "/search?term=x", "/popout/foo/chat", "/moderator/foo", "/embed/foo",
		"/u/settings", "/p/en/legal", "/team/foo", "/event/foo", "/inventory", "https://www.twitch.tv/drops/inventory"} {
		if got := loginFromPath("twitch.tv", p); got != "" {
			t.Errorf("loginFromPath(%q) = %q, want empty (reserved)", p, got)
		}
	}
	if got := loginFromPath("twitch.tv", "/somechan"); got != "somechan" {
		t.Errorf("a channel path must still resolve, got %q", got)
	}
}
