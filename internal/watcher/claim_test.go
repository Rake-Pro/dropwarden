package watcher

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/rake-pro/dropwarden/internal/config"
	"github.com/rake-pro/dropwarden/internal/domain"
	"github.com/rake-pro/dropwarden/internal/store"
)

// claimFixture seeds campaign camp1 (drop d1 queued), a completed progress row
// for d1 carrying claimID (may be empty), and an engine watching d1.
func claimFixture(t *testing.T, claimID string) (*store.Store, *fakeController, *fakePager, *recordingEmitter, *Engine) {
	t.Helper()
	st, _ := store.OpenMemory()
	t.Cleanup(func() { st.Close() })
	c := seedCampaign(t, st, false)
	if err := st.SaveProgress(domain.Progress{DropID: "d1", ClaimID: claimID, RealMinutes: 60, RequiredMinutes: 60, Claimable: true, Source: domain.SourceInventory}); err != nil {
		t.Fatal(err)
	}
	ctrl := newFakeController()
	pager := &fakePager{}
	emit := newRecordingEmitter()
	e := testEngine(t, st, ctrl, pager, &fakeDisco{}, emit)
	e.cur = &activeWatch{
		drop: c.Drops[0], campaign: c, login: "streamer_a",
		session:     domain.WatchSession{ID: "s1", DropID: "d1", ChannelLogin: "streamer_a", StartedAt: e.deps.Now()},
		lastMinutes: 60,
	}
	return st, ctrl, pager, emit, e
}

func claimedRow(dropID string) domain.Progress {
	at := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	return domain.Progress{DropID: dropID, RealMinutes: 60, RequiredMinutes: 60, ClaimedAt: &at, Source: domain.SourceInventory}
}

func assertClaimed(t *testing.T, st *store.Store, emit *recordingEmitter) {
	t.Helper()
	if has, _ := st.HasClaim("d1"); !has {
		t.Fatal("expected claim recorded in store")
	}
	if emit.count(EvClaimed) != 1 {
		t.Fatalf("expected 1 drop:claimed event, got %d", emit.count(EvClaimed))
	}
	q, _ := st.GetQueue()
	if q[0].Status != domain.ItemClaimed {
		t.Fatalf("queue item should be claimed, got %s", q[0].Status)
	}
	if p, _ := st.GetProgress("d1"); p == nil || p.ClaimedAt == nil {
		t.Fatalf("progress should carry ClaimedAt, got %+v", p)
	}
}

func assertNeedsAction(t *testing.T, st *store.Store, emit *recordingEmitter) {
	t.Helper()
	if has, _ := st.HasClaim("d1"); has {
		t.Fatal("no claim should be recorded on failure")
	}
	q, _ := st.GetQueue()
	if q[0].Status != domain.ItemNeedsAction {
		t.Fatalf("failed claim should mark needs_action, got %s", q[0].Status)
	}
	if emit.count(EvNeedsAction) != 1 {
		t.Fatal("failed claim should emit a needs_action event")
	}
}

func TestClaimMutationSuccessVerified(t *testing.T) {
	st, ctrl, pager, emit, e := claimFixture(t, "u#camp1#d1")
	pager.claimStatus = "ELIGIBLE_FOR_ALL"
	pager.inventory = []domain.Progress{{DropID: "d1", ClaimID: "u#camp1#d1", RealMinutes: 60, RequiredMinutes: 60, Claimable: true}}
	pager.afterClaim = []domain.Progress{claimedRow("d1")}

	next, _ := e.runClaiming(context.Background())
	if next != StateCooldown {
		t.Fatalf("expected Cooldown, got %s", next)
	}
	if len(pager.claimed) != 1 || pager.claimed[0] != "u#camp1#d1" {
		t.Fatalf("mutation should be sent once with the drop instance id, got %v", pager.claimed)
	}
	if pager.openInvCalls != 0 || ctrl.claimInvHits != 0 {
		t.Fatal("a verified mutation must not fall back to the inventory page")
	}
	assertClaimed(t, st, emit)
}

