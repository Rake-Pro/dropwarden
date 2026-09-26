package twitch

// This file centralizes every Twitch-specific selector and injected JS snippet.
// A Twitch redesign is a one-file fix. Every selector is marked verify-live:
// they must be confirmed against the real authenticated site during phase 1-3
// testing (see docs/NOTES-twitch.md) because they cannot be checked in-container.

import "encoding/json"

// URLs.
const (
	URLBase          = "https://www.twitch.tv"
	URLCampaigns     = "https://www.twitch.tv/drops/campaigns"
	URLInventory     = "https://www.twitch.tv/drops/inventory"
	URLDirectoryTmpl = "https://www.twitch.tv/directory/category/%s?filter=drops" // verify live: drops filter param
	gqlEndpoint      = "https://gql.twitch.tv/gql"                                // verify live: exact host/path
)

// Player/DOM selectors. // verify live (all).
const (
	selVideo         = "video"
	selPlayButton    = "button[data-a-target='player-play-pause-button']"
	selOfflineGate   = "[data-a-target='player-overlay-content-gate'], .content-overlay-gate"
	selPlayerError   = "[data-a-target='player-error-content'], .player-error"
	selRaidBanner    = "[data-test-selector='raid-banner'], .raid-banner"
	selRaidCancel    = "[data-test-selector='raid-banner'] [data-test-selector='raid-cancel-button'], .raid-banner [data-test-selector='raid-cancel-button'], [data-test-selector='raid-banner'] button[aria-label*='Cancel'], .raid-banner button[aria-label*='Cancel']" // scoped to the raid banner: an unscoped Cancel matched any dialog
	selDropsCallout  = "[data-test-selector='drops-campaign-details'], .drops-campaign-callout"
	selDropsChatIcon = "button[aria-label*='Drops'], [data-a-target='drops-in-chat']"
	selInventoryCard = "[data-test-selector='DropsCampaignInProgressDescription'], .inventory-drop-card" // verify live

	// selContentGateAccept is the content-classification (mature) gate's
	// "Start Watching" button. The gate replaces the player entirely until
	// acknowledged, so validation can never pass while it is up. Deliberately
	// button-specific selectors only - a container-scoped `button` would risk
	// clicking the adjacent "Go Home". Twitch remembers the acknowledgement
	// per channel, so one click per channel suffices.
	selContentGateAccept = "button[data-a-target='content-classification-gate-overlay-start-watching-button'], button[data-a-target='player-overlay-mature-accept'], button[data-test-selector='content-classification-gate-overlay-start-watching-button']"
)

// jsHealthProbe returns one JSON object describing playback and drop state in a
// single round trip. It never throws: missing elements yield defaults so the
// watchdog always gets a snapshot. // verify live: selector accuracy.
const jsHealthProbe = `(() => {
  const q = (s) => document.querySelector(s);
  const v = q('video');
  // The content-classification (mature) gate renders the same overlay family
  // as the offline gate; it must NOT read as offline - it has an accept
  // button, which validation clicks via ClickPlay. Offline = gate WITHOUT one.
  const acceptBtn = q("button[data-a-target='content-classification-gate-overlay-start-watching-button'], button[data-a-target='player-overlay-mature-accept'], button[data-test-selector='content-classification-gate-overlay-start-watching-button']");
  const gate = !!q("[data-a-target='player-overlay-content-gate'], .content-overlay-gate") && !acceptBtn;
  const perr = !!q("[data-a-target='player-error-content'], .player-error");
  const raid = q("[data-test-selector='raid-banner'], .raid-banner");
  let raidTarget = '';
  if (raid) {
    const a = raid.querySelector("a[href^='/']");
    if (a) raidTarget = a.getAttribute('href').replace(/^\//,'').split(/[/?#]/)[0].toLowerCase();
  }
  // Advisory only (see watcher validation): element selectors first, then the
  // stream-tag row scanned by TEXT, because the "Drops Enabled" tag has no
  // stable data attribute. // verify live: 2026-08-31 the element selectors
  // matched nothing on healthy drops channels.
  let drops = !!q("[data-test-selector='drops-campaign-details'], .drops-campaign-callout, button[aria-label*='Drops'], [data-a-target='drops-in-chat']");
  if (!drops) {
    const tags = document.querySelectorAll("a[href*='/tags/'], [data-a-target='stream-tag'], button[data-a-target='tag'], .tw-tag");
    for (const t of tags) { if (/drops/i.test(t.textContent || '')) { drops = true; break; } }
  }
  return {
    pathname: location.pathname,
    readyState: v ? v.readyState : 0,
    paused: v ? v.paused : true,
    ended: v ? v.ended : false,
    currentTime: v ? v.currentTime : 0,
    volume: v ? v.volume : 0,
    muted: v ? v.muted : true,
    offlineGate: gate,
    playerError: perr,
    raidBanner: !!raid,
    raidTarget: raidTarget,
    dropsIndicator: drops,
    visibility: document.visibilityState,
  };
})()`

