// Package watcher is the single-owner state machine that drives one Twitch
// watch session at a time: discovering work, selecting a channel, validating
// playback, watching with a set of watchdogs, and claiming completed drops.
//
// The engine runs as one goroutine. All state lives in that goroutine and is
// mutated only inside its loop, so the design needs no locks on engine state;
// external callers interact solely through the buffered command channel.
package watcher

import (
	"context"
	"sync"
	"time"

	"github.com/rs/zerolog"

	"github.com/rake-pro/dropwarden/internal/config"
	"github.com/rake-pro/dropwarden/internal/discovery"
	"github.com/rake-pro/dropwarden/internal/domain"
	"github.com/rake-pro/dropwarden/internal/platform"
	"github.com/rake-pro/dropwarden/internal/store"
)

// Pager is the engine's view of a platform's page driver (twitch.Pages,
// kick.Pages).
type Pager interface {
	OpenChannel(ctx context.Context, login string, policy platform.PlayerPolicy) error
	ReadInventory(ctx context.Context) ([]domain.Progress, error)
	DirectoryChannels(ctx context.Context, gameSlug string) ([]string, error)
	// LiveChannels returns the subset of logins currently live (with viewer
	// counts). Used for ACL campaigns, whose allowlist says nothing about who
	// is streaming right now. Platforms without a cheap status query return
	// errors.ErrUnsupported and the caller assumes every login is live.
	LiveChannels(ctx context.Context, logins []string) ([]domain.Channel, error)
	FetchCurrentDrop(ctx context.Context) (*domain.Progress, error)
	FetchInventoryProgress(ctx context.Context) ([]domain.Progress, error)
	// CampaignProgress returns the viewer's per-drop progress for one campaign
	// from its details view (login "" = the logged-in user). It still sees a
	// drop that just completed, which the in-progress inventory has already
	// dropped, so the claim path can learn its claim id. Platforms without a
	// details query return errors.ErrUnsupported.
	CampaignProgress(ctx context.Context, campaignID, login string) ([]domain.Progress, error)
	// DrainProgress returns progress entries observed passively since the last
	// call (Twitch: the GQL capture shim). Platforms without passive capture
	// return nil.
	DrainProgress(ctx context.Context) ([]domain.Progress, error)
	// DrainHeartbeat returns what the page observed of its own watch-time
	// reporting since the last call (Twitch: spade minute-watched beacons and
	// WebSocket drop-progress pushes). Platforms without capture return zero.
	DrainHeartbeat(ctx context.Context) (domain.Heartbeat, error)
	// ClaimDrop claims an earned drop by its claim id (domain.Progress.ClaimID)
	// through the platform's claim API, returning the raw status and response
	// body. Platforms without one return errors.ErrUnsupported and the engine
	// uses the inventory-page click instead.
	ClaimDrop(ctx context.Context, claimID string) (status, raw string, err error)
	// OpenInventory navigates the tab to the drops inventory page and waits
	// for it to load, for the DOM claim fallback.
	OpenInventory(ctx context.Context) error
	// InventoryButtons captures every button-like element on the current page
	// (JSON) and its count, written to debug/inventory-buttons.json on each DOM
	// claim fallback run. Unsupported platforms return errors.ErrUnsupported.
	InventoryButtons(ctx context.Context) ([]byte, int, error)
}

// Discoverer is the engine's view of the discovery pass (discovery.Discoverer).
type Discoverer interface {
	RunOnce(ctx context.Context) (discovery.Result, error)
}

// Deps are the engine's collaborators.
// WindowHolder is the engine's view of the coordinator that owns its Chrome
// window. Restore takes a refcounted hold (the window is visible and this
// engine's tab is in front); Release drops it, and the window only tucks itself
// away again when no other platform is still holding one. SendToBottom
// re-asserts browser.window.always_on_bottom; the coordinator gates it on the
// WINDOW's config and makes it a no-op where unsupported.
type WindowHolder interface {
	Restore(ctx context.Context)
	Release(ctx context.Context)
	SendToBottom(ctx context.Context)
}

