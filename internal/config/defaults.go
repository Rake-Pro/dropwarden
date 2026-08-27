package config

import (
	"fmt"
	"time"

	"github.com/rake-pro/dropwarden/internal/domain"
)

// DefaultGames is the MVP test set.
var DefaultGames = []string{
	"Rust",
	"Halo Infinite",
	"Halo Campaign Evolved",
	"Halo: The Master Chief Collection",
	"Once Human",
}

// applyDefaults fills any zero-valued field with its default. It is safe to call
// on a fully or partially populated Config.
func (c *Config) applyDefaults() {
	setString(&c.Theme, "dark")
	if len(c.Games) == 0 {
		c.Games = append([]string(nil), DefaultGames...)
	}

	setString(&c.Queue.PriorityMode, string(domain.PriorityTopDown))
	// AutoEnqueueNew defaults true; represented explicitly so a false in YAML sticks.
	// A freshly-parsed zero Config gets true here via the starter template.

	setDuration(&c.Discovery.Interval, 45*time.Minute)
	setDuration(&c.Discovery.Jitter, 30*time.Minute)
	setInt(&c.Discovery.DetailsPerCycle, 8)
	setDuration(&c.Discovery.DirectoryPoll, 5*time.Minute)
	setDuration(&c.Discovery.DirectoryJitter, 3*time.Minute)

	setDuration(&c.Watcher.StreamUpDelay, 90*time.Second)
	setDuration(&c.Watcher.ValidateTimeout, 60*time.Second)
	setDuration(&c.Watcher.SoftStall, 6*time.Minute)
	setDuration(&c.Watcher.HardStall, 12*time.Minute)
	setInt(&c.Watcher.HardStallMaxRetries, 2)
	setDuration(&c.Watcher.ProactiveRefresh, 30*time.Minute)
	setDuration(&c.Watcher.ProactiveJitter, 5*time.Minute)
	setDuration(&c.Watcher.HealthTick, 25*time.Second)
	setInt(&c.Watcher.HealthTickJitter, 40)
	setDuration(&c.Watcher.ProgressPoll, 60*time.Second)
	setDuration(&c.Watcher.MaxSession, 4*time.Hour)
	setDuration(&c.Watcher.Cooldown, 60*time.Second)
	setDuration(&c.Watcher.CooldownJitter, 120*time.Second)
	setDuration(&c.Watcher.ChannelBlacklistTTL, 45*time.Minute)
	setDuration(&c.Watcher.OfflineBlacklistTTL, 15*time.Minute)
	setInt(&c.Watcher.MaxSelectAttempts, 6)

	setString(&c.Claim.Strategy, "in_page")
	setDuration(&c.Claim.RetryBackoff, 5*time.Minute)

	setInt(&c.Browser.Window.Width, 640)
	setInt(&c.Browser.Window.Height, 400)
	setInt(&c.Browser.Window.X, 40)
	setInt(&c.Browser.Window.Y, 40)
	setFloat(&c.Browser.Burnin.DimOpacity, 0.55)
	setDuration(&c.Browser.Burnin.ShiftInterval, 20*time.Minute)
	setInt(&c.Browser.Burnin.ShiftPixels, 8)
	setFloat(&c.Browser.Player.Volume, 0.01)
	setDuration(&c.Browser.Player.ReassertInterval, 30*time.Second)

	setString(&c.Auth.Mode, "manual")

	setString(&c.Logging.Level, "info")
	setInt(&c.Logging.MaxSizeMB, 5)
}

// validate returns a typed error naming the offending key when a value is out
// of range.
func (c *Config) validate() error {
	switch c.Theme {
	case "dark", "light", "system":
	default:
		return fmt.Errorf("config: theme %q must be dark|light|system", c.Theme)
	}
	if !domain.ValidPriorityMode(domain.PriorityMode(c.Queue.PriorityMode)) {
		return fmt.Errorf("config: queue.priority_mode %q invalid", c.Queue.PriorityMode)
	}
	if !c.AnyGame && len(c.Games) == 0 {
		return fmt.Errorf("config: games must be non-empty unless any_game is true")
	}
	switch c.Claim.Strategy {
	case "in_page", "in_page_then_gql":
	default:
		return fmt.Errorf("config: claim.strategy %q must be in_page|in_page_then_gql", c.Claim.Strategy)
	}
	switch c.Auth.Mode {
	case "manual", "stored":
	default:
		return fmt.Errorf("config: auth.mode %q must be manual|stored", c.Auth.Mode)
	}
	if c.Auth.Mode == "stored" && !c.Auth.Stored.Enabled {
		return fmt.Errorf("config: auth.mode is stored but auth.stored.enabled is false")
	}
	if v := c.Browser.Player.Volume; v <= 0 || v > 1 {
		return fmt.Errorf("config: browser.player.volume %v must be in (0,1]; 0 (mute) pauses background video on Chrome 145+", v)
	}
	if o := c.Browser.Burnin.DimOpacity; o < 0 || o > 1 {
		return fmt.Errorf("config: browser.burnin.dim_opacity %v must be in [0,1]", o)
	}
	if c.Watcher.SoftStall >= c.Watcher.HardStall {
		return fmt.Errorf("config: watcher.soft_stall (%s) must be less than hard_stall (%s)", c.Watcher.SoftStall, c.Watcher.HardStall)
	}
	if j := c.Watcher.HealthTickJitter; j < 0 || j > 100 {
		return fmt.Errorf("config: watcher.health_tick_jitter %d must be a percent in [0,100]", j)
	}
	if _, err := zerologLevel(c.Logging.Level); err != nil {
		return err
	}
	return nil
}

func zerologLevel(s string) (string, error) {
	switch s {
	case "trace", "debug", "info", "warn", "error":
		return s, nil
	}
	return "", fmt.Errorf("config: logging.level %q must be trace|debug|info|warn|error", s)
}

func setString(p *string, def string) {
	if *p == "" {
		*p = def
	}
}
func setInt(p *int, def int) {
	if *p == 0 {
		*p = def
	}
}
func setFloat(p *float64, def float64) {
	if *p == 0 {
		*p = def
	}
}
func setDuration(p *time.Duration, def time.Duration) {
	if *p == 0 {
		*p = def
	}
}