// jsCaptureShim hooks fetch and XHR so the page's OWN GraphQL responses are
// buffered into window.__dw_gql (capped) as {op, body} pairs. It adds no
// requests of its own - it only observes what the Twitch client already sends.
// operationName is read from the request body the client sent (single object or
// batched array). Installed via SeedOnNewDocument so it runs before page scripts.
// verify live: gql host, batched-array request shape.
const jsCaptureShim = `(() => {
  if (window.__dw_installed) return; window.__dw_installed = true;
  window.__dw_gql = [];
  const CAP = 200;
  const isGQL = (u) => typeof u === 'string' && u.indexOf('gql.twitch.tv/gql') !== -1;
  const opsOf = (body) => {
    try {
      const j = JSON.parse(body);
      const arr = Array.isArray(j) ? j : [j];
      return arr.map(x => (x && x.operationName) ? x.operationName : '');
    } catch (e) { return ['']; }
  };
  const push = (ops, text) => {
    try { window.__dw_gql.push({ ops: ops, body: text, at: Date.now() }); }
    catch (e) {}
    while (window.__dw_gql.length > CAP) window.__dw_gql.shift();
  };
  const captureHeaders = (init) => {
    try {
      const hh = init && init.headers;
      if (!hh) return;
      const h = {};
      if (typeof hh.forEach === 'function') hh.forEach((v, k) => { h[String(k).toLowerCase()] = v; });
      else Object.keys(hh).forEach(k => { h[k.toLowerCase()] = hh[k]; });
      // Only keep the auth/integrity headers we need to replay; ignore the rest.
      const keep = {};
      ['client-id','client-integrity','authorization','client-session-id','client-version','x-device-id','device-id'].forEach(k => { if (h[k]) keep[k] = h[k]; });
      if (Object.keys(keep).length) window.__dw_headers = Object.assign(window.__dw_headers || {}, keep);
    } catch (e) {}
  };
  const of = window.fetch;
  window.fetch = function(input, init) {
    let url = (typeof input === 'string') ? input : (input && input.url) || '';
    let reqBody = (init && init.body) || '';
    const p = of.apply(this, arguments);
    if (isGQL(url)) {
      captureHeaders(init);
      p.then(r => { try { r.clone().text().then(t => push(opsOf(reqBody), t)); } catch(e){} }).catch(()=>{});
    }
    return p;
  };
  const oOpen = XMLHttpRequest.prototype.open;
  const oSend = XMLHttpRequest.prototype.send;
  XMLHttpRequest.prototype.open = function(m, u) { this.__dw_url = u; return oOpen.apply(this, arguments); };
  XMLHttpRequest.prototype.send = function(body) {
    if (isGQL(this.__dw_url)) {
      this.addEventListener('load', function() {
        try { push(opsOf(body || ''), this.responseText); } catch(e){}
      });
    }
    return oSend.apply(this, arguments);
  };

  // Heartbeat capture: the player reports watch time to spade.twitch.tv as
  // base64 JSON event batches (sendBeacon or fetch). Record each event's name
  // and the crediting-relevant properties so a "no credit" verdict can say
  // whether the player was heartbeating at all. Values only, no auth.
  window.__dw_spade = [];
  const SPADE_CAP = 120;
  const isSpade = (u) => typeof u === 'string' && u.indexOf('spade.twitch.tv') !== -1;
  const pushSpade = (text) => {
    try {
      let raw = String(text || '');
      const m = raw.match(/(?:^|&)data=([^&]+)/);
      if (m) raw = decodeURIComponent(m[1]);
      let arr = JSON.parse(atob(raw));
      if (!Array.isArray(arr)) arr = [arr];
      arr.forEach(ev => {
        if (!ev || !ev.event) return;
        const p = ev.properties || {};
        window.__dw_spade.push({ event: ev.event, at: Date.now(),
          channel: p.channel || p.login || '', channel_id: String(p.channel_id || ''),
          hidden: !!p.hidden, muted: !!p.muted, volume: (typeof p.volume === 'number') ? p.volume : -1,
          player: p.player || '', live: (p.live === undefined) ? null : !!p.live,
          logged_in: (p.logged_in === undefined) ? null : !!p.logged_in });
      });
      while (window.__dw_spade.length > SPADE_CAP) window.__dw_spade.shift();
    } catch (e) {}
  };
  const bodyText = (b, cb) => {
    try {
      if (!b) return;
      if (typeof b === 'string') return cb(b);
      if (b instanceof URLSearchParams) return cb(b.toString());
      if (typeof FormData !== 'undefined' && b instanceof FormData) return cb('data=' + encodeURIComponent(b.get('data') || ''));
      if (typeof Blob !== 'undefined' && b instanceof Blob) { b.text().then(cb).catch(()=>{}); return; }
    } catch (e) {}
  };
  const oBeacon = navigator.sendBeacon ? navigator.sendBeacon.bind(navigator) : null;
  if (oBeacon) {
    navigator.sendBeacon = function(url, data) {
      if (isSpade(url)) bodyText(data, pushSpade);
      return oBeacon(url, data);
    };
  }
  const of2 = window.fetch;
  window.fetch = function(input, init) {
    const url = (typeof input === 'string') ? input : (input && input.url) || '';
    if (isSpade(url) && init && init.body) bodyText(init.body, pushSpade);
    return of2.apply(this, arguments);
  };
  const oSend2 = XMLHttpRequest.prototype.send;
  XMLHttpRequest.prototype.send = function(body) {
    if (isSpade(this.__dw_url)) bodyText(body, pushSpade);
    return oSend2.apply(this, arguments);
  };

  // Drop-progress pushes ride the site's WebSocket (PubSub or Hermes framing).
  // Keep any message that mentions drop events, raw, for the Go side to parse.
  window.__dw_ws = [];
  const WS_CAP = 60;
  const OW = window.WebSocket;
  if (OW) {
    const DW = function(url, protocols) {
      const ws = (protocols === undefined) ? new OW(url) : new OW(url, protocols);
      try {
        ws.addEventListener('message', (ev) => {
          try {
            const d = (typeof ev.data === 'string') ? ev.data : '';
            if (d && /drop-progress|drop-claim|user-drop-events|drop_id/.test(d)) {
              window.__dw_ws.push({ url: String(url), at: Date.now(), body: d.slice(0, 4000) });
              while (window.__dw_ws.length > WS_CAP) window.__dw_ws.shift();
            }
          } catch (e) {}
        });
      } catch (e) {}
      return ws;
    };
    DW.prototype = OW.prototype;
    ['CONNECTING','OPEN','CLOSING','CLOSED'].forEach(k => { DW[k] = OW[k]; });
    window.WebSocket = DW;
  }
})()`

