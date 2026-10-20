# dropwarden live testing runbook

Everything Twitch-facing needs a real, logged-in machine to verify; it cannot
be exercised in CI. Work through these phases in order on Windows (primary) or
macOS. Record anything that differs in `docs/NOTES-twitch.md`.

Set the log level to `debug` in `~/.dropwarden/config.yaml` (or
`%USERPROFILE%\dropwarden\config.yaml`) while testing.

## Phase 1 - Browser session, login, player

Goal: prove the dedicated Chrome launches correctly, never steals focus, stays
logged in, and keeps crediting while backgrounded.

1. `go run ./cmd/dropwarden-cli doctor` - confirm it finds your Chrome binary
   and prints sane paths. If Chrome is not found, set `browser.chrome_path`.
2. `go run ./cmd/dropwarden-cli login`:
   - A ~640x400 dark Chrome window opens in a corner. **While it launches, keep
     typing in another app** - focus must not be stolen (this is the single
     most important behavioral check).
   - Log in to Twitch by hand. The command returns once the avatar is detected.
   - Set Twitch to its dark theme once (it persists in the profile).
   - Re-run `login`; it should report logged-in immediately (session persisted).
3. In that Chrome, open DevTools console and confirm:
   - the campaigns page populates (the page works under DevTools control)
   - `document.querySelector('video')` exists on a channel page,
     `video.volume` is ~0.01, `video.muted === false`.
4. **The Chrome 145 background test (critical).** Open a channel, then alt-tab
   away and leave the window visible-but-unfocused for ~10 minutes. Confirm
   `document.querySelector('video').currentTime` is still advancing. If it
   freezes, drop credit will silently stop - re-check the player volume (must
   be > 0) and the anti-throttle flags in `internal/browser/flags.go`.

## Phase 2 - Discovery and inventory

Goal: prove campaigns and progress parse correctly from the live session.

1. `go run ./cmd/dropwarden-cli discover` - it should print the campaigns for
   your configured games with drop names, required minutes, ACL sizes, and the
   account-link flag. Cross-check two or three against the real
   `twitch.tv/drops/campaigns` page.
2. `go run ./cmd/dropwarden-cli inventory` - for anything already in progress,
   confirm the minutes match what `twitch.tv/drops/inventory` shows.
3. If campaigns come back empty, the GraphQL field names or operation names have
   likely drifted. Set `logging.capture_gql_bodies: true`, re-run, and compare
   the captured JSON against the structs in `internal/twitch/gql/types.go`.
   Update the selectors/field names and note them in `docs/NOTES-twitch.md`.

## Phase 3 - Watcher and watchdogs

Goal: accumulate real watch time and exercise recovery.

1. `go run ./cmd/dropwarden-cli watch` against a live Rust or Halo campaign.
   Watch the log for the state transitions and progress ticking.
2. Temporarily set `watcher.soft_stall: 30s` and `watcher.hard_stall: 60s` to
   force the stall path quickly; confirm you see a soft nudge, then a hard
   refresh followed by a full re-validation in the log.
3. Induce failures:
   - Kill the tab from Chrome's task manager -> expect crash detection + reopen.
   - Pick a channel about to end -> expect `aborted_offline` and reselection.
   - Let a natural raid happen -> expect the banner cancel or the reactive path,
     always followed by a hard refresh.
4. Target: one full drop's required minutes accumulated end to end.

## Phase 4 - Claim and report

1. Complete one real 15-30 minute drop start to finish. Confirm:
   - a `drop:claimed` line in the log,
   - the reward actually present in your Twitch inventory,
   - `go run ./cmd/dropwarden-cli report` shows the claim and a session.
2. Confirm an account-link campaign (often a Halo one) is detected and skipped
   with a needs-action line rather than a failed claim attempt.
3. Leave it running overnight on a multi-drop campaign to exercise the
   precondition chain and proactive refreshes.

## Phase 5 - GUI

1. `go run ./cmd/dropwarden` (or `wails3 dev`). Confirm the tray icon appears,
   its menu shows a live status line, and clicking it shows/hides the dashboard.
2. The UI is live via the embedded JSON API - no binding step. Confirm the
   dashboard shows the real watcher state and queue, the progress bar advances
   during a watch (it polls every ~5s), toggling a queue item persists, and the
   priority selector saves.
3. Confirm the window starts hidden in the tray and closing it hides rather than
   quits (quit is from the tray). If the window ever freezes at start, see the
   `#4103` note in `docs/NOTES-wails.md`.

First macOS build note: `go run ./cmd/dropwarden` needs Xcode Command Line
Tools (`xcode-select --install`) because the macOS webview uses CGO. The CLI
harness needs none of that.
