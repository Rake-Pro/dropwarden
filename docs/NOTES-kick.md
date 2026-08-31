# Kick drops support - research + design (2026-08-27)

Goal: earn Kick drops in parallel with Twitch - two platforms, two browser
instances, two engines, one app/UI. Research below is web-sourced (primary:
Kick's own dev-docs FAQ at github.com/KickEngineering/KickDevDocs); every
Kick-side selector/endpoint is verify-live until exercised on a real machine.

## How Kick drops work (researched, cited)

- Program is ACTIVE and expanding: platform-wide Drops launched with Rust as
  the inaugural partner (April 2026); Rust, PUBG, football-themed, and
  KICKS-token campaigns have run through mid-2026. (about.kick.com news)
- Watch time accrues automatically while watching a live, drops-eligible
  channel; progress refreshes ~every 2 minutes. Kick's FAQ WARNS that
  "changing tabs or refreshing frequently" can hurt tracking - our Twitch
  stall/refresh ladder is too aggressive for Kick defaults. (KickDevDocs FAQ)
- Watch time credits ONE stream at a time per account, but credits ALL
  simultaneous campaigns on that channel (same multi-tier semantics our
  set-coverage selection already models). (KickDevDocs FAQ)
- Progress surfaces on the live stream page and at kick.com/drops/inventory.
  Claiming is MANUAL - a Claim button appears at 100%, and unclaimed rewards
  EXPIRE after a developer-set grace period (stronger reason for our
  needs_action banner + a claim attempt than on Twitch). (KickDevDocs FAQ)
- Game-account linking is required BEFORE CLAIMING, not before earning - so
  unlike Twitch, an unlinked Kick campaign is still worth watching; linking
  becomes a claim-time needs_action instead of a selection-time skip.
  (KickDevDocs FAQ)
- Campaigns are category(game)-scoped; some are any-drops-enabled-stream in
  the category, others are streamer allowlists (e.g. Rust "Kickoff 2" via
  kick.facepunch.com listed specific streamers; general category watching did
  not count). Same ACL-vs-any-channel shape as Twitch. (KickDevDocs FAQ,
  corrosionhour.com)

## Technical surface

- Official dev API (docs.kick.com, OAuth): drops endpoints are
  developer/fulfillment-only (GET/PATCH /public/v1/drops/claims). No viewer
  campaign/progress API. Not useful to us.
- Unofficial: `https://web.kick.com/api/v1/drops/campaigns` lists active
  campaigns - used by HyperBeats/KickDropsMiner from an authenticated browser
  session. Single-source and undocumented; treat as bonus, design around PAGE
  scraping + page-context fetch (our Twitch header-replay pattern) first.
- Watch-time credit protocol: UNKNOWN (no confirmed heartbeat endpoint in any
  source). Plan accordingly: keep a REAL player playing (as we do on Twitch)
  and read progress from the inventory/stream page rather than trying to
  replay telemetry. Chat/events ride Pusher websockets; video is HLS via
  live-video.net.
- Cloudflare: Kick-specific detection details are thin, but the working
  community miner (KickDropsMiner) drives a REAL Chrome with persisted
  login cookies - exactly our architecture (dedicated instance, real profile,
  no --headless / --enable-automation, human-like clicks). Expect friction,
  not a wall. nodriver/undetected-chromedriver tricks exist as fallback
  hardening if CDP fingerprinting bites.
- Muted/background: KickDropsMiner ships mute + hidden-player modes and
  credits still accrue in practice (anecdotal but consistent with Twitch).
  Our sink-level --mute-audio on a second dedicated instance carries over.

## Why the codebase is ready for this (audited 2026-08-27)

- internal/queue and internal/store: ZERO Twitch references. domain mentions
  Twitch only in comments. The whole selection/coverage/eligibility machinery
  is platform-neutral already.
- internal/watcher touches Twitch only via three interface types
  (twitch.Controller, twitch.Health, twitch.PlayerPolicy) plus one
  "twitch.tv/" literal in loginFromPath. The engine is one goroutine with
  injected Deps - instantiating TWO engines is natural.
- internal/browser is generic (launch/flags/session); a second instance needs
  only its own profile_dir + window position.

## Design

One app process; per-platform: one Chrome instance, one engine, one config
block. Store is shared with a `platform` discriminator.

Phase 0 - platform plumbing (no behavior change, Twitch keeps working):
- Move Controller/Health/PlayerPolicy interfaces into watcher (or a new
  internal/platform package); twitch implements them. loginFromPath gets the
  host from the platform driver.
- domain: Platform field on Campaign/QueueItem/Claim/WatchSession/Channel
  stats; store migration adds `platform TEXT DEFAULT 'twitch'` columns +
  filtered queries. Queue stays ONE list (user-ordered, mixed platforms);
  selection per engine filters to its own platform's items.
- app.StartRuntime -> StartRuntimes: builds N (platform, session, engine)
  triples; service layer aggregates status per platform.

Phase 1 - Kick driver (internal/kick):
- browser instance #2: config `kick.browser` (profile_dir default
  <approot>/chrome-profile-kick, own window slot, own mute/minimize flags).
