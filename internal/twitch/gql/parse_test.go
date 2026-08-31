package gql

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/rake-pro/dropwarden/internal/domain"
)

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return b
}

func TestParseCampaigns(t *testing.T) {
	body := readFixture(t, "campaigns.json")
	campaigns, err := ParseCampaigns(body)
	if err != nil {
		t.Fatalf("ParseCampaigns: %v", err)
	}
	if len(campaigns) != 2 {
		t.Fatalf("expected 2 campaigns, got %d", len(campaigns))
	}

	c0 := campaigns[0]
	if c0.ID != "camp-1" || c0.Name != "Test Campaign One" {
		t.Errorf("campaign 0 id/name = %q/%q", c0.ID, c0.Name)
	}
	if c0.Status != domain.StatusActive {
		t.Errorf("campaign 0 status = %q, want %q", c0.Status, domain.StatusActive)
	}
	if c0.GameID != "game-1" || c0.GameName != "Test Game" || c0.GameSlug != "test-game" {
		t.Errorf("campaign 0 game fields = %+v", c0)
	}
	if c0.StartAt.IsZero() || c0.EndAt.IsZero() {
		t.Errorf("campaign 0 expected non-zero start/end times")
	}
	if c0.AccountLinkURL != "" {
		t.Errorf("list view should not populate AccountLinkURL, got %q", c0.AccountLinkURL)
	}
	if c0.AccountConnected == nil || !*c0.AccountConnected {
		t.Errorf("campaign 0 AccountConnected = %+v, want true from self.isAccountConnected", c0.AccountConnected)
	}

	c1 := campaigns[1]
	if c1.Status != domain.StatusUpcoming {
		t.Errorf("campaign 1 status = %q, want %q", c1.Status, domain.StatusUpcoming)
	}
	if c1.AccountConnected != nil {
		t.Errorf("campaign 1 AccountConnected = %+v, want nil when self is absent", c1.AccountConnected)
	}
}

func TestParseCampaignDetails(t *testing.T) {
	body := readFixture(t, "campaign_details.json")
	c, _, err := ParseCampaignDetails(body)
	if err != nil {
		t.Fatalf("ParseCampaignDetails: %v", err)
	}

	if c.ID != "camp-1" || c.Name != "Test Campaign One" {
		t.Errorf("campaign id/name = %q/%q", c.ID, c.Name)
	}
	if !c.AnyChannel() {
		t.Errorf("expected AnyChannel() true for empty ACL, got false")
	}
	if len(c.Drops) != 2 {
		t.Fatalf("expected 2 drops, got %d", len(c.Drops))
	}

	d0, d1 := c.Drops[0], c.Drops[1]
	if d0.RequiredMinutes != 60 {
		t.Errorf("drop 0 RequiredMinutes = %d, want 60", d0.RequiredMinutes)
	}
	if len(d0.Benefits) != 1 || d0.Benefits[0].Name != "Cool Hat" {
		t.Errorf("drop 0 benefits = %+v", d0.Benefits)
	}
	if len(d0.PreconditionIDs) != 0 {
		t.Errorf("drop 0 expected no preconditions, got %v", d0.PreconditionIDs)
	}
	if d0.CampaignID != "camp-1" {
		t.Errorf("drop 0 CampaignID = %q, want camp-1", d0.CampaignID)
	}

	if d1.RequiredMinutes != 120 {
		t.Errorf("drop 1 RequiredMinutes = %d, want 120", d1.RequiredMinutes)
	}
	if len(d1.PreconditionIDs) != 1 || d1.PreconditionIDs[0] != "drop-1" {
		t.Errorf("drop 1 preconditions = %v, want [drop-1]", d1.PreconditionIDs)
	}
}

func TestParseCampaignDetails_WithACL(t *testing.T) {
	body := []byte(`{
		"data": {
			"user": {
				"dropCampaign": {
					"id": "camp-3",
					"name": "ACL Campaign",
					"status": "ACTIVE",
					"startAt": "2026-08-01T00:00:00Z",
					"endAt": "2026-09-01T00:00:00Z",
					"accountLinkURL": "https://example.com/link",
					"game": {"id": "game-1", "displayName": "Test Game", "slug": "test-game"},
					"allow": {"channels": [{"id": "chan-1", "name": "SomeStreamer"}]},
					"timeBasedDrops": []
				}
			}
		}
	}`)
	c, _, err := ParseCampaignDetails(body)
	if err != nil {
		t.Fatalf("ParseCampaignDetails: %v", err)
	}
	if c.AnyChannel() {
		t.Errorf("expected AnyChannel() false when ACL is non-empty")
	}
	if len(c.AllowedChannels) != 1 || c.AllowedChannels[0].Login != "somestreamer" {
		t.Errorf("allowed channels = %+v, want lowercased login somestreamer", c.AllowedChannels)
	}
	if !c.RequiresAccountLink() {
		t.Errorf("expected RequiresAccountLink() true")
	}
}

