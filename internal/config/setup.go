package config

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

// StarterConfig is written on first run so the app never fails merely for being
// unconfigured. It documents every knob and encodes the safety-critical
// defaults (volume 0.01, manual auth).
const StarterConfig = `# dropwarden configuration.
# App root: ~/.dropwarden (macOS/Linux) or %USERPROFILE%\dropwarden (Windows).

theme: dark            # dark | light | system  (dashboard UI)

# Campaigns for games outside this list are cached but never queued. The list
# order is the game priority: earlier games are watched first.
games:
  - Rust
  - Halo Infinite
  - Halo Campaign Evolved
  - "Halo: The Master Chief Collection"
  - Once Human
any_game: false        # future: queue every drops-enabled campaign

queue:
  priority_mode: top_down   # top_down | ending_soonest | closest_to_complete | shortest_first
  auto_enqueue_new: true    # new campaigns for listed games join the bottom of the queue
  # Within a game, watch channel-allowlisted campaigns before any-channel ones:
  # an allowlisted streamer also credits the game's any-channel drops, so those
  # only get their own watch when no allowlisted streamer of the game is live.
  # Games are prioritized in the order of the games list above; pinned drops
  # (Queue view star) outrank everything.
  prefer_channel_specific: true

discovery:
  interval: 45m
  jitter: 30m
  details_per_cycle: 8      # cap on CampaignDetails expansions per cycle
  directory_poll: 5m
  directory_jitter: 3m

watcher:
  enabled_on_start: true
  stream_up_delay: 90s          # ignore channels that just went live
  validate_timeout: 60s
  soft_stall: 6m
  hard_stall: 12m
  hard_stall_max_retries: 2
  proactive_refresh: 30m
  proactive_refresh_jitter: 5m
  health_tick: 25s
  health_tick_jitter: 40        # percent
  progress_poll: 60s            # active current-drop GQL poll while watching
  max_session: 4h
  cooldown: 60s
  cooldown_jitter: 120s
  channel_blacklist_ttl: 45m
  offline_blacklist_ttl: 15m
  wait_for_top: false           # false = fall through to the next available drop
  max_select_attempts: 6        # any-channel directory scrapes per pass; allowlists always checked
  timing_jitter: 0.2            # +/- fraction on periodic watcher delays (bot-detection avoidance: desynchronizes periodic actions); 0 disables, max 0.5

claim:
  # Claiming: the DropsPage_ClaimDropRewards GraphQL mutation (sent from the
  # page with its own auth/integrity headers) is the primary path, verified by
  # re-reading the inventory. If it fails, the tab opens the inventory page,
  # writes debug/inventory-buttons.json and clicks the first element matching
  # inventory_button_selector whose label text is inventory_button_text.
  sweep: true                  # also claim every completed, unclaimed drop after discovery / progress polls (max 10 per pass)
  # inventory_button_selector: 'button:has([data-a-target="tw-core-button-label-text"])'   # default; "" = capture only, no click
  inventory_button_text: "Claim Now"   # case-insensitive, whole trimmed label
  strategy: in_page            # legacy, not read by the claim flow
  allow_gql_fallback: false    # legacy, ignored since v0.1.4 (the mutation is now the primary path)
  retry_backoff: 5m
  skip_account_link_drops: true

browser:
  chrome_path: ""              # blank = auto-detect; override for Chrome Beta/Dev or odd installs
  profile_dir: ""              # blank = <approot>/chrome-profile
  mute_audio: false            # silence Chrome at the sink; player stays unmuted so crediting is unaffected
  extra_flags: []
  window:
    width: 640
    height: 400
    # x/y omitted: computed from platform + profile window_slot (window
    # tiling default); set both explicitly here to override for every profile.
    monitor: 0
    always_on_bottom: true     # Windows only: keep the watcher Chrome at the bottom of the z-order (covered but visible, never raised); no effect on macOS/Linux
    never_focus: true
    start_minimized: false     # minimize the watcher Chrome after launch (progress watchdogs still verify playback)
    minimize_while_watching: false  # keep it false: a minimized tab reads visibilityState "hidden" and Twitch stops crediting drops (cover the window instead of minimizing it)
  burnin:
    dim_opacity: 0.55          # 0 disables the overlay
    shift_interval: 20m
    shift_pixels: 8
  player:
    volume: 0.01               # NOT 0 - Chrome 145 pauses muted background video
    force_lowest_quality: true
    reassert_interval: 30s

kick:
  enabled: false               # parallel Kick drops runtime (its own engine)
  browser:
    # separate_window false (default) = SHARED WINDOW: Kick runs as a TAB in
    # this profile's Twitch Chrome window, so one profile costs ONE Chrome
    # instead of two. The profile_dir and window settings below are then
    # UNUSED (the shared window is the Twitch one), and Kick must be logged in
    # once in that window - an existing chrome-profile-kick login does NOT
    # carry over. true restores the legacy layout: a second Chrome instance
    # with its own profile dir and window.
    separate_window: false
    profile_dir: ""            # separate_window only; blank = <approot>/chrome-profile-kick
    mute_audio: false
    window:                    # separate_window only
      width: 640
      height: 400
      # x/y omitted: computed from platform + profile window_slot, offset so
      # the two watcher windows do not stack (window tiling default).
      # always_on_bottom (Windows only) is inherited from browser.window above
      # so the settings toggle stays truthful; set it here only to force it on.
      never_focus: true
      start_minimized: false

auth:
  mode: manual                 # manual | stored
  # stored mode is implemented but unsupported and OFF by default; it types
  # credentials into the real login form, which is exactly the behavior that
  # has produced login blocks and 24h lockouts for other drop bots.
  stored:
    enabled: false
    username: ""
    password_file: ""          # 0600 file; never inline

logging:
  level: info                  # trace | debug | info | warn | error
  file: true
  max_size_mb: 5
  capture_gql_bodies: false    # debug only: writes raw GQL JSON next to the log

ui:
  start_minimized: false   # true = start hidden in the tray (the dashboard window can always be opened from the tray icon)
  show_debug_pane: false

update:
  enabled: true                # background checks: ~30s after start, then every interval_hours (dev builds never auto-check)
  auto_install: true           # install a found update right away, or as soon as no watch is in progress
  show_changelog: true         # offer "What's new" release notes on the dashboard banner
  feed_url: "https://cdn.rake.pro/releases/dropwarden/latest.json"   # release manifest; "" disables update checks
  interval_hours: 24
`

