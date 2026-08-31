package store

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/rake-pro/dropwarden/internal/domain"
)

// UpsertQueueItem inserts or updates a queue row, preserving the existing
// position when the item already exists (position is managed by SetQueueOrder).
func (s *Store) UpsertQueueItem(it domain.QueueItem) error {
	// force_watch is deliberately absent from the ON CONFLICT SET (like position):
	// it is a user-set override that a re-upsert (e.g. discovery re-enqueue) must
	// preserve, not reset. It is set only via SetForceWatch.
	_, err := s.db.Exec(`INSERT INTO queue
		(drop_id,campaign_id,platform,position,enabled,status,reason,last_attempt_at,force_watch)
		VALUES (?,?,?,?,?,?,?,?,?)
		ON CONFLICT(drop_id) DO UPDATE SET
			campaign_id=excluded.campaign_id, platform=excluded.platform,
			enabled=excluded.enabled,
			status=excluded.status, reason=excluded.reason,
			last_attempt_at=excluded.last_attempt_at`,
		it.DropID, it.CampaignID, string(it.Platform.OrTwitch()), it.Position, boolToInt(it.Enabled),
		string(it.Status), it.Reason, unixPtr(it.LastAttemptAt), boolToInt(it.ForceWatch))
	if err != nil {
		return fmt.Errorf("store: upsert queue item: %w", err)
	}
	return nil
}

// GetQueue returns queue items in ascending position order.
func (s *Store) GetQueue() ([]domain.QueueItem, error) {
	rows, err := s.db.Query(`SELECT drop_id,campaign_id,platform,position,enabled,status,reason,last_attempt_at,COALESCE(force_watch,0) FROM queue ORDER BY position`)
	if err != nil {
		return nil, fmt.Errorf("store: get queue: %w", err)
	}
	defer rows.Close()
	var out []domain.QueueItem
	for rows.Next() {
		var it domain.QueueItem
		var enabled, forceWatch int
		var pf, status, reason string
		var last sql.NullInt64
		if err := rows.Scan(&it.DropID, &it.CampaignID, &pf, &it.Position, &enabled, &status, &reason, &last, &forceWatch); err != nil {
			return nil, fmt.Errorf("store: scan queue: %w", err)
		}
		it.Platform = domain.Platform(pf).OrTwitch()
		it.Enabled = enabled != 0
		it.ForceWatch = forceWatch != 0
		it.Status = domain.ItemStatus(status)
		it.Reason = reason
		it.LastAttemptAt = fromUnix(last)
		out = append(out, it)
	}
	return out, rows.Err()
}

// SetQueueOrder rewrites positions to match the given drop-id order. Ids not in
// the slice keep their rows but are pushed to the end in their existing order.
func (s *Store) SetQueueOrder(dropIDs []string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("store: begin: %w", err)
	}
	defer tx.Rollback()
	for i, id := range dropIDs {
		if _, err := tx.Exec(`UPDATE queue SET position=? WHERE drop_id=?`, i, id); err != nil {
			return fmt.Errorf("store: set order: %w", err)
		}
	}
	return tx.Commit()
}

// SetItemEnabled toggles a queue item on or off.
func (s *Store) SetItemEnabled(dropID string, enabled bool) error {
	_, err := s.db.Exec(`UPDATE queue SET enabled=? WHERE drop_id=?`, boolToInt(enabled), dropID)
	if err != nil {
		return fmt.Errorf("store: set enabled: %w", err)
	}
	return nil
}

// SetForceWatch sets or clears the manual owned-skip override for a queue item.
func (s *Store) SetForceWatch(dropID string, on bool) error {
	_, err := s.db.Exec(`UPDATE queue SET force_watch=? WHERE drop_id=?`, boolToInt(on), dropID)
	if err != nil {
		return fmt.Errorf("store: set force watch: %w", err)
	}
	return nil
}

// SetItemStatus updates a queue item's status and reason, stamping the attempt time.
func (s *Store) SetItemStatus(dropID string, status domain.ItemStatus, reason string) error {
	_, err := s.db.Exec(`UPDATE queue SET status=?, reason=?, last_attempt_at=? WHERE drop_id=?`,
		string(status), reason, time.Now().UnixMilli(), dropID)
	if err != nil {
		return fmt.Errorf("store: set status: %w", err)
	}
	return nil
}

// PruneQueue deletes every queue item whose drop id is not in keep, returning
// the number removed. Used to drop off-filter/expired/claimed items so the queue
// only holds current, workable drops.
func (s *Store) PruneQueue(keep map[string]bool) (int, error) {
	items, err := s.GetQueue()
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, it := range items {
		if keep[it.DropID] {
			continue
		}
		if _, err := s.db.Exec(`DELETE FROM queue WHERE drop_id=?`, it.DropID); err != nil {
			return removed, fmt.Errorf("store: prune queue: %w", err)
		}
		removed++
	}
	return removed, nil
}

// NextPosition returns the position to assign to a newly appended queue item.
func (s *Store) NextPosition() (int, error) {
	var max sql.NullInt64
	if err := s.db.QueryRow(`SELECT MAX(position) FROM queue`).Scan(&max); err != nil {
		return 0, fmt.Errorf("store: next position: %w", err)
	}
	if !max.Valid {
		return 0, nil
	}
	return int(max.Int64) + 1, nil
}