func TestParseCampaignDetails_ConnectedSelfOverridesLinkURL(t *testing.T) {
	body := []byte(`{
		"data": {
			"user": {
				"dropCampaign": {
					"id": "camp-1",
					"name": "Watch to Redeem",
					"status": "ACTIVE",
					"accountLinkURL": "https://example.com/game-site",
					"self": {"isAccountConnected": true},
					"game": {"id": "game-1", "displayName": "Special Events", "slug": "special-events"},
					"timeBasedDrops": []
				}
			}
		}
	}`)
	c, _, err := ParseCampaignDetails(body)
	if err != nil {
		t.Fatalf("ParseCampaignDetails: %v", err)
	}
	if c.AccountConnected == nil || !*c.AccountConnected {
		t.Fatalf("AccountConnected = %+v, want true", c.AccountConnected)
	}
	if c.RequiresAccountLink() {
		t.Errorf("expected RequiresAccountLink() false when self.isAccountConnected is true, despite non-empty accountLinkURL")
	}
}

func TestParseCampaignDetails_SelfProgress(t *testing.T) {
	body := []byte(`{
		"data": {
			"user": {
				"dropCampaign": {
					"id": "camp-1",
					"name": "Test Campaign One",
					"status": "ACTIVE",
					"startAt": "2026-08-01T00:00:00Z",
					"endAt": "2026-09-01T00:00:00Z",
					"game": {"id": "game-1", "displayName": "Test Game", "slug": "test-game"},
					"timeBasedDrops": [
						{
							"id": "drop-1",
							"name": "Tier 1",
							"requiredMinutesWatched": 60,
							"self": {"currentMinutesWatched": 60, "isClaimed": true, "dropInstanceID": "user1#camp-1#drop-1"}
						},
						{
							"id": "drop-2",
							"name": "Tier 2",
							"requiredMinutesWatched": 120,
							"self": {"currentMinutesWatched": 45, "isClaimed": false}
						}
					]
				}
			}
		}
	}`)
	c, progress, err := ParseCampaignDetails(body)
	if err != nil {
		t.Fatalf("ParseCampaignDetails: %v", err)
	}
	if len(c.Drops) != 2 {
		t.Fatalf("expected 2 drops, got %d", len(c.Drops))
	}
	if len(progress) != 2 {
		t.Fatalf("expected 2 progress entries, got %d", len(progress))
	}

	byID := map[string]domain.Progress{}
	for _, p := range progress {
		byID[p.DropID] = p
		if p.Source != domain.SourceInventory {
			t.Errorf("drop %s source = %q, want %q", p.DropID, p.Source, domain.SourceInventory)
		}
	}

	// drop-1: already claimed on Twitch -> ClaimedAt set, not claimable.
	p1 := byID["drop-1"]
	if p1.ClaimedAt == nil {
		t.Errorf("drop-1 expected ClaimedAt to be set (isClaimed true)")
	}
	if p1.Claimable {
		t.Errorf("drop-1 already claimed, should not be claimable")
	}
	if p1.RealMinutes != 60 {
		t.Errorf("drop-1 RealMinutes = %d, want 60", p1.RealMinutes)
	}
	if p1.ClaimID != "user1#camp-1#drop-1" {
		t.Errorf("drop-1 ClaimID = %q", p1.ClaimID)
	}

	// drop-2: in progress, not claimed -> no ClaimedAt.
	p2 := byID["drop-2"]
	if p2.ClaimedAt != nil {
		t.Errorf("drop-2 should not be claimed yet")
	}
	if p2.Claimable {
		t.Errorf("drop-2 expected not claimable (45 < 120)")
	}
	if p2.RealMinutes != 45 {
		t.Errorf("drop-2 RealMinutes = %d, want 45", p2.RealMinutes)
	}
}

