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
// it through the Session.Eval seam. Tried carries one compact entry per
// candidate endpoint attempted (path, status, content-type or body key NAMES -
// never values), so endpoint drift is mapped from the diagnostic instead of
// another blind guess.
type LoginProbe struct {
	State  string   `json:"state"`
	Status int      `json:"status"`
	Tried  []string `json:"tried"`
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
    // Candidate authenticated endpoints, first decisive answer wins. A 200
    // that is NOT JSON is the SPA catch-all serving the app shell for a dead
    // API route (live finding 2026-08-31 for /api/v1/user) - never an answer.
    const tried = [];
    for (const u of ['/api/v1/user', '/api/v2/user', 'https://web.kick.com/api/v1/user']) {
      try {
        const r = await fetch(u, { credentials: 'include', headers: { 'Accept': 'application/json' } });
        if (r.status === 401 || r.status === 403) return { state: 'out', status: r.status };
        const ct = (r.headers.get('content-type') || '').split(';')[0];
        if (r.ok && ct.includes('json')) {
          const b = await r.json().catch(() => null);
          const authed = !!(b && (b.id || b.user_id || (b.user && b.user.id) || b.username || (b.data && (b.data.id || b.data.username))));
          if (authed) return { state: 'in', status: r.status };
          // Key NAMES only (never values): enough to map a drifted shape.
          tried.push(u + ' 200-json keys:' + (b ? Object.keys(b).slice(0, 8).join('|') : 'null'));
          continue;
        }
        tried.push(u + ' ' + r.status + ' ' + ct);
      } catch (e) { tried.push(u + ' fetch-error'); }
    }
    return { state: 'unknown', tried: tried };
  } catch (e) { return { state: 'unknown', tried: [String(e)] }; }
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
		detail := strings.Join(out.Tried, "; ")
		if detail == "" {
			detail = fmt.Sprintf("probe status %d", out.Status)
		}
		return LoginState{}, detail, nil
	}
}
