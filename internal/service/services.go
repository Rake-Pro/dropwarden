package service

import (
	"fmt"
	"sync"
	"time"

	"github.com/rake-pro/dropwarden/internal/config"
	"github.com/rake-pro/dropwarden/internal/domain"
	"github.com/rake-pro/dropwarden/internal/store"
	"github.com/rake-pro/dropwarden/internal/watcher"
)

// ProfileBackend is one profile's slot in the Backend: its own store (opened
// eagerly at startup, so the UI can read a profile before its Chrome is up)
// plus the engine and discoverer handles attached per platform once that
// profile's runtime comes up. A handle stays nil while Chrome starts, or
// forever when that profile failed to launch.
type ProfileBackend struct {
	Name        string
	DisplayName string
	Store       *store.Store

	// startupEnabled/startupKick are what THIS process actually launched with.
	// The live config changes the moment the user saves a toggle, but the
	// runtime only follows on restart; the UI needs both to tell "restart
	// pending" apart from "failed to launch".
	startupEnabled bool
	startupKick    bool

	mu      sync.RWMutex
	engines map[domain.Platform]*watcher.Engine
	discos  map[domain.Platform]watcher.Discoverer
}

// NewProfileBackend builds one profile's slot, snapshotting its enabled/kick
// state as this process started with it.
func NewProfileBackend(cfg *config.Config, name string, st *store.Store) *ProfileBackend {
	p := &ProfileBackend{
		Name: name, DisplayName: name, Store: st,
		engines: map[domain.Platform]*watcher.Engine{},
		discos:  map[domain.Platform]watcher.Discoverer{},
	}
	if eff, err := cfg.Effective(name); err == nil {
		p.DisplayName = eff.DisplayName
		p.startupEnabled = eff.Enabled
		p.startupKick = eff.KickEnabled
	}
	return p
}

// Attach wires one platform's engine and discoverer once that runtime is up.
func (p *ProfileBackend) Attach(plat domain.Platform, e *watcher.Engine, d watcher.Discoverer) {
	p.mu.Lock()
	p.engines[plat.OrTwitch()] = e
	p.discos[plat.OrTwitch()] = d
	p.mu.Unlock()
}

// Engine returns the profile's engine for a platform, or nil when that runtime
// is not up.
func (p *ProfileBackend) Engine(plat domain.Platform) *watcher.Engine {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.engines[plat.OrTwitch()]
}

// Discoverer returns the profile's discoverer for a platform, or nil.
func (p *ProfileBackend) Discoverer(plat domain.Platform) watcher.Discoverer {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.discos[plat.OrTwitch()]
}

// Engines returns the profile's running engines, twitch first.
func (p *ProfileBackend) Engines() []*watcher.Engine {
	var out []*watcher.Engine
	for _, plat := range platformOrder {
		if e := p.Engine(plat); e != nil {
			out = append(out, e)
		}
	}
	return out
}

// platformOrder is the canonical platform order for every uniform per-platform
// listing (state, debug, tray).
var platformOrder = []domain.Platform{domain.PlatformTwitch, domain.PlatformKick}

// Backend is the shared state the bound services operate over. It is populated
// by the app at startup: one ProfileBackend per profile whose store was
// opened, in config order.
type Backend struct {
	ConfigPath string

	Profiles map[string]*ProfileBackend
	Order    []string

	mu  sync.RWMutex
	cfg *config.Config
}

// NewBackend builds a Backend over the profiles this process opened.
func NewBackend(cfg *config.Config, cfgPath string, profiles ...*ProfileBackend) *Backend {
	b := &Backend{ConfigPath: cfgPath, cfg: cfg, Profiles: map[string]*ProfileBackend{}}
	for _, p := range profiles {
		b.Profiles[p.Name] = p
		b.Order = append(b.Order, p.Name)
	}
	return b
}

// Profile returns the named profile's backend, or nil when this process did
// not open it (disabled at start, or added since).
func (b *Backend) Profile(name string) *ProfileBackend {
	if name == "" {
		return b.FirstProfile()
	}
	return b.Profiles[name]
}

// FirstProfile returns the first opened profile: the only one in a
// single-profile install, and the default target for calls that omit a profile.
func (b *Backend) FirstProfile() *ProfileBackend {
	if len(b.Order) == 0 {
		return nil
	}
	return b.Profiles[b.Order[0]]
}

// ProfileInfo names one configured profile for label-building callers (tray).
type ProfileInfo struct {
	Name        string
	DisplayName string
}

