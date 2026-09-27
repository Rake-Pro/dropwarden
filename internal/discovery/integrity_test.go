package discovery

import (
	"context"
	"strings"
	"testing"

	"github.com/rake-pro/dropwarden/internal/store"
	"github.com/rake-pro/dropwarden/internal/twitch"
)

// A details read rejected by the integrity check gets one fresh token (the
// captured header is dropped, the campaigns page reloaded) and one retry.
func TestDetailsRetriesOnceAfterIntegrityRefresh(t *testing.T) {
	st, err := store.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.UpsertCampaign(rustCampaign("c-int", "d-int", "Hazmat")); err != nil {
		t.Fatal(err)
	}
	details := 0
	sess := &evalSession{respond: func(js string) string {
		if strings.Contains(js, "dropCampaign(id") {
			details++
			if details == 1 {
				return `{"errors":[{"message":"failed integrity check","path":["user","dropCampaign"],"extensions":{"code":"IntegrityCheckFailed"}}],"data":{"user":{"dropCampaign":null}}}`
			}
			return `{"data":{"user":{"dropCampaign":{"id":"c-int","name":"Rust c-int","status":"ACTIVE","game":{"id":"g","displayName":"Rust","slug":"rust"},"self":{"isAccountConnected":true},"timeBasedDrops":[{"id":"d-int","name":"Tier 1","requiredMinutesWatched":60,"self":{"currentMinutesWatched":60,"isClaimed":false,"dropInstanceID":"u#c-int#d-int"}}]}}}}`
		}
		return `{"data":{"currentUser":{"login":"me"}}}`
	}}
	pages := twitch.NewPages(evalCtrl{sess})
	rows, err := pages.CampaignProgress(context.Background(), "c-int", "me")
	if err != nil || len(rows) != 1 || rows[0].ClaimID != "u#c-int#d-int" {
		t.Fatalf("retry must succeed after the refresh: rows=%+v err=%v", rows, err)
	}
	if details != 2 {
		t.Fatalf("want exactly one retry, got %d details reads", details)
	}
	dropped := false
	for _, js := range sess.evals {
		if strings.Contains(js, "client-integrity") && strings.Contains(js, "delete") {
			dropped = true
		}
	}
	if !dropped {
		t.Fatal("the captured client-integrity header must be dropped before the retry")
	}
}
