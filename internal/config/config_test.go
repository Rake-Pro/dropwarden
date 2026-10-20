package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStarterDefaultsValid(t *testing.T) {
	cfg, err := StarterDefaults()
	if err != nil {
		t.Fatalf("starter defaults: %v", err)
	}
	if cfg.Browser.Player.Volume <= 0 || cfg.Browser.Player.Volume > 1 {
		t.Fatalf("volume must be in (0,1], got %v", cfg.Browser.Player.Volume)
	}
	if cfg.Claim.AllowGQLFallback {
		t.Fatal("GQL claim fallback must ship OFF")
	}
	if cfg.Auth.Mode != "manual" {
		t.Fatalf("auth mode must default to manual, got %q", cfg.Auth.Mode)
	}
	if cfg.Games == nil || len(cfg.Games) != 0 {
		t.Fatalf("starter config ships no root games (picked per profile after the first scan), got %v", cfg.Games)
	}
	main := cfg.Profile(MainProfileName)
	if main == nil || main.Games == nil || len(main.Games) != 0 || main.AnyGame == nil || *main.AnyGame {
		t.Fatalf("starter main profile ships games: [] and any_game: false, got %+v", main)
	}
	if main.Queue.PriorityMode != "top_down" || main.Queue.AutoEnqueueNew == nil || !*main.Queue.AutoEnqueueNew ||
		main.Claim.SkipAccountLink == nil || !*main.Claim.SkipAccountLink || main.Kick.Enabled == nil || *main.Kick.Enabled {
		t.Fatalf("starter main profile queue/claim/kick defaults wrong: %+v", main)
	}
}

func TestValidateRejectsMutedVolume(t *testing.T) {
	cfg, _ := StarterDefaults()
	cfg.Browser.Player.Volume = 0
	if err := cfg.validate(); err == nil {
		t.Fatal("volume 0 (mute) must be rejected: Chrome 145 pauses muted background video")
	}
}

func TestValidateRejectsBadPriorityAndStall(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Config)
	}{
		{"bad priority", func(c *Config) { c.Queue.PriorityMode = "nonsense" }},
		{"soft>=hard", func(c *Config) { c.Watcher.SoftStall = c.Watcher.HardStall }},
		{"bad theme", func(c *Config) { c.Theme = "neon" }},
		{"bad keep_awake", func(c *Config) { c.Power.KeepAwake = "sometimes" }},
		{"bad claim strategy", func(c *Config) { c.Claim.Strategy = "spam" }},
		{"stored credentials mode removed", func(c *Config) { c.Auth.Mode = "stored" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, _ := StarterDefaults()
			tc.mutate(cfg)
			if err := cfg.validate(); err == nil {
				t.Fatalf("%s: expected validation error", tc.name)
			}
		})
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	cfg, _ := StarterDefaults()
	cfg.Theme = "system"
	cfg.Games = []string{"Rust"}
	if err := Save(path, cfg); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.Theme != "system" || len(got.Games) != 1 || got.Games[0] != "Rust" {
		t.Fatalf("round trip mismatch: %+v", got)
	}
}

func TestExpandPath(t *testing.T) {
	if got := ExpandPath("/abs/path"); got != "/abs/path" {
		t.Fatalf("abs path changed: %q", got)
	}
}

