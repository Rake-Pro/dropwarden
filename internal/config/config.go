// Package config defines dropwarden's on-disk YAML configuration, its defaults,
// and validation. The load path mirrors GoShareIt: read -> applyDefaults ->
// validate, with an atomic validate-into-temp-then-rename save.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// Config is the root configuration document.
type Config struct {
	Theme     string          `yaml:"theme"`    // dark|light|system (dashboard UI)
	Games     []string        `yaml:"games"`    // campaigns for these games are queued
	AnyGame   bool            `yaml:"any_game"` // future: queue every drops-enabled campaign
	Queue     QueueConfig     `yaml:"queue"`
	Discovery DiscoveryConfig `yaml:"discovery"`
	Watcher   WatcherConfig   `yaml:"watcher"`
	Claim     ClaimConfig     `yaml:"claim"`
	Browser   BrowserConfig   `yaml:"browser"`
	Kick      KickConfig      `yaml:"kick"`
	Auth      AuthConfig      `yaml:"auth"`
	Logging   LoggingConfig   `yaml:"logging"`
	UI        UIConfig        `yaml:"ui"`
	Update    UpdateConfig    `yaml:"update"`

	// Profiles lists the people this instance manages drops for. A config with
	// no profiles block synthesizes exactly one profile (MainProfileName)
	// rooted at the app dir itself, so existing single-profile installs need
	// zero file moves; see applyDefaults.
	Profiles []ProfileConfig `yaml:"profiles,omitempty"`
}

// MainProfileName is the legacy single-profile identity: synthesized when the
// config has no profiles block, rooted at the app dir itself (dir ".").
const MainProfileName = "main"

// ProfileConfig is one person's overlay onto the root config: their own data
// directory plus the few fields that can differ per profile. Every other
// section of Config (queue, watcher, claim, browser, kick defaults, auth,
// logging, theme) is shared across all profiles.
type ProfileConfig struct {
	Name string `yaml:"name"`

	// Dir is the profile's data directory. "" (omitted) resolves to
	// profiles/<name>/ under the app root; "." roots the profile at the app
	// dir itself (used by the synthesized main profile); anything else is
	// resolved relative to the app root, or used as-is if absolute.
	Dir string `yaml:"dir,omitempty"`

	Enabled     bool     `yaml:"enabled"`
	DisplayName string   `yaml:"display_name,omitempty"` // optional; UI label, defaults to Name
	Games       []string `yaml:"games,omitempty"`        // optional; omitted => inherit root games/any_game
	AnyGame     *bool    `yaml:"any_game,omitempty"`     // optional; nil => inherit root any_game

	Kick ProfileKickConfig `yaml:"kick,omitempty"`

	// WindowSlot is the tiling row index for this profile's engine windows.
	// nil (omitted) defaults to the profile's position in the Profiles list.
	WindowSlot *int `yaml:"window_slot,omitempty"`
}

// ProfileKickConfig is a profile's override of the shared kick block. The
// root kick block (browser, geometry) stays the defaults for every profile;
// only whether Kick runs for this profile is overridable.
type ProfileKickConfig struct {
	Enabled *bool `yaml:"enabled,omitempty"` // nil => inherit root kick.enabled
}

// Profile returns the named profile, or nil if it does not exist.
func (c *Config) Profile(name string) *ProfileConfig {
	for i := range c.Profiles {
		if c.Profiles[i].Name == name {
			return &c.Profiles[i]
		}
	}
	return nil
}

// KickConfig enables the parallel Kick runtime: its own Chrome instance and
// watcher engine over the shared store/queue. The game filter is shared with
// the top-level Games/AnyGame settings.
type KickConfig struct {
	Enabled bool `yaml:"enabled"`

	// Browser configures Kick's dedicated Chrome instance. A blank profile_dir
	// resolves to <approot>/chrome-profile-kick so the Twitch and Kick
	// sessions never share cookies or a profile lock.
	Browser BrowserConfig `yaml:"browser"`
}

type QueueConfig struct {
	PriorityMode   string `yaml:"priority_mode"` // top_down|ending_soonest|closest_to_complete|shortest_first
	AutoEnqueueNew bool   `yaml:"auto_enqueue_new"`

	// PreferChannelSpecific ranks, within one game, the drops of campaigns with
	// a channel allowlist ahead of any-channel drops that the same streamers
	// also credit. nil => true; read through Config.PreferChannelSpecific.
	PreferChannelSpecific *bool `yaml:"prefer_channel_specific,omitempty"`
}

