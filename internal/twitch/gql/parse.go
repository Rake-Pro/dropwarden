package gql

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
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
// single fully-expanded campaign, including its drop tiers and channel ACL. It
// also returns per-drop viewer progress for every drop that carries a "self"
// field, so a fully-claimed campaign (which the dashboard/inventory views
// exclude once every drop is claimed) is still recorded as claimed at
// discovery.
func ParseCampaignDetails(body []byte) (domain.Campaign, []domain.Progress, error) {
	var resp campaignDetailsResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return domain.Campaign{}, nil, fmt.Errorf("gql: parse campaign details response: %w", err)
	}
	if resp.Data.User == nil || resp.Data.User.DropCampaign == nil {
		return domain.Campaign{}, nil, fmt.Errorf("gql: parse campaign details response: no dropCampaign in response")
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

	now := time.Now()
	var progress []domain.Progress
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

		if d.Self != nil {
			p := domain.Progress{
				DropID:          d.ID,
				RequiredMinutes: d.RequiredMinutesWatched,
				Source:          domain.SourceInventory,
				ObservedAt:      now,
			}
			p.RealMinutes = d.Self.CurrentMinutesWatched
			p.Claimable = p.RealMinutes >= p.RequiredMinutes && !d.Self.IsClaimed
			if d.Self.DropInstanceID != nil {
				p.ClaimID = *d.Self.DropInstanceID
			}
			if d.Self.IsClaimed {
				t := now
				p.ClaimedAt = &t
			}
			progress = append(progress, p)
		}
	}

	return c, progress, nil
}

// gqlEnvelope is the error part of a GraphQL response: the GraphQL errors
// array and the in-page fetch wrapper's own {"error": ...} failure.
type gqlEnvelope struct {
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
	Error string `json:"error"`
}

// unbatch returns the first response of a batched (positional array) GraphQL
// response, or body unchanged when it is a single object.
func unbatch(body []byte) ([]byte, error) {
	t := bytes.TrimSpace(body)
	if len(t) == 0 || t[0] != '[' {
		return body, nil
	}
	var arr []json.RawMessage
	if err := json.Unmarshal(t, &arr); err != nil {
		return nil, err
	}
	if len(arr) == 0 {
		return nil, fmt.Errorf("empty batched response")
	}
	return arr[0], nil
}

