package twitch

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/rake-pro/dropwarden/internal/browser"
)

// urlSess is a browser.Session whose tab sits on url; Navigate moves it and
// Eval answers page-context GQL with raw and the claim-button probe with mark.
type urlSess struct {
	url       string
	navigated []string
	evalURLs  []string // tab URL at each Eval
	raw       string
	marks     []string // JSON results for jsMarkClaimButton, in order (last repeats)
	markCalls int
	loadMore  []int // jsLoadMore results, in order (then 0)
	loadCalls int
	clicks    []string
}

func (s *urlSess) Navigate(_ context.Context, u string) error {
	s.navigated = append(s.navigated, u)
	s.url = u
	return nil
}
func (s *urlSess) Reload(context.Context, bool) error         { return nil }
func (s *urlSess) CurrentURL(context.Context) (string, error) { return s.url, nil }
func (s *urlSess) Eval(_ context.Context, js string, out any) error {
	s.evalURLs = append(s.evalURLs, s.url)
	if js == jsLoadMore {
		n := 0
		if s.loadCalls < len(s.loadMore) {
			n = s.loadMore[s.loadCalls]
		}
		s.loadCalls++
		*(out.(*int)) = n
		return nil
	}
	if strings.Contains(js, "data-dw-claim") {
		m := s.marks[min(s.markCalls, len(s.marks)-1)]
		s.markCalls++
		return json.Unmarshal([]byte(m), out)
	}
	if p, ok := out.(*string); ok {
		*p = s.raw
	}
	return nil
}
func (s *urlSess) Click(_ context.Context, sel string) (bool, error) {
	s.clicks = append(s.clicks, sel)
	return true, nil
}
func (s *urlSess) SeedOnNewDocument(context.Context, string) error { return nil }
func (s *urlSess) Events() <-chan browser.Event                    { return nil }
func (s *urlSess) ShiftWindow(int, int) error                      { return nil }
func (s *urlSess) SetMinimized(context.Context, bool) error        { return nil }
func (s *urlSess) Alive() bool                                     { return true }
func (s *urlSess) Close() error                                    { return nil }

func TestIsTwitchURL(t *testing.T) {
	for u, want := range map[string]bool{
		"https://www.twitch.tv/drops/inventory": true,
		"https://twitch.tv/somechannel":         true,
		"https://m.twitch.tv/":                  true,
		"about:blank":                           false,
		"":                                      false,
		"https://kick.com/drops":                false,
		"https://evil-twitch.tv/":               false,
		"https://twitch.tv.example.com/":        false,
	} {
		if got := isTwitchURL(u); got != want {
			t.Errorf("isTwitchURL(%q) = %v, want %v", u, got, want)
		}
	}
}

func TestGQLNavigatesOffSiteTabFirst(t *testing.T) {
	offSiteSettle = 0
	s := &urlSess{url: "about:blank", raw: `{"data":{"users":[]}}`}
	p := NewPages(NewController(s))

	if _, err := p.LiveChannels(context.Background(), []string{"a"}); err != nil {
		t.Fatalf("LiveChannels: %v", err)
	}
	if len(s.navigated) != 1 || s.navigated[0] != URLInventory {
		t.Fatalf("an about:blank tab should be sent to the inventory once, got %v", s.navigated)
	}
	if len(s.evalURLs) != 1 || !isTwitchURL(s.evalURLs[0]) {
		t.Fatalf("the GQL eval must run on a twitch document, ran on %v", s.evalURLs)
	}

	// Already on Twitch: no navigation.
	_, _ = p.FetchCurrentDrop(context.Background())
	if len(s.navigated) != 1 {
		t.Fatalf("a twitch tab must not be navigated, got %v", s.navigated)
	}
}

func TestGQLIIFEToleratesCookieReadFailure(t *testing.T) {
	js := gqlFetchIIFE([]byte(`{}`))
	if !strings.Contains(js, "try {\n      const m = document.cookie") || !strings.Contains(js, "auth-token cookie is unreadable") {
		t.Fatal("the cookie read must be guarded and a failure reported descriptively")
	}
}

