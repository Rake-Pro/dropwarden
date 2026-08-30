# Multi-profile support - design (2026-08-30)

Goal: ONE app instance managing drops for several people at once. A PROFILE is
one person and bundles their platform logins (one Twitch + one Kick account).
Target deployment is the shared Linux thin client (N100 class), but the design
is platform-neutral and must keep working single-profile on the Mac/Windows dev
machines. Decisions below were locked with the owner on 2026-08-30: in-app
multi-profile (not instance-per-person), unified dashboard, standalone Wails
app (GTK3 on Linux).

## Model

- Profile = person. Each profile owns: its own SQLite DB (queue, campaigns,
  progress, sessions, claims, channel stats), its own Chrome profile dirs (one
  per platform), and its own engine pair (Twitch always; Kick when enabled for
  that profile). Nothing is shared between profiles except the process, the
  config file, the log, and the UI.
- Per-profile DB (NOT a profile column) is the load-bearing choice: the store,
  queue, selection, stats, and migration code stay byte-for-byte untouched.
  Campaign-catalog duplication across profiles is trivial cost.
- Up to N x 2 engines / Chrome windows. Sizing note for the thin client:
  2 profiles x 2 platforms = 4 Chromes at 160p ~= 2.5-3 GB RAM, light CPU.

## Directory layout + legacy migration (zero file moves)

```
~/.dropwarden/
  config.yaml            # single root config, includes the profiles block
  dropwarden.log         # shared log; every line tagged profile= platform=
  dropwarden.db          #   } legacy single-profile data: becomes the
  chrome-profile/        #   } implicit first profile IN PLACE
  chrome-profile-kick/   #   }
  debug/                 # per-profile subdirs for new profiles (see below)
  profiles/<name>/       # every ADDED profile lives here
    dropwarden.db
    chrome-profile/
    chrome-profile-kick/
    debug/
```

- Config with NO `profiles:` block = exactly one synthesized profile named
  `main` whose data dir IS the app root. Existing installs upgrade with zero
  file moves and zero behavior change; `main`'s explicit `dir: .` is written
  into the profiles block on first save.
- New profiles get `profiles/<name>/` (name = lowercase slug, validated
  `[a-z0-9-]{1,24}`). `config.ProfilePaths(name)` resolves db/chrome/debug
  paths from the profile's dir; all existing `config.DBPath()`-style helpers
  become thin wrappers over the `main` profile during the transition.

## Config shape (single root file, per-profile overlay)

```yaml
profiles:
  - name: greg
    dir: .                 # legacy-rooted; omitted => profiles/<name>/
    enabled: true
    display_name: Greg     # optional; UI label, defaults to name
    games: [...]           # optional; omitted => inherit root games/any_game
    any_game: false
    kick:
      enabled: true        # per-profile; root kick block stays the DEFAULTS
    window_slot: 0         # optional; tiling index, defaults to list position
```

- Root sections keep their current meaning but become DEFAULTS: `games`,
  `queue`, `watcher`, `discovery`, `claim`, `browser`, `kick`, `auth`,
  `logging`, `ui`, `theme` apply to every profile unless the profile overrides
  the few overridable fields above. Deliberately NOT per-profile in v1:
  player policy, watchdog cadence, claim strategy, theme (household-shared
  box, shared posture).
- Effective config per (profile, platform) = root -> profile overlay ->
  `KickRuntimeConfig()` derivation (unchanged, including the 2026-08-30
  inheritance fixes). Validation walks every enabled profile the same way
  kick is validated today.
- Window tiling: default geometry per engine = slot grid, x = 40 + col*660,
  y = 40 + row*420 (col = platform index, row = window_slot), overridable via
  the existing browser.window/kick.browser.window blocks per profile only if
  someone asks - v1 exposes just the slot.

## Runtime (internal/app)

```go
type ProfileRuntime struct {
    Name        string
    Store       *store.Store          // opened eagerly (UI reads pre-runtime)
    Twitch, Kick *PlatformRuntime     // Kick nil unless enabled for profile
}
type PlatformRuntime struct {
    Session browser.Session           // browser.Managed-wrapped
    Engine  *watcher.Engine
    Disco   watcher.Discoverer        // *discovery.Discoverer | *kick.Discoverer
}
App.Profiles []*ProfileRuntime        // replaces Engine/KickEngine/... fields
```

- `Open()` opens every enabled profile's store eagerly. `StartRuntime` loops
  profiles; each profile's launch failure logs and SKIPS that profile (same
  additive posture as kick-launch-failure today) - one broken profile must
  never take down the others.
- Engine identity: `watcher.Deps` gains nothing new; the engine already
  carries Platform. Logs get `.Str("profile", name)` via the per-profile
  zerolog logger (same pattern as the kick logger today). `Status` gains a
  `Profile string` field, set in `watcher.New` alongside Platform, preserved
  by the cooldown reset (add to the 2026-08-30 preserve list).
- Discovery pacing: engines already jitter independently; additionally seed
  each engine's first discovery with `index * 90s` stagger so N profiles do
  not burst GQL/API fetches simultaneously at boot.

## Service / API

