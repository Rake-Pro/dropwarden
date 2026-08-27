package store

import (
	"database/sql"
	"fmt"
	"time"
)

// ChannelStat is the success/failure record for one channel, used to rank
// candidates during selection.
type ChannelStat struct {
	Login     string
	Successes int
	Failures  int
	LastSeen  time.Time
}

// RecordChannelResult increments the success or failure counter for a channel.
func (s *Store) RecordChannelResult(login string, success bool) error {
	col := "failures"
	if success {
		col = "successes"
	}
	_, err := s.db.Exec(fmt.Sprintf(`INSERT INTO channel_stats (login,%s,last_seen) VALUES (?,1,?)
		ON CONFLICT(login) DO UPDATE SET %s=%s+1, last_seen=excluded.last_seen`, col, col, col),
		login, time.Now().UnixMilli())
	if err != nil {
		return fmt.Errorf("store: record channel result: %w", err)
	}
	return nil
}

// ChannelStats returns the stat record for a channel, or a zero record if none.
func (s *Store) ChannelStats(login string) (ChannelStat, error) {
	var st ChannelStat
	var last sql.NullInt64
	err := s.db.QueryRow(`SELECT login,successes,failures,last_seen FROM channel_stats WHERE login=?`, login).
		Scan(&st.Login, &st.Successes, &st.Failures, &last)
	if err == sql.ErrNoRows {
		return ChannelStat{Login: login}, nil
	}
	if err != nil {
		return st, fmt.Errorf("store: channel stats: %w", err)
	}
	st.LastSeen = fromUnix(last)
	return st, nil
}

// BlacklistChannel suppresses a channel until the given time.
func (s *Store) BlacklistChannel(login, reason string, until time.Time) error {
	_, err := s.db.Exec(`INSERT INTO blacklist (login,reason,until) VALUES (?,?,?)
		ON CONFLICT(login) DO UPDATE SET reason=excluded.reason, until=excluded.until`,
		login, reason, until.UnixMilli())
	if err != nil {
		return fmt.Errorf("store: blacklist: %w", err)
	}
	return nil
}

// IsBlacklisted reports whether a channel is currently suppressed. Expired
// entries are treated as not blacklisted.
func (s *Store) IsBlacklisted(login string, now time.Time) (bool, error) {
	var until sql.NullInt64
	err := s.db.QueryRow(`SELECT until FROM blacklist WHERE login=?`, login).Scan(&until)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("store: is blacklisted: %w", err)
	}
	if !until.Valid {
		return false, nil
	}
	return now.UnixMilli() < until.Int64, nil
}

// ClearBlacklist removes all blacklist entries.
func (s *Store) ClearBlacklist() error {
	if _, err := s.db.Exec(`DELETE FROM blacklist`); err != nil {
		return fmt.Errorf("store: clear blacklist: %w", err)
	}
	return nil
}