func TestClaimMutationUnverifiedFallsBackToDOM(t *testing.T) {
	st, ctrl, pager, emit, e := claimFixture(t, "u#camp1#d1")
	pager.claimStatus = "ELIGIBLE_FOR_ALL"
	// The inventory keeps showing the drop claimable: the accepted status is
	// not confirmed, so the mutation counts as failed and the DOM path runs;
	// its click is not confirmed either, so the drop needs manual action.
	pager.inventory = []domain.Progress{{DropID: "d1", ClaimID: "u#camp1#d1", RealMinutes: 60, RequiredMinutes: 60, Claimable: true}}
	ctrl.claimInv = true

	e.runClaiming(context.Background())
	if pager.openInvCalls != 1 || ctrl.claimInvHits != 1 {
		t.Fatalf("unverified mutation should fall back to the inventory page (open=%d click=%d)", pager.openInvCalls, ctrl.claimInvHits)
	}
	assertNeedsAction(t, st, emit)
}

func TestClaimMutationFailureFallsBackToDOM(t *testing.T) {
	st, ctrl, pager, emit, e := claimFixture(t, "u#camp1#d1")
	pager.claimStatus = "DROP_INSTANCE_NOT_FOUND"
	pager.claimErr = errors.New("claim rejected")
	ctrl.claimInv = true                                  // click succeeds
	pager.inventory = []domain.Progress{claimedRow("d1")} // and the re-read shows isClaimed

	next, _ := e.runClaiming(context.Background())
	if next != StateCooldown {
		t.Fatalf("expected Cooldown, got %s", next)
	}
	if pager.openInvCalls != 1 {
		t.Fatalf("DOM fallback must open the inventory page first, got %d opens", pager.openInvCalls)
	}
	if pager.buttonsCalls != 1 {
		t.Fatalf("DOM fallback must capture the inventory buttons, got %d", pager.buttonsCalls)
	}
	if ctrl.claimInvSel != config.DefaultClaimButtonSelector {
		t.Fatalf("DOM fallback should use the configured selector, got %q", ctrl.claimInvSel)
	}
	assertClaimed(t, st, emit)
}

func TestClaimUnsupportedUsesDOM(t *testing.T) {
	// Kick shape: no claim mutation and no inventory read, so the click is
	// trusted (the only evidence the platform offers).
	st, ctrl, pager, emit, e := claimFixture(t, "k1")
	pager.claimErr = errors.ErrUnsupported
	pager.invErr = errors.ErrUnsupported
	ctrl.claimInv = true

	e.runClaiming(context.Background())
	if ctrl.claimInvHits != 1 {
		t.Fatal("unsupported mutation should go straight to the inventory click")
	}
	assertClaimed(t, st, emit)
}

func TestClaimAllPathsFailFlagsNeedsAction(t *testing.T) {
	st, _, pager, emit, e := claimFixture(t, "u#camp1#d1")
	pager.claimErr = errors.New("failed integrity check")
	// ctrl.claimInv false: no button matched.
	next, _ := e.runClaiming(context.Background())
	if next != StateCooldown {
		t.Fatalf("expected Cooldown, got %s", next)
	}
	assertNeedsAction(t, st, emit)
}

func TestClaimEmptySelectorOnlyCaptures(t *testing.T) {
	st, ctrl, pager, emit, e := claimFixture(t, "")
	empty := ""
	e.deps.Cfg.Claim.InventoryButtonSelector = &empty
	pager.claimErr = errors.ErrUnsupported

	e.runClaiming(context.Background())
	if pager.buttonsCalls != 1 {
		t.Fatal("the button capture must run even with no selector configured")
	}
	if ctrl.claimInvSel != "" {
		t.Fatalf("empty selector should be passed through, got %q", ctrl.claimInvSel)
	}
	assertNeedsAction(t, st, emit)
}

func TestClaimRefreshesMissingClaimID(t *testing.T) {
	st, _, pager, emit, e := claimFixture(t, "")
	pager.claimStatus = "ELIGIBLE_FOR_ALL"
	pager.inventory = []domain.Progress{{DropID: "d1", ClaimID: "u#camp1#d1", RealMinutes: 60, RequiredMinutes: 60, Claimable: true}}
	pager.afterClaim = []domain.Progress{} // row gone after the claim

	e.runClaiming(context.Background())
	if len(pager.claimed) != 1 || pager.claimed[0] != "u#camp1#d1" {
		t.Fatalf("claim id should be refreshed from the inventory before the mutation, got %v", pager.claimed)
	}
	assertClaimed(t, st, emit)
}

