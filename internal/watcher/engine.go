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
	"github.com/rake-pro/dropwarden/internal/store"
	"github.com/rake-pro/dropwarden/internal/twitch"
)

// Pager is the engine's view of the Twitch page driver (twitch.Pages).
type Pager interface {
	OpenChannel(ctx context.Context, login string, policy twitch.PlayerPolicy) error
	ReadInventory(ctx context.Context) ([]domain.Progress, error)
	DirectoryChannels(ctx context.Context, gameSlug string) ([]string, error)
	FetchCurrentDrop(ctx context.Context) (*domain.Progress, error)
}

// Discoverer is the engine's view of the discovery pass (discovery.Discoverer).
type Discoverer interface {
	RunOnce(ctx context.Context) (discovery.Result, error)
}

// Deps are the engine's collaborators.
type Deps struct {
	Store *store.Store
	Ctrl  twitch.Controller
	Pages Pager
	Disco Discoverer
	Cfg   *config.Config
	Emit  Emitter
	Log   zerolog.Logger

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
	State        State     `json:"state"`
	DropID       string    `json:"dropID"`
	DropName     string    `json:"dropName"`
	ChannelLogin string    `json:"channelLogin"`
	Current      int       `json:"current"`
	Required     int       `json:"required"`
	Paused       bool      `json:"paused"`
	Since        time.Time `json:"since"`
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
	lastPoll      time.Time
	hardRefreshes int
	stallEvents   int
	raids         int
	sessionStart  time.Time
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
	e := &Engine{deps: d, cmds: make(chan command, 16), state: StateIdle}
	e.paused = !d.Cfg.Watcher.EnabledOnStart
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
	e.setStatus(func(s *Status) { s.State = to; s.Since = e.deps.Now() })
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
