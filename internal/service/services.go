package service

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/rake-pro/dropwarden/internal/browser"
	"github.com/rake-pro/dropwarden/internal/config"
	"github.com/rake-pro/dropwarden/internal/domain"
	"github.com/rake-pro/dropwarden/internal/store"
	"github.com/rake-pro/dropwarden/internal/twitch"
	"github.com/rake-pro/dropwarden/internal/watcher"
)

// ProfileBackend is one profile's slot in the Backend: its own store (opened
// eagerly at startup, so the UI can read a profile before its Chrome is up)
// plus the engine, discoverer, and session handles attached per platform once
// that profile's runtime comes up. A handle stays nil while Chrome starts, and
// is dropped again when the reconciler stops that runtime.
type ProfileBackend struct {
	Name        string
	DisplayName string
	Store       *store.Store

	mu         sync.RWMutex
	engines    map[domain.Platform]*watcher.Engine
	discos     map[domain.Platform]watcher.Discoverer
	sessions   map[domain.Platform]browser.Session
	needsLogin map[domain.Platform]bool
}

// NewProfileBackend builds one profile's slot.
func NewProfileBackend(cfg *config.Config, name string, st *store.Store) *ProfileBackend {
	p := &ProfileBackend{
		Name: name, DisplayName: name, Store: st,
		engines:    map[domain.Platform]*watcher.Engine{},
		discos:     map[domain.Platform]watcher.Discoverer{},
		sessions:   map[domain.Platform]browser.Session{},
		needsLogin: map[domain.Platform]bool{},
	}
	if eff, err := cfg.Effective(name); err == nil {
		p.DisplayName = eff.DisplayName
	}
	return p
}

// Attach wires one platform's engine, discoverer, and session once that runtime
// is up. needsLogin marks a runtime that came up against a fresh Chrome profile:
// its engine is paused and the UI must walk the user through the manual login.
func (p *ProfileBackend) Attach(plat domain.Platform, e *watcher.Engine, d watcher.Discoverer, sess browser.Session, needsLogin bool) {
	p.mu.Lock()
	p.engines[plat.OrTwitch()] = e
	p.discos[plat.OrTwitch()] = d
	p.sessions[plat.OrTwitch()] = sess
	p.needsLogin[plat.OrTwitch()] = needsLogin
	p.mu.Unlock()
}

// Detach drops one platform's handles when its runtime stops.
func (p *ProfileBackend) Detach(plat domain.Platform) {
	p.mu.Lock()
	delete(p.engines, plat.OrTwitch())
	delete(p.discos, plat.OrTwitch())
	delete(p.sessions, plat.OrTwitch())
	delete(p.needsLogin, plat.OrTwitch())
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

// Session returns the profile's browser session for a platform, or nil.
func (p *ProfileBackend) Session(plat domain.Platform) browser.Session {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.sessions[plat.OrTwitch()]
}

// NeedsLogin reports whether that platform's session is still waiting for its
// first manual login.
func (p *ProfileBackend) NeedsLogin(plat domain.Platform) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.needsLogin[plat.OrTwitch()]
}

