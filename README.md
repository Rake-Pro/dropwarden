# dropwarden

dropwarden watches Twitch and Kick drop campaigns for you.

Drop campaigns reward watch time, but the campaigns that matter are long,
multi-item, spread across dozens of streamers in every time zone, and nobody
can sit in front of them for the hours they ask. dropwarden does what you
would do if you could: it keeps one real, logged-in Chrome window open on an
eligible live stream, in the background, at low quality, tracks the minutes
and the items, claims what completes, and keeps a record so you never lose
your place. The stream plays for real. Streamers running a campaign get a
viewer who is there for the drop, which is the deal they signed up for when
they enabled drops. It deliberately does not spoof watching via the API.

## What it does and does not do

Does

- One account per person. Profiles are for people who share a machine
  (a household, a couple), each with their own Twitch sign-in and their own
  games, queue and history. Profiles are not a way to run several accounts
  for one person.
- Watches the real stream, visible and unminimized, in your own session.
- Plays the stream for real, at low volume. You can silence Chrome at the
  system level so you are not listening to it all day; the player itself
  stays playing and unmuted, because a muted player stops earning drops.
- Reads the same drop and campaign data the Twitch page shows you.
- Claims with the same request the page sends when you press Claim.
- Keeps the pace of a person: one action at a time, with natural timing.

Does not

- Hide the stream or stop it playing. The window stays visible and
  unminimized; ads play like they would for anyone with the tab open.
- Run more than one account per person, inflate viewer counts, or block ads.
- Store or type your password. You sign in to Twitch yourself, once.
- Spoof watch time through an API. If the stream is not playing, nothing
  is credited.

## Before you use it

Twitch and Kick terms restrict automated access to their services, and this
tool is automation. Enforcement to date has been against user accounts, not
tools: accounts have been flagged or temporarily banned. Use it on an account
you are prepared to lose that way. dropwarden is not affiliated with Twitch
or Kick.

## How it works

1. Discovery reads `twitch.tv/drops/campaigns` and `/drops/inventory` in the
   authenticated session and captures the page's own GraphQL responses
   passively (a small fetch/XHR shim buffers them; nothing extra is requested).
2. You queue and prioritize drops. The watcher works one at a time, in the
   order described under "Queue order" below.
3. For each drop it opens an eligible live channel, validates that playback is
   real and crediting, then watches with a set of watchdogs:
   - progress stall (soft nudge, then hard refresh + re-validate),
   - playback health (offline, player error, frozen video, volume drift),
   - crash / hang (renderer crash and unresponsive-tab detection),
   - raid (cancel the banner, or adopt an eligible target, always refreshing).
4. When the minutes are met it claims the drop (see Claiming below), records
   it, and moves on.

### Claiming

- Primary: Twitch's `DropsPage_ClaimDropRewards` GraphQL mutation, sent from
  the page with the page's own auth and client-integrity headers, keyed by the
  drop's `dropInstanceID`. A claim only counts once a fresh inventory read
  confirms it (success status plus the row `isClaimed` or gone; a failed or
  user-less read never confirms).
- Claim id: the stored progress row, the current drop session, the
  in-progress inventory, then the campaign's details view (Twitch drops a
  completed campaign from the in-progress list at once).
- Fallback (mutation unsupported or failed): the tab opens the drops
  inventory, waits up to 30 s for a claim button (then clicks "Load More" up
  to 5 times), and clicks the element matching `claim.inventory_button_selector`
  whose label is `claim.inventory_button_text` (defaults
  `button:has([data-a-target="tw-core-button-label-text"])` / "Claim Now";
  Settings > Platforms > Twitch > Advanced), or whose label or aria-label
  contains it, or any button labelled "claim". It then writes
  `debug/inventory-buttons.json` (URL, section headings, buttons). An empty
  selector only writes the capture.