// PreferChannelSpecific reports whether selection ranks channel-specific
// drops of a game ahead of that game's any-channel drops (default true).
func (c *Config) PreferChannelSpecific() bool { return boolOr(c.Queue.PreferChannelSpecific, true) }

type DiscoveryConfig struct {
	Interval        time.Duration `yaml:"interval"`
	Jitter          time.Duration `yaml:"jitter"`
	DetailsPerCycle int           `yaml:"details_per_cycle"`
	DirectoryPoll   time.Duration `yaml:"directory_poll"`
	DirectoryJitter time.Duration `yaml:"directory_jitter"`
}

type WatcherConfig struct {
	EnabledOnStart      bool          `yaml:"enabled_on_start"`
	StreamUpDelay       time.Duration `yaml:"stream_up_delay"`
	ValidateTimeout     time.Duration `yaml:"validate_timeout"`
	SoftStall           time.Duration `yaml:"soft_stall"`
	HardStall           time.Duration `yaml:"hard_stall"`
	HardStallMaxRetries int           `yaml:"hard_stall_max_retries"`
	ProactiveRefresh    time.Duration `yaml:"proactive_refresh"`
	ProactiveJitter     time.Duration `yaml:"proactive_refresh_jitter"`
	HealthTick          time.Duration `yaml:"health_tick"`
	HealthTickJitter    int           `yaml:"health_tick_jitter"` // percent
	ProgressPoll        time.Duration `yaml:"progress_poll"`
	MaxSession          time.Duration `yaml:"max_session"`
	Cooldown            time.Duration `yaml:"cooldown"`
	CooldownJitter      time.Duration `yaml:"cooldown_jitter"`
	ChannelBlacklistTTL time.Duration `yaml:"channel_blacklist_ttl"`
	OfflineBlacklistTTL time.Duration `yaml:"offline_blacklist_ttl"`

	// WaitForTop, when true, makes selection wait on the single highest-priority
	// drop even if no channel is available for it. The default (false) falls
	// through to the next available drop in priority order, and returns to the
	// top one as soon as a channel for it is available again.
	WaitForTop bool `yaml:"wait_for_top"`

	// MaxSelectAttempts caps how many any-channel candidate drops selection
	// will try per pass when falling through (bounds directory navigations).
	// Allowlist campaigns cost one status query each and are always checked.
	// 0 = default.
	MaxSelectAttempts int `yaml:"max_select_attempts"`

	// TimingJitter desynchronizes periodic watcher actions for bot-detection
	// avoidance: a covered delay D becomes D * (1 + timing_jitter * (2*r-1)),
	// r in [0,1), i.e. +/- that fraction. 0 disables it (exact legacy
	// intervals); valid range is [0, 0.5]. Unlike the other *Jitter fields
	// above (each already covering one specific site), this one is a single
	// knob applied by Engine.fuzzed at every site listed there. Not defaulted
	// here (0 is a legitimate explicit value, unlike the zero-means-unset
	// helpers below): the starter template writes the default 0.2 explicitly,
	// so an existing config predating this field reads as 0 = untouched.
	TimingJitter float64 `yaml:"timing_jitter"`
}

type ClaimConfig struct {
	Strategy         string        `yaml:"strategy"`           // legacy: in_page|in_page_then_gql, no longer read by the claim flow
	AllowGQLFallback bool          `yaml:"allow_gql_fallback"` // legacy: ignored since v0.1.4, the claim mutation is the primary path
	RetryBackoff     time.Duration `yaml:"retry_backoff"`
	SkipAccountLink  bool          `yaml:"skip_account_link_drops"`

	// Sweep claims every completed, unclaimed drop after each discovery pass
	// and each progress poll that sees one complete (nil => true). Read via
	// ClaimSweep.
	Sweep *bool `yaml:"sweep,omitempty"`

	// InventoryButtonSelector / InventoryButtonText drive the DOM claim
	// fallback on the inventory page: the first element matching the selector
	// whose trimmed label text equals the text (case-insensitive) is clicked.
	// The selector is a pointer so an explicit "" (fallback only writes the
	// button capture) sticks while nil means the default; read via
	// ClaimInventorySelector. Text defaults to DefaultClaimButtonText.
	InventoryButtonSelector *string `yaml:"inventory_button_selector,omitempty"`
	InventoryButtonText     string  `yaml:"inventory_button_text"`
}