type Deps struct {
	Store *store.Store
	Ctrl  platform.Controller
	Pages Pager
	Disco Discoverer
	Cfg   *config.Config
	Emit  Emitter
	Log   zerolog.Logger

	// Platform tags everything this engine touches (queue items, sessions,
	// claims, channel stats); zero value means Twitch. ChannelHost is the
	// platform's channel-URL host (e.g. "twitch.tv", "kick.com"), used to
	// recognize channel navigations (raids/redirects). Profile names the person
	// this engine watches for; it never selects a store (each profile has its
	// own) but tags the status so the UI can attribute state per profile.
	Platform    domain.Platform
	ChannelHost string
	Profile     string

	// Window is this engine's handle on the Chrome WINDOW its tab lives in
	// (browser.WindowHandle). In the default shared-window layout that window
	// is shared with the profile's other platform, so restores are refcounted
	// and the holding tab is brought to the front. nil in tests and anywhere no
	// coordinator was wired, which falls back to the direct minimize policy.
	Window WindowHolder

	// DiscoveryStagger delays the engine's FIRST discovery pass only (0 for
	// the first engine started), so N profiles' engines do not all hit their
	// platform's GQL/API at once on boot. Later discovery passes keep their
	// existing independent jitter (cooldown, cooldown_jitter), untouched.
	DiscoveryStagger time.Duration

	// DebugDir is the profile's debug dir (config.ProfilePaths.Debug) for
	// capture dumps; empty falls back to <approot>/debug.
	DebugDir string

	// OnLoggedOut, when set, runs on the engine goroutine after the engine
	// detected an expired platform session and paused itself: the app wires it
	// to the pending-login marker and the needs-login flag the UI shows.
	OnLoggedOut func()

	// Now and Rand are injectable for deterministic tests. Rand returns [0,1).
	Now  func() time.Time
	Rand func() float64
}

type cmdKind int

const (
	cmdStart cmdKind = iota
	cmdStop
	cmdSkip
	cmdRefresh
	cmdReloadConfig
	cmdShutdown
	cmdStatus
)

type command struct {
	kind  cmdKind
	cfg   *config.Config
	reply chan Status
}

// Status is a snapshot of the engine for the UI/CLI.
type Status struct {
	Profile      string    `json:"profile"`
	Platform     string    `json:"platform"`
	State        State     `json:"state"`
	DropID       string    `json:"dropID"`
	DropName     string    `json:"dropName"`
	ChannelLogin string    `json:"channelLogin"`
	Game         string    `json:"game"`
	Current      int       `json:"current"`
	Required     int       `json:"required"`
	Paused       bool      `json:"paused"`
	Since        time.Time `json:"since"`

	// Reason is the last state transition's reason (or a live warning such as
	// a restored minimized window), so the UI can explain idle/cooldown.
	Reason string `json:"reason"`

	// SessionStartedAt is when the CURRENT session began (zero when none is
	// active). Stats overlays its wall-clock duration on the lifetime totals so
	// the UI ticks live instead of only moving when a session is recorded.
	SessionStartedAt time.Time `json:"sessionStartedAt"`
}

