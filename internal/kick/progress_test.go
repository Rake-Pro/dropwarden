package kick

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/rake-pro/dropwarden/internal/domain"
)

// Field names come from the drops page's reward card (progress fraction,
// claimed, required_units) and the campaign status strings it switches on.
const progressBody = `{"data":[
 {"id":"01C","name":"Modern Warfare 4","status":"in progress","rewards":[
   {"id":"01R","name":"MW4 Ghost Badge","progress":0.002,"claimed":false,"required_units":3000}]},
 {"id":"01D","name":"Chat Emote - True Damage","status":"in progress","rewards":[
   {"id":"01E","name":"Chat Emote - True Damage","progress":1,"claimed":false,"required_units":30},
   {"id":"01F","name":"Old Emote","progress":1,"claimed":true,"required_units":30}]}
]}`

func TestParseProgress(t *testing.T) {
	now := time.Now()
	rows, err := ParseProgress([]byte(progressBody), now)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("want 3 rows, got %+v", rows)
	}
	badge := rows[0]
	if badge.DropID != "kick-01C-01R" || badge.RequiredMinutes != 3000 || badge.RealMinutes != 6 || badge.Claimable || badge.ClaimedAt != nil {
		t.Fatalf("badge row: %+v", badge)
	}
	if badge.Source != domain.SourceInventory {
		t.Fatalf("rows must be server-confirmed: %q", badge.Source)
	}
	if emote := rows[1]; !emote.Claimable || emote.RealMinutes != 30 || emote.ClaimedAt != nil {
		t.Fatalf("complete unclaimed reward must be claimable: %+v", emote)
	}
	if old := rows[2]; old.ClaimedAt == nil || old.Claimable {
		t.Fatalf("claimed reward must carry ClaimedAt: %+v", old)
	}
}

func TestParseProgressResponseAuth(t *testing.T) {
	_, err := ParseProgressResponse(`{"status":403,"body":"{\"data\":{\"type\":\"Forbidden\"}}","token":false}`, time.Now())
	if err == nil || errors.Is(err, domain.ErrLoggedOut) {
		t.Fatalf("403 must be a plain poll error, never a logged-out verdict (it paused a logged-in engine), got %v", err)
	}
	if !strings.Contains(err.Error(), "cookie present: false") {
		t.Fatalf("error must say whether the session cookie was there: %v", err)
	}
	if !strings.Contains(jsFetchProgress, "Bearer") || !strings.Contains(jsFetchProgress, "session_token") {
		t.Fatal("progress fetch must send the session_token cookie as a bearer token")
	}
	rows, err := ParseProgressResponse(`{"status":200,"body":"[]"}`, time.Now())
	if err != nil || len(rows) != 0 {
		t.Fatalf("empty inventory must be an empty list: %v %v", rows, err)
	}
}