- One bounded attempt per completed drop (60 s for the mutation path, a
  separate 90 s for the inventory fallback); if both paths fail the
  queue item goes to needs_action for a manual claim.
- Claim sweep (`claim.sweep`, default true): after each discovery pass and
  after a progress poll that sees another drop complete, up to 10 completed,
  unclaimed drops (per one fresh inventory read) are claimed via the
  mutation, 2-4 s apart, without navigating. Best-effort: a failed drop waits
  `claim.retry_backoff`; successes go to History and clear stuck needs_action
  items.
- Drops already claimed on Twitch (by hand, or `DROP_INSTANCE_ALREADY_CLAIMED`)
  flip their queue item to claimed and appear in History as "detected in
  inventory".

### Queue order

Selection ranks every eligible drop by these keys, in order, and tries them
top down, falling through past drops with no live channel:

| # | Key | Where it is set |
|---|-----|-----------------|
| 0 | Pinned drops first, rank 1 before rank 2 | Queue view: star a drop; up/down arrows re-rank pinned rows |
| 1 | Game rank: position of the campaign's game in the profile's `games` (case-insensitive); unlisted games last | Settings > Games (per profile): up/down per game, then Save |
| 2 | Channel-specific before any-channel, within a game | Settings > Games > Queue, or `profiles[].queue.prefer_channel_specific` (default `true`) |
| 3 | Priority mode (`top_down` = queue position, `ending_soonest`, `closest_to_complete`, `shortest_first`) | Queue view order menu / `profiles[].queue.priority_mode` |

- *Pins persist across campaign instances.* Starring a drop also stores a pin
  rule keyed by platform, game, and the drop's normalized reward name(s). When
  a later campaign re-offers the same reward under new IDs, discovery re-pins
  it automatically (log line `pin rule re-applied`). Unstarring deletes the
  rule.
- *Credits alongside.* An allowlisted streamer also credits the game's
  any-channel drops, so an any-channel drop that shares its game and platform
  with an eligible channel-specific drop waits behind those drops instead of
  taking a watch of its own. The Queue view marks it with a grey "credits
  alongside channel drops" badge. It is still watched when no allowlisted
  streamer of that game is live (selection falls through). Set
  `queue.prefer_channel_specific: false` to rank by game and priority mode only.
- Dragging rows changes queue position (and switches the mode to `top_down`);
  it never changes a pin rank. Pinned rows are not draggable and keep their
  queue position, so an unpinned drop returns to where it was.
- A profile with its own games list ranks games by that list's order.

### How it behaves

- You sign in yourself, once, in the dropwarden Chrome window. The app never
  stores or types a password.
- The player runs at 1% volume, never muted: Chrome 145+ pauses a muted video
  when its tab is backgrounded, which would silently stop drop credit.
- A single pacer rate-limits every synthetic action with jitter; there is no
  rapid-fire anything.
- Claims use the web client's own `ClaimDropRewards` mutation from the page
  context with the page's captured headers (older builds shipped it OFF over
  bot-block reports; the owner re-enabled it as the primary path in v0.1.4).
  The sweep is capped at 10 claims per pass, 2-4 s apart.
- The window is launched unfocused and dimmed. (A periodic few-pixel burn-in
  shift is configured and plumbed but not yet wired to a timer - see the
  STATUS.md backlog.)
- `watcher.timing_jitter` (default 0.2) desyncs periodic watcher actions
  (progress poll, player reassert, session rotation) by +/- that fraction so
  they do not land on an exact fixed interval every time; 0 disables it.

## Install

Releases are built by `.github/workflows/release.yml` from a `vX.Y.Z` tag and
published on the GitHub release and on the downloads host
(`https://cdn.rake.pro/releases/dropwarden/<tag>/`).

### Prerequisites

- Google Chrome installed (dropwarden drives its own dedicated Chrome profile,
  never your everyday one).
- A Twitch login done by hand once, in the dropwarden-driven Chrome window
  (dashboard: Log in). Kick, if enabled, the same.

