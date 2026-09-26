package twitch

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/rs/zerolog/log"

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
    // document.cookie throws a SecurityError on an opaque-origin document
    // (about:blank after a relaunch); fall back to the captured headers only.
    let t = '', cookieErr = '';
    try {
      const m = document.cookie.match(/auth-token=(\w+)/);
      t = m ? m[1] : '';
    } catch (e) { cookieErr = String(e); }
    const headers = Object.assign({}, base, { 'Content-Type': 'application/json' });
    if (!headers['client-id'] && !headers['Client-Id']) headers['Client-Id'] = '` + clientID + `';
    if (!headers['authorization'] && !headers['Authorization']) {
      if (cookieErr) return JSON.stringify({ error: 'no twitch auth on ' + location.href + ': no captured authorization header and the auth-token cookie is unreadable (' + cookieErr + ')' });
      headers['Authorization'] = 'OAuth ' + t;
    }
    const r = await fetch('` + gqlEndpoint + `', {
      method: 'POST',
      headers: headers,
      body: ` + string(lit) + `,
      signal: AbortSignal.timeout(20000),
    });
    return await r.text();
  } catch (e) { return JSON.stringify({ error: String(e) }); }
})()`
}

// offSiteSettle is the dwell after navigating an off-site tab back to Twitch,
// so the page fires its own GraphQL requests and the capture shim picks up the
// auth and integrity headers before we replay them.
var offSiteSettle = 3 * time.Second

// isTwitchURL reports whether u is on twitch.tv or a subdomain of it.
func isTwitchURL(u string) bool {
	pu, err := url.Parse(u)
	if err != nil {
		return false
	}
	h := strings.ToLower(pu.Hostname())
	return h == "twitch.tv" || strings.HasSuffix(h, ".twitch.tv")
}

// ensureOnTwitch makes sure the tab shows a twitch.tv document before a
// page-context GraphQL call. Such a call runs in the page's origin: on
// about:blank (a relaunched Chrome) or any other site it cannot read the auth
// cookie or reach gql.twitch.tv with the site's headers (live 2026-09-25: a
// SecurityError on document.cookie degraded channel selection). An off-site
// tab is navigated to the drops inventory (neutral, no playback) once and
// given a short settle. A failed URL read is left to the Eval to report.
func (p *Pages) ensureOnTwitch(ctx context.Context) {
	sess := p.ctrl.Session()
	cur, err := sess.CurrentURL(ctx)
	if err != nil || isTwitchURL(cur) {
		return
	}
	log.Info().Str("url", cur).Msg("twitch: tab was off-site, navigated before GQL")
	if err := sess.Navigate(ctx, URLInventory); err != nil {
		log.Warn().Err(err).Msg("twitch: navigate off-site tab back to twitch failed")
		return
	}
	select {
	case <-ctx.Done():
	case <-time.After(offSiteSettle):
	}
}

// gqlEval runs one page-context GraphQL request (gqlFetchIIFE) on a twitch.tv
// document and stores the response text in raw.
func (p *Pages) gqlEval(ctx context.Context, body []byte, raw *string) error {
	p.ensureOnTwitch(ctx)
	return p.ctrl.Session().Eval(ctx, gqlFetchIIFE(body), raw)
}

// FetchLogin returns the logged-in user's login via a page-context GraphQL call.
func (p *Pages) FetchLogin(ctx context.Context) (string, error) {
	if p.login != "" {
		return p.login, nil
	}
	body, _ := json.Marshal(map[string]any{"query": "{ currentUser { login } }"})
	var raw string
	if err := p.gqlEval(ctx, body, &raw); err != nil {
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
		if gql.CurrentUserNull([]byte(raw)) {
			return "", fmt.Errorf("twitch: login query: %w", domain.ErrLoggedOut)
		}
		return "", fmt.Errorf("twitch: login query returned no user (response: %.200s)", raw)
	}
	p.login = resp.Data.CurrentUser.Login
	return p.login, nil
}

// detailsQuery is a full DropCampaignDetails query (not a persisted hash, so it
// is immune to hash rotation). It returns data.user.dropCampaign, matching
// gql.ParseCampaignDetails.
const detailsQuery = `query($login:String!,$id:ID!){user(login:$login){dropCampaign(id:$id){id name status startAt endAt accountLinkURL self{isAccountConnected} game{id displayName slug} allow{isEnabled channels{id name displayName}} timeBasedDrops{id name requiredMinutesWatched startAt endAt benefitEdges{benefit{id name imageAssetURL}} preconditionDrops{id} self{currentMinutesWatched isClaimed dropInstanceID hasPreconditionsMet}}}}}`

// FetchCampaignDetails fetches and parses one campaign's full details (drops +
// channel ACL) via a page-context GraphQL call. It also returns per-drop viewer
// progress (including already-claimed drops, which the dashboard/inventory
// views omit once a campaign is fully claimed) and the raw response body for
// the debug dump.
func (p *Pages) FetchCampaignDetails(ctx context.Context, login, campaignID string) (domain.Campaign, []domain.Progress, string, error) {
	body, _ := json.Marshal(map[string]any{
		"query":     detailsQuery,
		"variables": map[string]string{"login": login, "id": campaignID},
	})
	var raw string
	if err := p.gqlEval(ctx, body, &raw); err != nil {
		return domain.Campaign{}, nil, raw, fmt.Errorf("twitch: fetch details %s: %w", campaignID, err)
	}
	c, prog, err := gql.ParseCampaignDetails([]byte(raw))
	if err != nil {
		return domain.Campaign{}, nil, raw, fmt.Errorf("twitch: parse details %s (%.160s): %w", campaignID, raw, err)
	}
	return c, prog, raw, nil
}

// CampaignProgress returns the viewer's per-drop progress for one campaign from
// its details query (watcher.Pager). Unlike the inventory read it still lists
// a drop that just completed, whose campaign Twitch drops from
// dropCampaignsInProgress the moment it hits its required minutes, so the
// claim path can learn the dropInstanceID there. An empty login is resolved
// (and cached) via FetchLogin.
func (p *Pages) CampaignProgress(ctx context.Context, campaignID, login string) ([]domain.Progress, error) {
	if login == "" {
		l, err := p.FetchLogin(ctx)
		if err != nil {
			return nil, err
		}
		login = l
	}
	_, prog, _, err := p.FetchCampaignDetails(ctx, login, campaignID)
	return prog, err
}

// currentDropQuery is a full dropCurrentSession query (not a persisted hash).
// It returns data.currentUser.dropCurrentSession, matching gql.ParseCurrentDrop.
// The page itself only fires DropCurrentSessionContext on load/panel-open (live
// updates arrive over a WebSocket the fetch/XHR capture shim cannot see), so
// the watcher polls this actively while watching.
const currentDropQuery = `{currentUser{dropCurrentSession{dropID currentMinutesWatched requiredMinutesWatched channel{id login}}}}`

// FetchCurrentDrop fetches the viewer's live drop-watching session via a
// page-context GraphQL call. It returns (nil, nil) when no session is active.
func (p *Pages) FetchCurrentDrop(ctx context.Context) (*domain.Progress, error) {
	body, _ := json.Marshal(map[string]any{"query": currentDropQuery})
	var raw string
	if err := p.gqlEval(ctx, body, &raw); err != nil {
		return nil, fmt.Errorf("twitch: fetch current drop: %w", err)
	}
	prog, err := gql.ParseCurrentDrop([]byte(raw))
	if err != nil {
		return nil, fmt.Errorf("twitch: parse current drop (%.160s): %w", raw, err)
	}
	return prog, nil
}

// inventoryQuery is a full inventory query (not a persisted hash). It returns
// data.currentUser.inventory.dropCampaignsInProgress, matching
// gql.ParseInventory, and exists so the watcher can refresh per-drop server
// progress mid-watch without navigating away from the channel page (the
// inventory PAGE cannot be read while watching).
const inventoryQuery = `{currentUser{inventory{dropCampaignsInProgress{id timeBasedDrops{id requiredMinutesWatched self{currentMinutesWatched isClaimed dropInstanceID hasPreconditionsMet}}}}}}`

// FetchInventoryProgress fetches server-confirmed progress for every
// in-progress drop via a page-context GraphQL call.
func (p *Pages) FetchInventoryProgress(ctx context.Context) ([]domain.Progress, error) {
	body, _ := json.Marshal(map[string]any{"query": inventoryQuery})
	var raw string
	if err := p.gqlEval(ctx, body, &raw); err != nil {
		return nil, fmt.Errorf("twitch: fetch inventory progress: %w", err)
	}
	ps, err := gql.ParseInventory([]byte(raw))
	if err != nil {
		return nil, fmt.Errorf("twitch: parse inventory progress (%.160s): %w", raw, err)
	}
	return ps, nil
}

// CapturedHeaderKeys returns the names (not values) of the GraphQL request
// headers captured from the page, so a run can confirm whether the integrity
// token was picked up without ever logging the secret values.
func (p *Pages) CapturedHeaderKeys(ctx context.Context) []string {
	var keys []string
	_ = p.ctrl.Session().Eval(ctx, `(() => Object.keys(window.__dw_headers || {}))()`, &keys)
	return keys
}

// liveChannelsQuery resolves a batch of logins to their current stream, if any.
// Not a persisted hash, so it is immune to hash rotation. A user with no
// stream field is offline; Twitch accepts up to 100 logins per call.
const liveChannelsQuery = `query($logins:[String!]!){users(logins:$logins){id login displayName stream{id viewersCount game{id displayName}}}}`

// LiveChannels returns the subset of logins that are live right now, with
// viewer counts, via a page-context GraphQL call. Used by selection for ACL
// campaigns (the allowlist says nothing about who is streaming) and by
// validation, because an offline channel with "autoplay recent broadcast"
// plays a VOD that passes every playback gate without ever crediting a drop.
func (p *Pages) LiveChannels(ctx context.Context, logins []string) ([]domain.Channel, error) {
	return liveChannelsChunked(logins, func(chunk []string) ([]domain.Channel, error) {
		body, _ := json.Marshal(map[string]any{
			"query":     liveChannelsQuery,
			"variables": map[string]any{"logins": chunk},
		})
		var raw string
		if err := p.gqlEval(ctx, body, &raw); err != nil {
			return nil, fmt.Errorf("twitch: fetch live channels: %w", err)
		}
		return gql.ParseLiveChannels([]byte(raw))
	})
}

// liveChannelsBatch is Twitch's users(logins:) cap per call.
const liveChannelsBatch = 100

// liveChannelsChunked runs fetch over logins in batches of liveChannelsBatch
// and merges the live results. A failed batch is logged and skipped - its
// logins read as offline - so one bad batch never degrades a large allowlist
// to "assume everyone is live". Only when EVERY batch fails is the last error
// returned (the caller's own fallback applies then).
func liveChannelsChunked(logins []string, fetch func([]string) ([]domain.Channel, error)) ([]domain.Channel, error) {
	if len(logins) == 0 {
		return nil, nil
	}
	var out []domain.Channel
	var lastErr error
	ok := 0
	for start := 0; start < len(logins); start += liveChannelsBatch {
		end := min(start+liveChannelsBatch, len(logins))
		chs, err := fetch(logins[start:end])
		if err != nil {
			lastErr = err
			log.Warn().Err(err).Int("from", start).Int("to", end).Int("total", len(logins)).
				Msg("twitch: live channels batch failed; treating its logins as offline")
			continue
		}
		ok++
		out = append(out, chs...)
	}
	if ok == 0 {
		return nil, lastErr
	}
	return out, nil
}

// claimMutation is the web client's DropsPage_ClaimDropRewards mutation as full
// query text (not a persisted hash). Its status field is parsed by
// gql.ParseClaimDropRewards.
const claimMutation = `mutation DropsPage_ClaimDropRewards($input: ClaimDropRewardsInput!) { claimDropRewards(input: $input) { status isUserAccountConnected __typename } }`

// ClaimDrop claims one earned drop by its dropInstanceID (domain.Progress.ClaimID)
// via a page-context GraphQL mutation, replaying the page's own auth and
// client-integrity headers like every other query here. It returns the raw
// claimDropRewards.status (when present), the raw response body for debug
// logging, and a non-nil error unless the status means the reward is on the
// account. verify live: whether Twitch accepts the mutation with replayed
// integrity headers, and the real status strings.
func (p *Pages) ClaimDrop(ctx context.Context, dropInstanceID string) (string, string, error) {
	if dropInstanceID == "" {
		return "", "", fmt.Errorf("twitch: claim: empty drop instance id")
	}
	body, _ := json.Marshal(map[string]any{
		"operationName": "DropsPage_ClaimDropRewards",
		"query":         claimMutation,
		"variables":     map[string]any{"input": map[string]string{"dropInstanceID": dropInstanceID}},
	})
	var raw string
	if err := p.gqlEval(ctx, body, &raw); err != nil {
		return "", raw, fmt.Errorf("twitch: claim %s: %w", dropInstanceID, err)
	}
	status, err := gql.ParseClaimDropRewards([]byte(raw))
	if err != nil {
		return status, raw, fmt.Errorf("twitch: claim %s: %w", dropInstanceID, err)
	}
	return status, raw, nil
}