func TestLegacySynthesizesMainProfile(t *testing.T) {
	cfg, err := StarterDefaults()
	if err != nil {
		t.Fatalf("starter defaults: %v", err)
	}
	if len(cfg.Profiles) != 1 {
		t.Fatalf("expected exactly 1 synthesized profile, got %d", len(cfg.Profiles))
	}
	p := cfg.Profiles[0]
	if p.Name != MainProfileName || p.Dir != "." || !p.Enabled {
		t.Fatalf("unexpected synthesized profile: %+v", p)
	}

	home := t.TempDir()
	t.Setenv("DROPWARDEN_HOME", home)

	root, err := Dir()
	if err != nil {
		t.Fatalf("Dir: %v", err)
	}
	pp, err := cfg.ProfilePaths(MainProfileName)
	if err != nil {
		t.Fatalf("ProfilePaths: %v", err)
	}
	if pp.DB != filepath.Join(root, "dropwarden.db") {
		t.Fatalf("main DB path = %q, want app-root dropwarden.db", pp.DB)
	}
	if pp.ChromeProfile != filepath.Join(root, "chrome-profile") {
		t.Fatalf("main chrome-profile path = %q", pp.ChromeProfile)
	}
	if pp.ChromeProfileKick != filepath.Join(root, "chrome-profile-kick") {
		t.Fatalf("main chrome-profile-kick path = %q", pp.ChromeProfileKick)
	}
	if pp.Debug != filepath.Join(root, "debug") {
		t.Fatalf("main debug path = %q", pp.Debug)
	}

	// Existing helpers stay unchanged and agree with ProfilePaths(main).
	dbPath, err := DBPath()
	if err != nil {
		t.Fatalf("DBPath: %v", err)
	}
	if dbPath != pp.DB {
		t.Fatalf("DBPath() = %q, want %q", dbPath, pp.DB)
	}
	chromeDir, err := cfg.ProfileDir()
	if err != nil {
		t.Fatalf("ProfileDir: %v", err)
	}
	if chromeDir != pp.ChromeProfile {
		t.Fatalf("ProfileDir() = %q, want %q", chromeDir, pp.ChromeProfile)
	}
	kickDir, err := cfg.KickProfileDir()
	if err != nil {
		t.Fatalf("KickProfileDir: %v", err)
	}
	if kickDir != pp.ChromeProfileKick {
		t.Fatalf("KickProfileDir() = %q, want %q", kickDir, pp.ChromeProfileKick)
	}
}

func TestSynthesisWrittenBackOnSave(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	cfg, _ := StarterDefaults()
	if err := Save(path, cfg); err != nil {
		t.Fatalf("save: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(raw), "profiles:") {
		t.Fatalf("expected synthesized profiles block written on save, got:\n%s", raw)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(got.Profiles) != 1 || got.Profiles[0].Name != MainProfileName {
		t.Fatalf("unexpected profiles after round trip: %+v", got.Profiles)
	}
}

func TestExplicitProfilesBlockParses(t *testing.T) {
	cfg, err := StarterDefaults()
	if err != nil {
		t.Fatalf("starter defaults: %v", err)
	}
	slot := 1
	kickOn := true
	cfg.Profiles = append(cfg.Profiles, ProfileConfig{
		Name:        "alice",
		Enabled:     true,
		DisplayName: "Alice",
		Kick:        ProfileKickConfig{Enabled: &kickOn},
		WindowSlot:  &slot,
	})
	if err := cfg.validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := Save(path, cfg); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(got.Profiles) != 2 {
		t.Fatalf("expected 2 profiles (main + alice), got %d", len(got.Profiles))
	}
	p := got.Profile("alice")
	if p == nil {
		t.Fatal("alice profile missing after round trip")
	}
	if p.DisplayName != "Alice" || p.Kick.Enabled == nil || !*p.Kick.Enabled || p.WindowSlot == nil || *p.WindowSlot != 1 {
		t.Fatalf("alice profile fields lost round trip: %+v", p)
	}
}

func TestProfilePathsForAddedProfile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("DROPWARDEN_HOME", home)

	cfg, _ := StarterDefaults()
	cfg.Profiles = append(cfg.Profiles, ProfileConfig{Name: "alice", Enabled: true})

	root, err := Dir()
	if err != nil {
		t.Fatalf("Dir: %v", err)
	}
	pp, err := cfg.ProfilePaths("alice")
	if err != nil {
		t.Fatalf("ProfilePaths: %v", err)
	}
	wantBase := filepath.Join(root, "profiles", "alice")
	if pp.DB != filepath.Join(wantBase, "dropwarden.db") {
		t.Fatalf("alice DB path = %q, want under %q", pp.DB, wantBase)
	}
	if pp.ChromeProfile != filepath.Join(wantBase, "chrome-profile") {
		t.Fatalf("alice chrome-profile path = %q", pp.ChromeProfile)
	}
	if pp.ChromeProfileKick != filepath.Join(wantBase, "chrome-profile-kick") {
		t.Fatalf("alice chrome-profile-kick path = %q", pp.ChromeProfileKick)
	}
	if pp.Debug != filepath.Join(wantBase, "debug") {
		t.Fatalf("alice debug path = %q", pp.Debug)
	}

	if _, err := cfg.ProfilePaths("nope"); err == nil {
		t.Fatal("expected error for unknown profile")
	}
}

func TestProfileNameValidation(t *testing.T) {
	cases := []struct {
		name string
		p1   string
		p2   string
	}{
		{"uppercase rejected", "Alice", ""},
		{"space rejected", "alice smith", ""},
		{"empty rejected", "", ""},
		{"too long rejected", strings.Repeat("a", 25), ""},
		{"duplicate rejected", "alice", "alice"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, _ := StarterDefaults()
			cfg.Profiles = append(cfg.Profiles, ProfileConfig{Name: tc.p1, Enabled: true, AnyGame: boolPtr(true)})
			if tc.p2 != "" {
				cfg.Profiles = append(cfg.Profiles, ProfileConfig{Name: tc.p2, Enabled: true, AnyGame: boolPtr(true)})
			}
			if err := cfg.validate(); err == nil {
				t.Fatalf("%s: expected validation error", tc.name)
			}
		})
	}

	// Valid slug boundaries accepted.
	cfg, _ := StarterDefaults()
	cfg.Profiles = append(cfg.Profiles, ProfileConfig{Name: "alice-2", Enabled: true, AnyGame: boolPtr(true)})
	if err := cfg.validate(); err != nil {
		t.Fatalf("valid slug rejected: %v", err)
	}
}

