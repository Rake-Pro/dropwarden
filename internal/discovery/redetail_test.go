package discovery

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"github.com/rake-pro/dropwarden/internal/browser"
	"github.com/rake-pro/dropwarden/internal/config"
	"github.com/rake-pro/dropwarden/internal/domain"
	"github.com/rake-pro/dropwarden/internal/platform"
	"github.com/rake-pro/dropwarden/internal/store"
	"github.com/rake-pro/dropwarden/internal/twitch"
)

// evalSession is a browser.Session whose Eval answers page-context GQL from a
// responder keyed on the JS source.
type evalSession struct {
	respond func(js string) string
	evals   []string
}

func (s *evalSession) Navigate(context.Context, string) error { return nil }
func (s *evalSession) Reload(context.Context, bool) error     { return nil }
func (s *evalSession) CurrentURL(context.Context) (string, error) {
	return "https://www.twitch.tv/drops/campaigns", nil
}
func (s *evalSession) Click(context.Context, string) (bool, error)     { return false, nil }
func (s *evalSession) SeedOnNewDocument(context.Context, string) error { return nil }
func (s *evalSession) Events() <-chan browser.Event                    { return nil }
func (s *evalSession) ShiftWindow(int, int) error                      { return nil }
func (s *evalSession) SetMinimized(context.Context, bool) error        { return nil }
func (s *evalSession) Alive() bool                                     { return true }
func (s *evalSession) Close() error                                    { return nil }
func (s *evalSession) Eval(_ context.Context, js string, out any) error {
	s.evals = append(s.evals, js)
	if out == nil {
		return nil
	}
	b, _ := json.Marshal(s.respond(js))
	return json.Unmarshal(b, out)
}

type evalCtrl struct{ sess *evalSession }

func (c evalCtrl) Session() browser.Session                                       { return c.sess }
func (c evalCtrl) Probe(context.Context) (platform.Health, error)                 { return platform.Health{}, nil }
func (c evalCtrl) ApplyPlayerPolicy(context.Context, platform.PlayerPolicy) error { return nil }
func (c evalCtrl) ReassertVolume(context.Context, platform.PlayerPolicy) error    { return nil }
func (c evalCtrl) ClickPlay(context.Context) error                                { return nil }
func (c evalCtrl) ToggleDropsPanel(context.Context) error                         { return nil }
func (c evalCtrl) CancelRaid(context.Context) (bool, error)                       { return false, nil }
func (c evalCtrl) ClaimInInventory(context.Context, string, string) (bool, []string, error) {
	return false, nil, nil
}

func detailsCount(s *evalSession) int {
	n := 0
	for _, js := range s.evals {
		if strings.Contains(js, "dropCampaign(id") {
			n++
		}
	}
	return n
}

func TestSetConfigReachesReconcileQueue(t *testing.T) {
	st, err := store.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	d := New(st, nil, &config.Config{Games: []string{"Rust"}, Queue: config.QueueConfig{AutoEnqueueNew: true}}, zerolog.Nop())
	halo := rustCampaign("c-halo", "d-halo", "Armor")
	halo.GameName = "Halo Infinite"
	if err := st.UpsertCampaign(halo); err != nil {
		t.Fatal(err)
	}
	if n, err := d.reconcileQueue(context.Background()); err != nil || n != 0 {
		t.Fatalf("halo is off the filter: enqueued %d, err %v", n, err)
	}
	d.SetConfig(&config.Config{Games: []string{"Rust", "Halo Infinite"}, Queue: config.QueueConfig{AutoEnqueueNew: true}})
	if n, err := d.reconcileQueue(context.Background()); err != nil || n != 1 {
		t.Fatalf("after SetConfig the new game's drop must be enqueued: %d, err %v", n, err)
	}
}