// dirName is the app root folder name: dotted on unix/macOS, undotted on
// Windows (dot-prefixed folders are alien there).
func dirName() string {
	if runtime.GOOS == "windows" {
		return "dropwarden"
	}
	return ".dropwarden"
}

// Dir returns the app's per-user root. DROPWARDEN_HOME overrides it (useful for
// tests and side-by-side profiles).
func Dir() (string, error) {
	if v := os.Getenv("DROPWARDEN_HOME"); v != "" {
		return ExpandPath(v), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("config: locate home dir: %w", err)
	}
	return filepath.Join(home, dirName()), nil
}

// DefaultConfigPath returns the canonical writable config location, honoring the
// DROPWARDEN_CONFIG override.
func DefaultConfigPath() (string, error) {
	if v := os.Getenv("DROPWARDEN_CONFIG"); v != "" {
		return ExpandPath(v), nil
	}
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.yaml"), nil
}

// DBPath returns the SQLite database path under the app root.
func DBPath() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "dropwarden.db"), nil
}

// LogPath returns the log file path under the app root.
func LogPath() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "dropwarden.log"), nil
}

// ProfileDir returns the dedicated Chrome user-data-dir, honoring an override.
func (c *Config) ProfileDir() (string, error) {
	if c.Browser.ProfileDir != "" {
		return ExpandPath(c.Browser.ProfileDir), nil
	}
	pp, err := c.ProfilePaths(MainProfileName)
	if err != nil {
		return "", err
	}
	return pp.ChromeProfile, nil
}

// KickProfileDir resolves the Kick instance's Chrome profile directory.
func (c *Config) KickProfileDir() (string, error) {
	if c.Kick.Browser.ProfileDir != "" {
		return ExpandPath(c.Kick.Browser.ProfileDir), nil
	}
	pp, err := c.ProfilePaths(MainProfileName)
	if err != nil {
		return "", err
	}
	return pp.ChromeProfileKick, nil
}

// ProfilePaths holds the on-disk paths for one profile, resolved from its
// dir. Existing single-profile helpers (ProfileDir, KickProfileDir, DBPath,
// LogPath) are thin wrappers over MainProfileName's paths.
type ProfilePaths struct {
	// Dir is the profile's data directory: the parent of everything below, and
	// where per-platform first-run markers live.
	Dir               string
	DB                string
	ChromeProfile     string
	ChromeProfileKick string
	Debug             string
}

// ProfilePaths resolves the named profile's db, chrome-profile,
// chrome-profile-kick, and debug paths from its dir.
func (c *Config) ProfilePaths(name string) (ProfilePaths, error) {
	p := c.Profile(name)
	if p == nil {
		return ProfilePaths{}, fmt.Errorf("config: unknown profile %q", name)
	}
	base, err := c.profileBaseDir(p)
	if err != nil {
		return ProfilePaths{}, err
	}
	return ProfilePaths{
		Dir:               base,
		DB:                filepath.Join(base, "dropwarden.db"),
		ChromeProfile:     filepath.Join(base, "chrome-profile"),
		ChromeProfileKick: filepath.Join(base, "chrome-profile-kick"),
		Debug:             filepath.Join(base, "debug"),
	}, nil
}

