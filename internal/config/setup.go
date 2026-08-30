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
// defaults (volume 0.01, GQL claim off, manual auth).
const StarterConfig = `# dropwarden configuration.
# App root: ~/.dropwarden (macOS/Linux) or %USERPROFILE%\dropwarden (Windows).

theme: dark            # dark | light | system  (dashboard UI)

# Campaigns for games outside this list are cached but never queued.
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
  max_select_attempts: 6        # candidates tried per pass when falling through

claim:
  strategy: in_page            # in_page | in_page_then_gql
  allow_gql_fallback: false    # OFF: replaying ClaimDropRewards has triggered "BAD BOT!" blocks
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
    x: 40
    y: 40
    monitor: 0
    always_on_bottom: true
    never_focus: true
    start_minimized: false     # minimize the watcher Chrome after launch (progress watchdogs still verify playback)
  burnin:
    dim_opacity: 0.55          # 0 disables the overlay
    shift_interval: 20m
    shift_pixels: 8
  player:
    volume: 0.01               # NOT 0 - Chrome 145 pauses muted background video
    force_lowest_quality: true
    reassert_interval: 30s

kick:
  enabled: false               # parallel Kick drops runtime (its own Chrome instance + engine)
  browser:
    profile_dir: ""            # blank = <approot>/chrome-profile-kick (never shared with Twitch)
    mute_audio: false
    window:
      width: 640
      height: 400
      x: 700                   # offset so the two watcher windows do not stack
      y: 40
      always_on_bottom: true
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
  start_minimized: true
  show_debug_pane: false
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
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "chrome-profile"), nil
}

// KickProfileDir resolves the Kick instance's Chrome profile directory.
func (c *Config) KickProfileDir() (string, error) {
	if c.Kick.Browser.ProfileDir != "" {
		return ExpandPath(c.Kick.Browser.ProfileDir), nil
	}
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "chrome-profile-kick"), nil
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