// ProfileList returns every CONFIGURED profile in config order, including ones
// this process did not open: the tray builds an item set per pair at startup
// and shows each one only while its engine exists.
func (b *Backend) ProfileList() []ProfileInfo {
	cfg := b.config()
	out := make([]ProfileInfo, 0, len(cfg.Profiles))
	for _, p := range cfg.Profiles {
		eff, err := cfg.Effective(p.Name)
		if err != nil {
			continue
		}
		out = append(out, ProfileInfo{Name: eff.Name, DisplayName: eff.DisplayName})
	}
	return out
}

// EngineFor returns the engine driving one (profile, platform) pair, or nil
// when that runtime is not up. The zero platform normalizes to Twitch.
func (b *Backend) EngineFor(profile string, plat domain.Platform) *watcher.Engine {
	p := b.Profile(profile)
	if p == nil {
		return nil
	}
	return p.Engine(plat)
}

// AllEngines returns every running engine across every profile.
func (b *Backend) AllEngines() []*watcher.Engine {
	var out []*watcher.Engine
	for _, name := range b.Order {
		out = append(out, b.Profiles[name].Engines()...)
	}
	return out
}

// ReloadEngines pushes the current config into every running engine, per
// profile: each engine gets its own profile's derived config (games overlay,
// and for Kick its own browser block and gentler watchdogs), so no engine ever
// receives the root config verbatim.
func (b *Backend) ReloadEngines() {
	cfg := b.config()
	for _, name := range b.Order {
		p := b.Profiles[name]
		if e := p.Engine(domain.PlatformTwitch); e != nil {
			if pc, err := cfg.ProfileRuntimeConfig(name); err == nil {
				e.ReloadConfig(pc)
			}
		}
		if e := p.Engine(domain.PlatformKick); e != nil {
			if kc, err := cfg.ProfileKickRuntimeConfig(name); err == nil {
				e.ReloadConfig(kc)
			}
		}
	}
}

func (b *Backend) config() *config.Config {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.cfg
}

// storeFor resolves a profile's store, erroring on a profile this process did
// not open.
func (b *Backend) storeFor(profile string) (*store.Store, error) {
	p := b.Profile(profile)
	if p == nil || p.Store == nil {
		return nil, fmt.Errorf("unknown profile %q", profile)
	}
	return p.Store, nil
}

// ---- CampaignService ----

type CampaignService struct {
	b           *Backend
	lastRefresh time.Time
	mu          sync.Mutex
}

func NewCampaignService(b *Backend) *CampaignService { return &CampaignService{b: b} }

func (s *CampaignService) List(profile string) ([]CampaignDTO, error) {
	st, err := s.b.storeFor(profile)
	if err != nil {
		return nil, err
	}
	campaigns, err := st.ListCampaigns()
	if err != nil {
		return nil, err
	}
	out := make([]CampaignDTO, 0, len(campaigns))
	for _, c := range campaigns {
		out = append(out, s.toDTO(st, c))
	}
	return out, nil
}

func (s *CampaignService) toDTO(st *store.Store, c domain.Campaign) CampaignDTO {
	drops := make([]DropDTO, 0, len(c.Drops))
	for _, d := range c.Drops {
		dd := DropDTO{ID: d.ID, Name: d.Name, RequiredMinutes: d.RequiredMinutes}
		if p, err := st.GetProgress(d.ID); err == nil && p != nil {
			dd.CurrentMinutes, _ = p.Effective()
			dd.Claimed = p.ClaimedAt != nil
		}
		drops = append(drops, dd)
	}
	return CampaignDTO{
		ID: c.ID, Name: c.Name, Game: c.GameName, Status: string(c.Status),
		EndAt: c.EndAt, AnyChannel: c.AnyChannel(), ChannelCount: len(c.AllowedChannels),
		AccountLinkURL: c.AccountLinkURL, Drops: drops,
	}
}

// RefreshNow asks a profile's engines to run discovery.
func (s *CampaignService) RefreshNow(profile string) error {
	p := s.b.Profile(profile)
	if p == nil {
		return fmt.Errorf("unknown profile %q", profile)
	}
	engines := p.Engines()
	if len(engines) == 0 {
		return fmt.Errorf("watcher not running")
	}
	for _, e := range engines {
		e.RefreshNow()
	}
	s.mu.Lock()
	s.lastRefresh = time.Now()
	s.mu.Unlock()
	return nil
}

func (s *CampaignService) LastRefresh() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastRefresh
}

// ---- QueueService ----

type QueueService struct{ b *Backend }

func NewQueueService(b *Backend) *QueueService { return &QueueService{b: b} }

