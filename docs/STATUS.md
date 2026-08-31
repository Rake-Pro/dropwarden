# dropwarden status (handoff)

Snapshot for resuming after a context clear. Repo: Rake-Pro/dropwarden (private),
branch main. This file is the "where are we" note; deeper design is in README.md,
NOTES-twitch.md, NOTES-wails.md.

Everything below is COMMITTED and PUSHED to main (commits b9a9996..851ac1a,
plus 7edcaca from the Windows session): multi-profile support, the live
runtime lifecycle, the shared-window/one-Chrome-per-profile work, and the
follow-up UI.

## Environment

- User runs the GUI on macOS (Apple Silicon, macOS 26) via GoLand run config
  "dropwarden GUI" (`go run ./cmd/dropwarden`). Xcode CLT installed.
- In THIS container: no Chrome/GUI to RUN, but the Linux GUI now BUILDS
  (2026-08-30): the stub is gone, `cmd/dropwarden` compiles for
  windows|darwin|linux, Linux uses Wails' GTK3 backend (`-tags gtk3` ->
  GTK3 + webkit2gtk-4.1; bookworm has no webkitgtk-6.0 so the default GTK4
  backend cannot build here). The GTK dev stack lives in a user sysroot at
  ~/tools/wails-sysroot (container user cannot apt-install); ~/.zshenv sources
  ~/tools/wails-sysroot.env automatically. Verify with `task check` or:
  `go vet -tags gtk3 ./...`, `go test -tags gtk3 ./...`,
  `go build -tags gtk3 ./cmd/dropwarden`, `GOOS=windows go build ./...`.
  Real Linux machines just install libgtk-3-dev libwebkit2gtk-4.1-dev
  libx11-dev. Long-term those packages get baked into claude-container.
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

## SESSION 2026-08-31 (later): proactive owned-reward skip + Kick drop debug

Commit `a1f7151` (pushed to main; `task check` green). The user supplied the
real `inventory-gql.json` dump that the prior session asked for.

- SCHEMA CORRECTION: the earned/owned rewards live under
  `data.currentUser.inventory` as `gameEventDropsConnection.edges[].node.name`
  (game-integration items, e.g. the Once Human skins) plus
  `earnedDropRewards.edges[].node.item.name` (badges/emotes/entitlements). The
  old `gql/types.go` guess (`gameEventDrops []{id}`) matched NOTHING and is
  replaced. Owned-reward IDs are campaign-instance-scoped
  (`<campaignUUID>_CUSTOM_ID_<n>`), so a new campaign offering the same cosmetic
  mints fresh IDs - the only stable cross-instance join key is the normalized
  reward NAME. This is exactly why the Once Human drops ground forever.

- PROACTIVE OWNED-REWARD SKIP (Twitch): a drop whose reward the account already
  holds is now skipped BEFORE watching, not after. Flow: `gql.ParseEarnedRewards`
  + `NormalizeRewardName` build the owned-name set from the Inventory op;
  `CaptureResult.OwnedRewards` carries it out of `Drain`; `Pages.ReadInventoryFull`
  returns `(progress, owned)` (the old `ReadInventory` is unchanged - it is on the
  watcher `Pager` interface and used by the CLI + Kick); `discovery.markOwnedDrops`
  flags matching drops via a persisted `domain.Progress.Owned` bool (append-only
  migration, `schemaVersion` 2, `ALTER TABLE progress ADD COLUMN owned`), running
  AFTER the inventory progress save so real minutes are preserved and matching only
  on `Benefit.Name` (never the drop's tier name, to avoid generic false-positives);
  `queue.eligibleNow` and `queue.EligibilityReason` both exclude owned drops, the
  latter with `needs_action` "reward already owned - Twitch will not credit it
  again". DESIGN NOTE: this uses an honest `Owned` flag, NOT a faked `ClaimedAt`
  (a fake claim would make `eligibleNow` and `EligibilityReason` disagree - the
  latter never reads `ClaimedAt` - and would mislabel the drop "Claimed" in the
  UI). No config toggle: kept always-on, consistent with the reactive no-credit
  give-up, which stays as the backstop for drops with no parseable benefit name.
  Tests: `TestParseEarnedRewards` (+ NoInventory), `TestNextDrop_OwnedRewardSkipped`.

- KICK DROP-PROGRESS DEBUG (own files, separate from the Twitch dump): Kick had
  no passive capture. Added a debug-gated fetch/XHR shim (`jsSeedNetCapture` /
  `jsDrainNetCapture` in `kick/selectors.go`, buffering
  `drop|inventor|campaign|quest|reward` URLs into `window.__dw_kick_net`) plus
  `Pages.SeedDropsCapture` / `DrainDropsCapture` / `InventoryRawHTML`. Kick
  discovery seeds the shim BEFORE navigating and, when `capture_gql_bodies` is on,
  dumps `kick-drops-network.json` (raw API traffic) and `kick-inventory-raw.html`
  (DOM-rendered progress) alongside the existing `kick-campaigns.json` /
  `kick-inventory.txt`. This captures a real Kick drop's progress shape the moment
  one is live so the parser can be built from real data.

- VERIFY-LIVE OWED: (1) owned-skip on the next Mac run - watch the log line
  `discovery: reward already owned - drop will be skipped before watching` and the
  amber `needs_action` pill; (2) Kick dumps only fire once a Kick campaign actually
  exists (none has yet). STILL-OPEN #1 UNCHANGED: minute crediting has NEVER been
  confirmed on a genuinely UNOWNED/earnable Twitch drop - every live test so far hit
  owned or non-participating drops. Needs a game with an active unearned drop (or
  `any_game`) to prove minutes tick + a real claim end to end.

## SESSION 2026-08-31 (evening): live Mac debugging - drops crediting saga

Long live-debug session on the user's Mac (build banner shows the running
revision; latest pushed is 9902693). Everything from the multi-profile /
lifecycle / shared-window / login-flow work was exercised live and mostly
works; the focus became "watches a channel but earns no drop progress." What
was found and fixed, in order:

