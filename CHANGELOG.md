# Changelog

All notable changes to dropwarden. Releases are semver tags (vX.Y.Z) with a
version heading each; the dated entries below v0.1.0 predate tagged releases
and are grouped by day and theme on `main`.

## v0.3.2 - 2026-10-18

### Fixed
- Headless service: the queue's Link account / Claim on Twitch / Claim on Kick
  buttons opened a background tab in the service's Chrome, which has no
  screen, and reported "Opened in Chrome". They now open the page in your own
  browser; Log in opens the remote login view. `POST /api/open` is refused in
  this mode.

## v0.3.1 - 2026-10-18

### Fixed
- Browser fingerprint: rod's default "laptop" device emulation injected a fixed
  Mac Chrome/114 user agent and `en` language on every page while the real
  Chrome is a different build on a different OS (`navigator.platform` said so).
  The viewport emulation stays; the user agent is now the browser's own and the
  language is `en-US,en`. Seen as Kick's "Too many attempts" on login.
- Resume mid-cycle left the dashboard saying Paused: Start/Stop now update the
  published status immediately instead of at the next Idle pass.
- Headless service: the paused card no longer mentions the tray menu.

## v0.3.0 - 2026-10-18

### Added
- `cmd/dropwarden-serve`: headless service mode. Same runtime as the desktop
  app (profiles, live reconcile, one Chrome per profile) with no tray or
  windows; serves the dashboard and API over HTTP (`-listen`, default
  127.0.0.1:8080), `GET /healthz`, `-chrome-path` (used only when
  `browser.chrome_path` is empty, never written to the config) and `-config`
  (same as `DROPWARDEN_CONFIG`). SIGTERM/SIGINT shut down cleanly. The
  self-updater never runs in this mode. Pure Go, builds with CGO off.
- Remote login: `GET /api/remote/{profile}/{platform}/ws` mirrors a watcher
  tab over CDP (JPEG screencast out, mouse/keyboard/paste in), same-origin
  only, one viewer per window. The dashboard's sign-in cards get an "Open
  remote login" view with a Login check button when running as the service.
- `/api/state` `host.remote` (headless service) and `host.updates`
  (self-updater running). With `updates: false` the dashboard hides the
  update banner and the Updates pane shows the version only.

### Changed
- Container image rebased from the Selkies web desktop (GTK GUI) onto the
  minimal Xvfb + Chrome base `ghcr.io/rake-pro/cluster-chrome`, published as
  `ghcr.io/rake-pro/dropwarden:<tag>` (was `dropwarden-desktop`), running
  `dropwarden-serve` on port 8080 with `DROPWARDEN_HOME=/home/chrome/dropwarden`.
  The Selkies autostart script is gone. Chrome sandbox, GPU and seccomp come
  from the base image and the pod spec.

### Fixed
- Keep-awake: a host with no sleep inhibitor (no `systemd-inhibit`, as in the
  container) now logs one warning instead of one per engine state change.

## v0.2.13 - 2026-10-18

### Added
- `deploy/container/`: dropwarden-desktop container image (Linux GTK3 GUI +
  Google Chrome stable inside the linuxserver Selkies web desktop) and
  `.github/workflows/container.yml` building `ghcr.io/rake-pro/dropwarden-desktop:vX.Y.Z`
  on tag push / dispatch. Prep for running the watcher on the rake.pro cluster's
  node4; nothing verified on hardware yet (gates listed in deploy/container/README.md).

## v0.2.12 - 2026-10-17

### Changed
- golang.org/x/sys 0.48.0 -> 0.49.0 (Dependabot). Dependency-only release.
  v0.2.11 was tagged on the same code without a changelog section, so its
  release run stopped at the notes step and no v0.2.11 release exists.

## v0.2.10 - 2026-10-09

### Changed
- Wails v3 3.0.0-beta.17 -> beta.28 (Dependabot), the desktop framework
  under the dashboard window and tray.

### Fixed
- macOS: the application menu next to the Apple menu was empty, so Cmd+Q
  did nothing and the Dock's context menu was the only way to quit. The
  menu bar now has the standard app menu (About, Hide, Quit), Edit
  (Cmd+C/V/X/A work inside the dashboard) and Window menus.
- Closing the dashboard window now hides it on every platform. On Windows
  and Linux the close button used to end the whole app, tray included.
