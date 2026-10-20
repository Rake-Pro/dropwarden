package service

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/rake-pro/dropwarden/internal/config"
)

// SettingsService loads and saves the subset of configuration the UI edits.
type SettingsService struct{ b *Backend }

func NewSettingsService(b *Backend) *SettingsService { return &SettingsService{b: b} }

// Load returns the current config as a flat DTO.
func (s *SettingsService) Load() ConfigDTO { return toConfigDTO(s.b.config()) }

// Save applies the DTO onto a COPY of the current config, validates and
// persists that, and only then commits it and hot-reloads the engines. A save
// that fails validation must leave the live config untouched, otherwise the
// rejected values keep echoing back into the form and every later save fails.
func (s *SettingsService) Save(dto ConfigDTO) error {
	cp := s.copyConfig()
	prevChrome := cp.Browser.ChromePath
	applyConfigDTO(cp, dto)
	// A changed Chrome path must point at a file; an unchanged one is left
	// alone so a save from another pane never fails over it.
	if p := strings.TrimSpace(cp.Browser.ChromePath); p != "" && p != prevChrome {
		if fi, err := os.Stat(p); err != nil || fi.IsDir() {
			return fmt.Errorf("config: browser.chrome_path %q was not found - enter the full path to chrome(.exe), or leave it blank to find Chrome automatically", p)
		}
	}

	if err := config.Save(s.b.ConfigPath, cp); err != nil {
		return err
	}
	s.commit(cp)
	return nil
}

// AddProfile appends a new profile and creates its data directories. The name
// is validated by the config validator (lowercase slug, unique) on the copy, so
// a bad name never reaches the live config or the filesystem. The directories
// are created BEFORE the commit, because the commit reconciles the new
// profile's runtime up immediately and its Chrome needs somewhere to live.
func (s *SettingsService) AddProfile(name, displayName string) error {
	cp := s.copyConfig()
	cp.Profiles = append(cp.Profiles, config.NewProfile(name, displayName))
	if err := config.Save(s.b.ConfigPath, cp); err != nil {
		return err
	}
	paths, err := cp.ProfilePaths(name)
	if err != nil {
		return err
	}
	for _, d := range []string{paths.ChromeProfile, paths.ChromeProfileKick, paths.Debug} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return fmt.Errorf("create profile dir %s: %w", d, err)
		}
	}
	s.commit(cp)
	return nil
}

// SetProfileEnabled turns a profile on or off. It takes effect immediately: the
// commit reconciles the profile's runtimes up or down. Disabling keeps the
// profile's store open so the UI can still show its queue and history.
func (s *SettingsService) SetProfileEnabled(name string, on bool) error {
	cp := s.copyConfig()
	p := cp.Profile(name)
	if p == nil {
		return fmt.Errorf("unknown profile %q", name)
	}
	p.Enabled = on
	if err := config.Save(s.b.ConfigPath, cp); err != nil {
		return err
	}
	s.commit(cp)
	return nil
}

// RemoveProfile drops a profile from the config and returns its data
// directory. The commit reconciles its runtimes down and closes its store; the
// DIRECTORY IS NEVER DELETED, so the caller can tell the user where their
// queue, history, and Chrome logins are still sitting. Removing the last
// remaining profile is refused: the app has nothing to run without one.
func (s *SettingsService) RemoveProfile(name string) (string, error) {
	cp := s.copyConfig()
	if cp.Profile(name) == nil {
		return "", fmt.Errorf("unknown profile %q", name)
	}
	if len(cp.Profiles) <= 1 {
		return "", fmt.Errorf("cannot remove %q: it is the only profile - add another one first", name)
	}
	paths, err := cp.ProfilePaths(name)
	if err != nil {
		return "", err
	}
	dataDir := filepath.Dir(paths.DB)
	out := make([]config.ProfileConfig, 0, len(cp.Profiles)-1)
	for _, p := range cp.Profiles {
		if p.Name != name {
			out = append(out, p)
		}
	}
	cp.Profiles = out
	if err := config.Save(s.b.ConfigPath, cp); err != nil {
		return "", err
	}
	s.commit(cp)
	return dataDir, nil
}

// Profiles returns every profile's own settings for the Settings UI (the
// profile group of panes edits them; /api/state carries the same resolved
// values for the views).
func (s *SettingsService) Profiles() []ProfileDTO {
	cfg := s.b.config()
	out := make([]ProfileDTO, 0, len(cfg.Profiles))
	for _, p := range cfg.Profiles {
		eff, err := cfg.Effective(p.Name)
		if err != nil {
			continue
		}
		out = append(out, profileDTO(p, eff))
	}
	return out
}

