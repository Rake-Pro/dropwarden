package service

import (
	"fmt"
	"sync"
	"time"

	"github.com/rake-pro/dropwarden/internal/config"
	"github.com/rake-pro/dropwarden/internal/discovery"
	"github.com/rake-pro/dropwarden/internal/domain"
	"github.com/rake-pro/dropwarden/internal/store"
	"github.com/rake-pro/dropwarden/internal/watcher"
)

// Backend is the shared state the bound services operate over. It is populated
// by the app at startup and passed to each service. The engine is attached
// after the UI starts (once Chrome is launched), so it is guarded and may be
// nil while the runtime is still coming up.
type Backend struct {
	Store      *store.Store
	ConfigPath string

	mu         sync.RWMutex
	cfg        *config.Config
	engine     *watcher.Engine
	kickEngine *watcher.Engine
	disco      *discovery.Discoverer
}

// NewBackend builds a Backend.
func NewBackend(st *store.Store, eng *watcher.Engine, cfg *config.Config, cfgPath string) *Backend {
	return &Backend{Store: st, engine: eng, cfg: cfg, ConfigPath: cfgPath}
}

// SetEngine attaches the watcher engine once the runtime is up.
func (b *Backend) SetEngine(e *watcher.Engine) {
	b.mu.Lock()
	b.engine = e
	b.mu.Unlock()
}

// Engine returns the attached engine, or nil if the runtime is not up yet.
func (b *Backend) Engine() *watcher.Engine {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.engine
}

// SetKickEngine attaches the Kick engine when the kick runtime is enabled.
func (b *Backend) SetKickEngine(e *watcher.Engine) {
	b.mu.Lock()
	b.kickEngine = e
	b.mu.Unlock()
}

// KickEngine returns the Kick engine, or nil when kick is disabled/not up.
func (b *Backend) KickEngine() *watcher.Engine {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.kickEngine
}

// SetDiscoverer attaches the discoverer once the runtime is up.
func (b *Backend) SetDiscoverer(d *discovery.Discoverer) {
	b.mu.Lock()
	b.disco = d
	b.mu.Unlock()
}

// Discoverer returns the attached discoverer, or nil if the runtime is not up.
func (b *Backend) Discoverer() *discovery.Discoverer {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.disco
}

func (b *Backend) config() *config.Config {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.cfg
}

// ---- CampaignService ----

type CampaignService struct {
	b           *Backend
	lastRefresh time.Time
	mu          sync.Mutex
}

func NewCampaignService(b *Backend) *CampaignService { return &CampaignService{b: b} }

func (s *CampaignService) List() ([]CampaignDTO, error) {
	campaigns, err := s.b.Store.ListCampaigns()
	if err != nil {
		return nil, err
	}
	out := make([]CampaignDTO, 0, len(campaigns))
	for _, c := range campaigns {
		out = append(out, s.toDTO(c))
	}
	return out, nil
}