- Settings > Profiles: unsaved changes are no longer lost when you switch to
  another settings page or leave Settings. You are asked to save first, and
  Save and Discard only light up when something changed. Adding or removing
  a profile keeps your unsaved changes on the other profiles, and clicking
  the words next to a switch now flips it.
- Settings > Profiles: if your profiles cannot be loaded, the page says so
  and offers Try again, instead of showing made-up sample profiles.
- A profile with its own games list must have at least one game before it
  can be saved; it no longer quietly switches back to the shared list. That
  list can now be reordered like the shared one, and the Queue sorts that
  profile's games by it.
- "Restart now" after changing a Chrome setting no longer resumes watching
  you had paused, and no longer skips a sign-in that is still needed. A
  paused window that does not come back is left stopped, not watching.
- Queue: progress and statuses keep updating after you click a row or choose
  a sort order. Pressing a row and letting go without moving it, or pressing
  Escape while dragging, no longer changes the order or switches sorting to
  "Your order". If a sort change fails, the page shows the order really in
  use. With a platform filter on, an empty list says so and offers Show all.
- Game picker: when the list of games with drops cannot load, it says so
  instead of showing "Loading..." forever, and Refresh keeps the old list on
  screen until the new one arrives. A failed search says so, and an older
  search answer can no longer replace a newer one.
- Progress never shows more minutes watched than the drop needs (such as
  "73 / 60 min").
- Dashboard buttons ignore a second press while the first one is still
  working, and Skip drop only skips the drop shown on the card.
- The Dashboard, Queue, History, Debugging and games lists keep keyboard
  focus where it was when they refresh or when you move an item. Game lists
  that are turned off can no longer be changed from the keyboard.
- Debugging: the log no longer jumps to the bottom or drops your text
  selection every few seconds.
- Settings > Profiles shows "failed to start" for a Chrome that could not
  start, instead of "starting" forever.
- Easier to read: stronger contrast for small text, buttons, switches, text
  boxes and the keyboard focus ring in both themes. Scrollbars and drop-down
  lists follow dark mode. With the theme set to System, the window opens in
  your computer's light or dark mode right away, without a dark flash first.
- Layout: at the smallest window size Settings keeps its menu beside the
  page; History never leaves one card alone on a row; card spacing, corners
  and headings match across pages; the menu in a narrow window closes with
  Escape or a click outside it, and scrolls when it is long.
- Screen readers: the Chrome path and claim fields have names, queue rows
  are announced as groups, and the sidebar status text is read out when the
  sidebar is narrow.