func profileDTO(p config.ProfileConfig, eff *config.ProfileEffective) ProfileDTO {
	return ProfileDTO{
		Name:                  p.Name,
		DisplayName:           p.DisplayName,
		Enabled:               p.Enabled,
		Games:                 append([]string{}, eff.Games...),
		AnyGame:               eff.AnyGame,
		PriorityMode:          eff.PriorityMode,
		AutoEnqueueNew:        eff.AutoEnqueueNew,
		PreferChannelSpecific: eff.PreferChannelSpecific,
		FallThrough:           !eff.WaitForTop,
		SkipAccountLink:       eff.SkipAccountLink,
		KickEnabled:           eff.KickEnabled,
	}
}

// UpdateProfile saves one profile's own settings in full: display name,
// games and any_game, queue policy, skip-account-link and Kick on/off. Like
// AddProfile, it validates on a copy before committing; a kick or games
// change takes effect immediately (the commit reconciles the runtime and
// reloads the engines). Enabled is not here: SetProfileEnabled owns it.
func (s *SettingsService) UpdateProfile(d ProfileDTO) error {
	cp := s.copyConfig()
	p := cp.Profile(d.Name)
	if p == nil {
		return fmt.Errorf("unknown profile %q", d.Name)
	}
	p.DisplayName = strings.TrimSpace(d.DisplayName)
	games := make([]string, 0, len(d.Games))
	for _, g := range d.Games {
		if g = strings.TrimSpace(g); g != "" {
			games = append(games, g)
		}
	}
	p.Games = games
	p.AnyGame = ptr(d.AnyGame)
	if d.PriorityMode != "" {
		p.Queue.PriorityMode = d.PriorityMode
	}
	p.Queue.AutoEnqueueNew = ptr(d.AutoEnqueueNew)
	p.Queue.PreferChannelSpecific = ptr(d.PreferChannelSpecific)
	p.Queue.WaitForTop = ptr(!d.FallThrough)
	p.Claim.SkipAccountLink = ptr(d.SkipAccountLink)
	p.Kick.Enabled = ptr(d.KickEnabled)
	if err := config.Save(s.b.ConfigPath, cp); err != nil {
		return err
	}
	s.commit(cp)
	return nil
}

// copyConfig returns a mutable copy of the live config, with the slices every
// mutation path touches copied too.
func (s *SettingsService) copyConfig() *config.Config {
	s.b.mu.RLock()
	defer s.b.mu.RUnlock()
	cp := *s.b.cfg
	cp.Games = append([]string(nil), s.b.cfg.Games...)
	cp.Profiles = append([]config.ProfileConfig(nil), s.b.cfg.Profiles...)
	for i := range cp.Profiles {
		cp.Profiles[i].Games = append([]string{}, cp.Profiles[i].Games...)
	}
	return &cp
}

// commit swaps the validated, persisted copy in as the live config, pushes the
// new values into the engines that are already running, and reconciles the set
// of running runtimes against it: an enabled profile, an added profile, or a
// kick toggle starts or stops its runtime without an app restart.
func (s *SettingsService) commit(cp *config.Config) {
	s.b.mu.Lock()
	s.b.cfg = cp
	rec := s.b.rec
	s.b.mu.Unlock()
	s.b.ReloadEngines()
	if rec != nil {
		rec.Reconcile(cp)
	}
	if p := s.b.power(); p != nil {
		p.Refresh()
	}
}

// ResetDefaults restores factory defaults and returns them (not yet persisted;
// the UI calls Save to commit).
func (s *SettingsService) ResetDefaults() (ConfigDTO, error) {
	def, err := config.StarterDefaults()
	if err != nil {
		return ConfigDTO{}, err
	}
	return toConfigDTO(def), nil
}