// profileBaseDir resolves a profile's data directory: "." roots it at the app
// dir itself, "" (omitted) roots it at profiles/<name>/ under the app dir,
// and anything else is resolved relative to the app dir (or used as-is if
// absolute).
func (c *Config) profileBaseDir(p *ProfileConfig) (string, error) {
	root, err := Dir()
	if err != nil {
		return "", err
	}
	switch p.Dir {
	case ".":
		return root, nil
	case "":
		return filepath.Join(root, "profiles", p.Name), nil
	default:
		d := ExpandPath(p.Dir)
		if filepath.IsAbs(d) {
			return d, nil
		}
		return filepath.Join(root, d), nil
	}
}

// ProfileEffective is a profile's overlay resolved onto the root config: the
// fields Phase 1 runtimes and API/UI code consume per profile.
type ProfileEffective struct {
	Name        string
	DisplayName string
	Enabled     bool
	Games       []string
	AnyGame     bool
	KickEnabled bool
	WindowSlot  int
}

// Effective resolves the named profile's overlay onto the root config.
func (c *Config) Effective(name string) (*ProfileEffective, error) {
	idx := -1
	for i := range c.Profiles {
		if c.Profiles[i].Name == name {
			idx = i
			break
		}
	}
	if idx == -1 {
		return nil, fmt.Errorf("config: unknown profile %q", name)
	}
	p := &c.Profiles[idx]
	games, anyGame := c.effectiveGames(p)
	return &ProfileEffective{
		Name:        p.Name,
		DisplayName: p.effectiveDisplayName(),
		Enabled:     p.Enabled,
		Games:       games,
		AnyGame:     anyGame,
		KickEnabled: c.effectiveKickEnabled(p),
		WindowSlot:  c.effectiveWindowSlot(idx, p),
	}, nil
}

// effectiveGames resolves a profile's games filter: omitted (no games, no
// any_game override) inherits the root filter wholesale; otherwise the
// profile's own games/any_game apply.
func (c *Config) effectiveGames(p *ProfileConfig) ([]string, bool) {
	if len(p.Games) == 0 && p.AnyGame == nil {
		return c.Games, c.AnyGame
	}
	anyGame := false
	if p.AnyGame != nil {
		anyGame = *p.AnyGame
	}
	return p.Games, anyGame
}

// effectiveDisplayName defaults to the profile's Name.
func (p *ProfileConfig) effectiveDisplayName() string {
	if p.DisplayName != "" {
		return p.DisplayName
	}
	return p.Name
}

// effectiveKickEnabled resolves a profile's Kick override, defaulting to the
// root kick.enabled.
func (c *Config) effectiveKickEnabled(p *ProfileConfig) bool {
	if p.Kick.Enabled != nil {
		return *p.Kick.Enabled
	}
	return c.Kick.Enabled
}

// effectiveWindowSlot defaults to the profile's position in the Profiles
// list.
func (c *Config) effectiveWindowSlot(idx int, p *ProfileConfig) int {
	if p.WindowSlot != nil {
		return *p.WindowSlot
	}
	return idx
}

// windowTileX and windowTileY are the window-tiling grid step: 660px wide
// (clears the 640px window width plus a margin) and 420px tall (clears the
// 400px window height plus a margin), so adjacent slots never overlap.
const (
	windowTileOriginX = 40
	windowTileOriginY = 40
	windowTileStepX   = 660
	windowTileStepY   = 420
)

// windowTileDefault computes an engine's default window position from its
// slot in the grid: col is the platform index (twitch 0, kick 1), row is the
// profile's window_slot. Slot (0,0) reproduces today's single-profile Twitch
// default (40,40); col 1 row 0 reproduces today's single-profile Kick default
// (700,40).
func windowTileDefault(col, row int) (x, y int) {
	return windowTileOriginX + col*windowTileStepX, windowTileOriginY + row*windowTileStepY
}

// applyWindowTileDefault fills w.X/w.Y from the slot grid, but only when
// neither has been set explicitly (both zero); an explicit x or y in the
// config always wins over the computed default.
func applyWindowTileDefault(w *WindowConfig, col, row int) {
	if w.X != 0 || w.Y != 0 {
		return
	}
	w.X, w.Y = windowTileDefault(col, row)
}

