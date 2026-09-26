package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/rake-pro/dropwarden/internal/domain"
)

// AppendSession records a completed watch session.
func (s *Store) AppendSession(ws domain.WatchSession) error {
	_, err := s.db.Exec(`INSERT INTO sessions
		(id,drop_id,campaign_id,platform,channel_login,started_at,ended_at,start_minutes,end_minutes,hard_refreshes,stall_events,outcome,note)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET
			ended_at=excluded.ended_at, end_minutes=excluded.end_minutes,
			hard_refreshes=excluded.hard_refreshes, stall_events=excluded.stall_events,
			outcome=excluded.outcome, note=excluded.note`,
		ws.ID, ws.DropID, ws.CampaignID, string(ws.Platform.OrTwitch()), ws.ChannelLogin,
		unixPtr(ws.StartedAt), unixPtr(ws.EndedAt), ws.StartMinutes, ws.EndMinutes,
		ws.HardRefreshes, ws.StallEvents, string(ws.Outcome), ws.Note)
	if err != nil {
		return fmt.Errorf("store: append session: %w", err)
	}
	return nil
}

// ListSessions returns the most recent sessions, newest first, up to limit
// (0 = all).
func (s *Store) ListSessions(limit int) ([]domain.WatchSession, error) {
	q := `SELECT id,drop_id,campaign_id,platform,channel_login,started_at,ended_at,start_minutes,end_minutes,hard_refreshes,stall_events,outcome,note FROM sessions ORDER BY started_at DESC`
	if limit > 0 {
		q += fmt.Sprintf(" LIMIT %d", limit)
	}
	rows, err := s.db.Query(q)
	if err != nil {
		return nil, fmt.Errorf("store: list sessions: %w", err)
	}
	defer rows.Close()
	var out []domain.WatchSession
	for rows.Next() {
		var ws domain.WatchSession
		var started, ended sql.NullInt64
		var pf, outcome string
		if err := rows.Scan(&ws.ID, &ws.DropID, &ws.CampaignID, &pf, &ws.ChannelLogin, &started, &ended,
			&ws.StartMinutes, &ws.EndMinutes, &ws.HardRefreshes, &ws.StallEvents, &outcome, &ws.Note); err != nil {
			return nil, fmt.Errorf("store: scan session: %w", err)
		}
		ws.Platform = domain.Platform(pf).OrTwitch()
		ws.StartedAt, ws.EndedAt = fromUnix(started), fromUnix(ended)
		ws.Outcome = domain.SessionOutcome(outcome)
		out = append(out, ws)
	}
	return out, rows.Err()
}

// AppendClaim records a claimed drop.
func (s *Store) AppendClaim(c domain.Claim) error {
	benefits, _ := json.Marshal(c.Benefits)
	at := c.ClaimedAt
	if at.IsZero() {
		at = time.Now()
	}
	_, err := s.db.Exec(`INSERT INTO claims
		(drop_id,campaign_id,platform,drop_name,game_name,benefits,claimed_at,source)
		VALUES (?,?,?,?,?,?,?,?)
		ON CONFLICT(drop_id) DO UPDATE SET
			campaign_id=excluded.campaign_id, platform=excluded.platform,
			drop_name=excluded.drop_name,
			game_name=excluded.game_name, benefits=excluded.benefits, claimed_at=excluded.claimed_at,
			source=excluded.source`,
		c.DropID, c.CampaignID, string(c.Platform.OrTwitch()), c.DropName, c.GameName, string(benefits), at.UnixMilli(), c.Source)
	if err != nil {
		return fmt.Errorf("store: append claim: %w", err)
	}
	return nil
}

// ListClaims returns claims within [from, to]. Zero bounds are treated as open.
func (s *Store) ListClaims(from, to time.Time) ([]domain.Claim, error) {
	lo := int64(0)
	hi := time.Now().Add(100 * 365 * 24 * time.Hour).UnixMilli()
	if !from.IsZero() {
		lo = from.UnixMilli()
	}
	if !to.IsZero() {
		hi = to.UnixMilli()
	}
	rows, err := s.db.Query(`SELECT drop_id,campaign_id,platform,drop_name,game_name,benefits,claimed_at,source FROM claims WHERE claimed_at BETWEEN ? AND ? ORDER BY claimed_at DESC`, lo, hi)
	if err != nil {
		return nil, fmt.Errorf("store: list claims: %w", err)
	}
	defer rows.Close()
	var out []domain.Claim
	for rows.Next() {
		var c domain.Claim
		var pf, benefits string
		var at sql.NullInt64
		if err := rows.Scan(&c.DropID, &c.CampaignID, &pf, &c.DropName, &c.GameName, &benefits, &at, &c.Source); err != nil {
			return nil, fmt.Errorf("store: scan claim: %w", err)
		}
		c.Platform = domain.Platform(pf).OrTwitch()
		_ = json.Unmarshal([]byte(benefits), &c.Benefits)
		c.ClaimedAt = fromUnix(at)
		out = append(out, c)
	}
	return out, rows.Err()
}

// HasClaim reports whether a drop has already been claimed.
func (s *Store) HasClaim(dropID string) (bool, error) {
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(1) FROM claims WHERE drop_id=?`, dropID).Scan(&n); err != nil {
		return false, fmt.Errorf("store: has claim: %w", err)
	}
	return n > 0, nil
}