// jsDrainHeartbeat returns and clears the spade heartbeat and WebSocket drop
// event buffers as one JSON string.
const jsDrainHeartbeat = `(() => {
  const s = window.__dw_spade || []; window.__dw_spade = [];
  const w = window.__dw_ws || []; window.__dw_ws = [];
  return JSON.stringify({ spade: s, ws: w });
})()`

// jsDrainCapture returns and clears the captured GQL buffer as a JSON string.
const jsDrainCapture = `(() => { const b = window.__dw_gql || []; window.__dw_gql = []; return JSON.stringify(b); })()`

// jsDirectoryChannels scrapes live channel logins from a game directory page.
// It reads anchor hrefs of channel preview cards. // verify live: card selector
// and that the drops filter param actually restricts to drops-enabled streams.
const jsDirectoryChannels = `(() => {
  const out = [];
  const seen = {};
  const anchors = document.querySelectorAll("a[data-a-target='preview-card-image-link'], a.preview-card-image-link, article a[href^='/']");
  anchors.forEach(a => {
    const href = a.getAttribute('href') || '';
    const login = href.replace(/^\//,'').split(/[/?#]/)[0].toLowerCase();
    if (login && !seen[login] && !login.startsWith('directory') && !login.startsWith('videos')) {
      seen[login] = true;
      out.push(login);
    }
  });
  return out;
})()`

