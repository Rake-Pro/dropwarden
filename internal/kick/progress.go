package kick

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/rake-pro/dropwarden/internal/domain"
)

// progressAPI is the viewer's drops inventory: every campaign the account
// has progress in, with per-reward progress. It is what kick.com/drops
// renders ("49 hr 54 min to unlock"); the site's client calls it
// getInventory. Unofficial like campaignsAPI; needs the session cookies, so
// it is fetched from page context.
const progressAPI = "https://web.kick.com/api/v1/drops/progress"

// jsFetchProgress is the page-context fetch of progressAPI in the same
// {status, body} wrapper as jsFetchCampaigns. Unlike the anonymous campaign
// list, the inventory is per account: Kick's own client sends the
// session_token cookie as a bearer token plus x-app-platform, and without
// them the endpoint answers 403 even for a logged-in session (live
// 2026-11-04). The cookie is readable from the page, which is how the site
// itself gets it.
const jsFetchProgress = `(async () => {
  try {
    const m = document.cookie.match(/(?:^|; )session_token=([^;]*)/);
    const tok = m ? decodeURIComponent(m[1]) : '';
    const headers = { 'Accept': 'application/json', 'x-app-platform': 'web' };
    if (tok) headers['Authorization'] = 'Bearer ' + tok;
    const r = await fetch('` + progressAPI + `', { credentials: 'include', headers, signal: AbortSignal.timeout(20000) });
    return JSON.stringify({ status: r.status, body: await r.text(), token: !!tok });
  } catch (e) { return JSON.stringify({ status: 0, body: String(e) }); }
})()`

// kpReward mirrors the reward object the drops page's reward card reads:
// progress is a 0..1 fraction of required_units, claimed is the terminal
// state. Alternate names are accepted in case the field set differs
// between the inventory and campaign views.
type kpReward struct {
	ID            flexID   `json:"id"`
	Name          string   `json:"name"`
	Progress      *float64 `json:"progress"`
	Claimed       bool     `json:"claimed"`
	IsClaimed     bool     `json:"is_claimed"`
	RequiredUnits int      `json:"required_units"`
	UnitsWatched  *int     `json:"units_watched"`
	Units         *int     `json:"units"`
}

type kpCampaign struct {
	ID         flexID     `json:"id"`
	CampaignID flexID     `json:"campaign_id"`
	Status     string     `json:"status"`
	Rewards    []kpReward `json:"rewards"`
	Drops      []kpReward `json:"drops"`
	Campaign   *struct {
		ID flexID `json:"id"`
	} `json:"campaign"`
}

// ParseProgressResponse unwraps the {status, body, token} fetch wrapper. A
// non-200 is a plain error, never domain.ErrLoggedOut: the login probe owns
// that verdict, and a 403 here once paused a logged-in Kick engine (live
// 2026-11-04, the fetch lacked the bearer token). The engine counts the
// failed poll and keeps watching.
func ParseProgressResponse(raw string, now time.Time) ([]domain.Progress, error) {
	var wrapped struct {
		Status int    `json:"status"`
		Body   string `json:"body"`
		Token  bool   `json:"token"`
	}
	if err := json.Unmarshal([]byte(raw), &wrapped); err != nil {
		return nil, fmt.Errorf("kick: decode progress wrapper (%.120s): %w", raw, err)
	}
	if wrapped.Status != 200 {
		return nil, fmt.Errorf("kick: progress endpoint returned %d (session_token cookie present: %v) (%.120s)", wrapped.Status, wrapped.Token, wrapped.Body)
	}
	return ParseProgress([]byte(wrapped.Body), now)
}

// ParseProgress maps the inventory body to one Progress row per reward,
// keyed like ParseCampaigns ("kick-<campaign>-<reward>") so the rows meet
// the queue. Minutes are the fraction applied to required_units unless an
// explicit watched-units field is present. Rows are server-confirmed
// (SourceInventory). It accepts a bare array or a {"data": [...]} wrapper.
func ParseProgress(body []byte, now time.Time) ([]domain.Progress, error) {
	trimmed := strings.TrimSpace(string(body))
	var list []kpCampaign
	if strings.HasPrefix(trimmed, "[") {
		if err := json.Unmarshal([]byte(trimmed), &list); err != nil {
			return nil, fmt.Errorf("kick: parse progress array: %w", err)
		}
	} else {
		var resp struct {
			Data json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal([]byte(trimmed), &resp); err != nil {
			return nil, fmt.Errorf("kick: parse progress response: %w", err)
		}
		if len(resp.Data) > 0 && resp.Data[0] == '[' {
			if err := json.Unmarshal(resp.Data, &list); err != nil {
				return nil, fmt.Errorf("kick: parse progress data: %w", err)
			}
		} else if len(resp.Data) > 0 {
			// {"data": {"campaigns": [...]}} or {"data": {"in_progress": [...], ...}}
			var obj map[string]json.RawMessage
			if err := json.Unmarshal(resp.Data, &obj); err != nil {
				return nil, fmt.Errorf("kick: parse progress data object: %w", err)
			}
			for _, v := range obj {
				if len(v) > 0 && v[0] == '[' {
					var part []kpCampaign
					if err := json.Unmarshal(v, &part); err == nil {
						list = append(list, part...)
					}
				}
			}
		}
	}

	var out []domain.Progress
	for _, c := range list {
		cid := c.ID.String()
		if cid == "" {
			cid = c.CampaignID.String()
		}
		if cid == "" && c.Campaign != nil {
			cid = c.Campaign.ID.String()
		}
		rewards := c.Rewards
		if len(rewards) == 0 {
			rewards = c.Drops
		}
		for _, r := range rewards {
			if cid == "" || r.ID.String() == "" {
				continue
			}
			p := domain.Progress{
				DropID:          "kick-" + cid + "-" + r.ID.String(),
				RequiredMinutes: r.RequiredUnits,
				Source:          domain.SourceInventory,
				ObservedAt:      now,
			}
			switch {
			case r.UnitsWatched != nil:
				p.RealMinutes = *r.UnitsWatched
			case r.Units != nil:
				p.RealMinutes = *r.Units
			case r.Progress != nil:
				f := math.Min(math.Max(*r.Progress, 0), 1)
				p.RealMinutes = int(math.Round(f * float64(r.RequiredUnits)))
			}
			if r.Claimed || r.IsClaimed {
				t := now
				p.ClaimedAt = &t
				p.RealMinutes = max(p.RealMinutes, r.RequiredUnits)
			} else if r.RequiredUnits > 0 && p.RealMinutes >= r.RequiredUnits {
				p.Claimable = true
			}
			out = append(out, p)
		}
	}
	return out, nil
}
