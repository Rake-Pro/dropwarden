# Changelog

All notable changes to dropwarden. Releases are semver tags (vX.Y.Z) with a
version heading each; the dated entries below v0.1.0 predate tagged releases
and are grouped by day and theme on `main`.

## v0.1.4 - 2026-09-26

### Fixed
- Claiming works: a completed drop is claimed with Twitch's
  `DropsPage_ClaimDropRewards` GraphQL mutation (sent from the page with its
  own auth and client-integrity headers, keyed by the drop's
  `dropInstanceID`), and the claim only counts once a fresh inventory read
  confirms it: a success status plus the row `isClaimed` or gone. A failure
  status, a row still unclaimed, or a read without a `currentUser` (auth
  expired, integrity rejection) is a failure; an inventory response with no
  user is now a parse error everywhere instead of an empty list. Log lines
  `claim: gql` / `claim: dom` name the path that worked; the raw mutation
  response is logged at debug level.
- The on-stream claim click (a guessed selector that could match an unrelated
  "Claim" button and hung until the claim timeout) is gone.

### Added
- Claim sweep (`claim.sweep`, default true): after every discovery pass, and
  after a progress poll that sees another drop complete, every completed,
  unclaimed drop is claimed through the mutation, at most 10 per pass, 2-4 s
  apart. Candidates come from one fresh inventory read (server minutes >=
  required, `dropInstanceID` present), never from stored rows. Successes land
  in History and drain stuck "completed - claim it manually" items; a failure
  is logged and the drop waits `claim.retry_backoff` (in memory) before the
  next try. The sweep never navigates or takes the window.
- Drops Twitch already reports claimed (claimed by hand, or the mutation
  answers `DROP_INSTANCE_ALREADY_CLAIMED`) are reconciled on every discovery
  pass, progress poll and sweep: a needs_action / pending / active queue item
  flips to claimed with reason "claimed (detected in inventory)" and History
  gets one entry marked "detected in inventory" (new `claims.source` column).
- `claim.inventory_button_selector` (default
  `button:has([data-a-target="tw-core-button-label-text"])`) and
  `claim.inventory_button_text` (default "Claim Now"), editable in
  Settings > Player > Claim. An empty selector makes the fallback capture-only.
- Every DOM fallback run writes `debug/inventory-buttons.json` (all buttons on
  the inventory page with text, aria-label, data attributes, class, nearest
  ancestor data-test-selector and truncated outerHTML), regardless of
  `logging.capture_gql_bodies`.

### Changed
- The DOM claim fallback now runs only when the mutation is unsupported or
  fails, opens the inventory page first, and clicks the first element matching
  the configured selector whose label text matches, then verifies the claim:
  only an `isClaimed` row confirms a click (a missing row does not). Kick has
  no inventory read and trusts the click as before.
- `claim.allow_gql_fallback` and `claim.strategy` are no longer read (the
  mutation is the primary path); the Settings toggle for the former is removed.

## v0.1.3 - 2026-09-26

### Added
- Pinned drops: star a drop in the Queue view to watch it before everything
  else; pinned rows form a ranked set at the top (up/down arrows re-rank,
  subtle left accent). Starring also stores a pin rule (new `drop_pins` table,
  keyed by platform, game, and normalized reward name) so a later campaign
  instance re-offering the same reward is re-pinned automatically by
  discovery. `POST /api/queue/pin`, `POST /api/queue/pin-rank`; queue items in
  `/api/state` carry `pin` and `alongside`.
- Game-order priority: selection ranks drops by the position of their game in
  the `games` list (unlisted games last), right after pins. Settings > Games
  lists the games with up/down buttons.
- Channel-specific first: within a game, drops of allowlisted campaigns are
  selected before any-channel drops that the same streamers also credit
  (`queue.prefer_channel_specific`, default true). Such any-channel rows show
  a "credits alongside channel drops" badge; they are still watched when no
  allowlisted streamer of the game is live.
- The selecting log line carries the top candidate's pin, game rank, and
  alongside flag; the fell-through line carries the chosen drop's.

### Fixed
- Queue rows no longer overflow: every row is one fixed six-cell grid, so the
  status pill is never clipped and the enable toggle never wraps, whichever
  buttons are present; long names and statuses truncate.
- "Watch anyway" renders only on rows whose reward is already owned, and
  never on a row that is complete or claimed.
- Discovery no longer flags a drop as already owned when the matching earned
  reward is the drop's own, earned in the current campaign instance (complete,
  claimable, or claimed); a stale flag from an older build is cleared. Such a
  row showed "completed - claim it manually" with a "Watch anyway" button.

## v0.1.2 - 2026-09-26

