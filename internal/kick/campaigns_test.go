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
