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
- The window is launched unfocused, dimmed, and shifts a few pixels on a timer
  to avoid monitor burn-in.

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

### The GUI (Windows / macOS)

```
go run ./cmd/dropwarden          # builds + runs the tray app (embedded assets)
# or, with hot reload and typed bindings:
wails3 dev
```

The dashboard, queue, history, and settings screens follow the same visual
language as the sibling GoShareIt app.

> The GUI does not build on Linux: Wails v3 there needs GTK4 + WebKitGTK, which
> is out of scope. The Linux build is a stub that points you at the CLI. This
> keeps `go build ./...` green everywhere.

### Connecting the UI to live data

The frontend renders from sample data until the Wails bindings are wired. This
is a one-time step on your machine, documented at the top of
`frontend/api.js`: run `wails3 generate bindings`, set `window.dw` to the
generated services, and subscribe to events via `@wailsio/runtime`. Everything
else in the UI is already built against that seam.

## GoLand

Run configurations are committed under `.run/`: `cli doctor`, `cli login`,
`cli discover`, `cli watch`, `cli report`, and `gui app`. Open the project and
pick one from the run dropdown.

## Development

```
task check     # gofmt + vet + test + windows build + forbidden-call scan
task test
```

There is intentionally no GitHub Actions workflow yet; builds and tests are run
locally during active development.