### Added
- `browser.window.always_on_bottom` is implemented on Windows: the watcher
  Chrome window(s) are pushed to the bottom of the z-order (`HWND_BOTTOM`,
  `SWP_NOACTIVATE`, never focused) at launch and relaunch, after every window
  restore/validation hold change, and on the player reassert interval while
  watching, so they stay covered but visible and keep crediting.
- Settings > Browser toggle "Keep Chrome behind other windows (Windows only)";
  `/api/state` now carries `host.os`.

### Changed
- macOS/Linux: `always_on_bottom` is unsupported (logged once at debug); the
  settings toggle is disabled there.
- The starter/example `kick.browser.window` block no longer sets
  `always_on_bottom`; it inherits the root `browser.window` value so the
  settings toggle also governs a separate Kick window.

### Fixed
- Windows: the burn-in window shift no longer creates a new EnumWindows
  callback on every tick (the Go runtime caps callbacks per process, so a long
  session would eventually panic); it shares one callback with the z-order push.

## v0.1.1 - 2026-09-26

### Changed
- macOS builds are signed with the Developer ID certificate and notarized;
  Gatekeeper no longer blocks the first launch. No code changes.

## v0.1.0 - 2026-09-26

### Added
- Self-updater (`internal/update`): checks a release manifest (`latest.json`,
  default `https://cdn.rake.pro/releases/dropwarden/latest.json`) ~30 s after
  start and every `update.interval_hours`, verifies the download's sha256
  against the manifest (fails closed), and installs through
  `dropwarden-cli update-apply`, which restarts the old version on any
  failure. Auto-install waits while any profile is opening, validating,
  watching or claiming. Dashboard banner with "What's new" and "Install now",
  tray item "Check for Updates" / "Install Update vX.Y.Z", Settings > Updates
  pane, `GET /api/update`, `POST /api/update/check`, `POST /api/update/install`,
  and an `update:` config block (`enabled`, `auto_install`, `show_changelog`,
  `feed_url`, `interval_hours`).
- Release pipeline (`.github/workflows/release.yml`, on `v*` tags or manual
  dispatch): Windows Inno Setup installer (per-user, optional login startup
  task, Smart App Control warning), macOS universal dmg + zip (ad-hoc signed,
  Developer ID signing/notarization when the secrets exist), Linux tarball,
  `checksums.txt`, `latest.json`, and an optional upload to the downloads host.
- App icon (`build/icons`, drawn from the loot-chest logo by
  `scripts/gen-icons.py`), Windows exe icon + manifest via
  `wails3 generate syso`, macOS `Info.plist` + `scripts/bundle.sh`.
- Build version stamp (`internal/version`, set with `-ldflags -X`), shown in
  Settings > Updates and returned by `/api/state`.

### Fixed
- Offline allowlist campaigns no longer consume the fall-through budget:
  `watcher.max_select_attempts` now bounds only any-channel directory scrapes,
  so an any-channel campaign further down the queue is still tried when many
  single-streamer allowlist campaigns are offline. (`dd08b93`)
- Quitting on macOS now tears down Chrome: `App.Close` is registered as the
  Wails shutdown hook, since a macOS quit exits without `wapp.Run` returning.

## 2026-08-31 - Live-debug arc: crediting, owned rewards, Kick login

### Fixed
- Drops-indicator selector made advisory for playback validation - a stale DOM
  selector must never block watching; server-side progress + the stall ladder
  are the real crediting check. Probe falls back to text-scanning the stream-tag
  row for "Drops". (`00abcff`)
- Already-claimed drops excluded from eligibility: details query now fetches
  `self { isClaimed, currentMinutesWatched, dropInstanceID }`; discovery
  self-heals stores detailed by pre-fix builds (re-details once until claim
  state is captured). (`2cb4bbc`, `cf7edac`)
- Minimized tab gets NO credit (confirmed live via visibility instrumentation):
  the watcher window now stays visible + unfocused while watching
  (`browser.window.minimize_while_watching` default false - cover, don't
  minimize). (`2cb4bbc`)
- Any-channel campaigns (empty ACL) only credit on participating drops-enabled
  streams: drops indicator is now a GATE for any-channel selection (fast fail +
  brief blacklist + reselect); ACL channels keep it advisory. (`b42ffc9`)
- Stale-event raid bug: browser events buffered while not watching (e.g. the
  tab-creation `about:blank` navigation) were adopted as raid targets - event
  backlog drained at watch entry, raid-target host shape validated. (`75696ca`)
- Login-check success auto-starts watching (login intent had no exit, windows
  lingered). (`a348d58`)