// ClearNeedsLogin drops the needs-login flag: the login probe came back
// authenticated, or the user started the engine anyway.
func (p *ProfileBackend) ClearNeedsLogin(plat domain.Platform) {
	p.mu.Lock()
	delete(p.needsLogin, plat.OrTwitch())
	p.mu.Unlock()
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

// Reconciler applies the desired state in a newly committed config to the
// running runtimes (internal/app implements it). It is nil in the CLI and in
// tests that do not exercise the lifecycle.
type Reconciler interface {
	Reconcile(cfg *config.Config)
}

// Backend is the shared state the bound services operate over. It is populated
// by the app at startup (one ProfileBackend per profile whose store was opened,
// in config order) and kept in step afterwards by the reconciler, which adds
// and removes profiles and attaches and detaches their runtimes live.
type Backend struct {
	ConfigPath string

	mu       sync.RWMutex
	cfg      *config.Config
	rec      Reconciler
	profiles map[string]*ProfileBackend
	order    []string
}

// NewBackend builds a Backend over the profiles this process opened.
func NewBackend(cfg *config.Config, cfgPath string, profiles ...*ProfileBackend) *Backend {
	b := &Backend{ConfigPath: cfgPath, cfg: cfg, profiles: map[string]*ProfileBackend{}}
	for _, p := range profiles {
		b.profiles[p.Name] = p
		b.order = append(b.order, p.Name)
	}
	return b
}

// SetReconciler installs the runtime reconciler every config commit triggers.
// markSessionReady persists a needs-login answer through the reconciler when it
// supports that (internal/app does; the recording fakes in tests do not) - the
// in-memory flag is cleared by the caller, this only stops the prompt from
// coming back on the next boot.
func (b *Backend) markSessionReady(profile string, plat domain.Platform) {
	b.mu.RLock()
	rec := b.rec
	b.mu.RUnlock()
	if m, ok := rec.(interface {
		MarkSessionReady(profile string, plat domain.Platform)
	}); ok {
		m.MarkSessionReady(profile, plat)
	}
}

func (b *Backend) SetReconciler(r Reconciler) {
	b.mu.Lock()
	b.rec = r
	b.mu.Unlock()
}

// AddProfile registers a profile the reconciler has just opened a store for.
func (b *Backend) AddProfile(name string, st *store.Store) {
	b.mu.Lock()
	cfg := b.cfg
	if _, ok := b.profiles[name]; !ok {
		b.profiles[name] = NewProfileBackend(cfg, name, st)
		b.order = append(b.order, name)
	}
	b.mu.Unlock()
}

// RemoveProfile forgets a profile whose store the reconciler is about to close.
func (b *Backend) RemoveProfile(name string) {
	b.mu.Lock()
	delete(b.profiles, name)
	out := b.order[:0]
	for _, n := range b.order {
		if n != name {
			out = append(out, n)
		}
	}
	b.order = out
	b.mu.Unlock()
}

// Attach publishes one live (profile, platform) runtime (app.Binder).
func (b *Backend) Attach(profile string, plat domain.Platform, e *watcher.Engine, d watcher.Discoverer, sess browser.Session, needsLogin bool) {
	if p := b.Profile(profile); p != nil {
		p.Attach(plat, e, d, sess, needsLogin)
	}
}

// Detach drops one (profile, platform) runtime that has stopped (app.Binder).
func (b *Backend) Detach(profile string, plat domain.Platform) {
	if p := b.Profile(profile); p != nil {
		p.Detach(plat)
	}
}

// Profile returns the named profile's backend, or nil when this process has no
// store open for it (disabled, or its store failed to open).
func (b *Backend) Profile(name string) *ProfileBackend {
	if name == "" {
		return b.FirstProfile()
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.profiles[name]
}

// FirstProfile returns the first opened profile: the only one in a
// single-profile install, and the default target for calls that omit a profile.
func (b *Backend) FirstProfile() *ProfileBackend {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if len(b.order) == 0 {
		return nil
	}
	return b.profiles[b.order[0]]
}

// openOrder returns the names of the profiles this process has stores open for.
func (b *Backend) openOrder() []string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return append([]string(nil), b.order...)
}

// ProfileInfo names one configured profile for label-building callers (tray).
type ProfileInfo struct {
	Name        string
	DisplayName string
}

// ProfileList returns every CONFIGURED profile in config order, including ones
// with no runtime up: the tray builds an item set per pair at startup and shows
// each one only while its engine exists.
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
	for _, name := range b.openOrder() {
		if p := b.Profile(name); p != nil {
			out = append(out, p.Engines()...)
		}
	}
	return out
}

// ReloadEngines pushes the current config into every running engine, per
// profile: each engine gets its own profile's derived config (games overlay,
// and for Kick its own browser block and gentler watchdogs), so no engine ever
// receives the root config verbatim. Values that are only read when Chrome is
// LAUNCHED (chrome_path, extra_flags, profile_dir, window geometry, mute_audio,
// start_minimized) reach a running session on its next launch, not here.
func (b *Backend) ReloadEngines() {
	cfg := b.config()
	for _, name := range b.openOrder() {
		p := b.Profile(name)
		if p == nil {
			continue
		}
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

// storeFor resolves a profile's store, erroring on a profile this process has
// no store open for.
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
	// Starting by hand answers the needs-login prompt: either the user logged
	// in, or they want the engine to work anyway. Either way stop nagging.
	if p := s.b.Profile(profile); p != nil && p.NeedsLogin(plat) {
		p.ClearNeedsLogin(plat)
		s.b.markSessionReady(profile, plat)
	}
	return s.engineDo(profile, plat, func(e *watcher.Engine) { e.Start() })
}

// LoginCheck probes whether one (profile, platform) session is authenticated.
// It returns nil when the answer is unknowable: no session is up for that pair,
// or the platform has no login probe (Kick - see docs/NOTES-kick.md). The probe
// drives a real navigation plus an eval in that Chrome, so it is on-demand only
// and never part of the state poll. A positive result clears the needs-login
// flag the UI shows.
func (s *WatcherService) LoginCheck(ctx context.Context, profile string, plat domain.Platform) (*bool, error) {
	p := s.b.Profile(profile)
	if p == nil {
		return nil, fmt.Errorf("unknown profile %q", profile)
	}
	sess := p.Session(plat)
	if sess == nil || plat.OrTwitch() != domain.PlatformTwitch {
		return nil, nil
	}
	// The probe NAVIGATES the watcher tab (twitch.CheckLogin loads the home
	// page), so it must never run against an engine mid-watch - that would
	// yank its player. Only a parked engine (paused or idle) may be probed.
	if e := p.Engine(plat); e != nil {
		if est := e.Status(); !est.Paused && est.State != watcher.StateIdle {
			return nil, fmt.Errorf("cannot check login while the %s watcher is %s - pause it first (the probe navigates its window)",
				plat.OrTwitch(), est.State)
		}
	}
	st, err := twitch.CheckLogin(ctx, sess)
	if err != nil {
		return nil, err
	}
	if st.LoggedIn && p.NeedsLogin(plat) {
		p.ClearNeedsLogin(plat)
		s.b.markSessionReady(profile, plat)
	}
	return &st.LoggedIn, nil
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