func (s *QueueService) Get(profile string) ([]QueueItemDTO, error) {
	st, err := s.b.storeFor(profile)
	if err != nil {
		return nil, err
	}
	items, err := st.GetQueue()
	if err != nil {
		return nil, err
	}
	campaigns, _ := st.ListCampaigns()
	cmap := map[string]domain.Campaign{}
	dmap := map[string]domain.Drop{}
	for _, c := range campaigns {
		cmap[c.ID] = c
		for _, d := range c.Drops {
			dmap[d.ID] = d
		}
	}
	out := make([]QueueItemDTO, 0, len(items))
	for _, it := range items {
		d := dmap[it.DropID]
		dto := QueueItemDTO{
			DropID: it.DropID, CampaignID: it.CampaignID, Platform: string(it.Platform.OrTwitch()),
			DropName: d.Name,
			Game:     cmap[it.CampaignID].GameName, Position: it.Position, Enabled: it.Enabled,
			Status: string(it.Status), Reason: it.Reason, RequiredMinutes: d.RequiredMinutes,
		}
		if p, err := st.GetProgress(it.DropID); err == nil && p != nil {
			dto.CurrentMinutes, _ = p.Effective()
		}
		out = append(out, dto)
	}
	return out, nil
}

func (s *QueueService) SetOrder(profile string, dropIDs []string) error {
	st, err := s.b.storeFor(profile)
	if err != nil {
		return err
	}
	return st.SetQueueOrder(dropIDs)
}

func (s *QueueService) SetEnabled(profile, dropID string, on bool) error {
	st, err := s.b.storeFor(profile)
	if err != nil {
		return err
	}
	return st.SetItemEnabled(dropID, on)
}

func (s *QueueService) ClearBlacklist(profile string) error {
	st, err := s.b.storeFor(profile)
	if err != nil {
		return err
	}
	return st.ClearBlacklist()
}

// SetPriorityMode is a root (shared) setting: every profile works its queue the
// same way.
func (s *QueueService) SetPriorityMode(mode string) error {
	if !domain.ValidPriorityMode(domain.PriorityMode(mode)) {
		return fmt.Errorf("invalid priority mode %q", mode)
	}
	cfg := s.b.config()
	cfg.Queue.PriorityMode = mode
	if err := config.Save(s.b.ConfigPath, cfg); err != nil {
		return err
	}
	s.b.ReloadEngines()
	return nil
}

// ---- WatcherService ----

type WatcherService struct{ b *Backend }

func NewWatcherService(b *Backend) *WatcherService { return &WatcherService{b: b} }

func (s *WatcherService) Status(profile string, plat domain.Platform) watcher.Status {
	eng := s.b.EngineFor(profile, plat)
	if eng == nil {
		return watcher.Status{Profile: profile, Platform: string(plat.OrTwitch()), State: watcher.StateIdle}
	}
	return eng.Status()
}

func (s *WatcherService) Start(profile string, plat domain.Platform) error {
	return s.engineDo(profile, plat, func(e *watcher.Engine) { e.Start() })
}

func (s *WatcherService) Stop(profile string, plat domain.Platform) error {
	return s.engineDo(profile, plat, func(e *watcher.Engine) { e.Stop() })
}

func (s *WatcherService) SkipCurrent(profile string, plat domain.Platform) error {
	return s.engineDo(profile, plat, func(e *watcher.Engine) { e.SkipCurrent() })
}

func (s *WatcherService) ForceRefresh(profile string, plat domain.Platform) error {
	return s.engineDo(profile, plat, func(e *watcher.Engine) { e.RefreshNow() })
}

func (s *WatcherService) engineDo(profile string, plat domain.Platform, f func(*watcher.Engine)) error {
	eng := s.b.EngineFor(profile, plat)
	if eng == nil {
		return fmt.Errorf("watcher not running")
	}
	f(eng)
	return nil
}

// ---- HistoryService ----

type HistoryService struct{ b *Backend }

func NewHistoryService(b *Backend) *HistoryService { return &HistoryService{b: b} }

func (s *HistoryService) Claims(profile string) ([]ClaimDTO, error) {
	st, err := s.b.storeFor(profile)
	if err != nil {
		return nil, err
	}
	claims, err := st.ListClaims(time.Time{}, time.Time{})
	if err != nil {
		return nil, err
	}
	out := make([]ClaimDTO, 0, len(claims))
	for _, c := range claims {
		out = append(out, ClaimDTO{DropID: c.DropID, Platform: string(c.Platform.OrTwitch()), DropName: c.DropName, Game: c.GameName, ClaimedAt: c.ClaimedAt})
	}
	return out, nil
}