func TestClaimNoUserInventoryIsNotConfirmation(t *testing.T) {
	// The mutation says success, but the verification read comes back without
	// a currentUser (auth expired / integrity failure): the parser reports an
	// error, which must never read as "row gone".
	st, _, pager, emit, e := claimFixture(t, "u#camp1#d1")
	pager.claimStatus = "ELIGIBLE_FOR_ALL"
	pager.invErr = errors.New("gql: inventory: no currentUser.inventory in response")

	e.runClaiming(context.Background())
	if len(pager.claimed) != 1 {
		t.Fatalf("mutation should be sent once, got %v", pager.claimed)
	}
	assertNeedsAction(t, st, emit)
}

func TestClaimStatusFailureWithRowGoneIsNotClaimed(t *testing.T) {
	// A failure status with the row absent from inventory is NOT a claim, and a
	// DOM click with the row absent has no status to vouch for it either.
	st, ctrl, pager, emit, e := claimFixture(t, "u#camp1#d1")
	pager.claimStatus = "DROP_INSTANCE_NOT_FOUND"
	pager.claimErr = errors.New("gql: claim rejected")
	pager.inventory = []domain.Progress{} // read succeeds, row gone
	ctrl.claimInv = true

	e.runClaiming(context.Background())
	if ctrl.claimInvHits != 1 {
		t.Fatalf("failure status should fall back to the inventory click, got %d", ctrl.claimInvHits)
	}
	assertNeedsAction(t, st, emit)
}

func TestClaimSuccessWithRowShortOfMinutesIsNotClaimed(t *testing.T) {
	// A present row that is not claimed (here short of its minutes, so not
	// claimable either) is never confirmation.
	st, _, pager, emit, e := claimFixture(t, "u#camp1#d1")
	pager.claimStatus = "ELIGIBLE_FOR_ALL"
	pager.afterClaim = []domain.Progress{{DropID: "d1", ClaimID: "u#camp1#d1", RealMinutes: 30, RequiredMinutes: 60}}

	e.runClaiming(context.Background())
	assertNeedsAction(t, st, emit)
}

func TestClaimAlreadyClaimedBooksDetected(t *testing.T) {
	st, _, pager, emit, e := claimFixture(t, "u#camp1#d1")
	pager.claimStatus = "DROP_INSTANCE_ALREADY_CLAIMED"
	pager.afterClaim = []domain.Progress{claimedRow("d1")}

	e.runClaiming(context.Background())
	assertClaimed(t, st, emit)
	claims, _ := st.ListClaims(time.Time{}, time.Time{})
	if len(claims) != 1 || claims[0].Source != domain.ClaimSourceDetected {
		t.Fatalf("already-claimed status should book one detected claim, got %+v", claims)
	}
	q, _ := st.GetQueue()
	if q[0].Reason != reasonDetected {
		t.Fatalf("queue reason = %q, want %q", q[0].Reason, reasonDetected)
	}
}

func TestReconcileClaimedRowsDetectsAndIsIdempotent(t *testing.T) {
	st, _ := store.OpenMemory()
	t.Cleanup(func() { st.Close() })
	c := seedCampaign(t, st, false)
	if err := st.SetItemStatus("d1", domain.ItemNeedsAction, "completed - claim it manually in the Twitch inventory"); err != nil {
		t.Fatal(err)
	}
	emit := newRecordingEmitter()
	e := testEngine(t, st, newFakeController(), &fakePager{}, &fakeDisco{}, emit)
	rows := []domain.Progress{claimedRow("d1"), {DropID: "other", RealMinutes: 5, RequiredMinutes: 60}}

	if n := e.reconcileClaimedRows(rows); n != 1 {
		t.Fatalf("first pass should book 1, got %d", n)
	}
	if n := e.reconcileClaimedRows(rows); n != 0 {
		t.Fatalf("second pass should book nothing, got %d", n)
	}
	claims, _ := st.ListClaims(time.Time{}, time.Time{})
	if len(claims) != 1 || claims[0].DropID != "d1" || claims[0].Source != domain.ClaimSourceDetected || claims[0].GameName != c.GameName {
		t.Fatalf("expected one detected History entry for d1, got %+v", claims)
	}
	if emit.count(EvClaimed) != 1 {
		t.Fatalf("expected 1 claimed event, got %d", emit.count(EvClaimed))
	}
	q, _ := st.GetQueue()
	if q[0].Status != domain.ItemClaimed || q[0].Reason != reasonDetected {
		t.Fatalf("needs_action item should flip to claimed/%q, got %s/%q", reasonDetected, q[0].Status, q[0].Reason)
	}
}

