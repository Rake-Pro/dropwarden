package config

import (
	"fmt"
	"net/url"
	"regexp"
	"time"

	"github.com/rake-pro/dropwarden/internal/domain"
)

// profileNameRe is the lowercase-slug rule for profile names.
var profileNameRe = regexp.MustCompile(`^[a-z0-9-]{1,24}$`)

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

	// Kick's dedicated browser window. X/Y are NOT defaulted here: 0,0 (unset)
	// is the signal that lets ProfileKickRuntimeConfig apply the window-tiling
	// default instead (see setup.go); an explicit x/y in the config always wins.
	setInt(&c.Kick.Browser.Window.Width, 640)
	setInt(&c.Kick.Browser.Window.Height, 400)
	setDuration(&c.Watcher.MaxSession, 4*time.Hour)
	setDuration(&c.Watcher.Cooldown, 60*time.Second)
	setDuration(&c.Watcher.CooldownJitter, 120*time.Second)
	setDuration(&c.Watcher.ChannelBlacklistTTL, 45*time.Minute)
	setDuration(&c.Watcher.OfflineBlacklistTTL, 15*time.Minute)
	setInt(&c.Watcher.MaxSelectAttempts, 6)

	setString(&c.Claim.Strategy, "in_page")
	setDuration(&c.Claim.RetryBackoff, 5*time.Minute)
	setString(&c.Claim.InventoryButtonText, DefaultClaimButtonText)

	// X/Y are NOT defaulted here (see the Kick window comment above); the
	// window-tiling default lives in ProfileRuntimeConfig.
	setInt(&c.Browser.Window.Width, 640)
	setInt(&c.Browser.Window.Height, 400)
	setFloat(&c.Browser.Burnin.DimOpacity, 0.55)
	setDuration(&c.Browser.Burnin.ShiftInterval, 20*time.Minute)
	setInt(&c.Browser.Burnin.ShiftPixels, 8)
	setFloat(&c.Browser.Player.Volume, 0.01)
	setDuration(&c.Browser.Player.ReassertInterval, 30*time.Second)

	c.migrateBakedWindowOrigins()

	setString(&c.Auth.Mode, "manual")

	setString(&c.Logging.Level, "info")
	setInt(&c.Logging.MaxSizeMB, 5)

	setInt(&c.Update.IntervalHours, 24)

	// No profiles block = legacy single-profile install: synthesize the one
	// implicit profile rooted at the app dir itself. This gets written back
	// into the profiles block the next time the config is saved.
	if len(c.Profiles) == 0 {
		c.Profiles = []ProfileConfig{{Name: MainProfileName, Dir: ".", Enabled: true}}
	}
}

// migrateBakedWindowOrigins un-pins the window positions the pre-tiling starter
// template baked into every config it wrote: browser.window x/y 40,40 and
// kick.browser.window x/y 700,40. Since the window-tiling default landed, an
// explicit x/y is an OVERRIDE that wins over the computed slot, so those baked
// values stack every added profile's windows on top of the first profile's.
// Both pairs are byte-identical to what the tiler computes for slot 0
// (windowTileDefault(0,0) and (1,0)), so clearing them to 0,0 (= unset) cannot
// change where any existing install's windows land; it only lets profiles in
// other slots tile. A deliberate override that happens to sit one pixel away
// (41,40) is left alone.
func (c *Config) migrateBakedWindowOrigins() {
	if x, y := windowTileDefault(0, 0); c.Browser.Window.X == x && c.Browser.Window.Y == y {
		c.Browser.Window.X, c.Browser.Window.Y = 0, 0
	}
	if x, y := windowTileDefault(1, 0); c.Kick.Browser.Window.X == x && c.Kick.Browser.Window.Y == y {
		c.Kick.Browser.Window.X, c.Kick.Browser.Window.Y = 0, 0
	}
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
	if err := c.validateProfiles(); err != nil {
		return err
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
	if c.anyKickEnabled() {
		// Validate the config the Kick engine will actually run with (its own
		// browser block, player policy inherited from Twitch when unset). The
		// kick block itself is shared, so one check covers every profile that
		// turns it on.
		kc := c.KickRuntimeConfig()
		if v := kc.Browser.Player.Volume; v <= 0 || v > 1 {
			return fmt.Errorf("config: kick.browser.player.volume %v must be in (0,1]", v)
		}
		if o := kc.Browser.Burnin.DimOpacity; o < 0 || o > 1 {
			return fmt.Errorf("config: kick.browser.burnin.dim_opacity %v must be in [0,1]", o)
		}
	}
	if c.Watcher.SoftStall >= c.Watcher.HardStall {
		return fmt.Errorf("config: watcher.soft_stall (%s) must be less than hard_stall (%s)", c.Watcher.SoftStall, c.Watcher.HardStall)
	}
	if j := c.Watcher.HealthTickJitter; j < 0 || j > 100 {
		return fmt.Errorf("config: watcher.health_tick_jitter %d must be a percent in [0,100]", j)
	}
	if j := c.Watcher.TimingJitter; j < 0 || j > 0.5 {
		return fmt.Errorf("config: watcher.timing_jitter %v must be a fraction in [0,0.5]", j)
	}
	if _, err := zerologLevel(c.Logging.Level); err != nil {
		return err
	}
	if c.Update.IntervalHours <= 0 {
		return fmt.Errorf("config: update.interval_hours %d must be greater than 0", c.Update.IntervalHours)
	}
	if feed := c.UpdateFeedURL(); feed != "" {
		u, err := url.Parse(feed)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return fmt.Errorf("config: update.feed_url %q must be an http(s) URL, or empty to disable update checks", feed)
		}
		if h := u.Hostname(); u.Scheme != "https" && h != "localhost" && h != "127.0.0.1" {
			return fmt.Errorf("config: update.feed_url %q must use https (plain http is only allowed for localhost)", feed)
		}
	}
	return nil
}

// validateProfiles walks every profile the same way the root config is
// validated: name shape and uniqueness for all profiles, and the games
// overlay invariant for enabled ones (disabled profiles never run, so their
// overlay does not need to resolve to anything runnable).
func (c *Config) validateProfiles() error {
	seen := make(map[string]bool, len(c.Profiles))
	for i := range c.Profiles {
		p := &c.Profiles[i]
		if !profileNameRe.MatchString(p.Name) {
			return fmt.Errorf("config: profiles[%d].name %q must match [a-z0-9-]{1,24}", i, p.Name)
		}
		if seen[p.Name] {
			return fmt.Errorf("config: profile name %q is not unique", p.Name)
		}
		seen[p.Name] = true
		if !p.Enabled {
			continue
		}
		games, anyGame := c.effectiveGames(p)
		if !anyGame && len(games) == 0 {
			return fmt.Errorf("config: profile %q: games must be non-empty unless any_game is true", p.Name)
		}
	}
	return nil
}

// anyKickEnabled reports whether the root kick block or any enabled profile's
// override turns Kick on, i.e. whether the shared kick browser config needs
// to be valid.
func (c *Config) anyKickEnabled() bool {
	if c.Kick.Enabled {
		return true
	}
	for i := range c.Profiles {
		p := &c.Profiles[i]
		if p.Enabled && c.effectiveKickEnabled(p) {
			return true
		}
	}
	return false
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