func toConfigDTO(c *config.Config) ConfigDTO {
	return ConfigDTO{
		Theme:              c.Theme,
		Volume:             c.Browser.Player.Volume,
		ForceLowestQuality: c.Browser.Player.ForceLowestQuality,
		NeverFocus:         c.Browser.Window.NeverFocus,
		AlwaysOnBottom:     c.Browser.Window.AlwaysOnBottom,
		StartMinimized:     c.Browser.Window.StartMinimized,
		DimOpacity:         c.Browser.Burnin.DimOpacity,
		ChromePath:         c.Browser.ChromePath,
		MuteAudio:          c.Browser.MuteAudio,
		ClaimStrategy:      c.Claim.Strategy,
		AllowGQLFallback:   c.Claim.AllowGQLFallback,

		ClaimButtonSelector: ptr(c.ClaimInventorySelector()),
		ClaimButtonText:     ptr(c.Claim.InventoryButtonText),
		AuthMode:            c.Auth.Mode,
		LogLevel:            c.Logging.Level,
		CaptureGQLBodies:    c.Logging.CaptureGQLBodies,

		UpdateEnabled:       ptr(c.UpdateEnabled()),
		UpdateAutoInstall:   ptr(c.UpdateAutoInstall()),
		UpdateShowChangelog: ptr(c.UpdateShowChangelog()),
		UpdateChannel:       ptr(c.UpdateChannel()),
		UpdateFeedURL:       ptr(c.UpdateFeedURL()),
		UpdateIntervalHours: ptr(c.Update.IntervalHours),

		KeepAwake: c.KeepAwakeMode(),
	}
}

func ptr[T any](v T) *T { return &v }

// applyConfigDTO writes the app-wide editable fields from the DTO onto cfg.
// Fields the UI does not expose are left untouched; per-profile fields are
// UpdateProfile's.
func applyConfigDTO(c *config.Config, d ConfigDTO) {
	c.Theme = d.Theme
	c.Browser.Player.Volume = d.Volume
	c.Browser.Player.ForceLowestQuality = d.ForceLowestQuality
	c.Browser.Window.NeverFocus = d.NeverFocus
	c.Browser.Window.AlwaysOnBottom = d.AlwaysOnBottom
	c.Browser.Window.StartMinimized = d.StartMinimized
	c.Browser.Burnin.DimOpacity = d.DimOpacity
	c.Browser.ChromePath = d.ChromePath
	c.Browser.MuteAudio = d.MuteAudio
	c.Claim.Strategy = d.ClaimStrategy
	c.Claim.AllowGQLFallback = d.AllowGQLFallback
	if d.ClaimButtonSelector != nil {
		sel := strings.TrimSpace(*d.ClaimButtonSelector)
		setUpdate(&c.Claim.InventoryButtonSelector, &sel, c.ClaimInventorySelector())
	}
	if d.ClaimButtonText != nil {
		if txt := strings.TrimSpace(*d.ClaimButtonText); txt != "" {
			c.Claim.InventoryButtonText = txt
		} else {
			c.Claim.InventoryButtonText = config.DefaultClaimButtonText
		}
	}
	c.Auth.Mode = d.AuthMode
	c.Logging.Level = d.LogLevel
	c.Logging.CaptureGQLBodies = d.CaptureGQLBodies
	// Fresh pointers, never the DTO's own: the copy must not alias the request.
	// A still-nil field is only pinned when the value differs from its
	// default, so saving another pane (which echoes the resolved values back)
	// does not bake today's defaults into config.yaml.
	setUpdate(&c.Update.Enabled, d.UpdateEnabled, c.UpdateEnabled())
	setUpdate(&c.Update.AutoInstall, d.UpdateAutoInstall, c.UpdateAutoInstall())
	setUpdate(&c.Update.ShowChangelog, d.UpdateShowChangelog, c.UpdateShowChangelog())
	// The channel decides the default feed, and the pane echoes the RESOLVED
	// feed back, so a feed equal to either channel's default is "not pinned":
	// switching channels must not pin the old channel's URL.
	prevDefault := config.DefaultFeedURLFor(c.UpdateChannel())
	if d.UpdateChannel != nil {
		if ch := strings.TrimSpace(*d.UpdateChannel); ch == config.UpdateChannelDev {
			c.Update.Channel = config.UpdateChannelDev
		} else {
			c.Update.Channel = ""
		}
	}
	if d.UpdateFeedURL != nil {
		feed := strings.TrimSpace(*d.UpdateFeedURL)
		if feed == prevDefault || feed == config.DefaultFeedURLFor(c.UpdateChannel()) {
			c.Update.FeedURL = nil
		} else {
			c.Update.FeedURL = ptr(feed)
		}
	}
	if d.UpdateIntervalHours != nil {
		c.Update.IntervalHours = *d.UpdateIntervalHours
	}
	if d.KeepAwake != "" {
		c.Power.KeepAwake = d.KeepAwake
	}
}

// setUpdate applies a DTO value onto an optional config field: nil leaves it
// alone, and an unset field stays unset while the value equals effective.
func setUpdate[T comparable](field **T, v *T, effective T) {
	if v == nil || (*field == nil && *v == effective) {
		return
	}
	*field = ptr(*v)
}
