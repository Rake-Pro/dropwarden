package gql

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/rake-pro/dropwarden/internal/domain"
)

// parseTime parses an RFC3339 timestamp, returning the zero time on any
// failure. Timestamps are treated as optional/best-effort: a bad or missing
// value must never fail the surrounding parse.
func parseTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// campaignStatus maps a raw Twitch status string to domain.CampaignStatus.
// An unrecognized value is kept as-is (cast to CampaignStatus) so callers
// can still see and log the raw value rather than losing it.
func campaignStatus(raw string) domain.CampaignStatus {
	return domain.CampaignStatus(raw)
}

// ParseCampaigns parses a "Campaigns" operation response into a list of
// campaigns. The list view lacks AccountLinkURL, ACL, and drop tiers; those
// are only available via ParseCampaignDetails.
func ParseCampaigns(body []byte) ([]domain.Campaign, error) {
	var resp campaignsResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("gql: parse campaigns response: %w", err)
	}
	if resp.Data.CurrentUser == nil {
		return nil, nil
	}

	out := make([]domain.Campaign, 0, len(resp.Data.CurrentUser.DropCampaigns))
	for _, entry := range resp.Data.CurrentUser.DropCampaigns {
		c := domain.Campaign{
			ID:      entry.ID,
			Name:    entry.Name,
			Status:  campaignStatus(entry.Status),
			StartAt: parseTime(entry.StartAt),
			EndAt:   parseTime(entry.EndAt),
		}
		if entry.Game != nil {
			c.GameID = entry.Game.ID
			c.GameName = entry.Game.DisplayName
			c.GameSlug = entry.Game.Slug
		}
		if entry.Self != nil {
			connected := entry.Self.IsAccountConnected
			c.AccountConnected = &connected
		}
		out = append(out, c)
	}
	return out, nil
}

// ParseCampaignDetails parses a "CampaignDetails" operation response into a
// single fully-expanded campaign, including its drop tiers and channel ACL.
func ParseCampaignDetails(body []byte) (domain.Campaign, error) {
	var resp campaignDetailsResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return domain.Campaign{}, fmt.Errorf("gql: parse campaign details response: %w", err)
	}
	if resp.Data.User == nil || resp.Data.User.DropCampaign == nil {
		return domain.Campaign{}, fmt.Errorf("gql: parse campaign details response: no dropCampaign in response")
	}
	dc := resp.Data.User.DropCampaign

	c := domain.Campaign{
		ID:             dc.ID,
		Name:           dc.Name,
		Status:         campaignStatus(dc.Status),
		StartAt:        parseTime(dc.StartAt),
		EndAt:          parseTime(dc.EndAt),
		AccountLinkURL: dc.AccountLinkURL,
	}
	if dc.Game != nil {
		c.GameID = dc.Game.ID
		c.GameName = dc.Game.DisplayName
		c.GameSlug = dc.Game.Slug
	}
	if dc.Self != nil {
		connected := dc.Self.IsAccountConnected
		c.AccountConnected = &connected
	}

	// verify live: whether "name" on the channel ref is login or display
	// name. Lowercasing assumes it is (or is safe to treat as) login.
	if dc.Allow != nil {
		for _, ch := range dc.Allow.Channels {
			c.AllowedChannels = append(c.AllowedChannels, domain.Channel{
				ID:    ch.ID,
				Login: strings.ToLower(ch.Name),
			})
		}
	}

	for _, d := range dc.TimeBasedDrops {
		drop := domain.Drop{
			ID:              d.ID,
			CampaignID:      c.ID,
			Name:            d.Name,
			RequiredMinutes: d.RequiredMinutesWatched,
			StartAt:         parseTime(d.StartAt),
			EndAt:           parseTime(d.EndAt),
		}
		for _, be := range d.BenefitEdges {
			if be.Benefit == nil {
				continue
			}
			drop.Benefits = append(drop.Benefits, domain.Benefit{
				ID:       be.Benefit.ID,
				Name:     be.Benefit.Name,
				ImageURL: be.Benefit.ImageAssetURL,
			})
		}
		for _, pd := range d.PreconditionDrops {
			drop.PreconditionIDs = append(drop.PreconditionIDs, pd.ID)
		}
		c.Drops = append(c.Drops, drop)
	}

	return c, nil
}

// ParseInventory parses an "Inventory" operation response into one Progress
// per in-progress time-based drop.
func ParseInventory(body []byte) ([]domain.Progress, error) {
	var resp inventoryResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("gql: parse inventory response: %w", err)
	}
	if resp.Data.CurrentUser == nil || resp.Data.CurrentUser.Inventory == nil {
		return nil, nil
	}

	now := time.Now()
	var out []domain.Progress
	for _, camp := range resp.Data.CurrentUser.Inventory.DropCampaignsInProgress {
		for _, d := range camp.TimeBasedDrops {
			p := domain.Progress{
				DropID:          d.ID,
				RequiredMinutes: d.RequiredMinutesWatched,
				Source:          domain.SourceInventory,
				ObservedAt:      now,
			}
			if d.Self != nil {
				p.RealMinutes = d.Self.CurrentMinutesWatched
				p.Claimable = p.RealMinutes >= p.RequiredMinutes && !d.Self.IsClaimed
				if d.Self.DropInstanceID != nil {
					p.ClaimID = *d.Self.DropInstanceID
				}
				if d.Self.IsClaimed {
					t := now
					p.ClaimedAt = &t
				}
			}
			out = append(out, p)
		}
	}
	return out, nil
}

// ParseCurrentDrop parses a "CurrentDrop" operation response. It returns nil
// (not an error) when the viewer has no active drop-watching session.
func ParseCurrentDrop(body []byte) (*domain.Progress, error) {
	var resp currentDropResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("gql: parse current drop response: %w", err)
	}
	if resp.Data.CurrentUser == nil || resp.Data.CurrentUser.DropCurrentSession == nil {
		return nil, nil
	}
	s := resp.Data.CurrentUser.DropCurrentSession

	p := &domain.Progress{
		DropID:          s.DropID,
		RealMinutes:     s.CurrentMinutesWatched,
		RequiredMinutes: s.RequiredMinutesWatched,
		Source:          domain.SourceCurrentDrop,
		ObservedAt:      time.Now(),
	}
	if s.Channel != nil {
		p.ChannelLogin = strings.ToLower(s.Channel.Login)
	}
	p.Claimable = p.RequiredMinutes > 0 && p.RealMinutes >= p.RequiredMinutes
	return p, nil
}

// DetectOperation extracts the operationName(s) from a GQL request body in
// positional order. The body may be a single request object or a batched
// array of request objects, matching Twitch's own client behavior.
func DetectOperation(postData []byte) []string {
	trimmed := bytes.TrimSpace(postData)
	if len(trimmed) == 0 {
		return nil
	}

	if trimmed[0] == '[' {
		var reqs []gqlRequest
		if err := json.Unmarshal(trimmed, &reqs); err != nil {
			return nil
		}
		names := make([]string, 0, len(reqs))
		for _, r := range reqs {
			names = append(names, r.OperationName)
		}
		return names
	}

	var req gqlRequest
	if err := json.Unmarshal(trimmed, &req); err != nil {
		return nil
	}
	if req.OperationName == "" {
		return nil
	}
	return []string{req.OperationName}
}
