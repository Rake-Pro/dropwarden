package kick

import (
	"testing"

	"github.com/rake-pro/dropwarden/internal/domain"
)

func TestParseCampaignsWrappedShape(t *testing.T) {
	body := []byte(`{"data":[{
		"id": 42,
		"name": "Rust Kickoff",
		"category": {"id": 7, "name": "Rust", "slug": "rust"},
		"starts_at": "2026-08-01T00:00:00Z",
		"ends_at": "2026-09-01T00:00:00Z",
		"channels": [{"id": 1, "slug": "Trainwreckstv"}, {"id": 2, "username": "buddha"}],
		"rewards": [
			{"id": 100, "name": "Door Skin", "required_minutes": 60, "image_url": "https://x/skin.png"},
			{"id": 101, "title": "AK Skin", "watch_time_minutes": 240}
		]
	}]}`)
	cs, err := ParseCampaigns(body)
	if err != nil {
		t.Fatalf("ParseCampaigns: %v", err)
	}
	if len(cs) != 1 {
		t.Fatalf("expected 1 campaign, got %d", len(cs))
	}
	c := cs[0]
	if c.Platform != domain.PlatformKick || c.ID != "kick-42" {
		t.Fatalf("id/platform wrong: %+v", c)
	}
	if c.GameName != "Rust" || c.GameSlug != "rust" {
		t.Fatalf("category wrong: %+v", c)
	}
	if len(c.AllowedChannels) != 2 || c.AllowedChannels[0].Login != "trainwreckstv" || c.AllowedChannels[1].Login != "buddha" {
		t.Fatalf("channels wrong: %+v", c.AllowedChannels)
	}
	if len(c.Drops) != 2 || c.Drops[0].RequiredMinutes != 60 || c.Drops[1].RequiredMinutes != 240 {
		t.Fatalf("drops wrong: %+v", c.Drops)
	}
	if c.Drops[1].Name != "AK Skin" {
		t.Fatalf("title fallback wrong: %+v", c.Drops[1])
	}
	if c.StartAt.IsZero() || c.EndAt.IsZero() {
		t.Fatalf("times not parsed: %+v", c)
	}
}

func TestParseCampaignsBareArrayAndAnyChannel(t *testing.T) {
	body := []byte(`[{"id": "abc", "title": "Global Drops", "category": {"id": 7, "name": "Rust", "slug": "rust"}, "drops": [{"id": 1, "name": "Global Skin", "minutes_required": 120}]}]`)
	cs, err := ParseCampaigns(body)
	if err != nil {
		t.Fatalf("ParseCampaigns: %v", err)
	}
	if len(cs) != 1 || cs[0].Name != "Global Drops" {
		t.Fatalf("bare array parse wrong: %+v", cs)
	}
	if !cs[0].AnyChannel() {
		t.Fatal("no channels listed must mean any-channel")
	}
	if len(cs[0].Drops) != 1 || cs[0].Drops[0].RequiredMinutes != 120 {
		t.Fatalf("drops alias key wrong: %+v", cs[0].Drops)
	}
}

// TestParseCampaignsLiveShape uses the field names the live endpoint sends
// (see internal/catalog/testdata/kick_campaigns.json): required_units under a
// "Watch to redeem" rule, a status string, and channels carrying slug + user.
func TestParseCampaignsLiveShape(t *testing.T) {
	body := []byte(`{"data":[
		{"id":"01ABC","name":"MW4 Launch","status":"active","rule":{"id":1,"name":"Watch to redeem"},
		 "category":{"id":9,"name":"Call of Duty: Modern Warfare 4","slug":"call-of-duty-modern-warfare-4"},
		 "starts_at":"2026-10-23T17:00:00Z","ends_at":"2026-11-05T16:59:00Z",
		 "channels":[{"id":1,"slug":"streamer-one","user":{"id":1,"username":"streamer-one"}}],
		 "rewards":[{"id":"R1","name":"Point Man","required_units":20,"category_id":9},{"id":"R2","name":"Deployment Ready","required_units":45}]},
		{"id":"01OLD","name":"Kick Off 2","status":"expired","rule":{"id":1,"name":"Watch to redeem"},
		 "rewards":[{"id":"R3","name":"Large Box","required_units":120}]},
		{"id":"01SUB","name":"Sub event","status":"active","rule":{"id":2,"name":"Subscribe to redeem"},
		 "rewards":[{"id":"R4","name":"Badge","required_units":1}]}
	]}`)
	cs, err := ParseCampaigns(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) != 3 {
		t.Fatalf("want 3 campaigns, got %d", len(cs))
	}
	mw := cs[0]
	if mw.GameName != "Call of Duty: Modern Warfare 4" || mw.Status != domain.StatusActive {
		t.Fatalf("game/status: %q %q", mw.GameName, mw.Status)
	}
	if len(mw.Drops) != 2 || mw.Drops[0].RequiredMinutes != 20 || mw.Drops[1].RequiredMinutes != 45 {
		t.Fatalf("required_units must become minutes: %+v", mw.Drops)
	}
	if len(mw.AllowedChannels) != 1 || mw.AllowedChannels[0].Login != "streamer-one" {
		t.Fatalf("channels: %+v", mw.AllowedChannels)
	}
	if cs[1].Status != domain.StatusExpired {
		t.Fatalf("expired status must map: %q", cs[1].Status)
	}
	if cs[2].Drops[0].RequiredMinutes != 0 {
		t.Fatalf("a non-watch rule must leave minutes at zero (event-based): %+v", cs[2].Drops)
	}
}