- FIRST-RUN LOGIN worked end to end (shared window, tab-per-platform, engines
  start paused + needsLogin, login page navigation). A confirmed login now
  auto-starts watching. KICK LOGIN PROBE built by live DevTools iteration
  (kick/auth.go): who-am-I is GET web.kick.com/api/v1/user/session, answers
  {data:{session_id},message} on a plain cookie fetch; verdict = session_id
  present AND a session_token cookie/storage key found = logged in (bare
  session_id could be anonymous). VERIFY-LIVE: on a LOGGED-OUT kick browser,
  confirm session_token is absent (else the heuristic needs strengthening).
- PLAYBACK VALIDATION reselect loop: every channel timed out at validate_timeout.
  Root cause via new gate-breakdown logging: drops_indicator selector matched
  nothing (visible/playing/vol-ok/advancing all true). Made the indicator
  ADVISORY, added a stream-tag text fallback. Validation then reached `watching`.
- STALE about:blank RAID: right after `watching`, the engine adopted a "raid"
  to about:blank (the tab's own creation navigation sat buffered; the engine
  only reads events while watching). Fixed: drain the event backlog at watching
  entry + loginFromPath rejects non-channel URLs.
- MINIMIZED TAB = NO CREDIT (confirmed via visibility field in the poll): after
  validation the window re-minimized -> tab visibilityState "hidden" -> Twitch
  pauses minute-watched heartbeats. Fix: stay VISIBLE while watching
  (browser.window.minimize_while_watching default false; cover the window, do
  not minimize - occlusion flags keep a covered window crediting). Answers the
  long-open "credits after re-minimize?" verify: NO.
- ELIGIBILITY red herring: the user's 3 Once Human "Console Launch Drops" drops
  looked already-claimed. Added claimed-drop capture (details query now fetches
  self{isClaimed}, discovery persists it, eligibility gates on ClaimedAt; heal
  re-details a campaign until claim state is captured). BUT the details dump
  proved they are self.isClaimed=FALSE, 0 min, hasPreconditionsMet=true -
  GENUINELY UNCLAIMED. The "claimed 3 days ago" inventory was a PRIOR campaign
  instance; same cosmetics, new drop IDs. Eligibility was right all along.
- ANY-CHANNEL PARTICIPATION: an any-channel campaign (acl:0) credits only on a
  PARTICIPATING (Drops-Enabled) stream. Selection was landing on ordinary Once
  Human streamers. Fix: for any-channel campaigns the drops indicator is a GATE
  (playback-valid + no drops indicator = not participating -> fail fast,
  reselect). ACL keeps it advisory. It then landed on a genuine participating
  channel (lilbit_007, indicator present) and STILL earned nothing.

THE ACTUAL BLOCKER (unresolved): the user ALREADY OWNS these cosmetics in-game
(from the prior console launch), and Twitch will not open a crediting session
for an owned reward - session_found stays false even on a validated
participating, playing, visible channel. So these specific drops can NEVER
credit for this account, and NO amount of code makes them.

- STANDALONE HANDLING shipped: give up on a drop that watches a participating
  channel with zero credit and no crediting session (creditChannel empty) after
  noFirstCreditTimeout = 5 min -> flag noCreditDrops (in-memory, session-scoped),
  mark needs_action ("you may already own this reward"), selection skips it and
  falls through. So an unattended box self-resolves owned/un-earnable drops in
  ~5 min instead of grinding forever.
- PROACTIVE owned-reward skip (NEXT WORK ITEM, needs data): when
  capture_gql_bodies is on, discovery now dumps the drops-inventory page's raw
  GraphQL to debug/inventory-gql.json (Pages.CaptureInventoryRaw, captured on
  the first navigation). That file carries Twitch's real earned/claimed shape
  (gameEventDrops, or per-campaign self.isClaimed:true). NEXT: from that file,
  find the op listing earned/owned rewards, match a queued drop's reward
  (benefit id, or reward name cross-campaign) against it, and at discovery mark
  owned drops claimed (SaveProgress with ClaimedAt) so eligibility excludes them
  BEFORE watching. The file is large - extract just the op whose response lists
  claimed/earned drops (grep for the reward names, e.g. "DECAL: GRAFFITI", or
  the gameEventDrops / isClaimed keys) rather than the whole buffer.

THE ONE THING STILL NEVER VERIFIED: minute-watched crediting actually working
on a drop the account does NOT own. Every live attempt has been an owned or
non-participating drop. To close it: point the filter at any game with an
active UNEARNED drop (or set any_game: true) and confirm minutes tick +
active_in_rows true + a real claim. That is the remaining end-to-end proof.

## SESSION 2026-08-30: launch-on-demand + ref-counted window + orphan cleanup

Debugged live on the user's Windows box (first Windows run of the multi-profile
build). Three fixes, all in the working tree, COMMITTED this session.

- SYMPTOM 1 "black Chrome, no Wails UI": the black window was the watcher Chrome
  (dimmed via the burn-in overlay + dark emulation on a blank/login page), NOT
  the dashboard - the dashboard was hidden in the tray because `ui.start_minimized`
  was true. The deeper issue: Chrome launched on boot before the user had logged
  in or configured anything.
- SYMPTOM 2 "Log in opens 3 about:blank, DevToolsActivePort timeout": orphaned
  Chrome from force-killed prior runs held the exclusive profile's singleton
  lock, so each new launch was FORWARDED to the orphan (the stray about:blanks)
  and exited without publishing a DevTools port. Root cause of the orphans:
  nothing killed dropwarden's Chrome when the app died uncleanly.
- SYMPTOM 3 "Kick Log in does nothing": shared-window coupling - Kick was a tab
  in Twitch's window and the reconcile gate silently dropped Kick whenever
  Twitch's window was down.

