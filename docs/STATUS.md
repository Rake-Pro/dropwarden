# dropwarden status (handoff)

Snapshot for resuming after a context clear. Repo: Rake-Pro/dropwarden (private),
branch main. This file is the "where are we" note; deeper design is in README.md,
NOTES-twitch.md, NOTES-wails.md.

## Environment

- User runs the GUI on macOS (Apple Silicon, macOS 26) via GoLand run config
  "dropwarden GUI" (`go run ./cmd/dropwarden`). Xcode CLT installed.
- In THIS container: no Chrome/GUI. Verify only with `go build ./...`,
  `GOOS=windows go build ./...`, `go test ./...`, `go vet ./...`. GUI (Wails)
  cross-compiles for Windows headless; it does NOT build on linux (stub).
  `go env -w GOTMPDIR=/home/claude/gotmp` is set (test exec needs it).
- The frontend talks to Go over a JSON API on the Wails asset origin
  (`internal/service/httpapi.go`, `/api/*`) - NO generated Wails bindings.

## What works (verified live on the user's Mac)

- GUI launches, tray + dashboard, watcher engine starts, Chrome opens.
- Discovery: captures the page's own GraphQL passively (fetch/XHR shim in
  `twitch/selectors.go` -> `window.__dw_gql`), including BATCHED responses
  (array split in `twitch/capture.go`). Real op name is `ViewerDropsDashboard`
  (aliased in `gql/operations.go`).
- Campaign DETAILS (drops + channel ACL) are fetched via a page-context GraphQL
  call that REPLAYS the auth+integrity headers captured from the page's own
  requests (`twitch/details.go`, shim captures headers into `window.__dw_headers`).
  This is what defeats Twitch's `IntegrityCheckFailed`. ~115 campaigns parsed,
  details fetched for up to `details_per_cycle` (8) per pass.
- Eligibility is time-window based (not the `status` string) and no longer gated
  on the stale queue-item status (`queue/select.go`). List upsert preserves
  detailed ACL/slug/link (`store/campaigns.go`) so re-discovery doesn't clobber.
- Selection FALLS THROUGH to the next available drop when the top has no live
  channel (`queue.EligibleCandidates` + `watcher/select.go`), bounded by
  `watcher.max_select_attempts`; `watcher.wait_for_top` disables it. UI toggle
  "Fall through to next available" (default on).
- Debugging view (own left-nav): `/api/debug` -> runtime state, eligibility
  tally, last-discovery diagnostic, `client-integrity` captured indicator, paths,
  log tail (in-memory ring buffer in `logging`), Copy log / Copy JSON, and the
  `capture_gql_bodies` dump toggle (writes `~/.dropwarden/debug/*.json`, no auth).

## NEXT live milestone (where to resume)

The watcher had reached `selecting_channel` but not yet `watching` (last blocker
was the ACL clobber, now fixed - a re-run should restore NARAKA's ACL and pick a
channel). The next thing to confirm on the user's Mac:

1. Does it reach state `watching` and does progress tick? This is the FIRST real
   test of the six-point playback validation and its DOM selectors
   (`twitch/selectors.go`: `video`, offline gate, player error, raid banner,
   drops indicator). If it loops `validating_playback -> selecting_channel`,
   inspect the Debugging log tail for `aborted_offline` (channels just offline,
   expected churn) vs validation timeout on a channel that IS live (selector fix
   needed).
2. Then claim end-to-end (15-30 min drop) and confirm a `drop:claimed` + history
   row + the reward in Twitch inventory.

## Known follow-ups / open items

- ACL-offline churn: pickChannel is optimistic for ACL campaigns (no liveness
  check), so it may validate offline ACL channels ~60s each before blacklisting.
  A liveness pre-check (stream-status GQL or directory cross-check) would speed
  this up. Not done.
- Mid-watch preemption: when a higher-priority drop's channel goes live while
  watching a lower one, we only switch at the next session boundary (cooldown/
  rotation), not mid-watch. Fall-through picks best-available at selection time.
- Any-channel campaigns (empty ACL) need `game.slug` for the directory. Confirm
  `slug` is populated in a `~/.dropwarden/debug/details-*.json` (turn on the
  dump toggle). If empty, any-channel campaigns can't find a channel.
- Adversarial UI review findings APPLIED (theme/light-OS, switch a11y, live
  refresh of active view, volume NaN guard, partial-state guards, drag guard,
  responsive breakpoints).
- Claim flow, raid handling, stall watchdogs: implemented but NOT yet exercised
  live.
- UI still un-run in-container (no browser); the JS changes are static-reviewed
  only. Confirm the new Settings sub-nav, Debugging view, queue drag, and queue
  prune behave on the user's Mac.

## Recent commits (newest first)

- 4606ad9 fix: queue prune to filter + apply UI review findings
- 1163e19 docs: STATUS handoff
- 34ca57d ui+watcher: settings restructure, Debugging view, queue drag, fall-through
- 88cc65e debug: consolidated Debugging pane + /api/debug + log ring
- 428c1fa fix: list upsert clobbering detailed ACL/slug
- a54e13f fix: drop stale-status gate in selection
- 353fdde fix: time-window eligibility + selection diagnostic + dump toggle
- aa83f4f fix: pass integrity check by replaying captured headers
- 32ef9fd feat: fetch DropCampaignDetails (drops + ACL)
- bb7cbec fix: split batched GraphQL responses
- 3412401 fix: real op names (ViewerDropsDashboard) + capture diagnostics
