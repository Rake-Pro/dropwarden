package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/rake-pro/dropwarden/internal/domain"
)

func unixPtr(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UnixMilli()
}

func fromUnix(ms sql.NullInt64) time.Time {
	if !ms.Valid {
		return time.Time{}
	}
	return time.UnixMilli(ms.Int64)
}

// UpsertCampaign writes a campaign and replaces its drop rows in one
// transaction. Drops absent from c are not deleted here (discovery may fetch
// details incrementally); callers pass the full drop set they know about.
func (s *Store) UpsertCampaign(c domain.Campaign) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("store: begin: %w", err)
	}
	defer tx.Rollback()

	acl := make([]string, 0, len(c.AllowedChannels))
	for _, ch := range c.AllowedChannels {
		acl = append(acl, ch.Login)
	}
	aclJSON, _ := json.Marshal(acl)

	var connected any
	if c.AccountConnected != nil {
		connected = *c.AccountConnected
	}

	_, err = tx.Exec(`INSERT INTO campaigns
		(id,name,game_id,game_name,game_slug,status,start_at,end_at,account_link_url,account_connected,allowed_channels,details_fetched)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET
			name=excluded.name, game_id=excluded.game_id, game_name=excluded.game_name,
			status=excluded.status, start_at=excluded.start_at, end_at=excluded.end_at,
			details_fetched=excluded.details_fetched,
			-- Preserve detail-only fields when the incoming row lacks them, so the
			-- basic list upsert (no ACL/slug/link) never clobbers a detailed campaign.
			game_slug=CASE WHEN excluded.game_slug='' THEN campaigns.game_slug ELSE excluded.game_slug END,
			account_link_url=CASE WHEN excluded.account_link_url='' THEN campaigns.account_link_url ELSE excluded.account_link_url END,
			account_connected=CASE WHEN excluded.account_connected IS NULL THEN campaigns.account_connected ELSE excluded.account_connected END,
			allowed_channels=CASE WHEN excluded.allowed_channels IN ('','[]','null') THEN campaigns.allowed_channels ELSE excluded.allowed_channels END`,
		c.ID, c.Name, c.GameID, c.GameName, c.GameSlug, string(c.Status),
		unixPtr(c.StartAt), unixPtr(c.EndAt), c.AccountLinkURL, connected, string(aclJSON), unixPtr(c.DetailsFetched))
	if err != nil {
		return fmt.Errorf("store: upsert campaign: %w", err)
	}

	for _, d := range c.Drops {
		benefits, _ := json.Marshal(d.Benefits)
		pre, _ := json.Marshal(d.PreconditionIDs)
		_, err = tx.Exec(`INSERT INTO drops
			(id,campaign_id,name,required_minutes,benefits,preconditions,start_at,end_at)
			VALUES (?,?,?,?,?,?,?,?)
			ON CONFLICT(id) DO UPDATE SET
				campaign_id=excluded.campaign_id, name=excluded.name,
				required_minutes=excluded.required_minutes, benefits=excluded.benefits,
				preconditions=excluded.preconditions, start_at=excluded.start_at, end_at=excluded.end_at`,
			d.ID, c.ID, d.Name, d.RequiredMinutes, string(benefits), string(pre),
			unixPtr(d.StartAt), unixPtr(d.EndAt))
		if err != nil {
			return fmt.Errorf("store: upsert drop %s: %w", d.ID, err)
		}
	}
	return tx.Commit()
}

// ListCampaigns returns all cached campaigns with their drops.
func (s *Store) ListCampaigns() ([]domain.Campaign, error) {
	rows, err := s.db.Query(`SELECT id,name,game_id,game_name,game_slug,status,start_at,end_at,account_link_url,account_connected,allowed_channels,details_fetched FROM campaigns ORDER BY end_at`)
	if err != nil {
		return nil, fmt.Errorf("store: list campaigns: %w", err)
	}
	defer rows.Close()

	var out []domain.Campaign
	for rows.Next() {
		var c domain.Campaign
		var status, aclJSON string
		var start, end, details sql.NullInt64
		var connected sql.NullBool
		if err := rows.Scan(&c.ID, &c.Name, &c.GameID, &c.GameName, &c.GameSlug, &status, &start, &end, &c.AccountLinkURL, &connected, &aclJSON, &details); err != nil {
			return nil, fmt.Errorf("store: scan campaign: %w", err)
		}
		if connected.Valid {
			v := connected.Bool
			c.AccountConnected = &v
		}
		c.Status = domain.CampaignStatus(status)
		c.StartAt, c.EndAt, c.DetailsFetched = fromUnix(start), fromUnix(end), fromUnix(details)
		var acl []string
		_ = json.Unmarshal([]byte(aclJSON), &acl)
		for _, login := range acl {
			c.AllowedChannels = append(c.AllowedChannels, domain.Channel{Login: login})
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		drops, err := s.dropsFor(out[i].ID)
		if err != nil {
			return nil, err
		}
		out[i].Drops = drops
	}
	return out, nil
}

func (s *Store) dropsFor(campaignID string) ([]domain.Drop, error) {
	rows, err := s.db.Query(`SELECT id,campaign_id,name,required_minutes,benefits,preconditions,start_at,end_at FROM drops WHERE campaign_id=? ORDER BY required_minutes`, campaignID)
	if err != nil {
		return nil, fmt.Errorf("store: drops: %w", err)
	}
	defer rows.Close()
	var out []domain.Drop
	for rows.Next() {
		var d domain.Drop
		var benefits, pre string
		var start, end sql.NullInt64
		if err := rows.Scan(&d.ID, &d.CampaignID, &d.Name, &d.RequiredMinutes, &benefits, &pre, &start, &end); err != nil {
			return nil, fmt.Errorf("store: scan drop: %w", err)
		}
		_ = json.Unmarshal([]byte(benefits), &d.Benefits)
		_ = json.Unmarshal([]byte(pre), &d.PreconditionIDs)
		d.StartAt, d.EndAt = fromUnix(start), fromUnix(end)
		out = append(out, d)
	}
	return out, rows.Err()
}

// GetCampaign returns a single campaign with drops, or (nil, nil) if absent.
func (s *Store) GetCampaign(id string) (*domain.Campaign, error) {
	all, err := s.ListCampaigns()
	if err != nil {
		return nil, err
	}
	for i := range all {
		if all[i].ID == id {
			return &all[i], nil
		}
	}
	return nil, nil
}