// Defaults for the DOM claim fallback. data-a-target="tw-core-button-label-text"
// is Twitch's generic core-button label, so the button is identified by
// selector AND label text together.
const (
	DefaultClaimButtonSelector = `button:has([data-a-target="tw-core-button-label-text"])`
	DefaultClaimButtonText     = "Claim Now"
)

// ClaimSweep reports whether the claim sweep runs (default true).
func (c *Config) ClaimSweep() bool { return boolOr(c.Claim.Sweep, true) }

// ClaimInventorySelector returns the DOM claim fallback's button selector; ""
// means the fallback only writes the inventory button capture.
func (c *Config) ClaimInventorySelector() string {
	if c.Claim.InventoryButtonSelector == nil {
		return DefaultClaimButtonSelector
	}
	return strings.TrimSpace(*c.Claim.InventoryButtonSelector)
}

type BrowserConfig struct {
	ChromePath string       `yaml:"chrome_path"` // blank = auto-detect
	ProfileDir string       `yaml:"profile_dir"` // blank = <approot>/chrome-profile
	MuteAudio  bool         `yaml:"mute_audio"`  // silence the dedicated Chrome at the sink (player stays unmuted)
	ExtraFlags []string     `yaml:"extra_flags"`
	Window     WindowConfig `yaml:"window"`
	Burnin     BurninConfig `yaml:"burnin"`
	Player     PlayerConfig `yaml:"player"`

	// SeparateWindow is read ONLY from the kick block
	// (kick.browser.separate_window); see KickSeparateWindow. It is declared
	// here because the kick block reuses this whole struct.
	SeparateWindow bool `yaml:"separate_window,omitempty"`

	// SharedWindowExperimental (kick block only) opts back into the shared
	// window while Kick is enabled. Owner-only/experimental: see
	// KickSeparateWindow.
	SharedWindowExperimental bool `yaml:"shared_window_experimental,omitempty"`
}

// KickSeparateWindow reports whether Kick gets its OWN Chrome instance (its own
// user-data dir and window) instead of a TAB in the profile's Twitch window.
//
// Default false = SHARED: one Chrome per profile, so four people running both
// platforms cost four windows instead of eight. Setting
// kick.browser.separate_window: true restores the legacy two-instance layout,
// kept as the fallback if background-tab playback proves unreliable live.
//
// While Kick is enabled (root or any profile) the separate window is FORCED:
// in the shared window a Kick tab and a Twitch tab compete for the one
// foreground slot, and a backgrounded player reads "hidden" and stops
// crediting. The shared layout stays available only behind
// kick.browser.shared_window_experimental (owner testing).
//
// It is a LAUNCH setting: a change reaches a runtime on its next launch
// (runtime toggle, crash relaunch, or app restart).
func (c *Config) KickSeparateWindow() bool {
	if c.Kick.Browser.SeparateWindow {
		return true
	}
	return c.anyKickEnabled() && !c.Kick.Browser.SharedWindowExperimental
}

type WindowConfig struct {
	Width          int  `yaml:"width"`
	Height         int  `yaml:"height"`
	X              int  `yaml:"x"`
	Y              int  `yaml:"y"`
	Monitor        int  `yaml:"monitor"`
	AlwaysOnBottom bool `yaml:"always_on_bottom"` // Windows only: HWND_BOTTOM z-order, re-asserted while watching
	NeverFocus     bool `yaml:"never_focus"`
	StartMinimized bool `yaml:"start_minimized"` // minimize the watcher Chrome window after launch (CDP)

	// MinimizeWhileWatching re-minimizes the window once playback is validated.
	// OFF by default and it SHOULD stay off: a minimized window's tab reports
	// document.visibilityState "hidden", and Twitch (and platforms like it)
	// pause their minute-watched heartbeats for a hidden tab, so drops never
	// credit (confirmed live 2026-08-31). The window stays visible-but-
	// unfocused while watching instead - cover it with another window if it is
	// in the way; the occlusion flags keep a covered window crediting.
	MinimizeWhileWatching bool `yaml:"minimize_while_watching,omitempty"`
}

type BurninConfig struct {
	DimOpacity    float64       `yaml:"dim_opacity"`
	ShiftInterval time.Duration `yaml:"shift_interval"`
	ShiftPixels   int           `yaml:"shift_pixels"`
}