// ProfileRuntimeConfig builds the config the named profile's engines run with: a copy
// of the root config with the profile's games overlay applied, and the
// window-tiling default filled in for the Twitch window (col 0) when the
// config left it unset.
func (c *Config) ProfileRuntimeConfig(name string) (*Config, error) {
	eff, err := c.Effective(name)
	if err != nil {
		return nil, err
	}
	cp := *c
	cp.Games = eff.Games
	cp.AnyGame = eff.AnyGame
	applyWindowTileDefault(&cp.Browser.Window, 0, eff.WindowSlot)
	return &cp, nil
}

// ProfileKickRuntimeConfig builds the config the named profile's Kick engine
// runs with: the profile's games overlay applied to a copy of the root
// config, then the same KickRuntimeConfig derivation used for the shared
// (single-profile) case, with the window-tiling default filled in when the
// config left it unset.
//
// Geometry depends on the window layout. In the default SHARED layout Kick is a
// tab in the profile's Twitch window, so there is no second window to place: the
// slot stays the profile's own (col 0), matching the Twitch config, and the
// kick.browser.window block is unused. In separate_window mode Kick keeps its
// own window at col 1, beside the Twitch one.
func (c *Config) ProfileKickRuntimeConfig(name string) (*Config, error) {
	eff, err := c.Effective(name)
	if err != nil {
		return nil, err
	}
	cp, err := c.ProfileRuntimeConfig(name)
	if err != nil {
		return nil, err
	}
	kc := cp.KickRuntimeConfig()
	col := 0
	if c.KickSeparateWindow() {
		col = 1
	}
	applyWindowTileDefault(&kc.Browser.Window, col, eff.WindowSlot)
	return kc, nil
}

// KickRuntimeConfig builds the config the KICK engine runs with: a copy of the
// shared config with Browser swapped for the Kick instance's and the watchdog
// cadence retuned. Kick's own FAQ warns that frequent refreshes hurt watch
// credit, so stalls get longer fuses and the proactive refresh is disabled.
func (c *Config) KickRuntimeConfig() *Config {
	kc := *c
	kc.Browser = c.Kick.Browser
	// The settings UI edits only the ROOT browser block, so shared behavior
	// (player policy, mute, minimize/focus flags, burn-in, Chrome binary)
	// inherits from it; the kick block contributes geometry/profile overrides.
	// Bool flags OR so the kick block can force one on but the UI stays truthful.
	if kc.Browser.Player.Volume == 0 {
		kc.Browser.Player = c.Browser.Player
	}
	if kc.Browser.ChromePath == "" {
		kc.Browser.ChromePath = c.Browser.ChromePath
	}
	if kc.Browser.Burnin == (BurninConfig{}) {
		kc.Browser.Burnin = c.Browser.Burnin
	}
	kc.Browser.MuteAudio = kc.Browser.MuteAudio || c.Browser.MuteAudio
	kc.Browser.Window.NeverFocus = kc.Browser.Window.NeverFocus || c.Browser.Window.NeverFocus
	kc.Browser.Window.StartMinimized = kc.Browser.Window.StartMinimized || c.Browser.Window.StartMinimized
	kc.Browser.Window.AlwaysOnBottom = kc.Browser.Window.AlwaysOnBottom || c.Browser.Window.AlwaysOnBottom
	if kc.Watcher.SoftStall < 10*time.Minute {
		kc.Watcher.SoftStall = 10 * time.Minute
	}
	if kc.Watcher.HardStall < 20*time.Minute {
		kc.Watcher.HardStall = 20 * time.Minute
	}
	kc.Watcher.ProactiveRefresh = 0 // 0 = disabled (see watch.go guard)
	return &kc
}

// StarterDefaults parses the starter template into a Config: the canonical
// factory-defaults object used by the settings "reset" action.
func StarterDefaults() (*Config, error) {
	return parse([]byte(StarterConfig))
}

// WriteStarter creates the app dir and writes StarterConfig to configPath if it
// does not already exist. Existing files are never overwritten.
func WriteStarter(configPath string) error {
	dir := filepath.Dir(configPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("config: create %s: %w", dir, err)
	}
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		if err := os.WriteFile(configPath, []byte(StarterConfig), 0o600); err != nil {
			return fmt.Errorf("config: write starter %s: %w", configPath, err)
		}
	}
	return nil
}

// LoadOrInit loads the config at its default path, writing the starter template
// first if nothing is there yet.
func LoadOrInit() (*Config, string, error) {
	path, err := DefaultConfigPath()
	if err != nil {
		return nil, "", err
	}
	if err := WriteStarter(path); err != nil {
		return nil, "", err
	}
	cfg, err := Load(path)
	if err != nil {
		return nil, path, err
	}
	return cfg, path, nil
}