func TestPollProgressReconcilesClaimedInventoryRow(t *testing.T) {
	st, _, pager, emit, e := claimFixture(t, "u#camp1#d1")
	if err := st.SetItemStatus("d1", domain.ItemNeedsAction, "stale"); err != nil {
		t.Fatal(err)
	}
	pager.inventory = []domain.Progress{claimedRow("d1")}
	e.pollProgress(context.Background())
	e.pollProgress(context.Background())
	assertClaimed(t, st, emit)
}

// sweepFixture seeds one campaign with n completed, unclaimed drops (each with
// a claim id and a needs_action queue item) and returns an idle engine.
func sweepFixture(t *testing.T, n int) (*store.Store, *fakePager, *recordingEmitter, *Engine) {
	t.Helper()
	st, _ := store.OpenMemory()
	t.Cleanup(func() { st.Close() })
	c := domain.Campaign{ID: "camp1", Name: "Rust Drops", GameName: "Rust", Status: domain.StatusActive, EndAt: time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)}
	for i := 0; i < n; i++ {
		c.Drops = append(c.Drops, domain.Drop{ID: fmt.Sprintf("d%02d", i), CampaignID: "camp1", Name: fmt.Sprintf("Tier %d", i), RequiredMinutes: 60})
	}
	if err := st.UpsertCampaign(c); err != nil {
		t.Fatal(err)
	}
	for i, d := range c.Drops {
		if err := st.UpsertQueueItem(domain.QueueItem{DropID: d.ID, CampaignID: "camp1", Position: i, Enabled: true, Status: domain.ItemNeedsAction, Reason: "completed - claim it manually in the Twitch inventory"}); err != nil {
			t.Fatal(err)
		}
		if err := st.SaveProgress(domain.Progress{DropID: d.ID, ClaimID: "u#camp1#" + d.ID, RealMinutes: 60, RequiredMinutes: 60, Claimable: true, Source: domain.SourceInventory}); err != nil {
			t.Fatal(err)
		}
	}
	pager := &fakePager{claimStatus: "ELIGIBLE_FOR_ALL", claimMarks: true}
	for _, d := range c.Drops {
		pager.inventory = append(pager.inventory, domain.Progress{DropID: d.ID, ClaimID: "u#camp1#" + d.ID, RealMinutes: 60, RequiredMinutes: 60, Claimable: true, Source: domain.SourceInventory})
	}
	emit := newRecordingEmitter()
	e := testEngine(t, st, newFakeController(), pager, &fakeDisco{}, emit)
	return st, pager, emit, e
}

func TestClaimSweepClaimsAndRespectsCap(t *testing.T) {
	st, pager, emit, e := sweepFixture(t, 12)
	waits := 0
	e.wait = func(context.Context, time.Duration) { waits++ }

	e.claimSweep(context.Background(), "test")
	if len(pager.claimed) != sweepMax {
		t.Fatalf("sweep should claim at most %d drops, sent %d", sweepMax, len(pager.claimed))
	}
	if pager.openInvCalls != 0 {
		t.Fatal("the sweep must never open the inventory page")
	}
	if waits != sweepMax-1 {
		t.Fatalf("claims should be spaced (%d waits), got %d", sweepMax-1, waits)
	}
	if emit.count(EvClaimed) != sweepMax {
		t.Fatalf("expected %d claimed events, got %d", sweepMax, emit.count(EvClaimed))
	}
	claims, _ := st.ListClaims(time.Time{}, time.Time{})
	if len(claims) != sweepMax {
		t.Fatalf("expected %d History claims, got %d", sweepMax, len(claims))
	}
	q, _ := st.GetQueue()
	claimed := 0
	for _, it := range q {
		if it.Status == domain.ItemClaimed {
			claimed++
		}
	}
	if claimed != sweepMax {
		t.Fatalf("swept needs_action items should drain to claimed, got %d", claimed)
	}

	// The next pass picks up the remaining two.
	e.claimSweep(context.Background(), "test")
	if len(pager.claimed) != 12 {
		t.Fatalf("second pass should claim the rest, total sent %d", len(pager.claimed))
	}
}

func TestClaimSweepSkipsActiveAndIneligible(t *testing.T) {
	_, pager, _, e := sweepFixture(t, 3)
	// d00 is being watched, d01 has no claim id, d02 is short of its minutes
	// on the server even though the STORED row (fixture) says complete.
	e.cur = &activeWatch{drop: domain.Drop{ID: "d00"}}
	pager.inventory[1].ClaimID = ""
	pager.inventory[2].RealMinutes, pager.inventory[2].Claimable = 30, false

	e.claimSweep(context.Background(), "test")
	if len(pager.claimed) != 0 {
		t.Fatalf("nothing is claimable, sent %v", pager.claimed)
	}
}

