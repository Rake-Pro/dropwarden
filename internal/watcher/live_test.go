package watcher

import (
	"context"
	"testing"

	"github.com/rake-pro/dropwarden/internal/domain"
	"github.com/rake-pro/dropwarden/internal/store"
)

// The live sweep answers "who is streaming for this drop" for every queued
// allowlist campaign in one status query, and selection exposes it too.
func TestLiveSweepSnapshotsQueuedACLCampaigns(t *testing.T) {
	st, _ := store.OpenMemory()
	defer st.Close()
	c := seedCampaign(t, st, false)
	c.AllowedChannels = []domain.Channel{{Login: "europeanhalo"}, {Login: "lvthalo"}}
	if err := st.UpsertCampaign(c); err != nil {
		t.Fatal(err)
	}
	pager := &fakePager{live: []domain.Channel{{Login: "lvthalo", Live: true, ViewerCount: 900}}}
	e := testEngine(t, st, newFakeController(), pager, &fakeDisco{}, newRecordingEmitter())

	e.liveSweep(context.Background(), "test")
	lv := e.Liveness()["camp1"]
	if len(lv.Logins) != 1 || lv.Logins[0] != "lvthalo" || lv.CheckedAt.IsZero() {
		t.Fatalf("sweep must record the live allowlisted login: %+v", lv)
	}
	if pager.liveCalls != 1 {
		t.Fatalf("one batched status query expected, got %d", pager.liveCalls)
	}

	pager.live = nil
	e.liveSweep(context.Background(), "test")
	if lv := e.Liveness()["camp1"]; lv.Logins == nil || len(lv.Logins) != 0 {
		t.Fatalf("no live channel must read as checked-but-empty, got %+v", lv)
	}

	if next, _ := e.runSelecting(context.Background()); next != StateCooldown {
		t.Fatalf("nothing live: expected Cooldown, got %s", next)
	}
}
