package kick

import (
	"context"
	"fmt"
	"strings"
)

// LoginState is the Kick login probe result. Known=false means the answer is
// unknowable (endpoint shape drifted, network failure, still off-origin) and
// the caller must report null rather than guess.
type LoginState struct {
	LoggedIn bool
	Known    bool
}

// LoginProbe is the raw JS probe result. Exported so test fakes can populate
// it through the Session.Eval seam. Keys carries the top-level body key NAMES
// on an unrecognized 2xx (never values), so a shape drift can be mapped from
// the diagnostic without another blind guess.
type LoginProbe struct {
	State  string   `json:"state"`
	Status int      `json:"status"`
	Keys   []string `json:"keys"`
}

// jsLoggedIn probes the session via kick.com's own authenticated user endpoint
// from PAGE CONTEXT (cookies + Cloudflare clearance ride along, same pattern
// as jsFetchCampaigns) - an API status check, NOT a DOM-selector guess:
// 401/403 = logged out, 2xx with a user object = logged in, anything else =
// unknown. Same-origin only, so the caller ensures the tab is on kick.com.
// verify live: endpoint path (/api/v1/user) + body shape.
const jsLoggedIn = `(async () => {
  try {
    if (!/(^|\.)kick\.com$/.test(location.hostname)) return { state: 'wrong-origin' };
    const r = await fetch('/api/v1/user', { credentials: 'include', headers: { 'Accept': 'application/json' } });
    if (r.status === 401 || r.status === 403) return { state: 'out', status: r.status };
    if (!r.ok) return { state: 'unknown', status: r.status };
    const b = await r.json().catch(() => null);
    const authed = !!(b && (b.id || b.user_id || (b.user && b.user.id) || b.username));
    // Key NAMES only (never values): enough to map a drifted body shape.
    return { state: authed ? 'in' : 'unknown', status: r.status, keys: b ? Object.keys(b).slice(0, 12) : [] };
  } catch (e) { return { state: 'unknown', status: 0 }; }
})()`

// CheckLogin reports whether the Kick session is authenticated. The login flow
// parks the tab on kick.com already; if the tab is somewhere else entirely it
// navigates home once and retries (callers gate this on a parked engine, same
// as the Twitch probe - the navigation must never hit a mid-watch tab).
func CheckLogin(ctx context.Context, sess interface {
	Navigate(context.Context, string) error
	Eval(context.Context, string, any) error
}) (LoginState, string, error) {
	var out LoginProbe
	if err := sess.Eval(ctx, jsLoggedIn, &out); err != nil {
		return LoginState{}, "", fmt.Errorf("kick: probe login: %w", err)
	}
	if out.State == "wrong-origin" {
		if err := sess.Navigate(ctx, URLBase); err != nil {
			return LoginState{}, "", fmt.Errorf("kick: navigate home: %w", err)
		}
		if err := sess.Eval(ctx, jsLoggedIn, &out); err != nil {
			return LoginState{}, "", fmt.Errorf("kick: probe login: %w", err)
		}
	}
	switch out.State {
	case "in":
		return LoginState{LoggedIn: true, Known: true}, "", nil
	case "out":
		return LoginState{Known: true}, "", nil
	case "wrong-origin":
		return LoginState{}, "tab not on kick.com even after navigating home", nil
	default:
		// Unknown: name WHY, so the endpoint/shape drift is fixable from the
		// UI message instead of another blind guess.
		detail := fmt.Sprintf("probe status %d", out.Status)
		if len(out.Keys) > 0 {
			detail += ", body keys: " + strings.Join(out.Keys, ",")
		}
		return LoginState{}, detail, nil
	}
}