func (s *CampaignService) toDTO(c domain.Campaign) CampaignDTO {
	drops := make([]DropDTO, 0, len(c.Drops))
	for _, d := range c.Drops {
		dd := DropDTO{ID: d.ID, Name: d.Name, RequiredMinutes: d.RequiredMinutes}
		if p, err := s.b.Store.GetProgress(d.ID); err == nil && p != nil {
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

// RefreshNow asks the engine to run discovery.
func (s *CampaignService) RefreshNow() error {
	eng := s.b.Engine()
	if eng == nil {
		return fmt.Errorf("watcher not running")
	}
	eng.RefreshNow()
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

func (s *QueueService) Get() ([]QueueItemDTO, error) {
	items, err := s.b.Store.GetQueue()
	if err != nil {
		return nil, err
	}
	campaigns, _ := s.b.Store.ListCampaigns()
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
			Game: cmap[it.CampaignID].GameName, Position: it.Position, Enabled: it.Enabled,
			Status: string(it.Status), Reason: it.Reason, RequiredMinutes: d.RequiredMinutes,
		}
		if p, err := s.b.Store.GetProgress(it.DropID); err == nil && p != nil {
			dto.CurrentMinutes, _ = p.Effective()
		}
		out = append(out, dto)
	}
	return out, nil
}

func (s *QueueService) SetOrder(dropIDs []string) error { return s.b.Store.SetQueueOrder(dropIDs) }
func (s *QueueService) SetEnabled(dropID string, on bool) error {
	return s.b.Store.SetItemEnabled(dropID, on)
}
func (s *QueueService) ClearBlacklist() error { return s.b.Store.ClearBlacklist() }

func (s *QueueService) SetPriorityMode(mode string) error {
	if !domain.ValidPriorityMode(domain.PriorityMode(mode)) {
		return fmt.Errorf("invalid priority mode %q", mode)
	}
	cfg := s.b.config()
	cfg.Queue.PriorityMode = mode
	if err := config.Save(s.b.ConfigPath, cfg); err != nil {
		return err
	}
	if eng := s.b.Engine(); eng != nil {
		eng.ReloadConfig(cfg)
	}
	return nil
}

// ---- WatcherService ----

type WatcherService struct{ b *Backend }

func NewWatcherService(b *Backend) *WatcherService { return &WatcherService{b: b} }

func (s *WatcherService) Status() watcher.Status {
	eng := s.b.Engine()
	if eng == nil {
		return watcher.Status{State: watcher.StateIdle}
	}
	return eng.Status()
}

func (s *WatcherService) Start() error { return s.engineDo(func(e *watcher.Engine) { e.Start() }) }
func (s *WatcherService) Stop() error  { return s.engineDo(func(e *watcher.Engine) { e.Stop() }) }
func (s *WatcherService) SkipCurrent() error {
	return s.engineDo(func(e *watcher.Engine) { e.SkipCurrent() })
}
func (s *WatcherService) ForceRefresh() error {
	return s.engineDo(func(e *watcher.Engine) { e.RefreshNow() })
}

func (s *WatcherService) engineDo(f func(*watcher.Engine)) error {
	eng := s.b.Engine()
	if eng == nil {
		return fmt.Errorf("watcher not running")
	}
	f(eng)
	return nil
}

// ---- HistoryService ----

type HistoryService struct{ b *Backend }

func NewHistoryService(b *Backend) *HistoryService { return &HistoryService{b: b} }

func (s *HistoryService) Claims() ([]ClaimDTO, error) {
	claims, err := s.b.Store.ListClaims(time.Time{}, time.Time{})
	if err != nil {
		return nil, err
	}
	out := make([]ClaimDTO, 0, len(claims))
	for _, c := range claims {
		out = append(out, ClaimDTO{DropID: c.DropID, Platform: string(c.Platform.OrTwitch()), DropName: c.DropName, Game: c.GameName, ClaimedAt: c.ClaimedAt})
	}
	return out, nil
}

func (s *HistoryService) Sessions(limit int) ([]SessionDTO, error) {
	sessions, err := s.b.Store.ListSessions(limit)
	if err != nil {
		return nil, err
	}
	out := make([]SessionDTO, 0, len(sessions))
	for _, ws := range sessions {
		out = append(out, SessionDTO{
			ID: ws.ID, DropID: ws.DropID, ChannelLogin: ws.ChannelLogin,
			StartedAt: ws.StartedAt, EndedAt: ws.EndedAt, Minutes: ws.DurationMinutes(), Outcome: string(ws.Outcome),
		})
	}
	return out, nil
}

func (s *HistoryService) Stats() (StatsDTO, error) {
	claims, err := s.b.Store.ListClaims(time.Time{}, time.Time{})
	if err != nil {
		return StatsDTO{}, err
	}
	sessions, err := s.b.Store.ListSessions(0)
	if err != nil {
		return StatsDTO{}, err
	}
	st := StatsDTO{DropsClaimed: len(claims), Sessions: len(sessions), OutcomeCounts: map[string]int{}}
	// Earned = confirmed claims plus watch-complete drops still awaiting a
	// claim (failed auto-claim, or completed in the background of a
	// simultaneous multi-drop campaign).
	st.DropsEarned = len(claims)
	claimedSet := make(map[string]bool, len(claims))
	for _, c := range claims {
		claimedSet[c.DropID] = true
	}
	if progress, err := s.b.Store.ListProgress(); err == nil {
		for _, p := range progress {
			if !claimedSet[p.DropID] && p.ClaimedAt == nil && p.Complete() {
				st.DropsEarned++
			}
		}
	}
	success := 0
	for _, ws := range sessions {
		// Wall-clock time on stream, NOT per-drop credited minutes: drops accrue
		// simultaneously, so credited deltas badly undercount time watched.
		st.MinutesWatched += ws.DurationMinutes()
		st.OutcomeCounts[string(ws.Outcome)]++
		if ws.Outcome == domain.OutcomeClaimed || ws.Outcome == domain.OutcomeCompleted {
			success++
		}
	}
	if len(sessions) > 0 {
		st.SuccessRate = success * 100 / len(sessions)
	}
	// Overlay the in-flight session so totals tick live: recorded sessions only
	// land in the store when they END, which otherwise freezes the numbers for
	// hours mid-watch. Success rate stays completed-sessions-only.
	for _, eng := range []*watcher.Engine{s.b.Engine(), s.b.KickEngine()} {
		if eng == nil {
			continue
		}
		if es := eng.Status(); es.State == watcher.StateWatching && !es.SessionStartedAt.IsZero() {
			st.MinutesWatched += int(time.Since(es.SessionStartedAt).Minutes())
			st.Sessions++
		}
	}
	return st, nil
}