LAUNCH ONLY WHEN A WATCHER RUNS (`internal/app/reconcile.go`,`app.go`;
`internal/service/{services,httpapi}.go`; `frontend/{app.js,api.js}`). Chrome
now exists for a (profile, platform) exactly when its intent is watch or login,
never merely because it is enabled. A per-(profile, platform) run intent
(off | watch | login) lives on `App.intents`, guarded by `App.mu`, seeded at
boot: watch only when the platform is enabled-on-start AND already past its
first login (read from the on-disk markers via `App.NeedsLogin`, no Chrome
needed), otherwise off. Run-control seams `StartWatching` / `StopWatching` /
`BeginLogin` set the intent and reconcile; the service layer reaches them
through optional interfaces on the reconciler (same pattern as
`markSessionReady`). `WatcherService.Start` launches (or unpauses an up
login-mode window in place); `Stop` tears the window down (no watcher -> no
Chrome); new `Login` brings the window up parked on the login page - the ONE
launch with no watcher, because logging in needs the browser. The engine action
route no longer requires a live engine for start/login/stop; only skip/refresh
keep the "not running" error. `/api/state` reports `needsLogin` while a runtime
is DOWN (via `Backend.needsLoginDown` -> `App.NeedsLogin`) so the dashboard can
offer a Log in button before any Chrome exists. Frontend: a down+enabled
platform shows Start (+ "Log in" / "Start anyway" when it has never signed in);
Stop closes Chrome; the footer/profiles-pane status reads "stopped" / "needs
login" / "logging in" instead of "starting/failed". Boot honors
`enabled_on_start`: a logged-in platform with it on still auto-launches and
watches. CLI path unchanged (no intents seeded -> filesystem first-run check is
the fallback in `startTwitch/KickRuntime`).

REFERENCE-COUNTED SHARED WINDOW (`internal/browser/group.go`,`instance.go`;
`internal/app/app.go`,`reconcile.go`). Replaces the owner-tab model: the
window's initial page is a KEEPER dropped (`Instance.CloseInitialTab`) once the
first platform tab exists, every platform is an equal `NewTab`, and the window
closes only when its LAST tab goes (`Group.CloseSession` counts remaining tabs).
So stopping Twitch now leaves Kick running (and vice versa), and EITHER platform
brings the profile window up - `startKickRuntime` calls `ensureProfileGroup`
(with the Twitch profile dir/config the shared window uses) instead of erroring
when Twitch is down, and the reconcile gate that dropped Kick without Twitch is
gone. `WindowHost` changed: `Owner()` removed, `CloseInitialTab()` and window-
level `SetMinimized()` added (launch-time minimize no longer needs an owner tab).
`tab.Close`'s owner->instance.Close special-case is UNCHANGED (the separate-
window Managed path still relies on it). Group/shared-window tests reworked to
the ref-count model (`TestGroupWindowClosesOnlyWithLastTab` replaces the old
owner-close test; fakes gained `CloseInitialTab`/`SetMinimized`).

CLEAN UP AFTER ITSELF - CROSS-PLATFORM ORPHAN PREVENTION. Windows
(`launch_windows.go`): every launched Chrome is created SUSPENDED, joined to a
kill-on-close Job Object, then resumed - Windows kills the whole Chrome tree the
instant dropwarden exits for any reason (clean, panic, force-kill), so no orphan
can wedge the next launch. POSIX parity (`profile_posix.go`,`launch_darwin.go`,
`launch_other.go`): a startup reap reads Chrome's `SingletonLock` symlink
(`hostname-pid`) and kills a live orphan still holding the exclusive watcher
profile before the stale lock is cleared, and Chrome is launched in its own
process group and killed as a group on shutdown. `killLockOwner` is a no-op on
Windows (the job object already guarantees it). `clearStaleLock` calls it first.
Verified: darwin/linux/windows all cross-compile, `task check`-equivalent green.
(Pre-fix Windows orphans are cleared once with `~/dropwarden/kill-orphans.ps1`.)

## ROADMAP - multi-profile + thin-client (decided 2026-08-30, NOT started)

User decisions, locked: (1) MULTI-PROFILE inside ONE app instance - a profile
= one person bundling their Twitch + Kick logins; per-profile subdirectory
(own sqlite DB, own chrome-profile dirs, own queue/history; NO store
migration - the app holds N profile runtimes, up to Nx2 engines/Chromes);
per-profile game filters with a shared default. (2) UI = UNIFIED DASHBOARD:
every profile's platform cards grouped by person, profile selector scoping
queue/history/settings; keep it a standalone app (Wails), not browser-only.
(3) Linux GUI = Wails GTK NATIVE (gtk3 tag; build works everywhere since
2026-08-30, runtime unverified until the thin client exists). (4) Target
hardware: sub-$150 N100/16GB-class mini PC (~$110-140 street; 4 streams max
= 2 people x 2 platforms, well within it; Pi 5 ruled out - price + ARM
fingerprint). OPEN QUESTION (never decided): an optional --listen serve mode
alongside the GUI so the wife can manage her queue from her own devices via
gpm+Authentik; without it, remote management = RustDesk to the box.
DESIGN DOC WRITTEN 2026-08-30: docs/NOTES-multiprofile.md (read it before
implementing - config profiles block w/ legacy zero-move migration, per-
profile DBs, N ProfileRuntimes, /api/engine/{profile}/{platform}/{action},
unified dashboard + profile selector, tray generalization, 4 phases).