Backend refactor: `Backend.Profiles map[string]*ProfileBackend` (store +
engine/disco handles + startupKick flag per profile), plus `Order []string`.
`EngineFor(profile, platform)`; `ReloadEngines` loops all.

- `GET /api/state` returns everything in one round trip (2-3 profiles scale):

```json
{
  "config": {...},
  "profiles": [
    { "name": "greg", "displayName": "Greg", "enabled": true,
      "platforms": [ {"platform":"twitch","enabled":true,"up":true,
                      "pendingRestart":false,"status":{...}}, ... ],
      "queue": [...], "claims": [...], "stats": {...}, "spark": [...] }
  ]
}
```

- Engine actions: `POST /api/engine/{profile}/{platform}/{action}` (start/
  stop/skip/refresh). Unknown profile/platform/action -> 400 with the same
  honest disabled-vs-not-running messages, now per profile.
- Queue actions gain `profile` in the body (`/api/queue/order|enabled|
  priority`); priority mode stays a root (shared) setting in v1.
- `POST /api/profiles` (add) and `POST /api/profiles/enabled` (enable/
  disable, effective on restart like kick.enabled, same pendingRestart
  honesty via per-profile startup snapshot). NO delete/rename in v1 -
  documented as manual file ops with the app stopped.
- `GET /api/debug` -> `profiles: {<name>: {twitch: {...}, kick: {...}}}` +
  shared logTail/paths (paths listed per profile).

## UI (unified dashboard - locked decision)

- Dashboard: one section per enabled profile - person header (display name +
  per-profile needy banner) with their platform cards side by side; then the
  shared side column (Up next + Totals) gains per-profile grouping: up-next
  rows badge profile when >1 profile, Totals shows per-profile rows (reuse
  the existing byPlatform row pattern, keyed by profile).
- Profile selector: pill bar rendered in the Queue / History / Settings
  headers when >1 profile; selection is a module-level var defaulting to the
  first profile (no persistence needed in v1 - it is a shared screen).
- Queue/History: current views unchanged, fed from the selected profile's
  block. Platform badges keep their existing multi-platform rule, evaluated
  per profile.
- Settings: new "Profiles" pane (list profiles: display name, enabled toggle
  with pendingRestart copy, per-profile Kick toggle, per-profile games
  override with an "inherit shared list" switch, Add-profile row). The
  existing Platforms pane keeps the SHARED claim/kick defaults. Everything
  else stays shared.
- Footer: one row per (profile, platform): "Greg - Twitch running". With one
  profile the label collapses to today's platform-only text.
- Preview SAMPLE data: two profiles so the unified layout is designable
  offline.

## Tray

Generalize `trayPlatform` -> `trayEngine{profile, platform}`: build items for
every configured (profile, platform) pair at startup, `SetHidden` tracking
engine presence each tick exactly like the 2026-08-30 kick pattern. Labels:
"Pause Greg's Twitch watching" etc.; with one profile, labels collapse to the
current platform-only wording. Tooltip: one segment per running engine.

## Explicitly out of scope (v1)

- Profile delete/rename in-app; per-profile theme/player/watchdog tuning;
  any auth between profiles (household-trusted UI); store merges between
  layouts; per-profile priority mode.
- OPEN QUESTION (owner, undecided): optional `--listen <addr>` serve mode
  exposing the same mux over plain HTTP alongside the Wails window, so a
  profile's owner can manage their queue from their own device via
  gpm+Authentik. Without it, remote management = RustDesk to the box. The
  mux already serves both roles; this is a ~20-line flag if wanted.

## Phases

- Phase 0 - config + paths: profiles block, legacy `main` synthesis,
  ProfilePaths, per-profile validation. No behavior change single-profile.
  DONE (2026-08-30, code in working tree, uncommitted).
- Phase 1 - N runtimes + API: App.Profiles, ProfileBackend map, profile-
  dimensioned routes/state/debug, Status.Profile, tray generalization.
  Single-profile UI keeps working against profiles[0]. DONE (2026-08-30, code
  in working tree, uncommitted).
- Phase 2 - UI: unified dashboard sections, profile selector, Profiles
  settings pane, footer/tray labels, SAMPLE data. DONE (2026-08-30, code in
  working tree, uncommitted).
- Phase 3 - polish + live: window tiling defaults, discovery stagger, Mac
  test pass (two profiles on the Mac = the full thin-client shape minus GTK),
  docs/STATUS update. CODE DONE (2026-08-31, working tree, uncommitted): both
  window tiling and the 90s discovery stagger are wired and unit-tested (see
  STATUS.md). Mac test pass is the one item still PENDING - it needs a real
  display and is not doable in-container.

## Risks / notes

- 4 Chromes is the steady-state budget; engines already tolerate slow starts,
  but first-boot login for a NEW profile needs its windows findable -
  Add-profile flow should surface "log in to Twitch/Kick in the new windows"
  copy (reuse kick first-run copy).
- Shared config file + per-profile saves: the 2026-08-30 save-on-copy +
  validate-before-commit pattern already covers concurrent-ish edits; keep
  every profile mutation flowing through the same SettingsService path.
- Wails GTK runtime on Linux remains verify-live until the thin client
  exists; nothing in this design depends on it (all testable on macOS).