func TestParseInventory(t *testing.T) {
	body := readFixture(t, "inventory.json")
	progs, err := ParseInventory(body)
	if err != nil {
		t.Fatalf("ParseInventory: %v", err)
	}
	if len(progs) != 3 {
		t.Fatalf("expected 3 progress entries, got %d", len(progs))
	}

	byID := map[string]domain.Progress{}
	for _, p := range progs {
		byID[p.DropID] = p
		if p.Source != domain.SourceInventory {
			t.Errorf("drop %s source = %q, want %q", p.DropID, p.Source, domain.SourceInventory)
		}
	}

	// drop-1: minutes met, not claimed -> claimable, has claim id.
	p1 := byID["drop-1"]
	if !p1.Claimable {
		t.Errorf("drop-1 expected claimable")
	}
	if p1.ClaimID != "user1#camp-1#drop-1" {
		t.Errorf("drop-1 ClaimID = %q", p1.ClaimID)
	}
	if p1.ClaimedAt != nil {
		t.Errorf("drop-1 should not be claimed yet")
	}

	// drop-2: minutes short -> not claimable.
	p2 := byID["drop-2"]
	if p2.Claimable {
		t.Errorf("drop-2 expected not claimable (45 < 120)")
	}
	if p2.ClaimID != "" {
		t.Errorf("drop-2 expected empty ClaimID, got %q", p2.ClaimID)
	}

	// drop-3: already claimed -> not claimable, ClaimedAt set.
	p3 := byID["drop-3"]
	if p3.Claimable {
		t.Errorf("drop-3 already claimed, should not be claimable")
	}
	if p3.ClaimedAt == nil {
		t.Errorf("drop-3 expected ClaimedAt to be set")
	}
}

func TestParseEarnedRewards(t *testing.T) {
	// Real inventory shape (trimmed): gameEventDropsConnection nodes carry the
	// reward name directly; earnedDropRewards nodes nest it under item.name. Names
	// are normalized (trimmed + lowercased); the trailing space on the skin name
	// must not defeat a later drop-benefit match.
	body := []byte(`{"data":{"currentUser":{"id":"1","inventory":{
		"dropCampaignsInProgress":[],
		"gameEventDropsConnection":{"edges":[
			{"node":{"id":"camp_CUSTOM_ID_1","name":"VOID WINGS SKIN: UNKNOWN ","__typename":"UserDropReward"}},
			{"node":{"id":"camp_CUSTOM_ID_2","name":"DECAL: GRAFFITI","__typename":"UserDropReward"}}
		],"pageInfo":{"hasNextPage":false}},
		"earnedDropRewards":{"edges":[
			{"node":{"id":"e1","item":{"id":"i1","name":"Pichu","distributionType":"BADGE"},"status":"CLAIMED"}},
			{"node":{"id":"e2","item":{"id":"i2","name":"CREP Emote","distributionType":"EMOTE"}}}
		]}
	}}}}`)

	owned, err := ParseEarnedRewards(body)
	if err != nil {
		t.Fatalf("ParseEarnedRewards: %v", err)
	}
	want := []string{"void wings skin: unknown", "decal: graffiti", "pichu", "crep emote"}
	for _, w := range want {
		if !owned[w] {
			t.Errorf("owned set missing %q; got %v", w, owned)
		}
	}
	if len(owned) != len(want) {
		t.Errorf("owned set size = %d, want %d (%v)", len(owned), len(want), owned)
	}
	// A drop benefit named with different casing/whitespace must match.
	if !owned[NormalizeRewardName("  Void Wings Skin: Unknown  ")] {
		t.Errorf("normalized benefit name should match an owned reward")
	}
}

func TestParseEarnedRewards_NoInventory(t *testing.T) {
	owned, err := ParseEarnedRewards([]byte(`{"data":{"currentUser":null}}`))
	if err != nil {
		t.Fatalf("ParseEarnedRewards: %v", err)
	}
	if owned != nil {
		t.Errorf("expected nil owned set for absent inventory, got %v", owned)
	}
}

func TestParseCurrentDrop(t *testing.T) {
	body := readFixture(t, "current_drop.json")
	p, err := ParseCurrentDrop(body)
	if err != nil {
		t.Fatalf("ParseCurrentDrop: %v", err)
	}
	if p == nil {
		t.Fatalf("expected non-nil progress")
	}
	if p.DropID != "drop-1" {
		t.Errorf("DropID = %q, want drop-1", p.DropID)
	}
	if p.RealMinutes != 42 || p.RequiredMinutes != 60 {
		t.Errorf("minutes = %d/%d, want 42/60", p.RealMinutes, p.RequiredMinutes)
	}
	if p.Source != domain.SourceCurrentDrop {
		t.Errorf("source = %q, want %q", p.Source, domain.SourceCurrentDrop)
	}
	if p.Claimable {
		t.Errorf("expected not claimable, 42 < 60")
	}
}

