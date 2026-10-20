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

// applyDefaults fills any zero-valued field with its default. It is safe to call
// on a fully or partially populated Config.
func (c *Config) applyDefaults() {
	setString(&c.Theme, "dark")
	// The root games list is only a seed for profiles that predate their own
	// (materializeProfiles); absent means nothing picked.
	if c.Games == nil {
		c.Games = []string{}
	}

	setString(&c.Queue.PriorityMode, string(domain.PriorityTopDown))
	setString(&c.Power.KeepAwake, KeepAwakeWhileWatching)
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
	c.materializeProfiles()
}

// materializeProfiles completes every profile from the root's legacy values:
// a profile with no games filter of its own takes the root games/any_game, and
// every unset queue, claim and kick field takes the root's. This is the one
// place inheritance happens, and it happens once: after it every profile is
// complete, Save writes it in full, and a later root edit changes nothing for
// anyone. A profile written by this version never has a nil here.
func (c *Config) materializeProfiles() {
	for i := range c.Profiles {
		p := &c.Profiles[i]
		if p.Games == nil && p.AnyGame == nil {
			p.Games = append([]string{}, c.Games...)
			p.AnyGame = ptr(c.AnyGame)
		}
		if p.Games == nil {
			p.Games = []string{}
		}
		if p.AnyGame == nil {
			p.AnyGame = ptr(false)
		}
		setString(&p.Queue.PriorityMode, c.Queue.PriorityMode)
		setBool(&p.Queue.AutoEnqueueNew, c.Queue.AutoEnqueueNew)
		setBool(&p.Queue.PreferChannelSpecific, c.PreferChannelSpecific())
		setBool(&p.Queue.WaitForTop, c.Watcher.WaitForTop)
		setBool(&p.Claim.SkipAccountLink, c.Claim.SkipAccountLink)
		setBool(&p.Kick.Enabled, c.Kick.Enabled)
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
	switch c.Power.KeepAwake {
	case KeepAwakeWhileWatching, KeepAwakeAlways, KeepAwakeNever:
	default:
		return fmt.Errorf("config: power.keep_awake %q must be while_watching|always|never", c.Power.KeepAwake)
	}
	// An empty games list is valid: the starter config ships "games: []" and
	// nothing is queued until the user picks games after the first scan.
	if err := c.validateProfiles(); err != nil {
		return err
	}
	switch c.Claim.Strategy {
	case "in_page", "in_page_then_gql":
	default:
		return fmt.Errorf("config: claim.strategy %q must be in_page|in_page_then_gql", c.Claim.Strategy)
	}
	if c.Auth.Mode != "manual" {
		return fmt.Errorf("config: auth.mode %q must be manual (stored credentials are not supported)", c.Auth.Mode)
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
	switch c.Update.Channel {
	case "", UpdateChannelStable, UpdateChannelDev:
	default:
		return fmt.Errorf("config: update.channel %q must be stable|dev", c.Update.Channel)
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
// validated: name shape and uniqueness, and a recognized queue order. An
// empty games list is valid (nothing is queued until games are picked).
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
		// "" is a profile built in code and not yet materialized (Effective
		// falls back to the root); a loaded profile always has one.
		if m := p.Queue.PriorityMode; m != "" && !domain.ValidPriorityMode(domain.PriorityMode(m)) {
			return fmt.Errorf("config: profiles[%d].queue.priority_mode %q invalid", i, m)
		}
	}
	return nil
}

// anyKickEnabled reports whether any enabled profile runs Kick, i.e. whether
// the shared kick browser config needs to be valid.
func (c *Config) anyKickEnabled() bool {
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
func setBool(p **bool, def bool) {
	if *p == nil {
		*p = ptr(def)
	}
}
func ptr[T any](v T) *T { return &v }