// activeWatch holds the mutable state of the current watch attempt.
type activeWatch struct {
	drop     domain.Drop
	campaign domain.Campaign
	login    string
	session  domain.WatchSession

	startMinutes  int
	lastMinutes   int
	lastProgress  time.Time
	creditChannel string // channel Twitch last reported as crediting (from the poll)
	heartbeats    int    // minute-watched beacons the player sent this session
	hardRefreshes int
	stallEvents   int
	raids         int
	sessionStart  time.Time

	// nextPollAt is when pollProgress may next fire; set to now + fuzzed(interval)
	// each time a poll actually runs, so the cadence of this network-visible GQL
	// fetch is not an exact fixed interval.
	nextPollAt time.Time

	// nextZOrderAt is when the watch loop next re-asserts always_on_bottom; it
	// rides the player reassert interval, fuzzed the same way. The zero value
	// fires on the first watching tick.
	nextZOrderAt time.Time

	// maxSessionAt is this session's rotation target, computed ONCE at session
	// start (sessionStart + fuzzed(MaxSession)) rather than recomputed on every
	// re-entry into runWatching, so a channel rotation does not land at exactly
	// the same offset every session.
	maxSessionAt time.Time

	// pollErrors counts failed progress polls in the current no-credit window
	// (reset whenever lastProgress is): a window with a failed poll proves
	// nothing about the drop, so it never ends in giveUpNoCredit.
	pollErrors int
	// hiddenPolls counts consecutive progress polls that read the tab hidden.
	hiddenPolls int
	// loggedOut is set by the progress poll when the platform answered for an
	// anonymous viewer (domain.ErrLoggedOut).
	loggedOut bool
	// softChecked is the lastProgress value the soft-stall live check last
	// ran for, so it runs once per stall episode rather than every tick.
	softChecked time.Time
}

// Engine is the watcher state machine.
type Engine struct {
	deps       Deps
	cmds       chan command
	paused     bool // true when the user has stopped watching (Idle by request)
	state      State
	cur        *activeWatch
	backoff    int
	shutdown   bool
	skipReq    bool
	refreshReq bool

	statusMu    sync.Mutex
	status      Status
	eligibility map[string]int

	// noCreditDrops are drops that watched a participating channel with healthy
	// playback but earned ZERO server-side progress across the full stall
	// window - un-earnable for this account (a reward already owned, a region
	// block, etc.). Selection skips them so the watcher does not grind one
	// forever. In-memory and session-scoped: a fresh run retries, in case the
	// cause was transient. A drop that DID credit before stalling is never
	// added (that is a channel problem, handled by channel blacklisting).
	noCreditDrops map[string]bool

	staggered bool // true once the first-discovery stagger delay has run (or been skipped)

	// sweepTried records when the claim sweep last failed to claim a drop, so
	// it waits claim.retry_backoff before trying that drop again. In-memory.
	sweepTried map[string]time.Time

	// wait is the ctx-aware pause between sweep claims and after a DOM claim
	// click. Replaced in tests.
	wait func(ctx context.Context, d time.Duration)

	// live is the per-campaign live-channel snapshot the Queue view shows
	// (see live.go); nextLiveAt gates the watch-time refresh.
	liveMu     sync.Mutex
	live       map[string]CampaignLive
	nextLiveAt time.Time

	// lastDiscoveryAt is when the last successful discovery pass ran and
	// discoveryGap the jittered discovery.interval drawn for the next one;
	// runDiscovering skips the pass until the gap has elapsed.
	lastDiscoveryAt time.Time
	discoveryGap    time.Duration

	// cdpTimeout bounds every Reload/Navigate/Eval the engine issues (see
	// bounded). Replaced in tests.
	cdpTimeout time.Duration
}

// LastEligibility returns the most recent selection eligibility tally.
func (e *Engine) LastEligibility() map[string]int {
	e.statusMu.Lock()
	defer e.statusMu.Unlock()
	out := make(map[string]int, len(e.eligibility))
	for k, v := range e.eligibility {
		out[k] = v
	}
	return out
}

func (e *Engine) setEligibility(t map[string]int) {
	e.statusMu.Lock()
	e.eligibility = t
	e.statusMu.Unlock()
}

