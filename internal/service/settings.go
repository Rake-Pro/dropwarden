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
	cp.Profiles = append(cp.Profiles, config.ProfileConfig{Name: name, DisplayName: displayName, Enabled: true})
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

// Profiles returns every profile's editable overlay for the Profiles settings
// pane - the Profiles pane needs the raw per-profile overrides (games list,
// whether it inherits the shared one, kick override), not the resolved values
// /api/state already reports.
func (s *SettingsService) Profiles() []ProfileDTO {
	s.b.mu.RLock()
	cfg := s.b.cfg
	s.b.mu.RUnlock()
	out := make([]ProfileDTO, 0, len(cfg.Profiles))
	for _, p := range cfg.Profiles {
		kickEnabled := cfg.Kick.Enabled
		if eff, err := cfg.Effective(p.Name); err == nil {
			kickEnabled = eff.KickEnabled
		}
		out = append(out, ProfileDTO{
			Name:         p.Name,
			DisplayName:  p.DisplayName,
			Enabled:      p.Enabled,
			Games:        append([]string(nil), p.Games...),
			InheritGames: len(p.Games) == 0 && p.AnyGame == nil,
			KickEnabled:  kickEnabled,
		})
	}
	return out
}

// UpdateProfile saves one profile's editable overlay: display name, kick
// on/off, and the games filter (or inherit-the-shared-list when inheritGames
// is true). Like AddProfile, it validates on a copy before committing; a kick
// change takes effect immediately (the commit reconciles that runtime up or
// down).
func (s *SettingsService) UpdateProfile(name, displayName string, kickEnabled bool, games []string, inheritGames bool) error {
	cp := s.copyConfig()
	p := cp.Profile(name)
	if p == nil {
		return fmt.Errorf("unknown profile %q", name)
	}
	p.DisplayName = displayName
	// Only pin a kick override when it differs from the shared default: the
	// pane shows the RESOLVED value, so writing it back verbatim would turn
	// every inherited profile into a pinned one and the shared Platforms-pane
	// kick toggle would silently stop affecting it.
	if kickEnabled == cp.Kick.Enabled {
		p.Kick.Enabled = nil
	} else {
		p.Kick.Enabled = &kickEnabled
	}
	if inheritGames {
		p.Games = nil
		p.AnyGame = nil
	} else {
		p.Games = append([]string(nil), games...)
	}
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
		Games:              append([]string(nil), c.Games...),
		AnyGame:            c.AnyGame,
		PriorityMode:       c.Queue.PriorityMode,
		AutoEnqueueNew:     c.Queue.AutoEnqueueNew,
		FallThrough:        !c.Watcher.WaitForTop,
		Volume:             c.Browser.Player.Volume,
		ForceLowestQuality: c.Browser.Player.ForceLowestQuality,
		NeverFocus:         c.Browser.Window.NeverFocus,
		AlwaysOnBottom:     c.Browser.Window.AlwaysOnBottom,
		StartMinimized:     c.Browser.Window.StartMinimized,
		DimOpacity:         c.Browser.Burnin.DimOpacity,
		ChromePath:         c.Browser.ChromePath,
		MuteAudio:          c.Browser.MuteAudio,
		KickEnabled:        c.Kick.Enabled,
		ClaimStrategy:      c.Claim.Strategy,
		AllowGQLFallback:   c.Claim.AllowGQLFallback,
		SkipAccountLink:    c.Claim.SkipAccountLink,

		ClaimButtonSelector: ptr(c.ClaimInventorySelector()),
		ClaimButtonText:     ptr(c.Claim.InventoryButtonText),
		AuthMode:            c.Auth.Mode,
		StoredEnabled:       c.Auth.Stored.Enabled,
		LogLevel:            c.Logging.Level,
		CaptureGQLBodies:    c.Logging.CaptureGQLBodies,

		PreferChannelSpecific: c.PreferChannelSpecific(),

		UpdateEnabled:       ptr(c.UpdateEnabled()),
		UpdateAutoInstall:   ptr(c.UpdateAutoInstall()),
		UpdateShowChangelog: ptr(c.UpdateShowChangelog()),
		UpdateFeedURL:       ptr(c.UpdateFeedURL()),
		UpdateIntervalHours: ptr(c.Update.IntervalHours),

		KeepAwake: c.KeepAwakeMode(),
	}
}

func ptr[T any](v T) *T { return &v }

// applyConfigDTO writes editable fields from the DTO onto cfg. Fields the UI
// does not expose are left untouched.
func applyConfigDTO(c *config.Config, d ConfigDTO) {
	c.Theme = d.Theme
	c.Games = append([]string(nil), d.Games...)
	c.AnyGame = d.AnyGame
	c.Queue.PriorityMode = d.PriorityMode
	c.Queue.AutoEnqueueNew = d.AutoEnqueueNew
	c.Watcher.WaitForTop = !d.FallThrough
	c.Browser.Player.Volume = d.Volume
	c.Browser.Player.ForceLowestQuality = d.ForceLowestQuality
	c.Browser.Window.NeverFocus = d.NeverFocus
	c.Browser.Window.AlwaysOnBottom = d.AlwaysOnBottom
	c.Browser.Window.StartMinimized = d.StartMinimized
	c.Browser.Burnin.DimOpacity = d.DimOpacity
	c.Browser.ChromePath = d.ChromePath
	c.Browser.MuteAudio = d.MuteAudio
	c.Kick.Enabled = d.KickEnabled
	c.Claim.Strategy = d.ClaimStrategy
	c.Claim.AllowGQLFallback = d.AllowGQLFallback
	c.Claim.SkipAccountLink = d.SkipAccountLink
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
	c.Auth.Stored.Enabled = d.StoredEnabled
	c.Logging.Level = d.LogLevel
	c.Logging.CaptureGQLBodies = d.CaptureGQLBodies
	// Fresh pointers, never the DTO's own: the copy must not alias the request.
	// A still-nil field is only pinned when the value differs from its
	// default, so saving another pane (which echoes the resolved values back)
	// does not bake today's defaults into config.yaml.
	setUpdate(&c.Update.Enabled, d.UpdateEnabled, c.UpdateEnabled())
	setUpdate(&c.Update.AutoInstall, d.UpdateAutoInstall, c.UpdateAutoInstall())
	setUpdate(&c.Update.ShowChangelog, d.UpdateShowChangelog, c.UpdateShowChangelog())
	if d.UpdateFeedURL != nil {
		feed := strings.TrimSpace(*d.UpdateFeedURL)
		setUpdate(&c.Update.FeedURL, &feed, c.UpdateFeedURL())
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