// ParseInventory parses an "Inventory" operation response into one Progress
// per in-progress time-based drop. A response without a currentUser.inventory
// object (auth expired, integrity rejection, fetch failure, empty data) is an
// ERROR, never an empty result: callers treat a missing row as evidence (e.g.
// claim verification), so "no data" must not read as "nothing in progress". A
// null dropCampaignsInProgress with a present inventory is a real empty list,
// unless the response also carries GraphQL errors.
func ParseInventory(body []byte) ([]domain.Progress, error) {
	b, err := unbatch(body)
	if err != nil {
		return nil, fmt.Errorf("gql: parse inventory response: %w", err)
	}
	var resp struct {
		inventoryResponse
		gqlEnvelope
	}
	if err := json.Unmarshal(b, &resp); err != nil {
		return nil, fmt.Errorf("gql: parse inventory response: %w", err)
	}
	if resp.Error != "" {
		return nil, fmt.Errorf("gql: inventory request failed: %s", resp.Error)
	}
	if resp.Data.CurrentUser == nil || resp.Data.CurrentUser.Inventory == nil {
		if len(resp.Errors) > 0 {
			return nil, fmt.Errorf("gql: inventory: %s", resp.Errors[0].Message)
		}
		return nil, fmt.Errorf("gql: inventory: no currentUser.inventory in response")
	}
	if len(resp.Errors) > 0 && len(resp.Data.CurrentUser.Inventory.DropCampaignsInProgress) == 0 {
		return nil, fmt.Errorf("gql: inventory: no in-progress list with errors: %s", resp.Errors[0].Message)
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

// NormalizeRewardName canonicalizes a reward or benefit name for cross-campaign
// matching: trimmed and lowercased. Empty input yields "". Reward IDs are
// campaign-instance-scoped (of the form <campaignID>_CUSTOM_ID_<n>), so a new
// campaign offering the same cosmetic mints a fresh ID; the human name is the
// only stable join key across instances.
func NormalizeRewardName(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

// ParseEarnedRewards parses the earned/owned rewards from an "Inventory"
// response into a set of normalized reward names. These are rewards the account
// already holds - game-integration items (gameEventDropsConnection) plus claimed
// badges/emotes/entitlements (earnedDropRewards). A drop whose reward matches
// one of these will never credit for this account even though Twitch reports it
// isClaimed=false, so discovery can skip it before watching.
func ParseEarnedRewards(body []byte) (map[string]bool, error) {
	var resp inventoryResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("gql: parse earned rewards response: %w", err)
	}
	if resp.Data.CurrentUser == nil || resp.Data.CurrentUser.Inventory == nil {
		return nil, nil
	}
	inv := resp.Data.CurrentUser.Inventory
	owned := map[string]bool{}
	add := func(name string) {
		if n := NormalizeRewardName(name); n != "" {
			owned[n] = true
		}
	}
	for _, e := range inv.GameEventDropsConnection.Edges {
		add(e.Node.Name)
	}
	for _, e := range inv.EarnedDropRewards.Edges {
		add(e.Node.Name)
		if e.Node.Item != nil {
			add(e.Node.Item.Name)
		}
	}
	return owned, nil
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

// ParseHeartbeat parses the drained heartbeat buffer ({spade:[...], ws:[...]}).
// WebSocket bodies are matched loosely (the drop-progress payload is a JSON
// string nested inside PubSub or Hermes framing, so quotes arrive escaped)
// and every parsed push becomes a SourceWebSocket progress reading.
func ParseHeartbeat(body []byte) (domain.Heartbeat, error) {
	var raw struct {
		Spade []struct {
			Event   string `json:"event"`
			Channel string `json:"channel"`
			Hidden  bool   `json:"hidden"`
			Muted   bool   `json:"muted"`
		} `json:"spade"`
		WS []struct {
			Body string `json:"body"`
		} `json:"ws"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return domain.Heartbeat{}, fmt.Errorf("gql: parse heartbeat (%.120s): %w", body, err)
	}
	hb := domain.Heartbeat{Events: map[string]int{}, Raw: body}
	for _, ev := range raw.Spade {
		hb.Events[ev.Event]++
		if ev.Event == "minute-watched" {
			hb.MinuteWatched++
			hb.LastChannel = strings.ToLower(ev.Channel)
			hb.LastHidden = ev.Hidden
			hb.LastMuted = ev.Muted
		}
	}
	for _, m := range raw.WS {
		if !strings.Contains(m.Body, "drop-progress") {
			continue
		}
		hb.DropMessages++
		if p, ok := parseDropProgressPush(m.Body); ok {
			hb.Progress = append(hb.Progress, p)
		}
	}
	return hb, nil
}

var (
	reDropID = regexp.MustCompile(`\\*"drop_id\\*"\s*:\s*\\*"([0-9a-fA-F-]{8,})\\*"`)
	reCurMin = regexp.MustCompile(`\\*"current_progress_min\\*"\s*:\s*(\d+)`)
	reReqMin = regexp.MustCompile(`\\*"required_progress_min\\*"\s*:\s*(\d+)`)
)

// parseDropProgressPush pulls the drop-progress fields out of a WebSocket
// frame without depending on the exact framing.
func parseDropProgressPush(body string) (domain.Progress, bool) {
	id := reDropID.FindStringSubmatch(body)
	cur := reCurMin.FindStringSubmatch(body)
	if id == nil || cur == nil {
		return domain.Progress{}, false
	}
	p := domain.Progress{DropID: id[1], Source: domain.SourceWebSocket, ObservedAt: time.Now()}
	p.RealMinutes, _ = strconv.Atoi(cur[1])
	if req := reReqMin.FindStringSubmatch(body); req != nil {
		p.RequiredMinutes, _ = strconv.Atoi(req[1])
	}
	p.Claimable = p.RequiredMinutes > 0 && p.RealMinutes >= p.RequiredMinutes
	return p, true
}

// ParseLiveChannels parses a users(logins:) response and returns only the users
// whose stream is non-null, i.e. the ones live right now.
func ParseLiveChannels(body []byte) ([]domain.Channel, error) {
	var resp struct {
		Data struct {
			Users []*struct {
				ID          string `json:"id"`
				Login       string `json:"login"`
				DisplayName string `json:"displayName"`
				Stream      *struct {
					ID           string `json:"id"`
					ViewersCount int    `json:"viewersCount"`
					Game         *struct {
						ID          string `json:"id"`
						DisplayName string `json:"displayName"`
					} `json:"game"`
				} `json:"stream"`
			} `json:"users"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("gql: parse live channels (%.160s): %w", body, err)
	}
	if resp.Data.Users == nil {
		if len(resp.Errors) > 0 {
			return nil, fmt.Errorf("gql: live channels: %s", resp.Errors[0].Message)
		}
		return nil, fmt.Errorf("gql: live channels: no users in response (%.160s)", body)
	}
	var out []domain.Channel
	for _, u := range resp.Data.Users {
		if u == nil || u.Stream == nil {
			continue // unknown login or offline
		}
		ch := domain.Channel{ID: u.ID, Login: strings.ToLower(u.Login), DisplayName: u.DisplayName, Live: true, ViewerCount: u.Stream.ViewersCount}
		if u.Stream.Game != nil {
			ch.GameID = u.Stream.Game.ID
			ch.GameName = u.Stream.Game.DisplayName
		}
		out = append(out, ch)
	}
	return out, nil
}

// Claim mutation statuses Twitch returns in claimDropRewards.status that mean
// the reward is (now) on the account. Anything else is a failure.
const (
	ClaimStatusEligible       = "ELIGIBLE_FOR_ALL"
	ClaimStatusAlreadyClaimed = "DROP_INSTANCE_ALREADY_CLAIMED"
)

// ParseClaimDropRewards parses a DropsPage_ClaimDropRewards mutation response.
// It returns the raw status string whenever one is present (so callers can log
// the real values) and a non-nil error unless the status means the reward is
// on the account: ELIGIBLE_FOR_ALL, or DROP_INSTANCE_ALREADY_CLAIMED (claimed
// earlier, e.g. by hand, which is the same end state). GraphQL errors, a page
// fetch error, or a missing payload are failures. A batched (positional array)
// response is read from its first element.
func ParseClaimDropRewards(body []byte) (string, error) {
	b, err := unbatch(body)
	if err != nil {
		return "", fmt.Errorf("gql: parse claim response (%.160s): %w", body, err)
	}
	var resp struct {
		Data *struct {
			ClaimDropRewards *struct {
				Status string `json:"status"`
			} `json:"claimDropRewards"`
		} `json:"data"`
		gqlEnvelope
	}
	if err := json.Unmarshal(b, &resp); err != nil {
		return "", fmt.Errorf("gql: parse claim response (%.160s): %w", body, err)
	}
	if resp.Error != "" {
		return "", fmt.Errorf("gql: claim request failed: %s", resp.Error)
	}
	if len(resp.Errors) > 0 {
		return "", fmt.Errorf("gql: claim: %s", resp.Errors[0].Message)
	}
	if resp.Data == nil || resp.Data.ClaimDropRewards == nil {
		return "", fmt.Errorf("gql: claim: no claimDropRewards payload (%.160s)", body)
	}
	status := resp.Data.ClaimDropRewards.Status
	switch status {
	case ClaimStatusEligible, ClaimStatusAlreadyClaimed:
		return status, nil
	}
	return status, fmt.Errorf("gql: claim rejected: status %q", status)
}