- Plainer wording: error messages in everyday words (including "dropwarden
  isn't responding" while the app restarts), plainer reasons on queue rows
  and status cards, the same "Watch time" and "Session success" labels on
  the Dashboard and History, Up next shows the next drop of each profile
  when you have several, the Kick setting says it applies when you press
  Save, and "Copy failed" now shows as an error.
- The update check interval can no longer be left blank and saved.

### Added
- Status in the menu bar / tray icon: the chest gets a green dot while a
  drop is being watched, amber when watching is paused or backing off, red
  when an account needs you (sign in, or the watcher stopped after repeated
  errors). On macOS the Dock icon
  shows the same dot. The icon replaces the old "dropwarden" text label.

## v0.2.9 - 2026-10-09

### Changed
- Built with Go 1.27.2, the 2026-10-08 security release (GO-2026-6599 to
  GO-2026-6617: net/http, crypto/tls, html/template and mime/multipart
  fixes). The release workflow now pins that exact toolchain instead of the
  1.27 line, so a runner's cached older patch cannot slip in.
- modernc.org/sqlite 1.59.0 -> 1.60.1 (Dependabot, reviewed).

## v0.2.8 - 2026-09-30

### Fixed
- Chrome no longer opens the "Restore pages? Chrome didn't shut down
  correctly" bubble every time dropwarden starts. The watcher browser is
  always killed rather than shut down, so Chrome recorded each run as a
  crash; the profile is now marked as cleanly exited before every launch,
  and `--hide-crash-restore-bubble` backs that up.

## v0.2.7 - 2026-09-27

### Fixed
- Twitch integrity check failing on everything (empty campaigns page, no
  claim ids, every completed drop parked on "claim it manually"): v0.2.2
  dropped `--disable-blink-features=AutomationControlled`, and a Chrome
  launched for remote debugging reports `navigator.webdriver === true`
  without it, which Twitch's bot check keys on. The flag is back, and the
  "unsupported command-line flag" bar it triggers is suppressed with
  `--test-type` instead of by removing the flag.
- Twitch integrity rejections are handled instead of read as "no
  dropCampaign in response": when the campaign list, a campaign's details
  or the claim mutation comes back `IntegrityCheckFailed`, dropwarden drops
  the captured token, reloads the campaigns page so the client mints a fresh
  one, and retries once (throttled to one refresh per 3 minutes). Discovery
  and the claim path log the rejection by name; a list still rejected after
  a fresh token is flagged as Twitch refusing the browser's own requests.
- Queue rows no longer keep a stale "watching <channel>" label from a
  previous run: only the drop the engine is watching stays active.

## v0.2.6 - 2026-09-27

### Added
- Queue rows show whether a channel for the drop is live right now: a green
  dot before the name plus up to three live logins in the row's detail line
  (grey dot = none of the allowed channels is live, no dot = not checked yet).
  Allowlist campaigns are refreshed in one batched status query at selection
  and every ~5 minutes while watching; any-channel campaigns reflect the
  directory read from the last selection.

### Fixed
- "What's new" renders the notes as a changelog: real bullet lists, section
  headings, and hard-wrapped changelog lines joined back into one bullet
  instead of breaking mid-sentence.

## v0.2.5 - 2026-09-26

### Fixed
- Release notes: the GitHub release body and the "What's new" dialog now carry
  this file's section for the version, verbatim. The install boilerplate
  and the GitHub compare link (unreachable for anyone outside the repo) are
  gone. A tag without a changelog section fails the release.
- Release re-runs: the updater manifest hashes the copy the downloads host
  already serves for any asset it refuses to overwrite, so a rebuilt
  release can no longer publish a sha256 the updater cannot verify.
- Dashboard cards at narrow widths: the channel name no longer collapses
  under the status pill (header wraps, badges keep their size), and the two
  platform cards stack below 320 px each instead of cramping side by side.
- Queue rows reflow onto two lines in a narrow window instead of squeezing
  the drop name out.

## v0.2.4 - 2026-09-26

### Added
- `power.keep_awake` (Settings > App > Power): dropwarden holds the OS idle-sleep
  inhibitor while a drop is being watched (`while_watching`, the default),
  for its whole lifetime (`always`), or never. Display sleep is never
  blocked, so monitors still turn off. Windows uses
  `SetThreadExecutionState`, macOS `caffeinate -i`, Linux `systemd-inhibit`.
  The sidebar shows "Keeping PC awake" while the hold is active.

## v0.2.3 - 2026-09-26

### Fixed
- Zero-minute (event-based) drops such as sub or chat rewards never enter the
  queue: discovery skips them and prunes any already-queued rows on its next
  pass, instead of listing them as "not a watch-time drop".

## v0.2.2 - 2026-09-26

### Fixed
- Chrome no longer shows the "unsupported command-line flag" bar on every
  launch: `--disable-blink-features=AutomationControlled` is dropped. Without
  `--enable-automation` the automation tell it suppressed never existed, and
  the bar itself was one.

## v0.2.1 - 2026-09-26

### Fixed
- Claim id at completion: when the stored row, the current drop session and the in-progress inventory have no `dropInstanceID` (Twitch drops a campaign from `dropCampaignsInProgress` the moment it hits its minutes), the claim reads the campaign's details view (`Pager.CampaignProgress`) and saves the id, so the mutation runs instead of "no claim id known".
- The claim sweep reads the details view for completed, unclaimed stored rows missing from the inventory read (at most 3 campaigns per sweep; a drop the details cannot make claimable waits `claim.retry_backoff`).
- Page-context GraphQL never runs on a non-Twitch document: an off-site tab (about:blank after a Chrome relaunch) is sent to the drops inventory first ("twitch: tab was off-site, navigated before GQL"), a relaunched or rebuilt Twitch tab reopens twitch.tv at once so the header capture refills, and an unreadable auth cookie yields a descriptive error instead of a SecurityError.
- Inventory claim fallback: waits up to 30 s for a claim button, then clicks visible "Load More" (up to 5 rounds, 2 s apart, scrolling to the bottom) before giving up; labels are whitespace-collapsed and case-folded and match exact, then contains (text or aria-label), then any "claim" button (not "claimed"). A miss logs up to 12 button labels.

### Changed
- `debug/inventory-buttons.json` is now `{url, headings, buttons}` (h2/h3/h4 section headings, per-button `disabled`) and is written after the claim attempt, so it shows what the matcher finally saw.
- The claim attempt has separate budgets: 60 s for claim id lookup + mutation + verification, and 90 s for the inventory fallback (was 60 s for both together), so a slow mutation never eats the fallback's wait.

## v0.2.0 - 2026-09-26

### Added
- Reworked dashboard: one card per platform with plain-language state, a "Get started" checklist (Chrome found, sign in to Twitch, choose games, Start) that returns when a step regresses, and a needs-you banner that names the actions (link an account, claim a finished drop, sign in again).
- Game picker (Settings > Games and per profile): "Drops right now" from `GET /api/games/catalog` (own campaigns + Kick's public list + Twitch drops-enabled streams, anonymous, cached 10 min, `refresh=1` at most once a minute), Twitch category search via `GET /api/games/search`, and add-by-exact-name. `GET /api/games?profile=` (cached campaigns only) stays for scripts.
- Queue grouped by game in games-list order (Pinned first), drag or Alt+Up/Down within a game, per-game move arrows, and a one-click fix button on needs-you rows (`actionURL`/`actionLabel`: Link account, Claim on Twitch, Claim on Kick, Log in) opened through `POST /api/open` as a background tab in the watcher Chrome.
- Debugging view: Copy diagnostics (`GET /api/diagnostics`, redacted plain text) and Export diagnostics (`POST /api/diagnostics/export`, a zip on the Desktop; never Chrome profiles or cookies).
- Update flow: dashboard banner, update dialog with release notes, in-place progress (download percent, verify, install, restart), deferral copy while a watch runs, retry after a failed install, an "Updated to vX" notice with its notes (`installedNotes`), and a page reload once the restarted app answers on the new version.
- `/api/update` reports `status`, `phase`, `percent` (0-100, -1 unknown), `bytesDone`/`bytesTotal`, `devBuild`, `deferredReason`, `failedVersion`/`failMessage`, `skippedVersion`, `promptSeq` and `installedNotes`; check and install return the same object, and a feed 404 reads `none_published`.
- `/api/state` carries per-platform `reason`, `launchError` and `wanted`, `host.chromeFound`/`host.chromePath`, and queue `needsUser`.
- `POST /api/config` errors carry `field` (the ConfigDTO name) and the Settings form jumps to and marks that field; a changed Chrome path must exist.
- Microsoft Edge is a last-resort browser on Windows when Chrome is missing.

### Changed
- The starter config ships `games: []`; the checklist and picker lead the first run.
- The tray update item never installs: it opens the update dialog, or runs a check. Dev builds refuse checks and installs.
- A paused engine's tray line reads "paused - choose Resume in the dashboard"; the dashboard's Resume button starts it again.
- A failed automatic install skips that version until it is installed by hand; a failed manual install can be retried at once.
- Enabling Kick forces `separate_window`; the shared window is behind `kick.browser.shared_window_experimental`.
- The update controller moved into internal/service (UpdateController) with unit tests.

### Fixed
- Settings changes reach the Twitch and Kick discoverers live, and discovery honors `discovery.interval`/`jitter` (cooldowns and retries no longer force a full pass).
- Live-status checks batch allowlists over 100 channels; a streamer who switches games is dropped; an offline channel fails the soft stall fast.
- An expired Twitch session pauses the engine and asks for a login instead of misfiling drops; a hidden watch tab restores the window.
- Every engine reload, navigate, eval and page fetch is time-bounded; the log rotates to `dropwarden.log.1` instead of being truncated.
- Login check waits up to 10 s and confirms "logged out" with a `currentUser` query.
- Zero-minute drops are not selected; the claim sweep skips drops with unmet preconditions; needs-action rows clear once a drop is claimed elsewhere.
- A Kick drop that could not be claimed says "claim it in the Kick inventory" and links there, not to Twitch.
- Reserved Twitch paths are never read as raid targets; the raid-cancel click is scoped to the raid banner; Kick tabs open in the background.
- Debug dumps land in the profile's own debug dir.

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
