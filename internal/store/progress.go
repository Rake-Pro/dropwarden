package store

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/rake-pro/dropwarden/internal/domain"
)

// SaveProgress upserts the latest known progress for a drop.
func (s *Store) SaveProgress(p domain.Progress) error {
	var claimedAt any
	if p.ClaimedAt != nil {
		claimedAt = p.ClaimedAt.UnixMilli()
	}
	observed := p.ObservedAt
	if observed.IsZero() {
		observed = time.Now()
	}
	_, err := s.db.Exec(`INSERT INTO progress
		(drop_id,claim_id,real_minutes,extra_minutes,required_minutes,claimable,claimed_at,source,observed_at)
		VALUES (?,?,?,?,?,?,?,?,?)
		ON CONFLICT(drop_id) DO UPDATE SET
			claim_id=excluded.claim_id, real_minutes=excluded.real_minutes,
			extra_minutes=excluded.extra_minutes, required_minutes=excluded.required_minutes,
			claimable=excluded.claimable, claimed_at=excluded.claimed_at,
			source=excluded.source, observed_at=excluded.observed_at`,
		p.DropID, p.ClaimID, p.RealMinutes, p.ExtraMinutes, p.RequiredMinutes,
		boolToInt(p.Claimable), claimedAt, string(p.Source), observed.UnixMilli())
	if err != nil {
		return fmt.Errorf("store: save progress: %w", err)
	}
	return nil
}

// GetProgress returns progress for a drop, or (nil, nil) if none recorded.
func (s *Store) GetProgress(dropID string) (*domain.Progress, error) {
	var p domain.Progress
	var claimable int
	var claimedAt, observed sql.NullInt64
	var source string
	err := s.db.QueryRow(`SELECT drop_id,claim_id,real_minutes,extra_minutes,required_minutes,claimable,claimed_at,source,observed_at FROM progress WHERE drop_id=?`, dropID).
		Scan(&p.DropID, &p.ClaimID, &p.RealMinutes, &p.ExtraMinutes, &p.RequiredMinutes, &claimable, &claimedAt, &source, &observed)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: get progress: %w", err)
	}
	p.Claimable = claimable != 0
	p.Source = domain.ProgressSource(source)
	p.ObservedAt = fromUnix(observed)
	if claimedAt.Valid {
		t := time.UnixMilli(claimedAt.Int64)
		p.ClaimedAt = &t
	}
	return &p, nil
}

// ListProgress returns every recorded progress row.
func (s *Store) ListProgress() ([]domain.Progress, error) {
	rows, err := s.db.Query(`SELECT drop_id,claim_id,real_minutes,extra_minutes,required_minutes,claimable,claimed_at,source,observed_at FROM progress`)
	if err != nil {
		return nil, fmt.Errorf("store: list progress: %w", err)
	}
	defer rows.Close()
	var out []domain.Progress
	for rows.Next() {
		var p domain.Progress
		var claimable int
		var claimedAt, observed sql.NullInt64
		var source string
		if err := rows.Scan(&p.DropID, &p.ClaimID, &p.RealMinutes, &p.ExtraMinutes, &p.RequiredMinutes, &claimable, &claimedAt, &source, &observed); err != nil {
			return nil, fmt.Errorf("store: scan progress: %w", err)
		}
		p.Claimable = claimable != 0
		p.Source = domain.ProgressSource(source)
		p.ObservedAt = fromUnix(observed)
		if claimedAt.Valid {
			t := time.UnixMilli(claimedAt.Int64)
			p.ClaimedAt = &t
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
