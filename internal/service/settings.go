package service

import (
	"github.com/rake-pro/dropwarden/internal/config"
)

// SettingsService loads and saves the subset of configuration the UI edits.
type SettingsService struct{ b *Backend }

func NewSettingsService(b *Backend) *SettingsService { return &SettingsService{b: b} }

// Load returns the current config as a flat DTO.
func (s *SettingsService) Load() ConfigDTO { return toConfigDTO(s.b.config()) }

// Save applies the DTO onto the current config, validates and persists it, and
// hot-reloads the engine.
func (s *SettingsService) Save(dto ConfigDTO) error {
	s.b.mu.Lock()
	cfg := s.b.cfg
	applyConfigDTO(cfg, dto)
	s.b.mu.Unlock()

	if err := config.Save(s.b.ConfigPath, cfg); err != nil {
		return err
	}
	if eng := s.b.Engine(); eng != nil {
		eng.ReloadConfig(cfg)
	}
	return nil
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
		StartMinimized:     c.Browser.Window.StartMinimized,
		DimOpacity:         c.Browser.Burnin.DimOpacity,
		ChromePath:         c.Browser.ChromePath,
		MuteAudio:          c.Browser.MuteAudio,
		ClaimStrategy:      c.Claim.Strategy,
		AllowGQLFallback:   c.Claim.AllowGQLFallback,
		SkipAccountLink:    c.Claim.SkipAccountLink,
		AuthMode:           c.Auth.Mode,
		StoredEnabled:      c.Auth.Stored.Enabled,
		LogLevel:           c.Logging.Level,
		CaptureGQLBodies:   c.Logging.CaptureGQLBodies,
	}
}

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
	c.Browser.Window.StartMinimized = d.StartMinimized
	c.Browser.Burnin.DimOpacity = d.DimOpacity
	c.Browser.ChromePath = d.ChromePath
	c.Browser.MuteAudio = d.MuteAudio
	c.Claim.Strategy = d.ClaimStrategy
	c.Claim.AllowGQLFallback = d.AllowGQLFallback
	c.Claim.SkipAccountLink = d.SkipAccountLink
	c.Auth.Mode = d.AuthMode
	c.Auth.Stored.Enabled = d.StoredEnabled
	c.Logging.Level = d.LogLevel
	c.Logging.CaptureGQLBodies = d.CaptureGQLBodies
}