type PlayerConfig struct {
	Volume             float64       `yaml:"volume"` // NOT 0: Chrome 145 pauses muted background video
	ForceLowestQuality bool          `yaml:"force_lowest_quality"`
	ReassertInterval   time.Duration `yaml:"reassert_interval"`
}

type AuthConfig struct {
	Mode   string            `yaml:"mode"` // manual|stored
	Stored StoredCredentials `yaml:"stored"`
}

// StoredCredentials is implemented but ships disabled: typing credentials into
// the real login form is exactly the behavior that has triggered login blocks
// and lockouts for other drop bots.
type StoredCredentials struct {
	Enabled      bool   `yaml:"enabled"`
	Username     string `yaml:"username"`
	PasswordFile string `yaml:"password_file"` // 0600 file; never inline
}

type LoggingConfig struct {
	Level            string `yaml:"level"`
	File             bool   `yaml:"file"`
	MaxSizeMB        int    `yaml:"max_size_mb"`
	CaptureGQLBodies bool   `yaml:"capture_gql_bodies"` // debug only
}

type UIConfig struct {
	StartMinimized bool `yaml:"start_minimized"`
	ShowDebugPane  bool `yaml:"show_debug_pane"`
}

// UpdateConfig configures the in-app self-updater. The bools and feed_url are
// pointers so a config written before this block existed (nil) keeps the
// defaults, while an explicit false or "" in YAML sticks; read them through
// the Update* accessors.
type UpdateConfig struct {
	Enabled       *bool   `yaml:"enabled,omitempty"`        // nil => true: background checks
	AutoInstall   *bool   `yaml:"auto_install,omitempty"`   // nil => true: install found updates when no watch is in progress
	ShowChangelog *bool   `yaml:"show_changelog,omitempty"` // nil => true: offer release notes in the dashboard
	FeedURL       *string `yaml:"feed_url,omitempty"`       // nil => DefaultUpdateFeedURL; "" disables checking
	IntervalHours int     `yaml:"interval_hours"`           // background check period; 0 => 24
}

// DefaultUpdateFeedURL is the release manifest the updater reads by default.
const DefaultUpdateFeedURL = "https://cdn.rake.pro/releases/dropwarden/latest.json"

// UpdateEnabled reports whether background update checks run (default true).
func (c *Config) UpdateEnabled() bool { return boolOr(c.Update.Enabled, true) }

// UpdateAutoInstall reports whether a found update installs itself once no
// watch is in progress (default true).
func (c *Config) UpdateAutoInstall() bool { return boolOr(c.Update.AutoInstall, true) }

// UpdateShowChangelog reports whether the dashboard offers the release notes
// of a pending update (default true).
func (c *Config) UpdateShowChangelog() bool { return boolOr(c.Update.ShowChangelog, true) }

// UpdateFeedURL returns the manifest URL; "" means checking is disabled.
func (c *Config) UpdateFeedURL() string {
	if c.Update.FeedURL == nil {
		return DefaultUpdateFeedURL
	}
	return strings.TrimSpace(*c.Update.FeedURL)
}

func boolOr(p *bool, def bool) bool {
	if p == nil {
		return def
	}
	return *p
}

// Load reads, defaults, and validates the config at path.
func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: read %s: %w", path, err)
	}
	return parse(b)
}

func parse(b []byte) (*Config, error) {
	var cfg Config
	if err := yaml.Unmarshal(b, &cfg); err != nil {
		return nil, fmt.Errorf("config: parse: %w", err)
	}
	cfg.applyDefaults()
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// Save validates cfg, writes it to a temp file in the same directory, re-reads
// that temp file through the full validator, and only then atomically renames
// it into place. An invalid config can never reach the live path.
func Save(path string, cfg *Config) error {
	if err := cfg.validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("config: mkdir: %w", err)
	}
	out, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("config: marshal: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".config-*.yaml.tmp")
	if err != nil {
		return fmt.Errorf("config: temp: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(out); err != nil {
		tmp.Close()
		return fmt.Errorf("config: write temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("config: close temp: %w", err)
	}
	if _, err := Load(tmpName); err != nil {
		return fmt.Errorf("config: validate temp: %w", err)
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return fmt.Errorf("config: chmod temp: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("config: rename: %w", err)
	}
	return nil
}

// ExpandPath resolves a leading ~ to the user's home directory.
func ExpandPath(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			if p == "~" {
				return home
			}
			return filepath.Join(home, p[2:])
		}
	}
	return p
}