func TestParseCurrentDrop_NoSession(t *testing.T) {
	body := []byte(`{"data": {"currentUser": {"dropCurrentSession": null}}}`)
	p, err := ParseCurrentDrop(body)
	if err != nil {
		t.Fatalf("ParseCurrentDrop: %v", err)
	}
	if p != nil {
		t.Errorf("expected nil progress for no session, got %+v", p)
	}
}

func TestDetectOperation_Single(t *testing.T) {
	body := []byte(`{"operationName": "CurrentDrop", "variables": {}}`)
	ops := DetectOperation(body)
	if len(ops) != 1 || ops[0] != OpCurrentDrop {
		t.Errorf("DetectOperation single = %v, want [%s]", ops, OpCurrentDrop)
	}
}

func TestDetectOperation_Batched(t *testing.T) {
	body := readFixture(t, "batched_request.json")
	ops := DetectOperation(body)
	want := []string{OpCampaigns, OpInventory}
	if len(ops) != len(want) {
		t.Fatalf("DetectOperation batched = %v, want %v", ops, want)
	}
	for i := range want {
		if ops[i] != want[i] {
			t.Errorf("op[%d] = %q, want %q", i, ops[i], want[i])
		}
	}
}

func TestDetectOperation_Garbage(t *testing.T) {
	ops := DetectOperation([]byte(`not json at all`))
	if ops != nil {
		t.Errorf("expected nil ops for garbage input, got %v", ops)
	}
}

func TestRecognizedAndAllOps(t *testing.T) {
	if !Recognized(OpClaimDropRewards) {
		t.Errorf("expected OpClaimDropRewards to be recognized")
	}
	if Recognized("SomeUnknownOp") {
		t.Errorf("expected unknown op to be unrecognized")
	}
	if len(AllOps) != 5 {
		t.Errorf("AllOps length = %d, want 5", len(AllOps))
	}
}

// Truncated/garbage top-level JSON must produce an error from every parser.
func TestParsers_GarbageInput(t *testing.T) {
	garbage := []byte(`{"data": {`) // truncated

	if _, err := ParseCampaigns(garbage); err == nil {
		t.Errorf("ParseCampaigns: expected error on truncated JSON")
	}
	if _, _, err := ParseCampaignDetails(garbage); err == nil {
		t.Errorf("ParseCampaignDetails: expected error on truncated JSON")
	}
	if _, err := ParseInventory(garbage); err == nil {
		t.Errorf("ParseInventory: expected error on truncated JSON")
	}
	if _, err := ParseCurrentDrop(garbage); err == nil {
		t.Errorf("ParseCurrentDrop: expected error on truncated JSON")
	}
}

// Missing optional fields must degrade gracefully, not fail the parse.
func TestParsers_MissingOptionalFields(t *testing.T) {
	// Campaigns entry with no game, no self, no timestamps.
	body := []byte(`{"data": {"currentUser": {"dropCampaigns": [{"id": "c1", "name": "Minimal", "status": "ACTIVE"}]}}}`)
	campaigns, err := ParseCampaigns(body)
	if err != nil {
		t.Fatalf("ParseCampaigns degraded case: %v", err)
	}
	if len(campaigns) != 1 || campaigns[0].ID != "c1" {
		t.Errorf("campaigns = %+v", campaigns)
	}
	if !campaigns[0].StartAt.IsZero() {
		t.Errorf("expected zero StartAt for missing field")
	}

	// Inventory drop with no self object at all.
	invBody := []byte(`{"data": {"currentUser": {"inventory": {"dropCampaignsInProgress": [{"id": "c1", "timeBasedDrops": [{"id": "d1", "requiredMinutesWatched": 30}]}]}}}}`)
	progs, err := ParseInventory(invBody)
	if err != nil {
		t.Fatalf("ParseInventory degraded case: %v", err)
	}
	if len(progs) != 1 || progs[0].RealMinutes != 0 || progs[0].Claimable {
		t.Errorf("degraded inventory progress = %+v", progs)
	}
}
