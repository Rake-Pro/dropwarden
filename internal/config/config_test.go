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
		t.Fatalf("starter config ships games: [] (picked after the first scan), got %v", cfg.Games)
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
		{"bad claim strategy", func(c *Config) { c.Claim.Strategy = "spam" }},
		{"stored without enable", func(c *Config) { c.Auth.Mode = "stored"; c.Auth.Stored.Enabled = false }},
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
		Name:        "greg",
		Enabled:     true,
		DisplayName: "Greg",
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
		t.Fatalf("expected 2 profiles (main + greg), got %d", len(got.Profiles))
	}
	p := got.Profile("greg")
	if p == nil {
		t.Fatal("greg profile missing after round trip")
	}
	if p.DisplayName != "Greg" || p.Kick.Enabled == nil || !*p.Kick.Enabled || p.WindowSlot == nil || *p.WindowSlot != 1 {
		t.Fatalf("greg profile fields lost round trip: %+v", p)
	}
}

func TestProfilePathsForAddedProfile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("DROPWARDEN_HOME", home)

	cfg, _ := StarterDefaults()
	cfg.Profiles = append(cfg.Profiles, ProfileConfig{Name: "greg", Enabled: true})

	root, err := Dir()
	if err != nil {
		t.Fatalf("Dir: %v", err)
	}
	pp, err := cfg.ProfilePaths("greg")
	if err != nil {
		t.Fatalf("ProfilePaths: %v", err)
	}
	wantBase := filepath.Join(root, "profiles", "greg")
	if pp.DB != filepath.Join(wantBase, "dropwarden.db") {
		t.Fatalf("greg DB path = %q, want under %q", pp.DB, wantBase)
	}
	if pp.ChromeProfile != filepath.Join(wantBase, "chrome-profile") {
		t.Fatalf("greg chrome-profile path = %q", pp.ChromeProfile)
	}
	if pp.ChromeProfileKick != filepath.Join(wantBase, "chrome-profile-kick") {
		t.Fatalf("greg chrome-profile-kick path = %q", pp.ChromeProfileKick)
	}
	if pp.Debug != filepath.Join(wantBase, "debug") {
		t.Fatalf("greg debug path = %q", pp.Debug)
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
		{"uppercase rejected", "Greg", ""},
		{"space rejected", "greg smith", ""},
		{"empty rejected", "", ""},
		{"too long rejected", strings.Repeat("a", 25), ""},
		{"duplicate rejected", "greg", "greg"},
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
	cfg.Profiles = append(cfg.Profiles, ProfileConfig{Name: "greg-2", Enabled: true, AnyGame: boolPtr(true)})
	if err := cfg.validate(); err != nil {
		t.Fatalf("valid slug rejected: %v", err)
	}
}

func TestProfileGamesInheritance(t *testing.T) {
	cfg, _ := StarterDefaults()
	cfg.Games = []string{"Rust"}
	cfg.AnyGame = false
	cfg.Profiles = append(cfg.Profiles, ProfileConfig{Name: "inherits", Enabled: true})
	cfg.Profiles = append(cfg.Profiles, ProfileConfig{Name: "overrides", Enabled: true, Games: []string{"Halo Infinite"}})

	inherited, err := cfg.Effective("inherits")
	if err != nil {
		t.Fatalf("Effective: %v", err)
	}
	if len(inherited.Games) != 1 || inherited.Games[0] != "Rust" {
		t.Fatalf("expected inherited games [Rust], got %v", inherited.Games)
	}

	overridden, err := cfg.Effective("overrides")
	if err != nil {
		t.Fatalf("Effective: %v", err)
	}
	if len(overridden.Games) != 1 || overridden.Games[0] != "Halo Infinite" {
		t.Fatalf("expected overridden games [Halo Infinite], got %v", overridden.Games)
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
	if !cfg.PreferChannelSpecific() || cfg.Queue.PreferChannelSpecific == nil {
		t.Fatal("the starter config should set prefer_channel_specific: true explicitly")
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
	cfg.Kick.Enabled = true
	if !cfg.KickSeparateWindow() {
		t.Fatal("root kick.enabled must force separate_window")
	}
	cfg.Kick.Enabled = false
	cfg.Profiles[0].Kick.Enabled = boolPtr(true)
	if !cfg.KickSeparateWindow() {
		t.Fatal("a profile enabling kick must force separate_window")
	}
	cfg.Kick.Browser.SharedWindowExperimental = true
	if cfg.KickSeparateWindow() {
		t.Fatal("shared_window_experimental must keep the shared layout")
	}
}