MULTI-PROFILE PHASES 0-3 CODE COMPLETE (2026-08-31, committed on main).
What landed: config gained a `profiles:` block (name, dir,
enabled, display_name, games/any_game inherit-or-override, per-profile kick
enable, window_slot) with legacy synthesis - a config with no profiles block
still gets exactly one profile ("main", rooted at the app dir itself), so
existing installs move zero files and see zero behavior change; `config.
ProfilePaths`/`Effective`/`ProfileRuntimeConfig`/`ProfileKickRuntimeConfig`
resolve the per-profile overlay. `internal/app` now holds N `ProfileRuntime`s
(App.Profiles, replacing the old single Engine/KickEngine fields);
StartRuntime loops profiles and SKIPS one that fails to launch rather than
taking the others down. The HTTP API is profile-dimensioned:
`/api/engine/{profile}/{platform}/{action}`, `/api/state` returns a
`profiles` array (each with its platforms/queue/claims/stats/spark), and
`/api/profiles` gained add / enabled-toggle / update routes (no delete/rename
in v1, documented as a manual-file-ops-with-the-app-stopped operation). The
dashboard is unified: one section per enabled profile, a profile pill bar in
Queue/History/Settings headers when >1 profile is configured, a new
Settings > Profiles pane, and footer/tray labels that collapse to today's
platform-only wording for a single profile. Phase 3 polish: window-tiling
defaults (default engine geometry is a slot grid - x = 40 + platform_col*660,
y = 40 + window_slot*420, twitch col 0 / kick col 1 - applied in
`config.ProfileRuntimeConfig`/`ProfileKickRuntimeConfig` only when the config
left x/y unset, so an explicit `browser.window`/`kick.browser.window`
geometry always wins; slot 0 reproduces today's baked-in single-profile
positions (40,40) / (700,40) exactly, so this applies unconditionally, not
just when >1 profile is configured) and a 90s discovery stagger (each
started profile's engines seed their FIRST discovery pass with
`profile_index * 90s` via `watcher.Deps.DiscoveryStagger`, wired in
`internal/app/app.go`'s `discoveryStagger`/`startTwitchRuntime`/
`startKickRuntime`, applied once in `watcher.runDiscovering`; the
first-started profile keeps stagger 0, so single-profile boot timing is
unchanged) so N profiles do not burst their platform's GQL/API fetches
simultaneously at boot. config.example.yaml documents the profiles shape
(commented out) and the legacy-main note. New unit tests: config
`TestWindowTileDefault*` (single-profile matches legacy positions, multi-
profile slot grid, explicit override wins) and watcher
`TestRunDiscoveringZeroStaggerRunsImmediately` / `TestRunDiscoveringApplies
StaggerOnceOnly`, plus `app.TestDiscoveryStagger`. `task check` passes clean.

LIVE RUNTIME LIFECYCLE (2026-08-31, committed on main). The owner's
complaint - "restart the app to apply disease", and enabling Kick on the Mac
producing nothing but a restart hint - is fixed: adding a profile, enabling or
disabling a profile, enabling or disabling Kick (root default or per-profile),
and REMOVING a profile all take effect immediately.

- Reconciler (`internal/app/reconcile.go`). `App.Reconcile(cfg)` swaps in the
  newly committed config and kicks a background pass; `ReconcileSync` runs one,
  serialized, coalescing a trigger that lands mid-pass into exactly ONE trailing
  rerun (so a burst of saves cannot double-start a runtime). A pass diffs the
  desired set (enabled profiles x effective kick) against the running runtimes,
  stops the deltas first, then starts them. Each `PlatformRuntime` now owns its
  lifecycle: per-runtime context cancel, its engine's Run goroutine, and a
  teardown that cancels, waits (10s bound) for Run to return, detaches, then
  closes the Managed session (`browser.Managed.Close` sets its closed flag
  before closing the underlying session, so the supervisor exits instead of
  relaunching Chrome - verified in managed.go). A launch failure logs and leaves
  the runtime ABSENT (enabled + not up = "starting or failed"), never fatal.
  Profile DISABLE keeps the store open (UI still lists its queue/history);
  profile REMOVE closes it. The per-platform start function is an injectable
  field (`App.startPlat`), so the whole reconciler is unit-tested without Chrome.
- Wiring: `internal/service.Backend` implements `app.Binder`
  (AddProfile/RemoveProfile/Attach/Detach) and gained `SetReconciler`;
  `SettingsService.commit` now does ReloadEngines (config values into running
  engines) AND Reconcile (which runtimes exist). The GUI entry point is
  `App.StartLive(ctx, emit, binder)` - boot goes through the same reconcile path
  as every later change; `main_gui.go` no longer runs engines itself. The CLI is
  untouched: `App.StartRuntime` still just builds runtimes and the CLI drives
  `Engine.Run` itself.
- NEEDS-LOGIN posture. A (profile, platform) whose Chrome user-data dir is
  missing or EMPTY at launch is a first-ever session: its engine is built paused
  (`enabled_on_start` forced off for that engine only) and `/api/state` reports
  `needsLogin: true` for it. This is keyed on the Chrome dir, not on the profile
  being new, so a first-time Kick enable on the existing main profile gets the
  paused + needs-login posture too, at boot as well as live. The flag clears when
  a login check comes back authenticated or the user presses Start; that answer
  is PERSISTED (review fix 2026-08-31): an unanswered prompt writes
  `<profileDataDir>/.dw-<platform>-login-pending`, which survives restarts and
  beats every other signal, so quitting before logging in re-prompts on the next
  boot instead of silently watching logged out. Answering it removes the pending
  marker and writes the `-initialized` one. A used Chrome dir with no markers is
  grandfathered as logged in (installs from before the marker scheme). Also from
  the same review: `login-check` refuses to run against a busy engine (the probe
  NAVIGATES the tab - it only runs when the engine is paused or idle), and the
  shared-window coordinator serializes restore/minimize/activate ops (a release
  racing a take could leave the window minimized while held) and re-activates
  the remaining holder's tab when the other platform releases mid-validation.
  2026-08-31: a needs-login start also NAVIGATES its tab to the platform's site
  (twitch.tv / kick.com) - the engine is paused on a first run and would never
  navigate on its own, leaving the welcome flow staring at about:blank.
- NEW/CHANGED API. `POST /api/profiles/remove {name}` -> `{"ok":true,
  "dataDir":"..."}`; refuses the last remaining profile (400) and unknown names;
  the data directory is NEVER deleted. `POST /api/profiles/login-check
  {profile, platform}` -> `{"loggedIn": true|false|null}`, null when unknowable
  (no session up, or Kick, which has no probe - see docs/NOTES-kick.md); it is
  on-demand only, never part of the state poll. `/api/state`: `pendingRestart`
  is GONE from both the profile entries and the platform entries (the startup
  snapshot machinery behind it is deleted, not just hidden); platform entries
  gained `needsLogin`. Engine-action errors no longer say "restart".