// New builds an engine, filling default Now/Rand.
func New(d Deps) *Engine {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Rand == nil {
		// Deterministic-but-varying default: a tiny LCG so we avoid importing
		// math/rand's global state and stay reproducible per process.
		var s uint64 = 0x9e3779b97f4a7c15
		d.Rand = func() float64 {
			s = s*6364136223846793005 + 1442695040888963407
			return float64(s>>11) / float64(1<<53)
		}
	}
	e := &Engine{deps: d, cmds: make(chan command, 16), state: StateIdle, noCreditDrops: map[string]bool{}, sweepTried: map[string]time.Time{}, wait: waitCtx, cdpTimeout: cdpTimeout}
	e.paused = !d.Cfg.Watcher.EnabledOnStart
	e.status = Status{Profile: d.Profile, Platform: string(d.Platform.OrTwitch()), State: StateIdle, Paused: e.paused}
	return e
}

// Start requests the engine begin watching.
func (e *Engine) Start() { e.send(command{kind: cmdStart}) }

// Stop requests the engine pause (return to Idle after the current step).
func (e *Engine) Stop() { e.send(command{kind: cmdStop}) }

// SkipCurrent abandons the current drop/channel and selects the next.
func (e *Engine) SkipCurrent() { e.send(command{kind: cmdSkip}) }

// RefreshNow forces a discovery pass at the next opportunity.
func (e *Engine) RefreshNow() { e.send(command{kind: cmdRefresh}) }

// ReloadConfig swaps in a new config (browser.* changes need a restart, which
// the caller surfaces).
func (e *Engine) ReloadConfig(cfg *config.Config) { e.send(command{kind: cmdReloadConfig, cfg: cfg}) }

// Shutdown asks the engine loop to exit.
func (e *Engine) Shutdown() { e.send(command{kind: cmdShutdown}) }

// Status returns a snapshot of engine state.
func (e *Engine) Status() Status {
	e.statusMu.Lock()
	defer e.statusMu.Unlock()
	return e.status
}

func (e *Engine) send(c command) {
	select {
	case e.cmds <- c:
	default:
		// Channel full: drop the command rather than block a UI thread. Commands
		// are idempotent intents, so a coalesced duplicate is harmless.
	}
}

func (e *Engine) setStatus(mut func(*Status)) {
	e.statusMu.Lock()
	mut(&e.status)
	e.statusMu.Unlock()
}

// transition changes state, emits the event, and records it.
func (e *Engine) transition(to State, reason string) {
	from := e.state
	e.state = to
	e.setStatus(func(s *Status) { s.State = to; s.Since = e.deps.Now(); s.Reason = reason })
	e.deps.Emit.Emit(EvState, StatePayload{From: from, To: to, At: e.deps.Now(), Reason: reason})
	e.deps.Log.Info().Str("from", from.String()).Str("to", to.String()).Str("reason", reason).Msg("state")
}

// Run is the engine loop. It returns when a shutdown command is received or ctx
// is cancelled.
func (e *Engine) Run(ctx context.Context) error {
	if !e.paused {
		e.transition(StateDiscovering, "enabled on start")
	}
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var next State
		var reason string
		switch e.state {
		case StateIdle:
			if e.waitIdle(ctx) {
				return nil // shutdown
			}
			continue
		case StateDiscovering:
			next, reason = e.runDiscovering(ctx)
		case StateSelectingChannel:
			next, reason = e.runSelecting(ctx)
		case StateOpening:
			next, reason = e.runOpening(ctx)
		case StateValidating:
			next, reason = e.runValidating(ctx)
		case StateWatching:
			next, reason = e.runWatching(ctx)
		case StateClaiming:
			next, reason = e.runClaiming(ctx)
		case StateCooldown:
			next, reason = e.runCooldown(ctx)
		case StateNeedsUserAction:
			next, reason = e.runNeedsAction(ctx)
		case StateBackoff:
			next, reason = e.runBackoff(ctx)
		default:
			next, reason = StateIdle, "unknown state"
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if e.shutdown {
			return nil
		}
		if e.paused && next != StateIdle {
			next, reason = StateIdle, "stopped by user"
		}
		e.transition(next, reason)
	}
}