func TestClaimSweepFailureParksDrop(t *testing.T) {
	st, pager, _, e := sweepFixture(t, 1)
	pager.claimErr = errors.New("claim rejected")

	e.claimSweep(context.Background(), "test")
	e.claimSweep(context.Background(), "test")
	if len(pager.claimed) != 1 {
		t.Fatalf("a failed drop should wait claim.retry_backoff before a retry, sent %d", len(pager.claimed))
	}
	q, _ := st.GetQueue()
	if q[0].Status != domain.ItemNeedsAction {
		t.Fatalf("sweep failure must not change the item status, got %s", q[0].Status)
	}
}

func TestClaimSweepUnverifiedIsFailure(t *testing.T) {
	st, pager, _, e := sweepFixture(t, 1)
	pager.claimMarks = false // inventory keeps showing d00 claimable

	e.claimSweep(context.Background(), "test")
	if has, _ := st.HasClaim("d00"); has {
		t.Fatal("a mutation the inventory does not confirm must not be recorded")
	}
}

func TestClaimSweepInventoryReadFailureClaimsNothing(t *testing.T) {
	st, pager, _, e := sweepFixture(t, 2)
	pager.invErr = errors.New("gql: inventory: no currentUser.inventory in response")
	e.claimSweep(context.Background(), "test")
	if len(pager.claimed) != 0 {
		t.Fatalf("no fresh inventory read = no sweep claims, sent %v", pager.claimed)
	}
	if has, _ := st.HasClaim("d00"); has {
		t.Fatal("nothing may be recorded without a read")
	}
}

func TestClaimSweepSuccessClearsBackoff(t *testing.T) {
	_, pager, _, e := sweepFixture(t, 1)
	e.sweepTried["d00"] = e.deps.Now().Add(-time.Hour) // parked long ago, backoff elapsed
	e.claimSweep(context.Background(), "test")
	if len(pager.claimed) != 1 {
		t.Fatalf("elapsed backoff should allow a retry, sent %d", len(pager.claimed))
	}
	if _, ok := e.sweepTried["d00"]; ok {
		t.Fatal("a successful claim must clear the retry backoff entry")
	}
}

func TestClaimSweepDisabledAndUnsupported(t *testing.T) {
	_, pager, _, e := sweepFixture(t, 3)
	off := false
	e.deps.Cfg.Claim.Sweep = &off
	e.claimSweep(context.Background(), "test")
	if len(pager.claimed) != 0 {
		t.Fatal("claim.sweep=false must disable the sweep")
	}

	e.deps.Cfg.Claim.Sweep = nil
	pager.claimErr = errors.ErrUnsupported
	e.claimSweep(context.Background(), "test")
	if len(pager.claimed) != 1 {
		t.Fatalf("an unsupported platform should stop the sweep after one attempt, sent %d", len(pager.claimed))
	}
}

func TestDiscoveringRunsClaimSweep(t *testing.T) {
	st, pager, _, e := sweepFixture(t, 2)
	e.paused = false
	e.runDiscovering(context.Background())
	if len(pager.claimed) != 2 {
		t.Fatalf("discovery should sweep claimable drops, sent %d", len(pager.claimed))
	}
	if has, _ := st.HasClaim("d01"); !has {
		t.Fatal("swept drop should be in History")
	}
}

func TestPollProgressSweepsOtherCompletedDrop(t *testing.T) {
	st, pager, _, e := sweepFixture(t, 2)
	e.cur = &activeWatch{drop: domain.Drop{ID: "d00", RequiredMinutes: 60}}
	pager.inventory = []domain.Progress{
		{DropID: "d00", ClaimID: "u#camp1#d00", RealMinutes: 10, RequiredMinutes: 60, Source: domain.SourceInventory},
		{DropID: "d01", ClaimID: "u#camp1#d01", RealMinutes: 60, RequiredMinutes: 60, Claimable: true, Source: domain.SourceInventory},
	}
	pager.afterClaim = []domain.Progress{claimedRow("d01")}

	e.pollProgress(context.Background())
	if len(pager.claimed) != 1 || pager.claimed[0] != "u#camp1#d01" {
		t.Fatalf("poll should sweep the other completed drop only, sent %v", pager.claimed)
	}
	if has, _ := st.HasClaim("d01"); !has {
		t.Fatal("swept drop should be in History")
	}
}