// jsLoggedIn reports whether the Twitch session appears authenticated by probing
// for the logged-in user avatar / absence of the login button. // verify live.
const jsLoggedIn = `(() => {
  const login = document.querySelector("button[data-a-target='login-button']");
  const avatar = document.querySelector("button[data-a-target='user-menu-toggle'], img.tw-image-avatar");
  return { loggedIn: !login && !!avatar, hasLoginButton: !!login };
})()`

// jsInventoryButtons collects every button-like element on the current page
// (the drops inventory) with the attributes a claim selector could key on, so
// the owner can pick the exact claim.inventory_button_selector /
// claim.inventory_button_text from real markup when Twitch changes it, plus
// the page URL and its section headings (h2/h3/h4), which show whether an "In
// Progress" section rendered at all. Written to debug/inventory-buttons.json
// on every DOM claim fallback run as {url, headings, buttons}. Bounded: at
// most 400 buttons (text 200, outerHTML 600 characters) and 60 headings (120
// characters).
const jsInventoryButtons = `(() => {
  const out = [];
  document.querySelectorAll("button, a[role='button']").forEach(b => {
    if (out.length >= 400) return;
    const anc = b.parentElement ? b.parentElement.closest('[data-test-selector]') : null;
    out.push({
      text: (b.textContent || '').trim().slice(0, 200),
      ariaLabel: b.getAttribute('aria-label') || '',
      dataTestSelector: b.getAttribute('data-test-selector') || '',
      dataATarget: b.getAttribute('data-a-target') || '',
      className: (typeof b.className === 'string') ? b.className : '',
      ancestorTestSelector: anc ? (anc.getAttribute('data-test-selector') || '') : '',
      disabled: !!b.disabled,
      outerHTML: (b.outerHTML || '').slice(0, 600),
    });
  });
  const headings = [];
  document.querySelectorAll('h2, h3, h4').forEach(h => {
    if (headings.length >= 60) return;
    const t = String(h.textContent || '').replace(/\s+/g, ' ').trim();
    if (t) headings.push(h.tagName.toLowerCase() + ': ' + t.slice(0, 120));
  });
  return { url: location.href, headings: headings, buttons: out };
})()`