func (s *HistoryService) Sessions(profile string, limit int) ([]SessionDTO, error) {
	st, err := s.b.storeFor(profile)
	if err != nil {
		return nil, err
	}
	sessions, err := st.ListSessions(limit)
	if err != nil {
		return nil, err
	}
	out := make([]SessionDTO, 0, len(sessions))
	for _, ws := range sessions {
		out = append(out, SessionDTO{
			ID: ws.ID, Platform: string(ws.Platform.OrTwitch()), DropID: ws.DropID, ChannelLogin: ws.ChannelLogin,
			StartedAt: ws.StartedAt, EndedAt: ws.EndedAt, Minutes: ws.DurationMinutes(), Outcome: string(ws.Outcome),
		})
	}
	return out, nil
}

func (s *HistoryService) Stats(profile string) (StatsDTO, error) {
	st, err := s.b.storeFor(profile)
	if err != nil {
		return StatsDTO{}, err
	}
	claims, err := st.ListClaims(time.Time{}, time.Time{})
	if err != nil {
		return StatsDTO{}, err
	}
	sessions, err := st.ListSessions(0)
	if err != nil {
		return StatsDTO{}, err
	}
	stats := StatsDTO{Sessions: len(sessions), OutcomeCounts: map[string]int{}, ByPlatform: map[string]PlatformTotalsDTO{}}
	plat := func(p domain.Platform) string { return string(p.OrTwitch()) }
	bump := func(key string, f func(*PlatformTotalsDTO)) {
		t := stats.ByPlatform[key]
		f(&t)
		stats.ByPlatform[key] = t
	}
	// Drops are platform-tagged via their campaign; progress rows only carry the
	// drop ID, so build the mapping once for the earned tally below.
	dropPlatform := map[string]string{}
	if campaigns, err := st.ListCampaigns(); err == nil {
		for _, c := range campaigns {
			for _, d := range c.Drops {
				dropPlatform[d.ID] = plat(c.Platform)
			}
		}
	}
	// Earned = confirmed claims plus watch-complete drops still awaiting a
	// claim (failed auto-claim, or completed in the background of a
	// simultaneous multi-drop campaign).
	stats.DropsClaimed = len(claims)
	stats.DropsEarned = len(claims)
	claimedSet := make(map[string]bool, len(claims))
	for _, c := range claims {
		claimedSet[c.DropID] = true
		bump(plat(c.Platform), func(t *PlatformTotalsDTO) { t.DropsClaimed++; t.DropsEarned++ })
	}
	if progress, err := st.ListProgress(); err == nil {
		for _, p := range progress {
			if !claimedSet[p.DropID] && p.ClaimedAt == nil && p.Complete() {
				stats.DropsEarned++
				// Campaign gone from the store = platform unknown; count it in the
				// global total only rather than misattributing it to Twitch.
				if key := dropPlatform[p.DropID]; key != "" {
					bump(key, func(t *PlatformTotalsDTO) { t.DropsEarned++ })
				}
			}
		}
	}
	success := 0
	for _, ws := range sessions {
		// Wall-clock time on stream, NOT per-drop credited minutes: drops accrue
		// simultaneously, so credited deltas badly undercount time watched.
		mins := ws.DurationMinutes()
		stats.MinutesWatched += mins
		stats.OutcomeCounts[string(ws.Outcome)]++
		bump(plat(ws.Platform), func(t *PlatformTotalsDTO) { t.MinutesWatched += mins; t.Sessions++ })
		if ws.Outcome == domain.OutcomeClaimed || ws.Outcome == domain.OutcomeCompleted {
			success++
		}
	}
	if len(sessions) > 0 {
		stats.SuccessRate = success * 100 / len(sessions)
	}
	// Overlay this profile's in-flight sessions so totals tick live: recorded
	// sessions only land in the store when they END, which otherwise freezes the
	// numbers for hours mid-watch. Success rate stays completed-sessions-only.
	if p := s.b.Profile(profile); p != nil {
		for _, eng := range p.Engines() {
			if es := eng.Status(); es.State == watcher.StateWatching && !es.SessionStartedAt.IsZero() {
				mins := int(time.Since(es.SessionStartedAt).Minutes())
				stats.MinutesWatched += mins
				stats.Sessions++
				bump(plat(domain.Platform(es.Platform)), func(t *PlatformTotalsDTO) { t.MinutesWatched += mins; t.Sessions++ })
			}
		}
	}
	return stats, nil
}