// TestProfileGamesMaterializedFromRootOnce pins the migration: a profile
// written before it had games of its own takes the legacy root list when the
// config is LOADED, a profile with its own list keeps it, and after loading
// nothing inherits any more - a root edit changes no profile.
func TestProfileGamesMaterializedFromRootOnce(t *testing.T) {
	cfg, err := parse([]byte(`
games: [Rust]
any_game: false
queue:
  priority_mode: ending_soonest
  auto_enqueue_new: false
watcher:
  wait_for_top: true
claim:
  skip_account_link_drops: false
kick:
  enabled: true
profiles:
  - name: legacy
    dir: .
    enabled: true
  - name: own
    enabled: true
    games: [Halo Infinite]
    kick:
      enabled: false
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	legacy, err := cfg.Effective("legacy")
	if err != nil {
		t.Fatalf("Effective: %v", err)
	}
	if len(legacy.Games) != 1 || legacy.Games[0] != "Rust" || legacy.AnyGame {
		t.Fatalf("legacy profile must take the root games once: %+v", legacy)
	}
	if legacy.PriorityMode != "ending_soonest" || legacy.AutoEnqueueNew || !legacy.WaitForTop || legacy.SkipAccountLink || !legacy.KickEnabled {
		t.Fatalf("legacy profile must take the root queue/claim/kick values once: %+v", legacy)
	}
	own, err := cfg.Effective("own")
	if err != nil {
		t.Fatalf("Effective: %v", err)
	}
	if len(own.Games) != 1 || own.Games[0] != "Halo Infinite" || own.KickEnabled {
		t.Fatalf("a profile with its own values keeps them: %+v", own)
	}
	// Materialized: the root is no longer consulted.
	cfg.Games = []string{"Once Human"}
	cfg.Kick.Enabled = false
	again, _ := cfg.Effective("legacy")
	if len(again.Games) != 1 || again.Games[0] != "Rust" || !again.KickEnabled {
		t.Fatalf("a root edit after load must not change a profile: %+v", again)
	}
	// And the materialized values survive a round trip in full.
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := Save(path, cfg); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	p := got.Profile("legacy")
	if p.AnyGame == nil || p.Queue.AutoEnqueueNew == nil || p.Queue.PreferChannelSpecific == nil || p.Queue.WaitForTop == nil ||
		p.Claim.SkipAccountLink == nil || p.Kick.Enabled == nil || p.Queue.PriorityMode == "" {
		t.Fatalf("every per-profile field must be written explicitly: %+v", p)
	}
	eff, _ := got.Effective("legacy")
	if len(eff.Games) != 1 || eff.Games[0] != "Rust" || !eff.KickEnabled || eff.PriorityMode != "ending_soonest" {
		t.Fatalf("round trip lost per-profile values: %+v", eff)
	}
}

// TestProfileRuntimeConfigCarriesOwnSettings: the engines read the legacy root
// fields of the per-profile copy, so every per-profile value must land there.
func TestProfileRuntimeConfigCarriesOwnSettings(t *testing.T) {
	cfg, _ := StarterDefaults()
	p := NewProfile("p2", "Two")
	p.Games = []string{"Rust"}
	p.AnyGame = boolPtr(false)
	p.Queue.PriorityMode = "shortest_first"
	p.Queue.AutoEnqueueNew = boolPtr(false)
	p.Queue.PreferChannelSpecific = boolPtr(false)
	p.Queue.WaitForTop = boolPtr(true)
	p.Claim.SkipAccountLink = boolPtr(false)
	p.Kick.Enabled = boolPtr(true)
	cfg.Profiles = append(cfg.Profiles, p)
	rc, err := cfg.ProfileRuntimeConfig("p2")
	if err != nil {
		t.Fatalf("runtime config: %v", err)
	}
	if len(rc.Games) != 1 || rc.Games[0] != "Rust" || rc.Queue.PriorityMode != "shortest_first" || rc.Queue.AutoEnqueueNew ||
		rc.PreferChannelSpecific() || !rc.Watcher.WaitForTop || rc.Claim.SkipAccountLink || !rc.Kick.Enabled {
		t.Fatalf("runtime copy must carry the profile's own settings: games=%v queue=%+v wait=%v skip=%v kick=%v",
			rc.Games, rc.Queue, rc.Watcher.WaitForTop, rc.Claim.SkipAccountLink, rc.Kick.Enabled)
	}
	// The main profile's copy is untouched by p2's choices.
	mc, _ := cfg.ProfileRuntimeConfig(MainProfileName)
	if mc.Kick.Enabled || mc.Queue.PriorityMode != "top_down" || len(mc.Games) != 0 {
		t.Fatalf("main profile copy must keep its own settings: %+v", mc.Queue)
	}
}

func TestProfileKickEnabledOverride(t *testing.T) {
	cfg, _ := StarterDefaults()
	cfg.Kick.Enabled = true // root default
	on := true
	off := false
	cfg.Profiles = append(cfg.Profiles,
		ProfileConfig{Name: "inherits", Enabled: true, AnyGame: boolPtr(true)},
		ProfileConfig{Name: "on", Enabled: true, AnyGame: boolPtr(true), Kick: ProfileKickConfig{Enabled: &on}},
		ProfileConfig{Name: "off", Enabled: true, AnyGame: boolPtr(true), Kick: ProfileKickConfig{Enabled: &off}},
	)

	inherits, err := cfg.Effective("inherits")
	if err != nil {
		t.Fatalf("Effective: %v", err)
	}
	if !inherits.KickEnabled {
		t.Fatalf("expected 'inherits' profile to inherit root kick.enabled=true, got %v", inherits.KickEnabled)
	}
	on2, err := cfg.Effective("on")
	if err != nil {
		t.Fatalf("Effective: %v", err)
	}
	if !on2.KickEnabled {
		t.Fatal("expected 'on' profile KickEnabled true")
	}
	off2, err := cfg.Effective("off")
	if err != nil {
		t.Fatalf("Effective: %v", err)
	}
	if off2.KickEnabled {
		t.Fatal("expected 'off' profile KickEnabled false despite root true")
	}
}

func TestWindowTileDefaultMatchesLegacySingleProfile(t *testing.T) {
	// StarterDefaults synthesizes exactly one profile ("main", slot 0, no
	// explicit window geometry): the tiling default must reproduce today's
	// baked-in single-profile positions exactly, or existing installs would
	// see their windows jump on upgrade.
	cfg, _ := StarterDefaults()

	twitch, err := cfg.ProfileRuntimeConfig(MainProfileName)
	if err != nil {
		t.Fatalf("ProfileRuntimeConfig: %v", err)
	}
	if twitch.Browser.Window.X != 40 || twitch.Browser.Window.Y != 40 {
		t.Fatalf("twitch window = (%d,%d), want (40,40)", twitch.Browser.Window.X, twitch.Browser.Window.Y)
	}

	// Separate-window mode is the legacy two-instance layout: Kick keeps its
	// own window beside the Twitch one (col 1).
	cfg.Kick.Browser.SeparateWindow = true
	kick, err := cfg.ProfileKickRuntimeConfig(MainProfileName)
	if err != nil {
		t.Fatalf("ProfileKickRuntimeConfig: %v", err)
	}
	if kick.Browser.Window.X != 700 || kick.Browser.Window.Y != 40 {
		t.Fatalf("kick window = (%d,%d), want (700,40)", kick.Browser.Window.X, kick.Browser.Window.Y)
	}
}

func TestWindowTileDefaultSharedWindowUsesTheProfileSlot(t *testing.T) {
	// Shared window (the default): Kick is a TAB in the profile's Twitch
	// window, so there is no second window to tile - its geometry is the
	// profile's own slot, identical to Twitch's.
	cfg, _ := StarterDefaults()
	if cfg.KickSeparateWindow() {
		t.Fatal("shared window must be the default")
	}
	cfg.Kick.Browser.SharedWindowExperimental = true // kick forces separate_window otherwise
	slot1 := 1
	cfg.Profiles = append(cfg.Profiles, ProfileConfig{
		Name: "second", Enabled: true, AnyGame: boolPtr(true), WindowSlot: &slot1,
		Kick: ProfileKickConfig{Enabled: boolPtr(true)},
	})
	for _, tc := range []struct {
		name string
		x, y int
	}{
		{MainProfileName, 40, 40},
		{"second", 40, 460},
	} {
		kick, err := cfg.ProfileKickRuntimeConfig(tc.name)
		if err != nil {
			t.Fatalf("ProfileKickRuntimeConfig(%s): %v", tc.name, err)
		}
		if kick.Browser.Window.X != tc.x || kick.Browser.Window.Y != tc.y {
			t.Fatalf("%s: shared kick window = (%d,%d), want (%d,%d)",
				tc.name, kick.Browser.Window.X, kick.Browser.Window.Y, tc.x, tc.y)
		}
	}
}

func TestWindowTileDefaultMultiProfileSlots(t *testing.T) {
	cfg, _ := StarterDefaults()
	cfg.Kick.Browser.SeparateWindow = true
	slot1 := 1
	cfg.Profiles = append(cfg.Profiles, ProfileConfig{
		Name: "second", Enabled: true, AnyGame: boolPtr(true), WindowSlot: &slot1,
		Kick: ProfileKickConfig{Enabled: boolPtr(true)},
	})

	twitch, err := cfg.ProfileRuntimeConfig("second")
	if err != nil {
		t.Fatalf("ProfileRuntimeConfig: %v", err)
	}
	if twitch.Browser.Window.X != 40 || twitch.Browser.Window.Y != 460 {
		t.Fatalf("slot-1 twitch window = (%d,%d), want (40,460)", twitch.Browser.Window.X, twitch.Browser.Window.Y)
	}

	kick, err := cfg.ProfileKickRuntimeConfig("second")
	if err != nil {
		t.Fatalf("ProfileKickRuntimeConfig: %v", err)
	}
	if kick.Browser.Window.X != 700 || kick.Browser.Window.Y != 460 {
		t.Fatalf("slot-1 kick window = (%d,%d), want (700,460)", kick.Browser.Window.X, kick.Browser.Window.Y)
	}
}

func TestWindowTileDefaultExplicitOverrideWins(t *testing.T) {
	cfg, _ := StarterDefaults()
	cfg.Browser.Window.X, cfg.Browser.Window.Y = 999, 888
	cfg.Kick.Browser.Window.X, cfg.Kick.Browser.Window.Y = 111, 222
	slot1 := 1
	cfg.Profiles = append(cfg.Profiles, ProfileConfig{
		Name: "second", Enabled: true, AnyGame: boolPtr(true), WindowSlot: &slot1,
	})

	for _, name := range []string{MainProfileName, "second"} {
		twitch, err := cfg.ProfileRuntimeConfig(name)
		if err != nil {
			t.Fatalf("ProfileRuntimeConfig(%s): %v", name, err)
		}
		if twitch.Browser.Window.X != 999 || twitch.Browser.Window.Y != 888 {
			t.Fatalf("%s: explicit twitch override lost, got (%d,%d)", name, twitch.Browser.Window.X, twitch.Browser.Window.Y)
		}
		kick, err := cfg.ProfileKickRuntimeConfig(name)
		if err != nil {
			t.Fatalf("ProfileKickRuntimeConfig(%s): %v", name, err)
		}
		if kick.Browser.Window.X != 111 || kick.Browser.Window.Y != 222 {
			t.Fatalf("%s: explicit kick override lost, got (%d,%d)", name, kick.Browser.Window.X, kick.Browser.Window.Y)
		}
	}
}

func boolPtr(b bool) *bool { return &b }

// parseYAML is the load path (defaults + validation) without touching disk.
func parseYAML(t *testing.T, body string) *Config {
	t.Helper()
	cfg, err := parse([]byte(body))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return cfg
}

func TestMigrateBakedWindowOrigins(t *testing.T) {
	// The pre-tiling starter template baked slot-0's positions into every
	// config it wrote; they must be un-pinned on load so other slots tile.
	cfg := parseYAML(t, `
games: [Rust]
browser:
  window: {x: 40, y: 40}
kick:
  browser:
    separate_window: true
    window: {x: 700, y: 40}
`)
	if cfg.Browser.Window.X != 0 || cfg.Browser.Window.Y != 0 {
		t.Fatalf("legacy twitch origin not cleared: (%d,%d)", cfg.Browser.Window.X, cfg.Browser.Window.Y)
	}
	if cfg.Kick.Browser.Window.X != 0 || cfg.Kick.Browser.Window.Y != 0 {
		t.Fatalf("legacy kick origin not cleared: (%d,%d)", cfg.Kick.Browser.Window.X, cfg.Kick.Browser.Window.Y)
	}
	// Clearing must not move slot 0: the tiler recomputes the same values.
	tw, err := cfg.ProfileRuntimeConfig(MainProfileName)
	if err != nil {
		t.Fatalf("ProfileRuntimeConfig: %v", err)
	}
	if tw.Browser.Window.X != 40 || tw.Browser.Window.Y != 40 {
		t.Fatalf("slot 0 twitch moved: (%d,%d)", tw.Browser.Window.X, tw.Browser.Window.Y)
	}
	kc, err := cfg.ProfileKickRuntimeConfig(MainProfileName)
	if err != nil {
		t.Fatalf("ProfileKickRuntimeConfig: %v", err)
	}
	if kc.Browser.Window.X != 700 || kc.Browser.Window.Y != 40 {
		t.Fatalf("slot 0 kick moved: (%d,%d)", kc.Browser.Window.X, kc.Browser.Window.Y)
	}
}

func TestMigrateBakedWindowOriginsLeavesRealOverrides(t *testing.T) {
	cfg := parseYAML(t, `
games: [Rust]
browser:
  window: {x: 41, y: 40}
kick:
  browser:
    window: {x: 700, y: 41}
`)
	if cfg.Browser.Window.X != 41 || cfg.Browser.Window.Y != 40 {
		t.Fatalf("deliberate twitch override clobbered: (%d,%d)", cfg.Browser.Window.X, cfg.Browser.Window.Y)
	}
	if cfg.Kick.Browser.Window.X != 700 || cfg.Kick.Browser.Window.Y != 41 {
		t.Fatalf("deliberate kick override clobbered: (%d,%d)", cfg.Kick.Browser.Window.X, cfg.Kick.Browser.Window.Y)
	}
}

func TestUpdateDefaultsWhenBlockAbsent(t *testing.T) {
	cfg, err := parse([]byte("theme: dark\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.UpdateEnabled() || !cfg.UpdateAutoInstall() || !cfg.UpdateShowChangelog() {
		t.Error("update toggles must default on when the block is absent")
	}
	if cfg.UpdateFeedURL() != DefaultUpdateFeedURL {
		t.Errorf("feed = %q, want the default", cfg.UpdateFeedURL())
	}
	if cfg.Update.IntervalHours != 24 {
		t.Errorf("interval = %d, want 24", cfg.Update.IntervalHours)
	}
}

func TestUpdateStarterValues(t *testing.T) {
	cfg, err := StarterDefaults()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.UpdateEnabled() || !cfg.UpdateAutoInstall() || !cfg.UpdateShowChangelog() || cfg.UpdateFeedURL() != DefaultUpdateFeedURL || cfg.Update.IntervalHours != 24 {
		t.Errorf("starter update block = %+v", cfg.Update)
	}
}

func TestUpdateExplicitFalseAndEmptyFeedStick(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	cfg, err := parse([]byte("update:\n  enabled: false\n  auto_install: false\n  show_changelog: false\n  feed_url: \"\"\n  interval_hours: 6\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.UpdateEnabled() || cfg.UpdateAutoInstall() || cfg.UpdateShowChangelog() || cfg.UpdateFeedURL() != "" || cfg.Update.IntervalHours != 6 {
		t.Fatalf("explicit values lost on parse: %+v", cfg.Update)
	}
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.UpdateEnabled() || got.UpdateAutoInstall() || got.UpdateShowChangelog() || got.UpdateFeedURL() != "" || got.Update.IntervalHours != 6 {
		t.Fatalf("explicit values lost on save round trip: %+v", got.Update)
	}
}

func TestUpdateValidation(t *testing.T) {
	for _, body := range []string{
		"update:\n  interval_hours: -1\n",
		"update:\n  feed_url: \"ftp://example.com/latest.json\"\n",
		"update:\n  feed_url: \"not a url\"\n",
		"update:\n  feed_url: \"http://example.com/latest.json\"\n",
		"update:\n  feed_url: \"http://localhost.example.com/latest.json\"\n",
	} {
		if _, err := parse([]byte(body)); err == nil || !strings.Contains(err.Error(), "update.") {
			t.Errorf("%q: err = %v, want an update.* validation error", body, err)
		}
	}
	for _, feed := range []string{
		"https://example.com/latest.json",
		"http://localhost:8080/latest.json",
		"http://127.0.0.1/latest.json",
	} {
		if _, err := parse([]byte("update:\n  feed_url: \"" + feed + "\"\n")); err != nil {
			t.Errorf("%s: unexpected error %v", feed, err)
		}
	}
}

func TestPreferChannelSpecificDefault(t *testing.T) {
	cfg, err := parse([]byte("theme: dark\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.PreferChannelSpecific() {
		t.Fatal("prefer_channel_specific should default to true when omitted")
	}
	cfg, err = parse([]byte("queue:\n  prefer_channel_specific: false\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PreferChannelSpecific() {
		t.Fatal("an explicit prefer_channel_specific: false must stick")
	}
	cfg, err = parse([]byte(StarterConfig))
	if err != nil {
		t.Fatal(err)
	}
	if m := cfg.Profiles[0]; m.Queue.PreferChannelSpecific == nil || !*m.Queue.PreferChannelSpecific {
		t.Fatal("the starter config should set the main profile's prefer_channel_specific: true explicitly")
	}
}

func TestClaimDefaultsAndExplicitEmptySelector(t *testing.T) {
	cfg, err := parse([]byte("claim:\n  retry_backoff: 5m\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.ClaimSweep() {
		t.Fatal("claim.sweep must default true for configs without the key")
	}
	if cfg.ClaimInventorySelector() != DefaultClaimButtonSelector || cfg.Claim.InventoryButtonText != DefaultClaimButtonText {
		t.Fatalf("claim button defaults = %q / %q", cfg.ClaimInventorySelector(), cfg.Claim.InventoryButtonText)
	}
	cfg, err = parse([]byte("claim:\n  sweep: false\n  inventory_button_selector: \"\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ClaimSweep() {
		t.Fatal("explicit claim.sweep: false must stick")
	}
	if cfg.ClaimInventorySelector() != "" {
		t.Fatalf("explicit empty selector must stick, got %q", cfg.ClaimInventorySelector())
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	if got, err := Load(path); err != nil || got.ClaimInventorySelector() != "" {
		t.Fatalf("empty selector must survive save/load, got %v", err)
	}
	legacy, err := parse([]byte("claim:\n  strategy: in_page_then_gql\n  allow_gql_fallback: true\n"))
	if err != nil || !legacy.ClaimSweep() {
		t.Fatalf("legacy claim keys must still load: %v", err)
	}
	starter, _ := StarterDefaults()
	if !starter.ClaimSweep() || starter.ClaimInventorySelector() != DefaultClaimButtonSelector {
		t.Fatal("starter config must keep the claim sweep on and the default selector")
	}
}

func TestKickEnabledForcesSeparateWindow(t *testing.T) {
	cfg, _ := StarterDefaults()
	if cfg.KickSeparateWindow() {
		t.Fatal("kick off: shared layout is the (irrelevant) default")
	}
	// Legacy root kick.enabled reaches the profile at load, and forces it.
	legacy, err := parse([]byte("kick:\n  enabled: true\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !legacy.KickSeparateWindow() {
		t.Fatal("a legacy root kick.enabled must force separate_window through the materialized profile")
	}
	cfg.Profiles[0].Kick.Enabled = boolPtr(true)
	if !cfg.KickSeparateWindow() {
		t.Fatal("a profile enabling kick must force separate_window")
	}
	cfg.Kick.Browser.SharedWindowExperimental = true
	if cfg.KickSeparateWindow() {
		t.Fatal("shared_window_experimental must keep the shared layout")
	}
}

func TestUpdateChannelPicksTheFeed(t *testing.T) {
	cfg, _ := StarterDefaults()
	if cfg.UpdateChannel() != UpdateChannelStable || cfg.UpdateFeedURL() != DefaultUpdateFeedURL {
		t.Fatalf("starter = %q / %q, want stable + default feed", cfg.UpdateChannel(), cfg.UpdateFeedURL())
	}
	cfg.Update.Channel = UpdateChannelDev
	if cfg.UpdateFeedURL() != DefaultDevUpdateFeedURL {
		t.Fatalf("dev channel feed = %q", cfg.UpdateFeedURL())
	}
	// A pinned feed wins over the channel.
	cfg.Update.FeedURL = ptr("https://example.com/x.json")
	if cfg.UpdateFeedURL() != "https://example.com/x.json" {
		t.Fatalf("pinned feed must win, got %q", cfg.UpdateFeedURL())
	}
	cfg.Update.FeedURL = nil
	cfg.Update.Channel = "nightly"
	if err := cfg.validate(); err == nil {
		t.Fatal("unknown channel must be rejected")
	}
	// Legacy configs without the key read as stable and still validate.
	legacy, err := parse([]byte("update:\n  interval_hours: 12\n"))
	if err != nil || legacy.UpdateChannel() != UpdateChannelStable {
		t.Fatalf("legacy: %v %q", err, legacy.UpdateChannel())
	}
}
