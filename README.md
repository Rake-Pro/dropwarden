# dropwarden

A tray-first desktop app that collects Twitch Drops by driving one real,
visible, never-focused Chrome window. It watches actual streams in a genuine
logged-in session, tracks drop progress, claims completed drops, and keeps a
persistent report. It deliberately does not spoof watching via the API.

Windows is the primary target; macOS is secondary. The GUI is built with Wails
v3. Development happens directly from GoLand.

> Status: MVP. All Go logic and parsers are unit-tested and the app cross-
> compiles for Windows. The Twitch-facing selectors, GraphQL field names, and
> localStorage keys are marked "verify live" and must be confirmed on a real,
> logged-in machine (see `TESTING.md` and `docs/NOTES-twitch.md`). This is
> expected: none of that can be exercised from a headless CI box.

## How it works

1. Discovery reads `twitch.tv/drops/campaigns` and `/drops/inventory` in the
   authenticated session and captures the page's own GraphQL responses
   passively (a small fetch/XHR shim buffers them; nothing extra is requested).
2. You queue and prioritize drops. The watcher works one at a time, top to
   bottom.
3. For each drop it opens an eligible live channel, validates that playback is
   real and crediting, then watches with a set of watchdogs:
   - progress stall (soft nudge, then hard refresh + re-validate),
   - playback health (offline, player error, frozen video, volume drift),
   - crash / hang (renderer crash and unresponsive-tab detection),
   - raid (cancel the banner, or adopt an eligible target, always refreshing).
4. When the minutes are met it claims the drop by clicking the real Claim
   button, records it, and moves on.

### Safety posture (anti-bot)

- Manual login only by default. Stored credentials exist in config but ship
  OFF and unsupported.
- The player runs at 1% volume, never muted: Chrome 145+ pauses a muted video
  when its tab is backgrounded, which would silently stop drop credit.
- A single pacer rate-limits every synthetic action with jitter; there is no
  rapid-fire anything.
- Claims use in-page clicks. The `ClaimDropRewards` GraphQL replay is
  implemented as a config flag but ships OFF (it has triggered bot blocks).
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
  retitles the tray item to "Install Update vX.Y.Z".
- Auto-install never cuts a watch short: while any profile is opening,
  validating, watching or claiming, the install waits (re-checked every
  5 minutes) and runs between watches. "Install now" installs immediately.
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

- Tag and push: `git tag v0.1.0 && git push --tags`, or run the Release
  workflow by hand (workflow_dispatch) with `version` = an existing tag.
- Jobs: windows (Inno Setup installer + updater zip, icon/manifest via
  `wails3 generate syso`), macos (universal .app, ad-hoc signed unless the
  Developer ID secrets exist, dmg + zip), linux (tar.gz), publish.
- publish writes `checksums.txt` and `latest.json`, attaches all of it to the
  GitHub release, then uploads to the downloads host when the
  `DOWNLOADS_UPLOAD_TOKEN` secret is set.
- Repo settings the workflow reads:

| Name | Kind | Purpose |
| --- | --- | --- |
| `DOWNLOADS_BASE_URL` | variable | Downloads host base; default `https://cdn.rake.pro/releases`. |
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