### Per platform

| Platform | Asset | Install |
| --- | --- | --- |
| Windows 10/11 x64 | `dropwarden_<ver>_windows_amd64_setup.exe` | Run it; per-user install to `%LOCALAPPDATA%\Programs\dropwarden`, no admin. Optional "Start dropwarden when you log in" task. |
| macOS 12+ (universal) | `dropwarden_<ver>_darwin_universal.dmg` | Open the dmg, drag `dropwarden.app` to Applications. First launch: right-click the app > Open > Open. |
| Linux x64 | `dropwarden_<ver>_linux_amd64.tar.gz` | Extract anywhere writable (e.g. `~/.local/opt/dropwarden`), run `./dropwarden`. Needs GTK3 + webkit2gtk-4.1 runtime libs. |

- The binaries are not code-signed:
  - Windows SmartScreen: More info > Run anyway.
  - Windows 11 Smart App Control blocks unsigned apps with no per-app
    exception; the installer detects it and offers to open Windows Security
    (App & browser control > Smart App Control settings > Off).
  - macOS Gatekeeper: right-click > Open on first launch (ad-hoc signed only).
- Every install ships two binaries side by side: `dropwarden` (the tray app)
  and `dropwarden-cli` (the headless CLI, also the updater's installer).

### Updates

- The app checks a release manifest (`latest.json`) ~30 s after start and then
  every `update.interval_hours`; dev builds (`0.0.0-dev`) never auto-check.
- A found update shows a dashboard banner ("What's new", "Install now") and
  retitles the tray item to "Update vX.Y.Z Available...". Clicking the tray
  item opens the dashboard's update dialog (or runs a check); it never
  installs by itself.
- Auto-install never cuts a watch short: while any profile is opening,
  validating, watching or claiming, the install waits (re-evaluated the moment
  a watch ends, 5-minute fallback) and runs between watches; the banner says
  which watch it waits for. "Install now" installs immediately.
- Progress (download percent, verifying, installing, restarting) shows in the
  banner, the dialog and Settings > Updates. After the restart the page
  reloads itself once and shows "Updated to vX.Y.Z" with its release notes.
- A failed install shows "Couldn't install vX.Y.Z" with the reason and a
  Retry button. A failed automatic install is not retried automatically for
  that version; a manual install always runs.
- Install flow: the app downloads the platform archive, verifies its sha256
  against the manifest (fails closed), hands it to `dropwarden-cli
  update-apply`, and quits; the helper swaps the files, restarts dropwarden,
  and on any failure restarts the old version and logs to `update.log` in the
  app root.
- Settings > Updates edits these keys (config.yaml `update:` block):

| Key | Default | Meaning |
| --- | --- | --- |
| `enabled` | `true` | Background checks on/off. The manual check always works. |
| `auto_install` | `true` | Install a found update without asking (deferred while watching). |
| `show_changelog` | `true` | Offer the release notes on the dashboard banner. |
| `feed_url` | `https://cdn.rake.pro/releases/dropwarden/latest.json` | Release manifest. Empty disables update checks. |
| `interval_hours` | `24` | Hours between background checks (must be > 0). |

### Keeping the PC awake

- dropwarden holds the OS "no idle sleep" assertion so a watch is not cut
  short by the sleep timer. Display sleep is never blocked: monitors still
  turn off on the OS schedule.
- Windows: `SetThreadExecutionState(ES_SYSTEM_REQUIRED)`; macOS: `caffeinate
  -i` bound to the app's pid; Linux: `systemd-inhibit --what=sleep:idle`.
- Nothing wakes a sleeping machine. With nothing to watch (default mode) the
  PC sleeps as usual and picks up when someone wakes it.
- Settings > App edits this key (config.yaml `power:` block):

| Key | Default | Meaning |
| --- | --- | --- |
| `keep_awake` | `while_watching` | `while_watching` holds only while an engine is opening, validating, watching or claiming; `always` holds for the app's lifetime; `never` leaves the sleep timer alone. |

### Watcher window

- A minimized watcher tab reads `visibilityState` "hidden" and stops crediting,
  so the Chrome window stays visible and gets covered instead.
- Settings > Browser edits this key (config.yaml `browser.window:` block):

| Key | Default | Meaning |
| --- | --- | --- |
| `always_on_bottom` | `true` | Windows only. Keeps the watcher Chrome at the bottom of the z-order (`HWND_BOTTOM`, never activated), re-asserted after every restore/validation and on the player reassert interval while watching. macOS/Linux: no effect, the toggle is disabled; cover the window instead. |

## Layout

```
cmd/dropwarden        Wails v3 tray app (Windows/macOS)
cmd/dropwarden-cli    headless harness: doctor login discover inventory watch report
internal/config       YAML config, defaults, validation
internal/domain       pure types (no third-party imports)
internal/store        SQLite (modernc.org/sqlite): campaigns, progress, queue, sessions, claims
internal/browser      Chrome launch/attach/window/health (rod), generic Session interface
internal/twitch       Twitch-aware controller, selectors, GQL capture; gql/ parses responses
internal/discovery    reconcile campaigns + inventory into the store, auto-enqueue
internal/queue        pure ordering, precondition topo-sort, channel ranking
internal/watcher      the single-owner state machine + watchdogs + claim flow
internal/service      thin Wails-bound services (DTOs) + engine event emitter
internal/update       self-updater: manifest check, verified download, apply, helper job
internal/version      build-time version stamp (-ldflags -X)
frontend              vanilla HTML/CSS/JS UI (no build step), embeddable
```

Dependencies are kept minimal: `go-rod` (CDP), `zerolog` (logging), `yaml.v3`
(config), `modernc.org/sqlite` (pure-Go, no cgo), `wails/v3` (GUI), and
`golang.org/x/sys` (Windows launch). Nothing else.

## Quick start

### Prerequisites

- Go 1.27+
- Google Chrome installed
- For the GUI: on Windows nothing extra (WebView2 ships with the OS); on macOS
  nothing extra. `wails3` CLI is optional and only needed for hot reload.

### First run (CLI harness, any OS)

```
go run ./cmd/dropwarden-cli doctor     # shows resolved paths + detected Chrome
go run ./cmd/dropwarden-cli login      # opens Chrome; log in to Twitch by hand
go run ./cmd/dropwarden-cli discover   # one discovery pass; prints campaigns
go run ./cmd/dropwarden-cli watch      # runs the watcher until Ctrl-C
go run ./cmd/dropwarden-cli report     # claim history + session stats + queue
```

Config, database, log, and the dedicated Chrome profile live under
`~/.dropwarden` (macOS) or `%USERPROFILE%\dropwarden` (Windows). The starter
config is written on first run.

### The GUI (Windows / macOS / Linux)

```
go run ./cmd/dropwarden          # builds + runs the tray app (embedded assets)
# or, with hot reload and typed bindings:
wails3 dev
```

The dashboard, queue, history, and settings screens follow the same visual
language as the sibling GoShareIt app.

### Using the app

- First run: the dashboard shows a "Get started" checklist until every step
  passes, and brings it back if one regresses:

| Step | Done when | Button |
| --- | --- | --- |
| Chrome found | Chrome (or Edge on Windows) is found, or `chromePath` points at it | Browser settings |
| Sign in to Twitch | the Twitch runtime no longer needs a login | Log in |
| Choose your games | the games list is not empty, or Any game is on | Choose games |
| Start | the watcher has run once | Start |

- Game picker (Settings > Games, for the selected profile): "Drops right
  now" lists games with active drops (loaded when the picker opens, Refresh
  forces a fresh list), the search box searches all Twitch categories, and
  "Not listed?" adds an exact name. Press Save to apply.
