package twitch

import (
	"context"
	"encoding/json"
	"testing"
)

// fakeEval returns a fixed string for any Eval call (the drain buffer).
type fakeEval struct{ raw string }

func (f fakeEval) Eval(_ context.Context, _ string, out any) error {
	if p, ok := out.(*string); ok {
		*p = f.raw
	}
	return nil
}

func TestDrainBatchedResponse(t *testing.T) {
	// A single Twitch GQL request batches two operations; the response is an
	// array positionally aligned with the operation names.
	campaignsResult := `{"data":{"currentUser":{"dropCampaigns":[
		{"id":"c1","name":"Cinna's August Subathon","status":"ACTIVE",
		 "game":{"id":"g1","displayName":"Special Events","slug":"special-events"}}]}}}`
	inventoryResult := `{"data":{"currentUser":{"inventory":{"dropCampaignsInProgress":[
		{"id":"c1","timeBasedDrops":[{"id":"d1","requiredMinutesWatched":30,
		 "self":{"currentMinutesWatched":10,"isClaimed":false}}]}]}}}}`
	batchedBody := "[" + campaignsResult + "," + inventoryResult + "]"

	buf, _ := json.Marshal([]capturedEntry{{
		Ops:  []string{"ViewerDropsDashboard", "Inventory"},
		Body: batchedBody,
	}})

	res, err := Drain(context.Background(), fakeEval{raw: string(buf)})
	if err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if res.RawCount != 1 {
		t.Fatalf("RawCount=%d want 1", res.RawCount)
	}
	if len(res.Campaigns) != 1 || res.Campaigns[0].GameName != "Special Events" {
		t.Fatalf("campaigns not parsed from batch: %+v", res.Campaigns)
	}
	if len(res.Progress) != 1 || res.Progress[0].DropID != "d1" || res.Progress[0].RequiredMinutes != 30 {
		t.Fatalf("inventory not parsed from batch: %+v", res.Progress)
	}
	// Both real op names should be recorded as seen.
	if len(res.OpsSeen) != 2 {
		t.Fatalf("OpsSeen=%v want 2", res.OpsSeen)
	}
}

func TestDrainSingleObject(t *testing.T) {
	single := `{"data":{"currentUser":{"dropCampaigns":[{"id":"c1","name":"X","status":"ACTIVE"}]}}}`
	buf, _ := json.Marshal([]capturedEntry{{Ops: []string{"ViewerDropsDashboard"}, Body: single}})
	res, err := Drain(context.Background(), fakeEval{raw: string(buf)})
	if err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if len(res.Campaigns) != 1 || res.Campaigns[0].ID != "c1" {
		t.Fatalf("single-object parse failed: %+v", res.Campaigns)
	}
}
