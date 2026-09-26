package store

import (
	"fmt"
	"time"

	"github.com/rake-pro/dropwarden/internal/domain"
)

// PinRule is a persisted "keep this reward pinned" rule: it identifies a drop
// by what it grants rather than by its campaign-instance-scoped IDs, so a
// later campaign offering the same reward can be re-pinned automatically.
// GameName and RewardKey are stored already normalized by the caller.
type PinRule struct {
	Platform  domain.Platform
	GameName  string
	RewardKey string
	Rank      int
}

// UpsertPinRule creates a rule or updates its rank.
func (s *Store) UpsertPinRule(r PinRule) error {
	_, err := s.db.Exec(`INSERT INTO drop_pins (platform,game_name,reward_key,rank,created_at)
		VALUES (?,?,?,?,?)
		ON CONFLICT(platform,game_name,reward_key) DO UPDATE SET rank=excluded.rank`,
		string(r.Platform.OrTwitch()), r.GameName, r.RewardKey, r.Rank, time.Now().UnixMilli())
	if err != nil {
		return fmt.Errorf("store: upsert pin rule: %w", err)
	}
	return nil
}

// DeletePinRule removes a rule; deleting a missing rule is not an error.
func (s *Store) DeletePinRule(pf domain.Platform, gameName, rewardKey string) error {
	_, err := s.db.Exec(`DELETE FROM drop_pins WHERE platform=? AND game_name=? AND reward_key=?`,
		string(pf.OrTwitch()), gameName, rewardKey)
	if err != nil {
		return fmt.Errorf("store: delete pin rule: %w", err)
	}
	return nil
}

// ListPinRules returns one platform's rules in rank order.
func (s *Store) ListPinRules(pf domain.Platform) ([]PinRule, error) {
	rows, err := s.db.Query(`SELECT platform,game_name,reward_key,rank FROM drop_pins WHERE platform=? ORDER BY rank, game_name, reward_key`,
		string(pf.OrTwitch()))
	if err != nil {
		return nil, fmt.Errorf("store: list pin rules: %w", err)
	}
	defer rows.Close()
	var out []PinRule
	for rows.Next() {
		var r PinRule
		var p string
		if err := rows.Scan(&p, &r.GameName, &r.RewardKey, &r.Rank); err != nil {
			return nil, fmt.Errorf("store: scan pin rule: %w", err)
		}
		r.Platform = domain.Platform(p).OrTwitch()
		out = append(out, r)
	}
	return out, rows.Err()
}
