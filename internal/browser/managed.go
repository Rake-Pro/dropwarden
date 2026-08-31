package browser

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog"
)

// relaunchMaxBackoff caps the delay between Chrome relaunch attempts. Recovery
// retries forever - a drops daemon should outlive a Chrome crash at 3am - but
// never hammers.
const relaunchMaxBackoff = 60 * time.Second

// Managed wraps a Session and relaunches Chrome when the underlying instance
// dies (CDP disconnect or renderer crash). Callers hold the Managed session
// for the life of the app; the browser underneath may be replaced any number
// of times. Method calls during a relaunch fail fast with an error, which the
// engine's existing backoff handling absorbs until the browser is back.
type Managed struct {
	logger   zerolog.Logger
	relaunch func(ctx context.Context) (Session, error)

	mu     sync.RWMutex
	cur    Session
	events chan Event
	closed atomic.Bool
}

// NewManaged wraps an already-launched session. relaunch must build a fully
// initialized replacement (launch + any platform seeding like capture shims);
// it is invoked - serially - each time the current session dies.
func NewManaged(initial Session, relaunch func(ctx context.Context) (Session, error), logger zerolog.Logger) *Managed {
	m := &Managed{
		logger:   logger,
		relaunch: relaunch,
		cur:      initial,
		events:   make(chan Event, eventBuffer),
	}
	go m.supervise(initial)
	return m
}

// NewSwappable wraps a session whose replacements are pushed in from OUTSIDE
// (Swap) instead of pulled from a relaunch closure. The shared-window Group
// uses it: one supervisor owns the Chrome instance and rebuilds EVERY platform's
// tab together, so the per-platform handles must not each try to relaunch.
func NewSwappable(initial Session, logger zerolog.Logger) *Managed {
	m := &Managed{
		logger: logger,
		cur:    initial,
		events: make(chan Event, eventBuffer),
	}
	go m.supervise(initial)
	return m
}

// Swap installs an externally rebuilt session in place of the dead one. Only
// valid on a NewSwappable handle; the stable event stream and every caller's
// Session reference survive.
func (m *Managed) Swap(next Session) {
	m.mu.Lock()
	m.cur = next
	m.mu.Unlock()
	go m.supervise(next)
}

// supervise forwards one underlying session's events and, when that session's
// stream ends (its pump closes on disconnect/crash/close), starts recovery.
func (m *Managed) supervise(s Session) {
	for ev := range s.Events() {
		select {
		case m.events <- ev:
		default:
			// Never block recovery on a slow consumer; drop like session.emit.
		}
	}
	if m.closed.Load() {
		return
	}
	if m.relaunch == nil {
		// Externally supervised (shared window): the Group rebuilds the browser
		// and every platform tab, then Swaps the replacement in here.
		return
	}
	m.logger.Warn().Msg("browser session lost; relaunching chrome")
	_ = s.Close() // reap the dead process/profile lock; idempotent
	backoff := 5 * time.Second
	for !m.closed.Load() {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		next, err := m.relaunch(ctx)
		cancel()
		if err == nil {
			m.mu.Lock()
			m.cur = next
			m.mu.Unlock()
			m.logger.Info().Msg("browser relaunched; session restored")
			go m.supervise(next)
			return
		}
		m.logger.Warn().Err(err).Dur("retry_in", backoff).Msg("browser relaunch failed")
		select {
		case <-time.After(backoff):
		}
		if backoff *= 2; backoff > relaunchMaxBackoff {
			backoff = relaunchMaxBackoff
		}
	}
}

func (m *Managed) session() Session {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.cur
}

// guard returns the live session, or an error while it is being replaced.
func (m *Managed) guard() (Session, error) {
	s := m.session()
	if s == nil || !s.Alive() {
		return nil, fmt.Errorf("browser: restarting after a crash; retry shortly")
	}
	return s, nil
}

func (m *Managed) Navigate(ctx context.Context, url string) error {
	s, err := m.guard()
	if err != nil {
		return err
	}
	return s.Navigate(ctx, url)
}

func (m *Managed) Reload(ctx context.Context, ignoreCache bool) error {
	s, err := m.guard()
	if err != nil {
		return err
	}
	return s.Reload(ctx, ignoreCache)
}

func (m *Managed) CurrentURL(ctx context.Context) (string, error) {
	s, err := m.guard()
	if err != nil {
		return "", err
	}
	return s.CurrentURL(ctx)
}

func (m *Managed) Eval(ctx context.Context, js string, out any) error {
	s, err := m.guard()
	if err != nil {
		return err
	}
	return s.Eval(ctx, js, out)
}

func (m *Managed) Click(ctx context.Context, selector string) (bool, error) {
	s, err := m.guard()
	if err != nil {
		return false, err
	}
	return s.Click(ctx, selector)
}

func (m *Managed) SeedOnNewDocument(ctx context.Context, js string) error {
	s, err := m.guard()
	if err != nil {
		return err
	}
	return s.SeedOnNewDocument(ctx, js)
}

// Events returns the stable event stream, surviving relaunches. The stream
// carries each underlying session's disconnect, so the engine still sees and
// reacts to the outage; subsequent calls succeed once recovery completes.
func (m *Managed) Events() <-chan Event { return m.events }

func (m *Managed) ShiftWindow(dx, dy int) error {
	s, err := m.guard()
	if err != nil {
		return err
	}
	return s.ShiftWindow(dx, dy)
}

func (m *Managed) SetMinimized(ctx context.Context, minimized bool) error {
	s, err := m.guard()
	if err != nil {
		return err
	}
	return s.SetMinimized(ctx, minimized)
}

// Alive reports whether a live session is currently attached.
func (m *Managed) Alive() bool {
	s := m.session()
	return s != nil && s.Alive()
}

// Close permanently shuts the managed session down (no further relaunches).
func (m *Managed) Close() error {
	m.closed.Store(true)
	if s := m.session(); s != nil {
		return s.Close()
	}
	return nil
}
