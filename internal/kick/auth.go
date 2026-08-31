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
    // The site's own who-am-I call is GET web.kick.com/api/v1/user/session
    // (found live 2026-08-31 via its CORS preflight - which also proves it
    // carries a non-simple header, i.e. a bearer token). The SPA reads that
    // token from a JS-accessible store, so hunt the usual places: cookies and
    // local/sessionStorage keys that look like a session/auth token.
    const tried = [];
    const tokens = [];
    try {
      for (const kv of document.cookie.split('; ')) {
        const i = kv.indexOf('=');
        if (i < 0) continue;
        const name = kv.slice(0, i);
        if (/session_token|kick_session|auth[-_]?token/i.test(name)) tokens.push([ 'cookie:' + name, decodeURIComponent(kv.slice(i + 1)) ]);
      }
      for (const store of [localStorage, sessionStorage]) {
        for (let i = 0; i < store.length; i++) {
          const k = store.key(i);
          if (/session[-_]?token|auth[-_]?token|access[-_]?token/i.test(k)) tokens.push([ 'storage:' + k, store.getItem(k) ]);
        }
      }
    } catch (e) { tried.push('token-hunt-error'); }

    const url = 'https://web.kick.com/api/v1/user/session';
    const attempts = [['plain', null]].concat(tokens.slice(0, 4));
    let sawTokenReject = false;
    for (const [label, tok] of attempts) {
      try {
        const headers = { 'Accept': 'application/json' };
        if (tok) headers['Authorization'] = 'Bearer ' + tok.replace(/^"|"$/g, '');
        const r = await fetch(url, { credentials: 'include', headers: headers });
        const ct = (r.headers.get('content-type') || '').split(';')[0];
        if (r.ok && ct.includes('json')) {
          const b = await r.json().catch(() => null);
          // The live shape is { data, message }: dig into data (and its
          // nested user/session) for anything account-shaped.
          const cands = [b, b && b.data, b && b.data && b.data.user, b && b.data && b.data.session, b && b.user];
          let authed = cands.some(c => !!(c && typeof c === 'object' && (c.id || c.user_id || c.username || c.email || c.slug)));
          // Live shape 2026-08-31: { data: { session_id }, message } - a
          // SESSION endpoint, not a user object. A bare session_id could be an
          // anonymous session, so it only counts as logged in when an auth
          // token was also found in the browser (the session_token cookie only
          // exists on an authenticated kick.com). verify live on a LOGGED-OUT
          // browser: confirm session_token is absent there.
          if (!authed && b && b.data && b.data.session_id && tokens.length > 0) authed = true;
          if (authed) return { state: 'in', status: r.status };
          let dataDesc = 'absent';
          if (b && 'data' in b) {
            dataDesc = b.data === null ? 'null' : (typeof b.data === 'object' ? 'keys:' + Object.keys(b.data).slice(0, 8).join('|') : typeof b.data);
          }
          tried.push(label + ' 200-json keys:' + (b ? Object.keys(b).slice(0, 8).join('|') : 'null') + ' data=' + dataDesc);
          continue;
        }
        if ((r.status === 401 || r.status === 403) && tok) sawTokenReject = true;
        tried.push(label + ' ' + r.status + ' ' + ct);
      } catch (e) { tried.push(label + ' fetch-error'); }
    }
    // Every found token was rejected outright: the session is stale - a
    // definite logged-out, not an unknown.
    if (sawTokenReject && tokens.length > 0) return { state: 'out', status: 401 };
    if (tokens.length === 0) tried.push('no session-token cookie/storage found');
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
