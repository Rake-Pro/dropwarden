package service

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/rake-pro/dropwarden/internal/browser"
	"github.com/rake-pro/dropwarden/internal/config"
	"github.com/rake-pro/dropwarden/internal/discovery"
	"github.com/rake-pro/dropwarden/internal/domain"
	"github.com/rake-pro/dropwarden/internal/kick"
	"github.com/rake-pro/dropwarden/internal/queue"
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

// SetNeedsLogin raises the needs-login flag on a live runtime whose session
// expired mid-run (the engine paused itself).
func (p *ProfileBackend) SetNeedsLogin(plat domain.Platform) {
	p.mu.Lock()
	p.needsLogin[plat.OrTwitch()] = true
	p.mu.Unlock()
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
	upd      Updater
	pwr      Power
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

func (b *Backend) reconciler() Reconciler {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.rec
}

// startWatching, stopWatching, and beginLogin drive the app's per-(profile,
// platform) run intent: launching Chrome to watch, tearing it down, or bringing
// it up parked on the login page. They go through the optional run-control
// interface the reconciler (internal/app) implements; the CLI and the recording
// fakes in tests do not, so the call is a no-op there.
func (b *Backend) startWatching(profile string, plat domain.Platform) {
	if c, ok := b.reconciler().(interface {
		StartWatching(string, domain.Platform)
	}); ok {
		c.StartWatching(profile, plat)
	}
}

func (b *Backend) stopWatching(profile string, plat domain.Platform) {
	if c, ok := b.reconciler().(interface {
		StopWatching(string, domain.Platform)
	}); ok {
		c.StopWatching(profile, plat)
	}
}

func (b *Backend) beginLogin(profile string, plat domain.Platform) {
	if c, ok := b.reconciler().(interface {
		BeginLogin(string, domain.Platform)
	}); ok {
		c.BeginLogin(profile, plat)
	}
}

// wantsRun reports whether a (profile, platform) has a live run intent while
// its runtime is down: the UI renders "launching (or failed - see Debugging)"
// instead of a plain "stopped" the user never asked for. False when no
// reconciler is wired (CLI, tests).
func (b *Backend) wantsRun(profile string, plat domain.Platform) bool {
	if q, ok := b.reconciler().(interface {
		WantsRun(string, domain.Platform) bool
	}); ok {
		return q.WantsRun(profile, plat)
	}
	return false
}

// needsLoginDown reports whether a (profile, platform) still needs its first
// login while its runtime is DOWN, read from the reconciler's on-disk first-run
// markers. When a runtime is up the ProfileBackend flag is authoritative; this
// fills the gap so the dashboard can offer a Log in button before any Chrome has
// launched.
func (b *Backend) needsLoginDown(profile string, plat domain.Platform) bool {
	if q, ok := b.reconciler().(interface {
		NeedsLogin(string, domain.Platform) bool
	}); ok {
		return q.NeedsLogin(profile, plat)
	}
	return false
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

// SetNeedsLogin flags a (profile, platform) whose session expired while its
// runtime is up (the app's logged-out hook calls it).
func (b *Backend) SetNeedsLogin(profile string, plat domain.Platform) {
	if p := b.Profile(profile); p != nil {
		p.SetNeedsLogin(plat)
	}
}

// launchError returns the reconciler's last launch failure for a (profile,
// platform), "" when none (or no reconciler is wired).
func (b *Backend) launchError(profile string, plat domain.Platform) string {
	if q, ok := b.reconciler().(interface {
		LaunchError(string, domain.Platform) string
	}); ok {
		return q.LaunchError(profile, plat)
	}
	return ""
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
				setDiscoConfig(p.Discoverer(domain.PlatformTwitch), pc)
			}
		}
		if e := p.Engine(domain.PlatformKick); e != nil {
			if kc, err := cfg.ProfileKickRuntimeConfig(name); err == nil {
				e.ReloadConfig(kc)
				setDiscoConfig(p.Discoverer(domain.PlatformKick), kc)
			}
		}
	}
}

