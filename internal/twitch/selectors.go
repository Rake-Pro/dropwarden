package twitch

// This file centralizes every Twitch-specific selector and injected JS snippet.
// A Twitch redesign is a one-file fix. Every selector is marked verify-live:
// they must be confirmed against the real authenticated site during phase 1-3
// testing (see docs/NOTES-twitch.md) because they cannot be checked in-container.

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
	selRaidCancel    = "[data-test-selector='raid-cancel-button'], button[aria-label*='Cancel']"
	selDropsCallout  = "[data-test-selector='drops-campaign-details'], .drops-campaign-callout"
	selDropsChatIcon = "button[aria-label*='Drops'], [data-a-target='drops-in-chat']"
	selClaimButton   = "button[data-test-selector='DropsCampaignInProgressRewardPresentation-claim-button'], button[aria-label*='Claim']"
	selInventoryCard = "[data-test-selector='DropsCampaignInProgressDescription'], .inventory-drop-card" // verify live
)

// jsHealthProbe returns one JSON object describing playback and drop state in a
// single round trip. It never throws: missing elements yield defaults so the
// watchdog always gets a snapshot. // verify live: selector accuracy.
const jsHealthProbe = `(() => {
  const q = (s) => document.querySelector(s);
  const v = q('video');
  const gate = !!q("[data-a-target='player-overlay-content-gate'], .content-overlay-gate");
  const perr = !!q("[data-a-target='player-error-content'], .player-error");
  const raid = q("[data-test-selector='raid-banner'], .raid-banner");
  let raidTarget = '';
  if (raid) {
    const a = raid.querySelector("a[href^='/']");
    if (a) raidTarget = a.getAttribute('href').replace(/^\//,'').split(/[/?#]/)[0].toLowerCase();
  }
  const drops = !!q("[data-test-selector='drops-campaign-details'], .drops-campaign-callout, button[aria-label*='Drops'], [data-a-target='drops-in-chat']");
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