- Lock-reaper verifies the pid looks like Chrome before SIGKILL (pid-reuse
  guard); launch failure renders "launching (check Debugging)" instead of a
  fake "stopped"; `ui.start_minimized` default flipped to false. (`851ac1a`)

### Added
- Proactive owned-reward skip: inventory GQL (`gameEventDropsConnection` +
  `earnedDropRewards`) parsed into an earned set, matched by NORMALIZED reward
  name (reward IDs are campaign-instance-scoped, so cross-instance join must be
  by name); `Progress.Owned` gates eligibility with reason "reward already
  owned - Twitch will not credit it again". Confirmed working live. (`a1f7151`)
- Manual "Watch anyway" per-drop override (`QueueItem.ForceWatch`, persisted,
  bypasses only the Owned gate) - forces one owned drop past the skip to test
  crediting. `POST /api/queue/force-watch`, amber toggle on owned queue rows.
  (`914a620`)
- Give-up on never-crediting drops: no Twitch session + zero minutes across the
  stall window = needs_action "you may already own this reward", selection
  skips it; tightened to a standalone 5-minute no-first-credit timeout.
  (`2d675a7`, `7cc4141`)
- Kick login probe (iterated live): `GET web.kick.com/api/v1/user/session` on
  plain cookies; logged in = `data.session_id` present AND an auth token found
  in the browser; full attempt detail surfaced in the UI card. (`20fc458`)
- `watcher.timing_jitter` (default 0.2 in the starter config, range [0, 0.5])
  fuzzes progress-poll, player-reassert, and max-session-rotation cadence;
  30-90s random stagger when starting an engine within 2 minutes of another.
  (`a348d58`)
- Progress-poll instrumentation: one info line per poll (session_found /
  channel / minutes / inventory_rows); raw inventory GQL dump to
  `debug/inventory-gql.json` when `capture_gql_bodies` is on. (`75696ca`,
  `da3d890`, `9902693`)
- Kick drop-progress debug capture: passive fetch/XHR shim buffers
  drops/inventory/campaign/quest/reward URLs, dumped to
  `kick-drops-network.json` + `kick-inventory-raw.html` - records a real Kick
  drop's shape when a campaign finally appears. (`a1f7151`)

## 2026-08-31 - Live lifecycle, shared window, launch-on-demand

### Added
- Reconciler (`internal/app/reconcile.go`): profile/platform runtimes start and
  stop live on every settings commit - "restart to apply" removed for profiles
  and Kick; add/enable/disable/REMOVE profiles live (remove keeps the data dir,
  refuses the last profile). Only browser LAUNCH settings stay
  next-launch-bound. (`b9a9996`)
- Shared Chrome window per profile (default): Kick runs as a TAB in the
  profile's window; `kick.browser.separate_window: true` is the legacy
  fallback. Group supervisor relaunches Chrome rebuilding all tabs + shims.
  Tab activation is a PoC (one audited `TargetActivateTarget` call site) -
  release-blocking backlog item. (`b9a9996`)
- Welcome / needs-login flow: fresh session starts the engine paused with
  `needsLogin` in state; Check-login button (`POST /api/profiles/login-check`);
  unanswered prompt persisted as `.dw-<platform>-login-pending`. (`b9a9996`,
  `8ae2c59`)
- Launch-on-demand (debugged live on Windows): Chrome exists only when the
  per-(profile,platform) run intent says so (off | watch | login); dashboard
  Stop CLOSES Chrome (pause stays tray-only); ref-counted shared window closes
  with its last tab. (`7edcaca`)