// A campaign whose drops are all done falls off dropCampaignsInProgress, so
// its complete-but-unclaimed needs_action row only learns isClaimed from a
// re-detail. The second pass must not fetch again.
func TestRedetailBooksFinishedCampaignClaimed(t *testing.T) {
	st, err := store.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	c := rustCampaign("c-fin", "d-fin", "Hazmat Skin")
	if err := st.UpsertCampaign(c); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertQueueItem(domain.QueueItem{DropID: "d-fin", CampaignID: "c-fin", Platform: domain.PlatformTwitch,
		Position: 1, Enabled: true, Status: domain.ItemNeedsAction, Reason: "completed - claim it manually in the Twitch inventory"}); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveProgress(domain.Progress{DropID: "d-fin", RealMinutes: 60, RequiredMinutes: 60, Source: domain.SourceInventory,
		ObservedAt: time.Now().Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	sess := &evalSession{respond: func(js string) string {
		if strings.Contains(js, "dropCampaign(id") {
			return `{"data":{"user":{"dropCampaign":{"id":"c-fin","name":"Rust c-fin","status":"ACTIVE","game":{"id":"g","displayName":"Rust","slug":"rust"},"self":{"isAccountConnected":true},"timeBasedDrops":[{"id":"d-fin","name":"Tier 1","requiredMinutesWatched":60,"self":{"currentMinutesWatched":60,"isClaimed":true,"dropInstanceID":"u#c-fin#d-fin"}}]}}}}`
		}
		return `{"data":{"currentUser":{"login":"me"}}}`
	}}
	cfg := &config.Config{Games: []string{"Rust"}}
	d := New(st, twitch.NewPages(evalCtrl{sess}), cfg, zerolog.Nop())

	n, _, err := d.redetailUnclaimed(context.Background(), cfg)
	if err != nil || n != 1 {
		t.Fatalf("first pass must re-detail the finished campaign: n=%d err=%v", n, err)
	}
	if p, _ := st.GetProgress("d-fin"); p == nil || p.ClaimedAt == nil {
		t.Fatalf("re-detail must record isClaimed: %+v", p)
	}
	before := detailsCount(sess)
	if n, _, err := d.redetailUnclaimed(context.Background(), cfg); err != nil || n != 0 || detailsCount(sess) != before {
		t.Fatalf("second pass must be a no-op: n=%d err=%v fetches %d -> %d", n, err, before, detailsCount(sess))
	}
}

func TestRedetailLoggedOutSurfaces(t *testing.T) {
	st, err := store.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	_ = st.UpsertCampaign(rustCampaign("c1", "d1", "Skin"))
	_ = st.UpsertQueueItem(domain.QueueItem{DropID: "d1", CampaignID: "c1", Platform: domain.PlatformTwitch, Enabled: true, Status: domain.ItemNeedsAction})
	_ = st.SaveProgress(domain.Progress{DropID: "d1", RealMinutes: 60, RequiredMinutes: 60, Source: domain.SourceInventory})
	sess := &evalSession{respond: func(string) string { return `{"data":{"currentUser":null}}` }}
	cfg := &config.Config{Games: []string{"Rust"}}
	d := New(st, twitch.NewPages(evalCtrl{sess}), cfg, zerolog.Nop())
	if _, _, err := d.redetailUnclaimed(context.Background(), cfg); err == nil || !strings.Contains(err.Error(), domain.ErrLoggedOut.Error()) {
		t.Fatalf("a null currentUser must surface ErrLoggedOut, got %v", err)
	}
}

func TestOwnedAndCompleteBooksClaimed(t *testing.T) {
	st, err := store.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	d := &Discoverer{store: st, logger: zerolog.Nop()}
	if err := st.UpsertCampaign(rustCampaign("c-done", "d-done", "Hazmat Skin")); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveProgress(domain.Progress{DropID: "d-done", RealMinutes: 60, RequiredMinutes: 60, Source: domain.SourceInventory, Claimable: true}); err != nil {
		t.Fatal(err)
	}
	d.markOwnedDrops(map[string]bool{"hazmat skin": true})
	p, _ := st.GetProgress("d-done")
	if p == nil || p.ClaimedAt == nil || p.Claimable || p.Owned {
		t.Fatalf("complete here + reward held must book claimed (not owned): %+v", p)
	}
	first := *p.ClaimedAt
	d.markOwnedDrops(map[string]bool{"hazmat skin": true})
	if p2, _ := st.GetProgress("d-done"); p2 == nil || p2.ClaimedAt == nil || !p2.ClaimedAt.Equal(first) {
		t.Fatalf("second pass must be a no-op: %+v", p2)
	}
}
