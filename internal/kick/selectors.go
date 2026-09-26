package kick

// All selectors and page JS in this file are VERIFY-LIVE guesses: Kick ships a
// Vue/Tailwind frontend without the stable data-test attributes Twitch has, so
// these lean on ARIA labels, URL shapes, and structural patterns. Expect a
// live-debugging pass exactly like the Twitch one (see docs/NOTES-kick.md);
// the Debugging view's log tail plus kick debug dumps are the tools for it.

const (
	// URLBase is the site root; channel pages live at kick.com/<login>.
	URLBase = "https://kick.com"
	// URLDrops is the viewer-facing drops campaigns page.
	URLDrops = "https://kick.com/drops"
	// URLInventory shows earned progress and the manual Claim buttons.
	URLInventory = "https://kick.com/drops/inventory"
	// URLDirectoryTmpl lists live channels for one category slug.
	URLDirectoryTmpl = "https://kick.com/categories/%s"

	// campaignsAPI is the UNOFFICIAL campaigns listing used by community
	// tools; fetched from page context so it inherits the session's cookies
	// and Cloudflare clearance. Undocumented - may change or vanish.
	campaignsAPI = "https://web.kick.com/api/v1/drops/campaigns"
)

// selClaimButton targets a claim affordance on the inventory page.
// verify live: Kick's inventory claim button markup.
const selClaimButton = `button[aria-label*='Claim' i], button[data-testid*='claim' i]`

// selPlayButton targets the player's play control.
// verify live: Kick player controls markup.
const selPlayButton = `button[aria-label*='Play' i], [data-testid='play-button']`

// jsHealthProbe mirrors the Twitch probe: one Eval, one JSON result. The
// offline gate keys on Kick's offline banner text/slot; the drops indicator is
// reported true when no drops widget is found because its absence must not
// block validation until the real selector is confirmed live.
const jsHealthProbe = `(() => {
  const v = document.querySelector('video');
  const bodyText = (document.body && document.body.innerText || '');
  const offline = !v && /is offline|currently offline/i.test(bodyText.slice(0, 4000));
  const err = !!document.querySelector('[data-testid*="error" i], .player-error');
  return {
    pathname: location.pathname,
    readyState: v ? v.readyState : 0,
    paused: v ? v.paused : true,
    ended: v ? v.ended : false,
    currentTime: v ? v.currentTime : 0,
    volume: v ? v.volume : 0,
    muted: v ? v.muted : false,
    offlineGate: offline,
    playerError: err,
    raidBanner: false,
    raidTarget: '',
    dropsIndicator: true,
    visibility: document.visibilityState
  };
})()`

// jsSeedPlayer keeps the video element at the policy volume. Kick has no known
// localStorage volume key to seed (verify live), so an interval re-applies the
// volume until the player exists, then keeps it pinned.
func jsSeedPlayer(volume float64) string {
	v := jsNum(volume)
	return `(() => {
  if (window.__dw_kick_vol) return;
  window.__dw_kick_vol = setInterval(() => {
    const el = document.querySelector('video');
    if (el) { if (Math.abs(el.volume - ` + v + `) > 0.001) el.volume = ` + v + `; if (el.muted) el.muted = false; }
  }, 5000);
})()`
}

// jsReassertVolume forces the current video element back to the policy volume.
func jsReassertVolume(volume float64) string {
	v := jsNum(volume)
	return `(() => { const el = document.querySelector('video'); if (el) { el.volume = ` + v + `; el.muted = false; } })()`
}