- Login: manual, like Twitch (profile persists the session; Cloudflare
  clearance cookie lives in the profile).
- Discovery: navigate kick.com/drops + inventory; parse campaigns from the
  DOM and/or page-context fetch of web.kick.com/api/v1/drops/campaigns
  (auth inherited, same trick as Twitch details.go). All shapes verify-live.
- Progress: poll the stream page's on-page progress + periodic inventory
  page read; ~2min server granularity per FAQ.
- Watch: same six-point playback validation idea with Kick selectors
  (video element, live badge, category, claim/progress widgets).

Phase 2 - engine #2 + UI:
- Second watcher.Engine over the kick driver; independent state machines so a
  Twitch stall never blocks Kick and vice versa.
- Kick-tuned watchdog defaults: LONGER soft/hard stall + NO proactive
  refresh (FAQ: frequent refreshes hurt tracking) - per-platform watcher
  config overlay.
- UI: two Now watching cards (platform badge), queue rows badged, footer
  gains a Kick row, per-platform Start/Pause.

Phase 3 - claim + link:
- Manual-claim click on inventory at 100% (bounded single attempt, then
  needs_action - same policy as Twitch, more urgent because Kick rewards
  EXPIRE unclaimed).
- Account-link surfaces at claim time (not selection time) per Kick's model.

## Risks / open questions

- Everything DOM-shaped on Kick is a verify-live guess; expect a live-debug
  pass like Twitch's (op names, selectors, timing).
- Cloudflare may challenge on automation-adjacent signals; mitigation is the
  same posture that got Twitch working plus the KickDropsMiner precedent.
- Watch-credit sensitivity to refreshes means our recovery ladder must be
  gentler; if Kick progress stalls, prefer waiting over reloading.
- ToS: kick.com/kick-drops-terms was not fetchable (403); automation policy
  unverified. Same personal-use posture as the Twitch side.

## Login probe (implemented 2026-08-31, endpoint verify-live)

`kick.CheckLogin` (kick/auth.go) answers `/api/profiles/login-check` for Kick.
It is deliberately NOT a DOM-selector guess: from page context (cookies +
Cloudflare clearance ride along, same pattern as `jsFetchCampaigns`) it fetches
the same-origin authenticated user endpoint `/api/v1/user` and reads the
STATUS - 401/403 = logged out, 2xx with a user-shaped body (id / user_id /
user.id / username) = logged in, anything else = unknown, which the service
reports as `{"loggedIn": null}` rather than a guess. If the tab is off-origin
the probe navigates to kick.com once and retries (the service refuses the
probe unless the engine is parked, so this never yanks a mid-watch tab).

verify live: the endpoint path `/api/v1/user` and its body shape. If Kick has
moved or renamed it, the probe degrades to `null` (the pre-probe behavior) -
find the current authenticated endpoint in DevTools while logged in and update
`jsLoggedIn` in kick/auth.go.

A CONFIRMED Kick login flows straight into watching, exactly like Twitch
(launch-on-demand: a logged-in window parked with no watcher must not linger).