- STILL RESTART/RELAUNCH BOUND (deliberate, documented in the Settings copy):
  the browser LAUNCH settings - chrome_path, extra_flags, profile_dir, window
  geometry, mute_audio, start_minimized - are read when Chrome is launched, so a
  change reaches an already-running session only on its next launch (crash
  relaunch, runtime toggle, or app restart). Everything else hot-reloads through
  ReloadEngines. No snapshot machinery was kept for this: the settings rows that
  need it already say "Needs a Chrome restart".
- CONFIG MIGRATION: pre-tiling starter configs baked `browser.window` x/y 40,40
  and `kick.browser.window` x/y 700,40 into the file. Since tiling landed those
  act as explicit overrides and stack every added profile's windows on slot 0.
  `applyDefaults` now clears exactly those two pairs to unset (they are
  byte-identical to `windowTileDefault(0,0)` / `(1,0)`, so no install's windows
  move); a near-miss like 41,40 is left alone.
- TESTS: `internal/app/reconcile_test.go` (start on enable, stop on disable with
  the store kept open, re-enable, kick toggle both directions, added profile
  starts and publishes needsLogin, removed profile closes its store and leaves
  its data on disk, launch failure not fatal + retried, 8 concurrent commits
  start each runtime exactly once, chromeDirFresh, pausedConfig), service tests
  for the remove route (incl. last-profile refusal and the returned dataDir),
  the login-check contract (null / false / true and flag clearing), and
  `TestLiveTogglesReconcile` pinning that every desired-state commit reconciles;
  config tests for both baked-origin migrations plus a non-matching pair.

ONE CHROME WINDOW PER PROFILE (2026-08-30, committed on main). Four
profiles x two platforms used to cost EIGHT Chrome instances; it now costs
FOUR. Kick runs as a TAB in the profile's Twitch Chrome window instead of a
second instance.

- `internal/browser` split: `Instance` (instance.go) is one Chrome process +
  CDP connection hosting N tabs; `tab` (session.go) is the Session
  implementation, one page. `LaunchWith`/`Launch` are unchanged for callers -
  they return the instance's OWNER tab, and closing that tab closes the whole
  browser. `Instance.NewTab` opens an extra tab in the SAME window
  (Target.createTarget, no newWindow); closing it closes only that tab. One
  event pump per instance routes CDP events to the right tab by session id and,
  on transport loss, fails every tab and closes `Instance.Done()`.
- `browser.Group` (group.go) supervises ONE shared window: `Session(ctx, key,
  init)` hands each platform a STABLE handle (a `Managed` built with
  `NewSwappable`, i.e. no relaunch closure of its own), the first key claiming
  the owner tab. On CDP loss the group relaunches the browser ONCE and rebuilds
  EVERY registered tab in order, re-running each tab's `TabInit` (that is what
  brings the Twitch GQL capture shim back), then `Swap`s all handles at once so
  no engine sees a half-built browser. Dynamic tab creation works on a live
  supervised browser: enabling Kick mid-run opens a tab, it does not relaunch.
  `WindowHost` is the interface the group needs from a window, so all of this is
  unit-tested without Chrome.
- `browser.WindowCoordinator` (window.go) owns SetMinimized/restore for ONE
  window. Restore is REFCOUNTED: Opening/Validating/Claiming take a hold via
  `WindowHandle.Restore`, `beginWatching` releases it, and the window only
  re-minimizes when the LAST hold is gone - so Twitch validating a channel can
  no longer bury Kick mid-validation. `watcher.Deps.Window` carries the handle;
  `Engine.setWindowMinimized` now delegates to it (and keeps the old direct
  path as a fallback for tests).
- TAB ACTIVATION, PoC, owner decision 2026-08-30. Taking a hold on a window that
  hosts MORE THAN ONE tab activates the holding tab. That is the previously
  forbidden Activate family, accepted as the proof-of-concept mechanism. It
  lives in EXACTLY ONE call site, `WindowCoordinator.activate` in
  `internal/browser/window.go`, and the Taskfile `forbidden` task was split in
  two: `bringToFront` stays banned everywhere, `\.Activate\(` stays banned
  everywhere EXCEPT that one file. Do not widen the exemption.
- BACKLOG, RELEASE-BLOCKING: replace tab activation with a no-activation
  solution, or prove muted background-tab playback reliable. A muted background
  tab is the known silent-failure family (Chrome throttles it and drop minutes
  stop accruing); the soft/hard stall watchdogs are the only backstop today.
- BACKLOG (found in the 2026-08-31 goals review): the burn-in PIXEL SHIFT is
  dead code - `ShiftWindow`/`browser.burnin.shift_interval`/`shift_pixels`
  exist with per-OS implementations, but nothing calls `ShiftWindow` on a
  timer, so the README's promised periodic shift never happens (README wording
  corrected). Wiring it now belongs to the window coordinator (a shared window
  must shift once, not once per tab, and never while minimized); do it there
  or delete the plumbing.
- VERIFY LIVE (Mac): shared-mode Kick progress. Watch whether the Kick tab keeps
  accruing minutes while the Twitch tab is in front, and whether activation on a
  channel switch disturbs the other platform's playback. If it does not hold up,
  `kick.browser.separate_window: true` restores the two-instance layout per
  profile with no other change (it is the thin-client fallback and stays fully
  supported).