// jsDirectoryChannels scrapes live-channel logins from a category page:
// anchors whose href is a bare /<login> path, deduped, order preserved.
// verify live: category page card markup and whether href carries a prefix.
const jsDirectoryChannels = `(() => {
  const seen = new Set(); const out = [];
  const reserved = new Set(['drops','categories','category','search','help','terms','privacy','browse','following','messages','subscriptions','transactions','dashboard','community-guidelines']);
  for (const a of document.querySelectorAll('a[href^="/"]')) {
    const m = a.getAttribute('href').match(/^\/([A-Za-z0-9_-]{3,25})$/);
    if (!m) continue;
    const login = m[1].toLowerCase();
    if (reserved.has(login) || seen.has(login)) continue;
    seen.add(login); out.push(login);
    if (out.length >= 30) break;
  }
  return out;
})()`

// jsFetchCampaigns fetches the unofficial campaigns API from page context so
// cookies + Cloudflare clearance ride along, returning the raw response text.
const jsFetchCampaigns = `(async () => {
  try {
    const r = await fetch('` + campaignsAPI + `', { credentials: 'include', headers: { 'Accept': 'application/json' } });
    return JSON.stringify({ status: r.status, body: await r.text() });
  } catch (e) { return JSON.stringify({ status: 0, body: String(e) }); }
})()`

// jsSeedNetCapture installs a passive fetch/XHR shim that buffers response
// bodies for drops-related URLs into window.__dw_kick_net. It is DEBUG-ONLY
// (seeded only when capture_gql_bodies is on) and is the Kick analog of the
// Twitch GQL capture shim: Kick's drop-progress endpoint/DOM is unknown, so
// when a real Kick drop is finally live this records whatever the page fetches
// (path matches drop|inventor|campaign|quest|reward) verbatim, to build the
// progress parser from real data instead of a guess. Idempotent via a window
// flag so re-seeding each pass does not double-wrap. verify live.
const jsSeedNetCapture = `(() => {
  if (window.__dw_kick_net_seeded) return;
  window.__dw_kick_net_seeded = true;
  window.__dw_kick_net = [];
  const want = (u) => /drop|inventor|campaign|quest|reward/i.test(u || '');
  const push = (rec) => { try { if (window.__dw_kick_net.length < 100) window.__dw_kick_net.push(rec); } catch (e) {} };
  const of = window.fetch;
  if (of) {
    window.fetch = function(...a) {
      const url = (a[0] && a[0].url) || a[0] || '';
      return of.apply(this, a).then((res) => {
        if (want(url)) { try { res.clone().text().then((t) => push({ src: 'fetch', url: String(url), status: res.status, body: (t || '').slice(0, 20000), at: Date.now() })); } catch (e) {} }
        return res;
      });
    };
  }
  const oo = XMLHttpRequest.prototype.open, osend = XMLHttpRequest.prototype.send;
  XMLHttpRequest.prototype.open = function(m, u, ...r) { this.__dw_url = u; return oo.call(this, m, u, ...r); };
  XMLHttpRequest.prototype.send = function(...a) {
    this.addEventListener('load', () => { try { if (want(this.__dw_url)) push({ src: 'xhr', url: String(this.__dw_url), status: this.status, body: (this.responseText || '').slice(0, 20000), at: Date.now() }); } catch (e) {} });
    return osend.apply(this, a);
  };
})()`

// jsDrainNetCapture returns the buffered drops-related network records as JSON
// and clears the buffer.
const jsDrainNetCapture = `(() => { const b = window.__dw_kick_net || []; window.__dw_kick_net = []; return JSON.stringify(b); })()`

// jsInventoryRawHTML returns the inventory page's full rendered HTML (Kick is a
// Vue SPA that renders drop progress into the DOM), capped for a debug dump.
const jsInventoryRawHTML = `(() => (document.documentElement && document.documentElement.outerHTML || '').slice(0, 400000))()`

// jsNum renders a float for embedding in JS.
func jsNum(f float64) string {
	// Small fixed precision keeps the literal stable and locale-free.
	s := make([]byte, 0, 8)
	s = append(s, '0', '.')
	n := int(f*1000 + 0.5)
	s = append(s, byte('0'+n/100%10), byte('0'+n/10%10), byte('0'+n%10))
	return string(s)
}
