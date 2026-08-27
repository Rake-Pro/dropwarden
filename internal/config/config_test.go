package config

import (
	"path/filepath"
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
	if len(cfg.Games) != 5 {
		t.Fatalf("expected 5 default games, got %d", len(cfg.Games))
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