- CONFIG: new key `kick.browser.separate_window`, default FALSE (= shared). It
  is a LAUNCH setting (applies on a runtime's next launch). In shared mode the
  kick block's `profile_dir` and `window` geometry are UNUSED, and window tiling
  gives Kick col 0 row window_slot (the same window as Twitch) instead of col 1;
  separate mode keeps the col-per-platform grid.
- ONE-TIME KICK RE-LOGIN: shared mode runs Kick in the TWITCH Chrome profile
  dir, so an existing `chrome-profile-kick` login does NOT carry over. Kick
  reports `needsLogin` on its first shared start and the user logs in once in
  the shared window. Because that platform no longer owns a Chrome dir, first-run
  detection for shared Kick uses a marker file `<profileDataDir>/.dw-kick-
  initialized` written when the runtime first comes up; Twitch (and either
  platform in separate mode) still keys on Chrome-dir freshness. `/api/state`
  `needsLogin` semantics are unchanged.
- LIFECYCLE: SUPERSEDED 2026-08-30 by the reference-counted window (see the
  SESSION 2026-08-30 block above). No platform owns the window now: starting
  either opens a tab, stopping either closes only that tab, and the window closes
  only when the last tab goes. Kick no longer depends on Twitch - either can
  bring the profile window up.
- TESTS: browser `TestWindowCoordinator*` (refcounted holds, activation only for
  the holding tab, single-tab windows never activate, start_minimized off skips
  window state, unregister releases the hold, activation error not fatal) and
  `TestGroup*` (second platform gets a tab not a relaunch, close one tab without
  closing the window, closing the owner closes the window, relaunch rebuilds
  every tab and re-runs its init, tab on a closed window refused); app
  `TestSharedWindowKickStartsAsATabOnTheLiveBrowser`,
  `TestSharedWindowKickStopClosesOnlyItsTab`,
  `TestSharedWindowStoppingTheProfileClosesTheBrowser`,
  `TestSeparateWindowFallbackLaunchesTwoInstances`,
  `TestSharedModeKickNeedsLoginOnItsFirstStart`, `TestPlatformFirstRunRules`;
  config `TestWindowTileDefaultSharedWindowUsesTheProfileSlot` plus the existing
  tiling tests pinned to separate mode. The app tests run the REAL start path
  against fake browsers through two new injectable seams (`App.openWindow`,
  `App.launchSession`), so the window lifecycle itself is what is under test.
- FRONTEND: no state shape changed - no UI work is owed by this change.

UI HALF BUILT (2026-08-31, committed on main). The live-lifecycle UI
follow-up above is done: (1) `pendingRestart` reads stay removed (unchanged
from the prior pass). (2) The needsLogin now-card body is a real guided flow:
mode-agnostic copy ("Log in to <Platform> in this profile's watcher window,
then check." - never promises a separate window for Kick, since shared-window
mode runs it as a tab in Twitch's window), a "Check login" button hitting
`POST /api/profiles/login-check`, and the existing idle-state Start button
right below it. Check-login result: true shows "Logged in - press Start below
to begin."; false shows "Not logged in yet - finish logging in and check
again."; null shows "Can't auto-detect login for <Platform> - once you're
logged in, press Start." Both buttons stay disabled in preview mode.
(3) The Profiles pane has a Remove control per row: inline two-step confirm
(button flips to "Really remove <name>?" plus a Cancel button, no
window.confirm), disabled with a title when it is the only profile, and shows
"Profile removed. Its queue, history and logins are still on disk at
<dataDir>." on success. (4) Each row now shows live per-platform status
(running / starting / needs login / stopped / failed) pulled from /api/state,
reusing the footer's enabled/up/needsLogin semantics; saveProfileEdits diffs
against the loaded snapshot and only fires setProfileEnabled/updateProfile for
a profile whose fields actually changed, so an unrelated profile's toggles are
never re-sent. Add-profile copy no longer mentions restarting ("Profile added
- its watcher window is opening. Log in to Twitch (and Kick if enabled) there,
then press Start on the dashboard.") and polls /api/state briefly (4x, 1s
apart) so the new row's live status appears without leaving the pane. SAMPLE
data in api.js flips main's Kick platform to `needsLogin: true` so the flow is
designable offline.

MAC VERIFY-LIVE (added to the existing multi-profile surface test pass):
welcome/login flow end-to-end (needs-login card copy, Check login against a
real Twitch session, Start clearing the flag), the Profiles pane Remove flow
(confirm/cancel, the dataDir message, the disabled-with-title single-profile
case), and a live Kick toggle actually opening a tab in the shared window
(not a separate window) so the mode-agnostic copy holds up.

OWED: a Mac test pass of the whole multi-profile surface (two profiles on
the Mac = the full thin-client shape minus GTK - this needs a real display
and is not doable in this container); the Linux GUI runtime itself is still
verify-live (it builds, per the Environment section above, but has not run
on real hardware); the optional `--listen` remote-management question from
the design doc remains open/undecided. All of the above is committed on main
(see the commit-range note at the top of this file). NEXT WORK ITEM: the Mac
test pass.

## NEXT live milestone (where to resume)

MULTI-PLATFORM UX OVERHAUL 2026-08-30 (Kick promoted from bolt-on to first
class; static-reviewed only, confirm on the Mac): /api/state now carries a
uniform `platforms` array (twitch first: enabled/up/status, Status carries its
platform) and engine actions are `POST /api/engine/{platform}/{action}` (the
old /api/watch/* and /api/kick/* routes are GONE); the unused `campaigns` key
was dropped from state (it cost N+1 progress queries per 5s poll). Dashboard
renders one Now-watching card per enabled platform (platform badges appear
only when >1 platform is on, twitch purple / kick green, kick-green progress
bars); a kick launch failure now shows an explicit "not running" card +
footer row instead of silently vanishing. Stats gained byPlatform totals
(dashboard Totals card splits min/claims when multi). Settings restructured:
new Platforms pane (Twitch group = skip-account-link + GQL fallback moved
from Browser; Kick group = enable toggle + first-run copy), new App pane
(theme picker — previously config-only — + auth), Browser pane is chrome-path
only. Debugging view is per-platform (kick card shows api status/parsed/
enqueued/lastError from the new kick.Discoverer.LastDiagnostic) and Force
discovery hits every running engine. Tray: per-platform status lines +
pause/skip (kick items hidden until its runtime attaches — SetHidden is
verify-live on both OSes), refresh drives all engines. BUGS FIXED on the way:
settings Save / priority-mode change never hot-reloaded the KICK engine (now
ReloadEngines pushes cfg.KickRuntimeConfig()); kick config was never
validated (volume/dim now checked when enabled); the Debugging dump toggle
saved the shared cfg object and could commit unsaved Settings edits (config
is now cloned via loadCfg); watchdog chips lied ("Playback healthy"/"Volume
1%" were hardcoded — now "Playback validated" + real volume %); paused idle
shows Resume instead of Start; preview mode disables action buttons; queue
poll no longer re-renders under an open priority dropdown; config.example.yaml
gained the kick block it was missing.

2026-08-27 live run diagnosed: every queued drop was tallied `needs_action`
("account link required") so selection never left cooldown, even with Cinna's
Special Events campaign live 24/7. Root cause: `RequiresAccountLink()` keyed on
`accountLinkURL != ""`, but Twitch populates accountLinkURL on most campaigns
(often just the game's site). Fixed by capturing `self.isAccountConnected` (the
authoritative signal, per TwitchDropsMiner) from both the dashboard list and the
details query; URL-presence remains only as the fallback when the signal was
never captured. Store gained an `account_connected` column (migration 13,
NULL-preserving upsert), and fetchDetails re-fetches rows missing the signal so
existing DBs heal. Also split the misleading "no active campaigns matched the
game filter" log from the "all matched campaigns already detailed" case.

2026-08-27 second live run: WATCHING reached (grabbys, Once Human) but progress
stayed 0/60. Root cause: mid-watch progress relied on passively captured
fetch/XHR GQL, but Twitch pushes live drop progress over its WebSocket (which
the fetch/XHR shim cannot see); DropCurrentSessionContext only fires on page
load/panel toggle. Fixed with an ACTIVE poll: `twitch.FetchCurrentDrop` (full
dropCurrentSession query via the header-replay page-context fetch, same
technique as details) called from watchTick at most once per
`watcher.progress_poll` (default 60s), merged through the same applyProgress
path as capture. First poll fires on the first health tick (~25s in). UI also
polished: sidebar footer is now two aligned status rows (Chrome / Watcher,
ok/warn/off dots), the "Progress ticking" chip is honest (shows "Awaiting first
tick" until minutes move), and the brand logo is a loot-chest SVG on the purple
tile (replacing the water-drop emoji).

Third live report (screenshot from the 4335cad build, PRE-poll): active card
stuck 0/60 while up-next rows showed 18/120, 18/240 - server-confirmed minutes
from inventory reads between sessions, invisible to the active card because
Status.Current was never refreshed mid-watch. Two follow-ups shipped: the poll
now ALSO fetches the full inventory via page-context GQL (dropCurrentSession
names only ONE drop, which for simultaneous multi-tier campaigns need not be
the tier we queued; the inventory carries per-drop minutes for everything, so
up-next rows stay live too), and beginWatching seeds Status.Current from the
stored baseline. Totals (Minutes watched/Sessions) still only move at session
END - that is by design (sessions table), not a progress bug.

Also shipped on user request: (1) `browser.mute_audio` (Settings > Player >
"Mute Chrome audio", default off, needs app restart) - silences the dedicated
Chrome instance at the sink via --mute-audio while the player element stays
unmuted at 1%, so the page cannot tell; the flags.go design note about muted
background media pausing is unverified, and the frozen-video/stall watchdogs
recover that case if it turns out real. (2) Stall handling now understands
"credited elsewhere": the progress poll records which channel
dropCurrentSession says is being credited; when hard-stall retries are
exhausted AND Twitch credits a DIFFERENT channel (user watching on another
device holds the drop slot), the engine stands down to cooldown WITHOUT
blacklisting the channel and keeps retrying on the normal cadence. The
already-existing soft/hard stall ladder (panel nudge at 6m, page reload at 12m,
capped retries) is the "auto refresh on cooldown" behavior - tune
watcher.soft_stall / hard_stall if a faster reload is wanted now that the poll
confirms stalls within a minute.

(3) `browser.window.start_minimized` (Settings > Player > "Start Chrome
minimized", default off, app restart to apply): minimizes the watcher window
right after launch via CDP Browser.setWindowBounds (windowState minimized) -
targets only the dedicated instance, no macOS accessibility/TCC prompt, never
touches the user's personal Chrome. verify live: whether a minimized window
keeps crediting on macOS - the backgrounding launch flags should keep the
media clock running, and validation/stall watchdogs will surface it if not
(look for validation timeouts right after "watcher window minimized" in the
log; the fallback is un-toggling it).

Minimized-start CONFIRMED WORKING live, with one follow-up found the same run:
channels would not open until the window was manually restored - a minimized
(hidden) window suppresses the player's autoplay, so validation could never
pass. Fixed with engine-managed window state: when start_minimized is on, the
window restores itself entering Opening/Validating (also covers mid-watch hard
refreshes, which re-validate) and re-minimizes in beginWatching once playback
is confirmed. Watch next run: whether playback KEEPS running after the
re-minimize (expected - a started player continues when hidden). Un-minimize
DOES steal focus on macOS - user accepted the tradeoff 2026-08-27 given the
low frequency (per channel switch / proactive refresh, ~2x/hour steady-state).
If ACL-offline churn ever makes restores flappy, the fix is the liveness
pre-check from the open items below, not fighting the focus behavior. Dashboard card also
polished: state pill no longer overlaps the channel name (flex none + default
pill style for unmapped states + busy pulse), transitional states show real
copy (Scanning campaigns... / Validating playback on X...) instead of the idle
"Press Start" text, and Start/Pause/Skip buttons now follow the actual engine
state.

First claim attempt happened live (DECAL: GRAFFITI complete!) and exposed the
claim flow: ClaimVisible hung ~9.5 min in a single click (minimized window,
drops panel never opened), then "left for retry" with a misleading status.
Reworked per user decision (2026-08-27): claiming is ONE bounded attempt
(claimTimeout 60s for the whole state), the window is restored and the drops
panel toggled open before looking for the in-page button, and on failure the
item goes to needs_action ("completed - claim it manually in the Twitch
inventory") with an EvNeedsAction emit - NO automatic retries, engine moves to
the next drop. EligibilityReason now reports complete-but-unclaimed progress
as needs_action instead of falsely "already claimed" (also covers tiers that
finish in the background of simultaneous multi-drop campaigns). Dashboard got
the needs_action banner (was CSS with no renderer): count + first reasons +
"Open queue" button. BACKLOG (user asked, deliberately deferred): claim in a
SECOND TAB so watching is not interrupted, and a claim-all-claimable sweep for
simultaneous multi-tier campaigns (inventory page shows every claim button;
current flow only targets the just-completed drop).

Channel selection upgraded to SET COVERAGE (user design, Twitch Rivals as the
template: "global" drops creditable on any participating channel + team drops
locked to channel subsets). queue.ChannelCoverage counts how many eligible
drops a login can credit (ACL membership, or same-game any-channel campaigns -
the drops-indicator validation stays the hard gate for the any-channel
heuristic), and RankChannels ranks current-channel > coverage > stats >
viewers. Net behavior: pick the channel serving the biggest drop set, and
stickiness holds it there as each drop completes until nothing eligible
remains on it, then move on. "selecting: channel covers multiple eligible
drops" logs when coverage kicked in.

KICK DROPS BUILT 2026-08-28 (phases 0-2 of the plan below; behind
kick.enabled, default OFF, so Twitch behavior is unchanged until flipped).
What shipped: internal/platform owns the engine-facing interfaces (twitch
aliases them); platform column across the store (campaigns/queue/sessions/
claims + rebuilt channel_stats/blacklist with (platform,login) keys); the
engine is per-platform (Deps.Platform/ChannelHost, queue filtered, shared
discovery.ReconcileQueue scoped so parallel discoverers never cross-prune);
internal/kick implements Controller/Pages/Discoverer with VERIFY-LIVE
selectors, campaigns fetched from page context via the unofficial
web.kick.com endpoint (tolerant parser, kick-campaigns.json debug dump),
inventory text dumped for shape discovery; app launches a second Chrome
(chrome-profile-kick, offset window) + second engine when enabled - a kick
launch failure logs and leaves Twitch running; UI has a second Now watching
card with its own controls (/api/kick/*), KICK badges in queue rows, a Kick
footer row, and a Settings toggle. Kick engine runs with gentler watchdogs
(KickRuntimeConfig: no proactive refresh, longer stalls) because Kick's FAQ
says refreshes hurt credit tracking. BACKLOGGED 2026-08-28: no Kick campaigns the user
cares about are running right now, so live verification waits until one
appears. FIRST LIVE RUN PROTOCOL (when a campaign shows up): enable the
toggle, restart, log into Kick in the second window, turn on the GraphQL
dump toggle, and send back ~/.dropwarden/debug/kick-campaigns.json +
kick-inventory.txt - those dumps are what turn the guessed parser/selectors
into verified ones. Do NOT chase Kick issues before those dumps exist. KNOWN GAPS until then: no Kick progress source (stall
aborts are effectively disabled; drops will not complete on Kick yet),
claim/play/offline selectors unverified, Cloudflare behavior unverified.

Original research/design note: user wants Kick support running in PARALLEL with Twitch. Research
done + full design written in docs/NOTES-kick.md (read it before starting):
Kick drops are active and Twitch-shaped (category campaigns, ACL vs
any-channel, multi-tier simultaneous credit, manual claim, link-at-claim-time,
inventory page), community precedent (KickDropsMiner) proves real-Chrome +
persisted profile passes Cloudflare. Plan: Phase 0 platform plumbing (extract
Controller/Health/PlayerPolicy interfaces, `platform` column, N engines over N
browser instances), then internal/kick driver, then UI. Key Kick differences:
refreshes HURT credit tracking (gentler watchdogs, no proactive refresh),
rewards EXPIRE unclaimed, account link needed at claim not selection. NOT
STARTED - implementation is its own work session.

Mature/content-classification gate handled (live find 2026-08-28, screenshot
of naughty_kitteee): the "intended for certain audiences... Start Watching"
overlay blocked playback AND matched the offline-gate selector family, so
gated channels were insta-blacklisted as offline. Fix: the health probe only
reports offlineGate when the overlay has NO accept button, and twitch
ClickPlay (called every validation tick while not playing) clicks the Start
Watching button first (selContentGateAccept, verify-live but standard
data-a-targets). Twitch remembers the ack per channel. If a blacklisted gated
channel should be retried sooner than the 15m TTL, restart or wait it out.

Link-gate doctrine (0229f9b, live find: CREP Emote needs_action with 4/30
server-credited minutes): Twitch's flags cannot distinguish link-to-earn from
link-optional, so server-confirmed progress (RealMinutes>0 or Claimable)
overrides the account-link gate, and skip_account_link_drops is now a real
switch (off = watch everything; failed claims land in manual reconcile).

Chrome-death recovery (live find 2026-08-27 evening): a frozen-video refresh
coincided with the CDP connection dying, and the app spent 90+ minutes
retry-looping navigation on the closed socket - nothing ever relaunched
Chrome. Fix: browser.Managed wraps the session; when an underlying session's
event stream ends (disconnect/crash), it reaps the dead process and re-invokes
the launch closure (full re-init: capture shim, minimize policy) with capped
exponential backoff, forever, then swaps the live session in place - the
engine's normal backoff just starts succeeding again. Both platforms' sessions
are wrapped. During recovery, calls fail fast with 'browser restarting'.
Root cause of the original Chrome death itself is unknown (possibly renderer
crash on reload) - recovery makes it irrelevant.

Still to confirm on the user's Mac:

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

- a1f7151 feat: proactive owned-reward skip + Kick drop-progress debug capture
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