- Queue: grouped by game in your games-list order, pinned drops first. Rows
  that need you carry one button (Link account, Claim on Twitch, Claim on
  Kick, Log in) that opens the page as a background tab in that profile's
  watcher Chrome. In the headless service the page opens in your own browser
  instead, and Log in opens the remote login view.
- Debugging: "Copy diagnostics" copies a redacted text bundle (version, host,
  Chrome, update state, config without secrets, per-profile state, last 400
  log lines); "Export diagnostics" writes a zip to the Desktop. Neither
  includes Chrome profiles, cookies or tokens.
- Pause from the tray, Resume from the dashboard card.

> Linux builds the GUI via Wails' GTK3 backend (`go build -tags gtk3
> ./cmd/dropwarden`, GTK3 + webkit2gtk-4.1) since 2026-08-30 - Wails v3's
> default backend there needs GTK4 + WebKitGTK, which most distros (and this
> container) do not have, so the gtk3 tag is the portable path. It cross-
> compiles and passes `task check`; running it on real Linux hardware is still
> verify-live until the thin-client machine exists. Plain `go build ./...`
> (no tag) still needs the GTK4 stack on Linux; use the gtk3 tag or `task
> build-linux`.

### How the UI talks to Go

The frontend is live out of the box: it calls a small JSON API
(`internal/service/httpapi.go`) served on the same asset origin as the page, so
no Wails binding generation is required. `go run ./cmd/dropwarden` and
`wails3 dev` both work identically. If you open `frontend/index.html` with no
backend behind it, it falls back to sample data for design review.

### Game picker API (anonymous, on demand)

| Route | Returns |
|---|---|
| `GET /api/games?profile=<name>` | `[{game, platforms, campaigns, endsAt, tracked}]` from the profile's cached campaigns (no network) |
| `GET /api/games/catalog?profile=<name>[&refresh=1]` | `{fetchedAt, sources: {kick, twitchDrops}, games: [{game, source, platforms, campaigns, endsAt, live}]}` |
| `GET /api/games/search?q=<text>` | `[{game, id, boxArt}]` (Twitch category search, box art 52x72) |

- `catalog` merges three sources by trimmed, case-insensitive game name: the profile's own cached campaigns (`source: "campaign"`), Kick's public drops-campaign list (`"kick"`, game = `category.name`, expired campaigns skipped) and Twitch's drops-enabled live streams (`"twitch_drops"`, `live` = stream count, up to 5 pages of 30).
- `sources.kick` / `sources.twitchDrops` are false when that source failed; the other rows still return. A failure is logged once per outage.
- `catalog` is cached per process for 10 minutes; `refresh=1` bypasses the cache at most once per 60 s (otherwise the cached copy is returned).
- `search` needs 2-60 characters after trimming (400 otherwise), caches each query 10 minutes, and allows 2 upstream searches per second process-wide (429 `{error}` beyond; cache hits are never limited). Upstream failure is 502.
- All three upstream calls are anonymous (no cookies, no login, no browser), 10 s timeout each, and run only when these routes are called; nothing polls them.
- Twitch refuses anonymous stream pages past the first with "failed integrity check" (seen 2026-09-26); the walk keeps page 1 and stops, so `live` counts cover the top 30 drops-enabled streams in practice.

## Development notes

> Status: the Twitch-facing selectors, GraphQL field names, and localStorage
> keys are marked "verify live" and must be confirmed on a real, logged-in
> machine (see `TESTING.md` and `docs/NOTES-twitch.md`); none of that can be
> exercised from a headless CI box. Windows is the primary target; macOS and
> Linux are secondary. The GUI is built with Wails v3.

## GoLand

Run configurations are committed under `.run/`: `cli doctor`, `cli login`,
`cli discover`, `cli watch`, `cli report`, and `gui app`. Open the project and
pick one from the run dropdown.

## Development

```
task check     # gofmt + vet + test + windows build + forbidden-call scan
task test
```

CI:

- `.github/workflows/govulncheck.yml` - weekly + per-push vulnerability scan.
- `.github/workflows/release.yml` - tag-triggered release builds (below).
- Tests and `task check` run locally; there is no per-push test workflow.

### Releasing

- Branches: `dev` is where work lands (default branch); `main` is the release
  line. Every push to `dev` runs CI: lint + test, the container image
  `ghcr.io/rake-pro/dropwarden:dev-<sha>` (what GitOps pins for node4), and
  the desktop builds for the dev update channel.
- Promotion: the Sync to main workflow opens "Merge dev to main" whenever dev
  is ahead. Merging it (merge commit) mints the next tag - patch by default,
  `release:minor` / `release:major` labels change the bump - and the Release
  workflow builds from the tag in the same run: desktop assets (desktop.yml),
  the container image `:vX.Y.Z`, the GitHub release, `latest.json`, and the
  upload to the downloads host when `DOWNLOADS_UPLOAD_TOKEN` is set.
- By hand: `git tag v0.1.0 && git push --tags`, or run Release
  (workflow_dispatch) with `version` = an existing tag; `container`
  (workflow_dispatch) rebuilds an existing tag's image.
- Update channels (Settings > Updates): Stable reads
  `<base>/dropwarden/latest.json`; Dev reads `<base>/dropwarden/dev-latest.json`,
  which CI writes for every dev push with assets under
  `<base>/dropwarden/dev-<sha>/`. Dev versions are `X.Y.(Z+1)-dev.N.<sha>`:
  above the last release, below the next. Container installs have no updater
  (new image = new version) and no Updates pane.
- Repo settings the workflows read:

| Name | Kind | Purpose |
| --- | --- | --- |
| `DOWNLOADS_BASE_URL` | variable | Downloads host base; default `https://cdn.rake.pro/releases`. |
| `DEV_DESKTOP_CHANNEL` | variable | `false` skips the desktop builds and dev-latest.json on dev pushes (saves Windows/macOS runner minutes); images and checks still run. |
| `DOWNLOADS_UPLOAD_TOKEN` | secret | Enables the "Publish to downloads host" step. |
| `MACOS_CERT_P12`, `MACOS_CERT_P12_PASSWORD`, `DEVELOPER_ID_APP` | secrets | Developer ID signing (optional). |
| `AC_APPLE_ID`, `AC_PASSWORD`, `TEAM_ID` | secrets | Notarization (optional, needs signing). |