// fastClaimPoll shrinks the claim button wait for tests.
func fastClaimPoll(t *testing.T) {
	t.Helper()
	w, e, r, g := claimPollWindow, claimPollEvery, loadMoreRounds, loadMoreGap
	claimPollWindow, claimPollEvery, loadMoreGap = 30*time.Millisecond, 10*time.Millisecond, 15*time.Millisecond
	t.Cleanup(func() { claimPollWindow, claimPollEvery, loadMoreRounds, loadMoreGap = w, e, r, g })
}

const markMiss = `{"found":false,"matches":0,"candidates":["claimed","details"]}`

func TestClaimInInventoryReturnsCandidatesOnMiss(t *testing.T) {
	fastClaimPoll(t)
	s := &urlSess{url: URLInventory, marks: []string{markMiss}, loadMore: []int{1, 1}}
	ok, cands, err := NewController(s).ClaimInInventory(context.Background(), "button", "Claim Now")
	if err != nil || ok {
		t.Fatalf("a miss is (false, nil), got %v %v", ok, err)
	}
	if len(cands) != 2 || cands[0] != "claimed" {
		t.Fatalf("candidates should pass through, got %v", cands)
	}
	if len(s.clicks) != 0 {
		t.Fatal("nothing may be clicked on a miss")
	}
	if s.markCalls < 3 {
		t.Fatalf("the button should be polled, probed %d times", s.markCalls)
	}
	if s.loadCalls != 3 {
		t.Fatalf("Load More rounds should stop once none is left (2 clicked + 1 empty), got %d", s.loadCalls)
	}

	s = &urlSess{url: URLInventory, marks: []string{`{"found":true,"matches":3,"tier":"claim"}`}}
	ok, cands, err = NewController(s).ClaimInInventory(context.Background(), "button", "Claim Now")
	if err != nil || !ok || cands != nil || len(s.clicks) != 1 || s.clicks[0] != selMarkedClaim {
		t.Fatalf("a looser-tier hit should click the tagged element: ok=%v cands=%v err=%v clicks=%v", ok, cands, err, s.clicks)
	}
}

func TestMarkClaimButtonTiers(t *testing.T) {
	js := jsMarkClaimButton(`button:has([data-a-target="tw-core-button-label-text"])`, "  Claim   Now ")
	for _, want := range []string{
		`replace(/\s+/g, ' ')`, // whitespace collapsed
		`pick('exact'`, `pick('contains'`, `pick('claim'`,
		`getAttribute('aria-label')`,
		`!s.includes('claimed')`,
		`candidates.length >= 12`,
		`"  Claim   Now "`, // injected as a JSON literal, normalized in JS
	} {
		if !strings.Contains(js, want) {
			t.Errorf("jsMarkClaimButton missing %q", want)
		}
	}
}

func TestClaimInInventoryPollsThenLoadsMore(t *testing.T) {
	fastClaimPoll(t)
	claimPollWindow, loadMoreGap = 0, 0 // one probe per window: deterministic
	// Not rendered after the settle nor after round 1; appears after round 2.
	hit := `{"found":true,"matches":1,"tier":"exact"}`
	s := &urlSess{url: URLInventory, marks: []string{markMiss, markMiss, hit}, loadMore: []int{2, 1, 1}}
	ok, _, err := NewController(s).ClaimInInventory(context.Background(), "button", "Claim Now")
	if err != nil || !ok {
		t.Fatalf("the button should be found after Load More: ok=%v err=%v", ok, err)
	}
	if s.loadCalls != 2 || s.markCalls != 3 {
		t.Fatalf("want 2 Load More rounds and 3 probes, got %d and %d", s.loadCalls, s.markCalls)
	}
	if len(s.clicks) != 1 || s.clicks[0] != selMarkedClaim {
		t.Fatalf("the tagged button should be clicked once, got %v", s.clicks)
	}
}
