package kick

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// claimAPI is the reward claim the drops inventory page posts (its client
// calls it useClaimReward): body {reward_id, campaign_id}, same bearer
// token as progressAPI. Unofficial like the rest.
const claimAPI = "https://web.kick.com/api/v1/drops/claim"

// ClaimIDFor is the claim id the progress rows carry for a Kick reward:
// "<campaign>#<reward>", the two ids the claim call needs. It is derived
// from Kick's own ids, never from the "kick-" prefixed drop id.
func ClaimIDFor(campaignID, rewardID string) string {
	return campaignID + "#" + rewardID
}

// splitClaimID is the inverse of ClaimIDFor.
func splitClaimID(claimID string) (campaignID, rewardID string, ok bool) {
	c, r, found := strings.Cut(claimID, "#")
	if !found || c == "" || r == "" {
		return "", "", false
	}
	return c, r, true
}

// jsClaimReward posts the claim from page context with the session bearer
// token, in the same {status, body, token} wrapper as jsFetchProgress.
func jsClaimReward(campaignID, rewardID string) string {
	body, _ := json.Marshal(map[string]string{"reward_id": rewardID, "campaign_id": campaignID})
	return `(async () => {
  try {
    const m = document.cookie.match(/(?:^|; )session_token=([^;]*)/);
    const tok = m ? decodeURIComponent(m[1]) : '';
    const headers = { 'Accept': 'application/json', 'Content-Type': 'application/json', 'x-app-platform': 'web' };
    if (tok) headers['Authorization'] = 'Bearer ' + tok;
    const r = await fetch('` + claimAPI + `', { method: 'POST', credentials: 'include', headers, body: ` + jsString(string(body)) + `, signal: AbortSignal.timeout(20000) });
    return JSON.stringify({ status: r.status, body: await r.text(), token: !!tok });
  } catch (e) { return JSON.stringify({ status: 0, body: String(e) }); }
})()`
}

// jsString renders s as a JS string literal.
func jsString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// ClaimStatusClaimed is the status ClaimDrop reports for an accepted claim;
// the engine confirms it against the inventory afterwards either way.
const ClaimStatusClaimed = "claimed"

// ClaimDrop claims one reward via claimAPI. claimID is ClaimIDFor's form. A
// non-2xx answer is an error carrying the status and a body sample, so the
// engine falls back to the inventory page and, failing that, parks the drop
// for a manual claim.
func (p *Pages) ClaimDrop(ctx context.Context, claimID string) (status, raw string, err error) {
	campaignID, rewardID, ok := splitClaimID(claimID)
	if !ok {
		return "", "", fmt.Errorf("kick: claim id %q is not campaign#reward", claimID)
	}
	sess := p.ctrl.Session()
	if cur, err := sess.CurrentURL(ctx); err != nil || !strings.HasPrefix(cur, URLBase) {
		if err := sess.Navigate(ctx, URLDrops); err != nil {
			return "", "", fmt.Errorf("kick: open site for claim: %w", err)
		}
		p.settle(ctx)
	}
	var out string
	if err := sess.Eval(ctx, jsClaimReward(campaignID, rewardID), &out); err != nil {
		return "", "", fmt.Errorf("kick: claim fetch: %w", err)
	}
	var wrapped struct {
		Status int    `json:"status"`
		Body   string `json:"body"`
		Token  bool   `json:"token"`
	}
	if err := json.Unmarshal([]byte(out), &wrapped); err != nil {
		return "", out, fmt.Errorf("kick: decode claim wrapper (%.120s): %w", out, err)
	}
	if wrapped.Status < 200 || wrapped.Status > 299 {
		return fmt.Sprintf("http_%d", wrapped.Status), wrapped.Body,
			fmt.Errorf("kick: claim endpoint returned %d (session_token cookie present: %v) (%.160s)", wrapped.Status, wrapped.Token, wrapped.Body)
	}
	return ClaimStatusClaimed, wrapped.Body, nil
}