- Downloads host contract (the "Publish to downloads host" step):

| Request | Path | Body |
| --- | --- | --- |
| `PUT`, `Authorization: Bearer <DOWNLOADS_UPLOAD_TOKEN>` | `<base>/dropwarden/<tag>/<asset>` | each release asset, raw bytes |
| same | `<base>/dropwarden/latest.json` | the manifest, uploaded last |

- Manifest schema (`latest.json`, Go type `update.Manifest`):

```
{
  "name": "dropwarden",
  "version": "0.1.0",
  "tag": "v0.1.0",
  "published_at": "2026-09-26T18:00:00Z",
  "notes": "markdown body (the GitHub release notes)",
  "assets": [
    {"name": "dropwarden_0.1.0_windows_amd64.zip",
     "url": "https://cdn.rake.pro/releases/dropwarden/v0.1.0/dropwarden_0.1.0_windows_amd64.zip",
     "sha256": "<hex>", "size": 12345}
  ]
}
```

- The updater picks the asset named `dropwarden_<ver>_windows_<arch>.zip`,
  `dropwarden_<ver>_darwin_universal.zip` or
  `dropwarden_<ver>_<goos>_<arch>.tar.gz`; a missing or empty `sha256` fails
  the install.
- Icons: `task icons` regenerates `build/icons/` (committed) from the logo.
