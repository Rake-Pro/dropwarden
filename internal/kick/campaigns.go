package kick

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/rake-pro/dropwarden/internal/domain"
)

// The campaigns endpoint is undocumented, so this parser is written against a
// GUESSED response shape with alternate key spellings and degrades field by
// field rather than failing the whole parse. The raw body is always dumped by
// the discoverer when capture_gql_bodies is on - the first live run's dump is
// what corrects this file. verify live: every json tag below.

// flexID accepts both numeric and string ids (the endpoint's id type is
// unverified). It renders as the bare digits/string either way.
type flexID string

func (f *flexID) UnmarshalJSON(b []byte) error {
	*f = flexID(bytes.Trim(bytes.TrimSpace(b), `"`))
	return nil
}

func (f flexID) String() string { return string(f) }

type kcCategory struct {
	ID   flexID `json:"id"`
	Name string `json:"name"`
	Slug string `json:"slug"`
}

type kcChannel struct {
	ID       flexID `json:"id"`
	Slug     string `json:"slug"`
	Username string `json:"username"`
}

type kcReward struct {
	ID    flexID `json:"id"`
	Name  string `json:"name"`
	Title string `json:"title"`
	// RequiredUnits is what the live endpoint sends (minutes under the
	// "Watch to redeem" rule, see catalog/testdata/kick_campaigns.json); the
	// three names below were guesses from before a real capture existed.
	RequiredUnits   int    `json:"required_units"`
	RequiredMinutes int    `json:"required_minutes"`
	WatchMinutes    int    `json:"watch_time_minutes"`
	MinutesRequired int    `json:"minutes_required"`
	ImageURL        string `json:"image_url"`
}

type kcRule struct {
	ID   flexID `json:"id"`
	Name string `json:"name"`
}

type kcCampaign struct {
	ID       flexID      `json:"id"`
	Name     string      `json:"name"`
	Title    string      `json:"title"`
	Status   string      `json:"status"`
	Rule     *kcRule     `json:"rule"`
	Category *kcCategory `json:"category"`
	StartsAt string      `json:"starts_at"`
	StartAt  string      `json:"start_time"`
	EndsAt   string      `json:"ends_at"`
	EndAt    string      `json:"end_time"`
	Channels []kcChannel `json:"channels"`
	Rewards  []kcReward  `json:"rewards"`
	Drops    []kcReward  `json:"drops"`
}

type kcResponse struct {
	Data []kcCampaign `json:"data"`
}

// ParseCampaigns parses the campaigns API body into domain campaigns tagged
// PlatformKick. It accepts either a bare array or a {"data": [...]} wrapper.
func ParseCampaigns(body []byte) ([]domain.Campaign, error) {
	trimmed := strings.TrimSpace(string(body))
	var list []kcCampaign
	if strings.HasPrefix(trimmed, "[") {
		if err := json.Unmarshal([]byte(trimmed), &list); err != nil {
			return nil, fmt.Errorf("kick: parse campaigns array: %w", err)
		}
	} else {
		var resp kcResponse
		if err := json.Unmarshal([]byte(trimmed), &resp); err != nil {
			return nil, fmt.Errorf("kick: parse campaigns response: %w", err)
		}
		list = resp.Data
	}

	out := make([]domain.Campaign, 0, len(list))
	for _, kc := range list {
		c := domain.Campaign{
			ID:       "kick-" + kc.ID.String(),
			Platform: domain.PlatformKick,
			Name:     firstNonEmpty(kc.Title, kc.Name),
			StartAt:  parseTime(firstNonEmpty(kc.StartsAt, kc.StartAt)),
			EndAt:    parseTime(firstNonEmpty(kc.EndsAt, kc.EndAt)),
			Status:   kickStatus(kc.Status),
		}
		if kc.Category != nil {
			c.GameID = kc.Category.ID.String()
			c.GameName = kc.Category.Name
			c.GameSlug = kc.Category.Slug
		}
		for _, ch := range kc.Channels {
			login := strings.ToLower(firstNonEmpty(ch.Slug, ch.Username))
			if login != "" {
				c.AllowedChannels = append(c.AllowedChannels, domain.Channel{Login: login})
			}
		}
		rewards := kc.Rewards
		if len(rewards) == 0 {
			rewards = kc.Drops
		}
		for _, r := range rewards {
			// Units are watch minutes only under a watch rule; any other rule
			// (subs, gifts) leaves the drop at zero minutes so the queue skips
			// it as event-based, the same as Twitch's zero-minute drops.
			mins := 0
			if watchRule(kc.Rule) {
				mins = r.RequiredUnits
			}
			if mins == 0 {
				mins = r.RequiredMinutes
			}
			if mins == 0 {
				mins = r.WatchMinutes
			}
			if mins == 0 {
				mins = r.MinutesRequired
			}
			c.Drops = append(c.Drops, domain.Drop{
				ID:              "kick-" + kc.ID.String() + "-" + r.ID.String(),
				CampaignID:      c.ID,
				Name:            firstNonEmpty(r.Title, r.Name),
				RequiredMinutes: mins,
				Benefits:        []domain.Benefit{{ID: r.ID.String(), Name: firstNonEmpty(r.Title, r.Name), ImageURL: r.ImageURL}},
			})
		}
		out = append(out, c)
	}
	return out, nil
}

// kickStatus maps the list endpoint's status string; unknown values are
// treated as active and left to the campaign window checks, as before.
func kickStatus(s string) domain.CampaignStatus {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "expired", "ended", "completed":
		return domain.StatusExpired
	case "upcoming", "scheduled":
		return domain.StatusUpcoming
	}
	return domain.StatusActive
}

// watchRule reports whether a campaign's rule is time-based. A missing rule
// is treated as watch-based so a payload without the field keeps working.
func watchRule(r *kcRule) bool {
	return r == nil || strings.Contains(strings.ToLower(r.Name), "watch")
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// parseTime accepts RFC3339 and a couple of laravel-ish spellings, returning
// the zero time on failure (timestamps are best-effort).
func parseTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05.000000Z", "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}
