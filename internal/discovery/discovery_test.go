package discovery

import (
	"testing"
	"time"

	"github.com/rs/zerolog"

	"github.com/rake-pro/dropwarden/internal/config"
	"github.com/rake-pro/dropwarden/internal/domain"
	"github.com/rake-pro/dropwarden/internal/store"
)

func rustCampaign(id, dropID, reward string) domain.Campaign {
	return domain.Campaign{
		ID: id, Name: "Rust " + id, GameName: "Rust", Platform: domain.PlatformTwitch,
		Status: domain.StatusActive, EndAt: time.Now().Add(48 * time.Hour),
		Drops: []domain.Drop{{ID: dropID, CampaignID: id, Name: "Tier 1", RequiredMinutes: 60,
			Benefits: []domain.Benefit{{ID: id + "-b", Name: reward}}}},
	}
}

func TestPinRuleReappliedToNewCampaignInstance(t *testing.T) {
	st, err := store.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	cfg := &config.Config{Games: []string{"Rust"}, Queue: config.QueueConfig{AutoEnqueueNew: true}}
	log := zerolog.Nop()

	old := rustCampaign("c-old", "d-old", "Hazmat Skin")
	other := rustCampaign("c-other", "d-other", "Door Skin")
	for _, c := range []domain.Campaign{old, other} {
		if err := st.UpsertCampaign(c); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := ReconcileQueue(st, cfg, domain.PlatformTwitch, log); err != nil {
		t.Fatal(err)
	}

	// The user stars the old instance's drop: pin + rule.
	if err := st.SetPin("d-old", 1); err != nil {
		t.Fatal(err)
	}
	game, reward := PinKey(old, old.Drops[0])
	if game != "rust" || reward != "hazmat skin" {
		t.Fatalf("PinKey = %q, %q", game, reward)
	}
	if err := st.UpsertPinRule(store.PinRule{Platform: domain.PlatformTwitch, GameName: game, RewardKey: reward, Rank: 1}); err != nil {
		t.Fatal(err)
	}

	// The old campaign ends; a new instance re-offers the same reward under new
	// IDs (different casing/whitespace in the name, same normalized reward).
	old.EndAt = time.Now().Add(-time.Hour)
	if err := st.UpsertCampaign(old); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertCampaign(rustCampaign("c-new", "d-new", "  HAZMAT skin ")); err != nil {
		t.Fatal(err)
	}
	if _, err := ReconcileQueue(st, cfg, domain.PlatformTwitch, log); err != nil {
		t.Fatal(err)
	}

	q, _ := st.GetQueue()
	pins := map[string]int{}
	for _, it := range q {
		pins[it.DropID] = it.Pin
	}
	if _, stillQueued := pins["d-old"]; stillQueued {
		t.Fatalf("expired instance should be pruned: %v", pins)
	}
	if pins["d-new"] != 1 {
		t.Fatalf("new instance should inherit the pin via the rule, pins = %v", pins)
	}
	if pins["d-other"] != 0 {
		t.Fatalf("an unrelated reward must stay unpinned, pins = %v", pins)
	}
}

func TestPinKeySortsBenefitsAndFallsBackToDropName(t *testing.T) {
	c := domain.Campaign{GameName: " Rust "}
	_, a := PinKey(c, domain.Drop{Benefits: []domain.Benefit{{Name: "B"}, {Name: "a"}}})
	_, b := PinKey(c, domain.Drop{Benefits: []domain.Benefit{{Name: "A"}, {Name: "b "}}})
	if a != b || a != "a\nb" {
		t.Fatalf("reward keys %q vs %q", a, b)
	}
	if _, r := PinKey(c, domain.Drop{Name: "Tier 3"}); r != "tier 3" {
		t.Fatalf("fallback key = %q", r)
	}
}

func TestMarkOwnedSkipsDropEarnedInThisInstance(t *testing.T) {
	st, err := store.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	d := &Discoverer{store: st, logger: zerolog.Nop()}

	done := rustCampaign("c-done", "d-done", "Hazmat Skin")
	prior := rustCampaign("c-prior", "d-prior", "Door Skin")
	for _, c := range []domain.Campaign{done, prior} {
		if err := st.UpsertCampaign(c); err != nil {
			t.Fatal(err)
		}
	}
	// d-done finished its 60/60 in this instance but is not claimed yet (a stale
	// Owned flag from an older build must heal); d-prior has no progress here.
	if err := st.SaveProgress(domain.Progress{DropID: "d-done", RealMinutes: 60, RequiredMinutes: 60,
		Source: domain.SourceInventory, Owned: true}); err != nil {
		t.Fatal(err)
	}
	// The earned inventory lists both rewards: d-done's own, d-prior's from an
	// earlier campaign instance.
	d.markOwnedDrops(map[string]bool{"hazmat skin": true, "door skin": true})

	if p, _ := st.GetProgress("d-done"); p == nil || p.Owned {
		t.Fatalf("a drop complete in this instance must not be marked owned by its own reward: %+v", p)
	}
	if p, _ := st.GetProgress("d-prior"); p == nil || !p.Owned {
		t.Fatalf("a reward held from a prior instance must be marked owned: %+v", p)
	}
}