// setDiscoConfig pushes a reloaded config into a discoverer: it holds its own
// copy (games filter, any_game, capture toggle, details_per_cycle), so without
// this a settings change would only reach discovery on the next launch.
func setDiscoConfig(d watcher.Discoverer, cfg *config.Config) {
	if s, ok := d.(interface{ SetConfig(*config.Config) }); ok {
		s.SetConfig(cfg)
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
	progress := map[string]domain.Progress{}
	claimed := map[string]bool{}
	for _, it := range items {
		if p, err := st.GetProgress(it.DropID); err == nil && p != nil {
			progress[it.DropID] = *p
		}
		if has, err := st.HasClaim(it.DropID); err == nil && has {
			claimed[it.DropID] = true
		}
	}
	alongside := queue.Alongside(items, cmap, progress, claimed, s.b.config().Claim.SkipAccountLink, time.Now())
	// Live snapshot per platform engine, read once per platform.
	pb := s.b.Profile(profile)
	liveness := map[domain.Platform]map[string]watcher.CampaignLive{}
	liveFor := func(plat domain.Platform) map[string]watcher.CampaignLive {
		plat = plat.OrTwitch()
		if lv, ok := liveness[plat]; ok {
			return lv
		}
		var lv map[string]watcher.CampaignLive
		if pb != nil {
			if eng := pb.Engine(plat); eng != nil {
				lv = eng.Liveness()
			}
		}
		liveness[plat] = lv
		return lv
	}
	out := make([]QueueItemDTO, 0, len(items))
	for _, it := range items {
		d := dmap[it.DropID]
		dto := QueueItemDTO{
			DropID: it.DropID, CampaignID: it.CampaignID, Platform: string(it.Platform.OrTwitch()),
			DropName: d.Name,
			Game:     cmap[it.CampaignID].GameName, Position: it.Position, Enabled: it.Enabled,
			Status: string(it.Status), Reason: it.Reason, RequiredMinutes: d.RequiredMinutes,
			ForceWatch: it.ForceWatch, Pin: it.Pin, Alongside: alongside[it.DropID],
			NeedsUser: needsUser(it),
		}
		dto.ActionURL, dto.ActionLabel = queueAction(it, cmap[it.CampaignID])
		if l, ok := liveFor(it.Platform)[it.CampaignID]; ok {
			dto.LiveLogins = l.Logins
			at := l.CheckedAt
			dto.LiveCheckedAt = &at
		}
		if p, ok := progress[it.DropID]; ok {
			dto.CurrentMinutes, _ = p.Effective()
			dto.Owned = p.Owned
		}
		out = append(out, dto)
	}
	return out, nil
}

// needsUser reports whether a queue row needs a human: needs_action with an
// account-link, manual-claim, or expired-session reason.
func needsUser(it domain.QueueItem) bool {
	if it.Status != domain.ItemNeedsAction {
		return false
	}
	r := strings.ToLower(it.Reason)
	for _, k := range []string{"account link", "claim it", "session expired"} {
		if strings.Contains(r, k) {
			return true
		}
	}
	return false
}

// queueAction returns the link and button label that resolve a needs_action
// row: the campaign's account-link page, the Twitch inventory for a manual
// claim, or the login page for an expired session. Empty otherwise.
func queueAction(it domain.QueueItem, c domain.Campaign) (string, string) {
	if it.Status != domain.ItemNeedsAction {
		return "", ""
	}
	r := strings.ToLower(it.Reason)
	switch {
	case strings.Contains(r, "account link"):
		if c.AccountLinkURL != "" {
			return c.AccountLinkURL, "Link account"
		}
	case strings.Contains(r, "claim it"):
		if it.Platform.OrTwitch() == domain.PlatformKick {
			return kick.URLInventory, "Claim on Kick"
		}
		return watcher.URLInventoryClaim, "Claim on Twitch"
	case strings.Contains(r, "session expired"):
		if it.Platform.OrTwitch() == domain.PlatformKick {
			return "https://kick.com/", "Log in"
		}
		return "https://www.twitch.tv/login", "Log in"
	}
	return "", ""
}

// SetPin stars (pinned=true) or unstars one drop. Starring appends it to the
// bottom of the pinned set and stores a pin rule keyed by its game and reward
// name(s), so discovery re-pins the same reward in a later campaign instance;
// unstarring clears the pin, deletes the rule, and closes the rank gap.
func (s *QueueService) SetPin(profile, dropID string, pinned bool) error {
	st, err := s.b.storeFor(profile)
	if err != nil {
		return err
	}
	it, c, d, err := queueEntry(st, dropID)
	if err != nil {
		return err
	}
	game, reward := "", ""
	if c != nil && d != nil {
		game, reward = discovery.PinKey(*c, *d)
	}
	if !pinned {
		if it.Pin == 0 {
			return nil
		}
		if err := st.SetPin(dropID, 0); err != nil {
			return err
		}
		if reward != "" {
			if err := st.DeletePinRule(it.Platform, game, reward); err != nil {
				return err
			}
		}
		return s.syncPins(st)
	}
	if it.Pin > 0 {
		return nil
	}
	last, err := st.MaxPin()
	if err != nil {
		return err
	}
	if err := st.SetPin(dropID, last+1); err != nil {
		return err
	}
	if reward != "" {
		if err := st.UpsertPinRule(store.PinRule{Platform: it.Platform, GameName: game, RewardKey: reward, Rank: last + 1}); err != nil {
			return err
		}
	}
	return s.syncPins(st)
}

// MovePin swaps a pinned drop with its neighbor in the pinned set
// (direction "up" = toward rank 1, "down" = away from it). Moving past either
// end is a no-op.
func (s *QueueService) MovePin(profile, dropID, direction string) error {
	st, err := s.b.storeFor(profile)
	if err != nil {
		return err
	}
	var step int
	switch direction {
	case "up":
		step = -1
	case "down":
		step = 1
	default:
		return fmt.Errorf("invalid direction %q (want up or down)", direction)
	}
	it, _, _, err := queueEntry(st, dropID)
	if err != nil {
		return err
	}
	if it.Pin == 0 {
		return fmt.Errorf("drop %q is not pinned", dropID)
	}
	if err := st.CompactPins(); err != nil {
		return err
	}
	items, err := st.GetQueue()
	if err != nil {
		return err
	}
	var rank int
	byRank := map[int]string{}
	for _, q := range items {
		if q.Pin > 0 {
			byRank[q.Pin] = q.DropID
		}
		if q.DropID == dropID {
			rank = q.Pin
		}
	}
	other, ok := byRank[rank+step]
	if !ok {
		return nil
	}
	if err := st.SetPin(other, rank); err != nil {
		return err
	}
	if err := st.SetPin(dropID, rank+step); err != nil {
		return err
	}
	return s.syncPins(st)
}

// syncPins compacts pin ranks and mirrors them into the pin rules. Ranks are
// queue-wide, so a move can shift another platform's pin: sync every platform.
func (s *QueueService) syncPins(st *store.Store) error {
	for _, pf := range []domain.Platform{domain.PlatformTwitch, domain.PlatformKick} {
		if err := discovery.ApplyPinRules(st, pf, log.Logger); err != nil {
			return err
		}
	}
	return nil
}

// queueEntry loads one queue item plus its campaign and drop (nil when the
// campaign or drop is no longer stored).
func queueEntry(st *store.Store, dropID string) (domain.QueueItem, *domain.Campaign, *domain.Drop, error) {
	items, err := st.GetQueue()
	if err != nil {
		return domain.QueueItem{}, nil, nil, err
	}
	for _, it := range items {
		if it.DropID != dropID {
			continue
		}
		c, err := st.GetCampaign(it.CampaignID)
		if err != nil || c == nil {
			return it, nil, nil, nil
		}
		for i := range c.Drops {
			if c.Drops[i].ID == dropID {
				return it, c, &c.Drops[i], nil
			}
		}
		return it, c, nil, nil
	}
	return domain.QueueItem{}, nil, nil, fmt.Errorf("drop %q is not in the queue", dropID)
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

// SetForceWatch sets or clears the manual owned-skip override for one drop.
func (s *QueueService) SetForceWatch(profile, dropID string, on bool) error {
	st, err := s.b.storeFor(profile)
	if err != nil {
		return err
	}
	return st.SetForceWatch(dropID, on)
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
	// Chrome exists only while a watcher runs: launch the window if it is down,
	// or unpause a login-mode window in place. The launch takes seconds, so this
	// returns immediately and the UI polls /api/state to see the runtime appear.
	s.b.startWatching(profile, plat)
	return nil
}

// Login brings a (profile, platform) up parked on its login page so the user can
// authenticate before any watching starts - the one time a window opens with no
// watcher running.
func (s *WatcherService) Login(profile string, plat domain.Platform) error {
	s.b.beginLogin(profile, plat)
	return nil
}

// LoginCheck probes whether one (profile, platform) session is authenticated.
// It returns nil when the answer is unknowable: no session is up for that pair,
// or the platform has no login probe (Kick - see docs/NOTES-kick.md). The probe
// drives a real navigation plus an eval in that Chrome, so it is on-demand only
// and never part of the state poll. A positive result clears the needs-login
// flag the UI shows.
func (s *WatcherService) LoginCheck(ctx context.Context, profile string, plat domain.Platform) (*bool, string, error) {
	p := s.b.Profile(profile)
	if p == nil {
		return nil, "", fmt.Errorf("unknown profile %q", profile)
	}
	sess := p.Session(plat)
	if sess == nil {
		return nil, "", nil
	}
	// Either probe may NAVIGATE the watcher tab (twitch.CheckLogin always
	// loads the home page; kick.CheckLogin navigates when off-origin), so it
	// must never run against an engine mid-watch - that would yank its player.
	// Only a parked engine (paused or idle) may be probed.
	if e := p.Engine(plat); e != nil {
		if est := e.Status(); !est.Paused && est.State != watcher.StateIdle {
			return nil, "", fmt.Errorf("cannot check login while the %s watcher is %s - pause it first (the probe navigates its window)",
				plat.OrTwitch(), est.State)
		}
	}
	var loggedIn bool
	if plat.OrTwitch() == domain.PlatformKick {
		// Kick: an API status check from page context (kick/auth.go), not a
		// DOM guess. Unknown stays null - never guessed - but carries a
		// diagnostic detail so a drifted endpoint is fixable from the UI.
		st, detail, err := kick.CheckLogin(ctx, sess)
		if err != nil {
			return nil, "", err
		}
		if !st.Known {
			return nil, detail, nil
		}
		loggedIn = st.LoggedIn
	} else {
		st, err := twitch.CheckLogin(ctx, sess)
		if err != nil {
			return nil, "", err
		}
		loggedIn = st.LoggedIn
	}
	if loggedIn && p.NeedsLogin(plat) {
		p.ClearNeedsLogin(plat)
		s.b.markSessionReady(profile, plat)
		// A CONFIRMED first login flows straight into watching: the init flow
		// is "log in, then it sets everything up", and the launch-on-demand
		// rule says Chrome exists only while a watcher runs - a logged-in
		// window parked with no watcher would linger open for nothing. The
		// runtime is already up in login mode, so this unpauses in place.
		s.b.startWatching(profile, plat)
	}
	return &loggedIn, "", nil
}

func (s *WatcherService) Stop(profile string, plat domain.Platform) error {
	// Stop closes the window entirely: with no watcher running there is no
	// reason to keep Chrome up. A down runtime is already stopped, so this is a
	// harmless no-op then.
	s.b.stopWatching(profile, plat)
	return nil
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
		out = append(out, ClaimDTO{DropID: c.DropID, Platform: string(c.Platform.OrTwitch()), DropName: c.DropName, Game: c.GameName, ClaimedAt: c.ClaimedAt, Source: c.Source})
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
