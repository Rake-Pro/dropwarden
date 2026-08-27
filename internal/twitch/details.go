package twitch

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/rake-pro/dropwarden/internal/domain"
	"github.com/rake-pro/dropwarden/internal/twitch/gql"
)

// clientID is the public Twitch web-client Client-ID. It is the same value the
// site sends on every GraphQL request and is not a secret.
const clientID = "kimne78kx3ncx6brgo4mv6wki5h1ko"

// gqlFetchIIFE builds a JS invoked-async-expression that POSTs bodyJSON to the
// Twitch GraphQL endpoint from the page context, authenticating with the
// session's own auth-token cookie (the same request the site makes when a
// campaign is expanded), and returns the response text. bodyJSON is injected as
// a safe JS string literal.
func gqlFetchIIFE(bodyJSON []byte) string {
	lit, _ := json.Marshal(string(bodyJSON)) // JSON string == valid JS string literal
	// Reuse the auth + integrity headers captured from the page's own GraphQL
	// requests (window.__dw_headers, populated by the capture shim). Twitch
	// rejects requests without a valid Client-Integrity token, and the page
	// already holds one; replaying its headers makes this request pass the same
	// integrity check as the client's own. Falls back to Client-Id + the
	// auth-token cookie if no headers were captured yet.
	return `(async () => {
  try {
    const base = window.__dw_headers || {};
    const m = document.cookie.match(/auth-token=(\w+)/);
    const t = m ? m[1] : '';
    const headers = Object.assign({}, base, { 'Content-Type': 'application/json' });
    if (!headers['client-id'] && !headers['Client-Id']) headers['Client-Id'] = '` + clientID + `';
    if (!headers['authorization'] && !headers['Authorization']) headers['Authorization'] = 'OAuth ' + t;
    const r = await fetch('` + gqlEndpoint + `', {
      method: 'POST',
      headers: headers,
      body: ` + string(lit) + `,
    });
    return await r.text();
  } catch (e) { return JSON.stringify({ error: String(e) }); }
})()`
}

// FetchLogin returns the logged-in user's login via a page-context GraphQL call.
func (p *Pages) FetchLogin(ctx context.Context) (string, error) {
	if p.login != "" {
		return p.login, nil
	}
	body, _ := json.Marshal(map[string]any{"query": "{ currentUser { login } }"})
	var raw string
	if err := p.ctrl.Session().Eval(ctx, gqlFetchIIFE(body), &raw); err != nil {
		return "", fmt.Errorf("twitch: fetch login: %w", err)
	}
	var resp struct {
		Data struct {
			CurrentUser *struct {
				Login string `json:"login"`
			} `json:"currentUser"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		return "", fmt.Errorf("twitch: parse login (%.120s): %w", raw, err)
	}
	if resp.Data.CurrentUser == nil {
		return "", fmt.Errorf("twitch: login query returned no user (response: %.200s)", raw)
	}
	p.login = resp.Data.CurrentUser.Login
	return p.login, nil
}

// detailsQuery is a full DropCampaignDetails query (not a persisted hash, so it
// is immune to hash rotation). It returns data.user.dropCampaign, matching
// gql.ParseCampaignDetails.
const detailsQuery = `query($login:String!,$id:ID!){user(login:$login){dropCampaign(id:$id){id name status startAt endAt accountLinkURL self{isAccountConnected} game{id displayName slug} allow{channels{id name displayName}} timeBasedDrops{id name requiredMinutesWatched startAt endAt benefitEdges{benefit{id name imageAssetURL}} preconditionDrops{id}}}}}`

// FetchCampaignDetails fetches and parses one campaign's full details (drops +
// channel ACL) via a page-context GraphQL call. It also returns the raw response
// body for the debug dump.
func (p *Pages) FetchCampaignDetails(ctx context.Context, login, campaignID string) (domain.Campaign, string, error) {
	body, _ := json.Marshal(map[string]any{
		"query":     detailsQuery,
		"variables": map[string]string{"login": login, "id": campaignID},
	})
	var raw string
	if err := p.ctrl.Session().Eval(ctx, gqlFetchIIFE(body), &raw); err != nil {
		return domain.Campaign{}, raw, fmt.Errorf("twitch: fetch details %s: %w", campaignID, err)
	}
	c, err := gql.ParseCampaignDetails([]byte(raw))
	if err != nil {
		return domain.Campaign{}, raw, fmt.Errorf("twitch: parse details %s (%.160s): %w", campaignID, raw, err)
	}
	return c, raw, nil
}

// CapturedHeaderKeys returns the names (not values) of the GraphQL request
// headers captured from the page, so a run can confirm whether the integrity
// token was picked up without ever logging the secret values.
func (p *Pages) CapturedHeaderKeys(ctx context.Context) []string {
	var keys []string
	_ = p.ctrl.Session().Eval(ctx, `(() => Object.keys(window.__dw_headers || {}))()`, &keys)
	return keys
}
