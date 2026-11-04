package kick

import (
	"strings"
	"testing"
)

func TestClaimIDRoundTrip(t *testing.T) {
	id := ClaimIDFor("01C", "01R")
	c, r, ok := splitClaimID(id)
	if !ok || c != "01C" || r != "01R" {
		t.Fatalf("round trip: %q -> %q %q %v", id, c, r, ok)
	}
	if _, _, ok := splitClaimID("kick-01C-01R"); ok {
		t.Fatal("a drop id must not pass as a claim id")
	}
}

func TestClaimJSCarriesIdsAndAuth(t *testing.T) {
	js := jsClaimReward("01C", "01R")
	// The body is embedded as a JS string literal, so its quotes are escaped.
	for _, want := range []string{`\"reward_id\":\"01R\"`, `\"campaign_id\":\"01C\"`, "Bearer", "session_token", "x-app-platform", "method: 'POST'", claimAPI} {
		if !strings.Contains(js, want) {
			t.Fatalf("claim JS lacks %q:\n%s", want, js)
		}
	}
}