// jsLoadMore clicks every visible "Load More" button on the inventory page
// (label text, whitespace collapsed, case folded), then scrolls the window and
// Twitch's root scroller to the bottom so lazily rendered cards mount. It
// returns the number of buttons clicked. Plain element.click() is enough for a
// pagination control. // verify live: root scroller selector.
const jsLoadMore = `(() => {
  let n = 0;
  document.querySelectorAll("button, a[role='button'], [role='button']").forEach(b => {
    const t = String(b.textContent || '').replace(/\s+/g, ' ').trim().toLowerCase();
    if (t !== 'load more' || b.disabled) return;
    const r = b.getBoundingClientRect();
    if (r.width === 0 && r.height === 0) return;
    try { b.scrollIntoView({ block: 'center' }); b.click(); n++; } catch (e) {}
  });
  try { window.scrollTo(0, document.body.scrollHeight); } catch (e) {}
  document.querySelectorAll("[data-a-target='root-scroller'], .simplebar-scroll-content, main").forEach(el => {
    try { el.scrollTop = el.scrollHeight; } catch (e) {}
  });
  return n;
})()`

// jsMarkClaimButton returns JS that finds the claim button, scrolls it into
// view and tags it data-dw-claim="1" so the session's human-like CDP click can
// target it by attribute (Session.Click only takes a CSS selector, and the
// button is only distinguishable by its label text). Labels are compared with
// whitespace collapsed and case folded, in tiers, enabled buttons first:
//
//  1. "exact": an element matching selector whose text equals text;
//  2. "contains": an element matching selector whose text or aria-label
//     contains text;
//  3. "claim": any button whose text or aria-label contains "claim" (but not
//     "claimed"), for when the configured selector or text went stale.
//
// Live 2026-09-25 the selector plus "Claim Now" matched none of 40 buttons. On
// a miss the result lists the labels of up to 12 buttons on the page
// (candidates), so the warn log alone explains it. selector and text are
// injected as JSON string literals; an invalid selector skips tiers 1-2. The
// result is {found, matches, tier, candidates, error}.
func jsMarkClaimButton(selector, text string) string {
	sel, _ := json.Marshal(selector)
	txt, _ := json.Marshal(text)
	return `(() => {
  try {
    document.querySelectorAll('[data-dw-claim]').forEach(x => x.removeAttribute('data-dw-claim'));
    const norm = (s) => String(s || '').replace(/\s+/g, ' ').trim().toLowerCase();
    const label = (x) => norm(x.textContent);
    const aria = (x) => norm(x.getAttribute('aria-label'));
    const enabledFirst = (arr) => arr.filter(x => !x.disabled).concat(arr.filter(x => x.disabled));
    const want = norm(` + string(txt) + `);
    let els = [];
    try { els = Array.from(document.querySelectorAll(` + string(sel) + `)); } catch (e) {}
    const all = Array.from(document.querySelectorAll("button, a[role='button'], [role='button']"));
    const pick = (tier, arr) => {
      const hit = enabledFirst(arr)[0];
      if (!hit) return null;
      hit.scrollIntoView({ block: 'center' });
      hit.setAttribute('data-dw-claim', '1');
      return { found: true, matches: els.length, tier: tier, candidates: [], error: '' };
    };
    let r = null;
    if (want) {
      r = pick('exact', els.filter(x => label(x) === want)) ||
          pick('contains', els.filter(x => label(x).includes(want) || aria(x).includes(want)));
    }
    const claimish = (s) => s.includes('claim') && !s.includes('claimed');
    r = r || pick('claim', all.filter(x => claimish(label(x)) || claimish(aria(x))));
    if (r) return r;
    const candidates = [];
    for (const x of all) {
      if (candidates.length >= 12) break;
      const t = label(x) || (aria(x) ? '[aria] ' + aria(x) : '');
      if (t) candidates.push(t.slice(0, 60));
    }
    return { found: false, matches: els.length, tier: '', candidates: candidates, error: '' };
  } catch (e) { return { found: false, matches: 0, tier: '', candidates: [], error: String(e) }; }
})()`
}

// selMarkedClaim targets the element jsMarkClaimButton tagged.
const selMarkedClaim = "[data-dw-claim='1']"