- Orphan cleanup: Windows = suspended-start + kill-on-close Job Object (an
  orphaned Chrome held SingletonLock and wedged relaunches - the "black
  window" bug); POSIX = SingletonLock pid reap + process-group kill.
  (`7edcaca`)

## 2026-08-30 - Multi-platform UX, multi-profile, Linux GUI

### Added
- Kick promoted to first class: `/api/state` exposes a uniform `platforms`
  array; engine actions are `POST /api/engine/{platform}/{action}` (old
  `/api/watch/*` + `/api/kick/*` routes removed). Per-platform now-watching
  cards, platform badges (only when >1 platform visible), platform-split
  totals, per-platform Debugging cards and tray items; Settings restructured
  (Platforms pane, App pane with theme picker). 13 adversarial-review findings
  fixed, notably: Kick inherits shared browser config from the root block,
  settings Save hot-reloads BOTH engines, Save validates against a config copy,
  cooldown status reset no longer wipes `Status.Platform` (Kick minutes could
  credit Twitch). (`feca81b`)
- Linux GUI is real: Wails GTK3 backend (`-tags gtk3`), `stub_linux.go`
  deleted, builds and passes `task check` in-container; runtime on real
  hardware still verify-live. (`feca81b`)
- Multi-profile design locked + phases 0-3 implemented: profile = person with
  its own SQLite DB and Chrome profile dirs under
  `~/.dropwarden/profiles/<name>/` (no store migration; legacy data becomes
  the zero-move `main` profile). `profiles:` config block, N ProfileRuntimes,
  `POST /api/engine/{profile}/{platform}/{action}`, profile CRUD API, unified
  dashboard with profile pill bar, window tiling defaults + 90s per-profile
  first-discovery stagger. (`7380929` design, `a2da217` code)

## 2026-08-27..28 - Platform-neutral engine, Kick phase 0-2

### Added
- Platform-neutral plumbing: `internal/platform` interfaces, platform column
  through the store (composite keys on channel_stats/blacklist), per-platform
  engine, shared per-platform queue reconcile. (`f1d9351`)
- Kick runtime behind `kick.enabled` (default off): unofficial web.kick.com
  campaigns endpoint via page-context fetch, second Chrome profile, KICK
  badges, `/api/kick/*` controls (later unified). No proactive refresh and
  long stall windows - Kick's FAQ says refreshes hurt credit. All selectors
  verify-live; first-run dump protocol in `docs/STATUS.md`. (`4679aed`)
- Self-healing browser session (`browser.Managed`): supervises the CDP event
  stream and relaunches Chrome with capped backoff forever, swapping the
  session in place - found after a 90-minute dead-socket retry loop.
  (`25ebfa4`)

### Fixed
- Twitch mature/content-classification gate treated as not-offline; ClickPlay
  accepts it (Start Watching), acknowledged once per channel. (`c3abc06`)
- Link-gate doctrine: Twitch flags cannot distinguish link-to-earn from
  link-optional, so server-credited progress (real minutes > 0 or claimable)
  overrides the account-link gate; `skip_account_link_drops` is a real switch
  (off = watch everything, failed claims go to manual reconcile). (`0229f9b`)

## 2026-08-27 - MVP and Twitch core

### Added
- MVP: tray-first Twitch drops watcher driving one real, unfocused Chrome via
  go-rod CDP (not API-spoofed). CLI harness (`cmd/dropwarden-cli`: doctor /
  login / discover / inventory / watch / report) + Wails v3 GUI; frontend talks
  to Go via `/api/*` JSON on the Wails asset origin, no generated bindings.
  (`16fa27e`, `f955376`)
- Active drop-progress poll (`watcher.progress_poll`, default 60s): Twitch
  pushes progress over WebSocket the passive capture shim cannot see; poll
  replays captured auth + integrity headers in page-context GQL and also reads
  the full inventory (simultaneous multi-tier campaigns). (`4009c3a`,
  `5dc6faf`)
- Claim policy: ONE bounded 60-second attempt (restore window + toggle drops
  panel first); failure = needs_action "claim manually" + banner, no
  auto-retry. (`c291849`)
- Set-coverage channel selection: rank current > coverage > stats > viewers -
  park on the channel crediting the most eligible drops; stickiness drains the
  set before moving. (`98400e7`)
- Lifetime stats from SQLite; minutes watched = wall-clock session duration
  (per-drop credited deltas undercount with simultaneous drops); live session
  overlay; mid-watch revalidation no longer mints a new session. (`9376f38`,
  `7cc21e4`, `e254d8a`)
- Opt-in Chrome sink-level mute (`browser.mute_audio`, player stays at 1%
  volume); credited-elsewhere stall handling (a different crediting channel
  means another device is watching - stand down without blacklisting).
  (`c797bd0`)
- Opt-in start-minimized via CDP `Browser.setWindowBounds`; engine restores the
  window for opening/validating and re-minimizes when watching begins.
  (`6ffd97b`, `c27964c`)

### Fixed
- Discovery made real against live Twitch: actual GQL op name is
  `ViewerDropsDashboard`; Twitch batches GraphQL (positional array responses
  split in `twitch/capture.go`); campaign details/ACL fetched via page-context
  GQL replaying captured auth + integrity headers (defeats
  IntegrityCheckFailed). (`3412401`..`aa83f4f`)
- Eligibility is time-window based, not status-string based; list upsert no
  longer clobbers detailed ACL/slug; selection falls through to the next
  available drop. (`353fdde`..`428c1fa`)
- Account-link false positives: `self.isAccountConnected` is authoritative
  (Twitch fills `accountLinkURL` on most campaigns); empty accountLinkURL with
  nothing to link is eligible. (`4335cad`, `3bd84fa`)
