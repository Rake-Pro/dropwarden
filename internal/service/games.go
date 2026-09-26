package service

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/rake-pro/dropwarden/internal/catalog"
	"github.com/rake-pro/dropwarden/internal/domain"
)

// GameDTO is one row of GET /api/games: a game with at least one cached,
// not yet ended campaign in the profile's store.
type GameDTO struct {
	Game      string   `json:"game"`
	Platforms []string `json:"platforms"` // "twitch", "kick" (twitch first)
	Campaigns int      `json:"campaigns"`
	EndsAt    string   `json:"endsAt"`  // RFC 3339 of the latest campaign end, "" when unknown
	Tracked   bool     `json:"tracked"` // in the profile's effective games list (case-insensitive)
}

// Games lists the distinct games of a profile's cached campaigns (both
// platforms), sorted by name. Empty before the first discovery pass.
func (b *Backend) Games(profile string) ([]GameDTO, error) {
	pb := b.Profile(profile)
	if pb == nil || pb.Store == nil {
		return nil, fmt.Errorf("unknown profile %q", profile)
	}
	campaigns, err := pb.Store.ListCampaigns()
	if err != nil {
		return nil, err
	}
	tracked := map[string]bool{}
	if eff, err := b.config().Effective(pb.Name); err == nil {
		for _, g := range eff.Games {
			tracked[strings.ToLower(strings.TrimSpace(g))] = true
		}
	}
	return gamesFrom(campaigns, tracked, time.Now()), nil
}

func gamesFrom(campaigns []domain.Campaign, tracked map[string]bool, now time.Time) []GameDTO {
	type agg struct {
		dto   GameDTO
		plats map[string]bool
		end   time.Time
	}
	byKey := map[string]*agg{}
	for _, c := range campaigns {
		name := strings.TrimSpace(c.GameName)
		if name == "" || c.Status == domain.StatusExpired || (!c.EndAt.IsZero() && c.EndAt.Before(now)) {
			continue
		}
		key := strings.ToLower(name)
		a := byKey[key]
		if a == nil {
			a = &agg{dto: GameDTO{Game: name, Tracked: tracked[key]}, plats: map[string]bool{}}
			byKey[key] = a
		}
		a.dto.Campaigns++
		a.plats[string(c.Platform.OrTwitch())] = true
		if c.EndAt.After(a.end) {
			a.end = c.EndAt
		}
	}
	out := make([]GameDTO, 0, len(byKey))
	for _, a := range byKey {
		a.dto.Platforms = []string{}
		for _, p := range platformOrder {
			if a.plats[string(p)] {
				a.dto.Platforms = append(a.dto.Platforms, string(p))
			}
		}
		if !a.end.IsZero() {
			a.dto.EndsAt = a.end.UTC().Format(time.RFC3339)
		}
		out = append(out, a.dto)
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Game) < strings.ToLower(out[j].Game) })
	return out
}

// catalogEntries turns /api/games rows into the catalogue's own-source input.
func catalogEntries(games []GameDTO) []catalog.Entry {
	out := make([]catalog.Entry, 0, len(games))
	for _, g := range games {
		end, _ := time.Parse(time.RFC3339, g.EndsAt)
		out = append(out, catalog.Entry{Game: g.Game, Platforms: g.Platforms, Campaigns: g.Campaigns, EndsAt: end})
	}
	return out
}
