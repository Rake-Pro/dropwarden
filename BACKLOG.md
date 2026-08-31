# Backlog

Reference snapshot as of 2026-08-31 (`main` @ `914a620`). Working detail and
session handoffs live in `docs/STATUS.md`; this file is the curated list.

## The open verify

Minute-watched crediting has NEVER been confirmed on a genuinely earnable
(unowned) Twitch drop - every live test so far hit owned rewards or
non-participating channels. Needs a campaign with a drop the user does not own
(or `any_game: true`), then confirm: minutes tick, first real end-to-end claim.
The "Watch anyway" override (`914a620`) exists to force this test.

## Release-blocking

- Shared-window tab activation is a PoC: one audited `TargetActivateTarget`
  call site in `internal/browser/window.go` (Taskfile forbidden-scan
  carve-out). Replace with a no-activation solution, or prove muted
  background-tab playback reliable. Confirmed working on macOS, but this is
  the known silent-failure family.
- Thin-client deployment story is absent: no packaging, autostart, or update
  mechanism (goals adversarial review P1).
- Kick live verification (deliberately parked until a campaign the user wants
  is running): progress sources are nil-stubbed (`internal/kick/pages.go`),
  server accrues silently and unclaimed Kick rewards EXPIRE; claim/play/offline
  selectors and Cloudflare behavior unverified; no quality clamp (the N100
  4-stream 160p budget is invalid for Kick). First-run dump protocol is in
  `docs/STATUS.md`; the debug capture shim will record real shapes on the first
  campaign.

## Verify-live owed

- Mac test pass of the full multi-profile surface (two profiles = the
  thin-client shape minus GTK): shared-mode Kick progress (fallback =
  `separate_window: true`), remove flow, login -> both platforms in one
  window, stop-one-keeps-other refcount, tray/window behaviors, Settings
  Platforms/App panes + theme picker, owned-skip log line. Full reset of both
  test environments first (exercises the new `start_minimized: false` default
  and a pristine e2e).
- Linux GUI runtime on real hardware: GTK window, tray SetHidden, CDP window
  management under X11 (builds in-container; box not purchased yet).
- Kick login probe negative case: on a logged-OUT browser, verify
  `session_token` is absent (a bare `session_id` could be an anonymous
  session).
- Any-channel drops-indicator gate: a selector false-negative on a real
  participating stream would skip it; the directory `?filter=drops` scrape may
  leak non-drops channels.

## Features (deliberately deferred)

- Claim in a second tab so watching is not interrupted; claim-all-claimable
  inventory sweep for simultaneous multi-tier campaigns.
- Liveness pre-check before validating each ACL channel (fixes ACL-offline
  churn; also the fix if window restore/minimize churn ever gets flappy).
- Mid-watch preemption (currently a better drop is only picked up at a session
  boundary).
- Any-channel campaigns need `game.slug` populated (unconfirmed).
- Burn-in pixel shift is DEAD CODE: `ShiftWindow` + config exist with no
  caller. Wire it via the window coordinator (shift once per window, never
  minimized) or delete it.
- Proactive owned-reward skip has no config toggle (always-on by choice);
  revisit if a false positive ever surfaces - the reactive 5-minute give-up
  remains the backstop for drops with no parseable benefit name.
- Kick welcome flow has no real login probe beyond the session check (TODO in
  `docs/NOTES-kick.md`).
- Multi-profile out of scope for v1 (revisit later): profile rename polish,
  per-profile theme/watchdog tuning, cross-profile auth, store merges,
  per-profile priority mode.

## Open decisions

- `--listen <addr>` serve mode beside the GUI for remote queue management
  (via reverse proxy + SSO), vs remote desktop only - never decided. Related:
  `/api` has zero auth today, which is the real cost of `--listen` (goals
  review P2).
- Goals adversarial review decision form D1-D5 pending answers (workspace
  report `reports/2026-08-31_dropwarden-goals-adversarial-review.html`).
- First semver release not yet minted (no tags; standard is vX.Y.Z).

## Minor (P2)

- Window tiling y-overflow: slot 3 lands off-screen on 1080p. Old on-disk
  configs with baked x/y keep them explicit - tiling will not spread added
  profiles until those lines are deleted.
- Dashboard lost Pause (tray-only now; dashboard Stop closes Chrome).
- Existing configs read `timing_jitter` as 0 until regenerated.

## Roadmap (locked 2026-08-30)

1. Multi-profile in-app (profile = person, own DB + Chrome profiles) - phases
   0-3 CODE COMPLETE; Mac pass owed.
2. Unified dashboard (chosen over a profile switcher) - shipped.
3. Linux GUI = Wails GTK native (`gtk3` build tag) - builds; runtime
   verify-live.
4. Buy and deploy the sub-$150 N100/16GB thin client (~10W, 4 streams max =
   2 people x Twitch + Kick; Pi 5 ruled out on price + ARM fingerprint), then
   the deployment story above.
