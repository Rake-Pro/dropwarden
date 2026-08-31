package browser

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog"
)

// TabInit prepares a freshly created tab before its engine uses it (the Twitch
// GQL capture shim is the only one today). It is re-run on EVERY rebuilt tab, so
// a relaunched browser comes back with the same shims installed.
type TabInit func(ctx context.Context, s Session) error

// WindowHost is everything a Group needs from a Chrome window: its owner tab,
// extra tabs, liveness, and a signal for when it died. *Instance is the real
// implementation; tests substitute a fake so the shared-window lifecycle can be
// exercised without launching Chrome.
type WindowHost interface {
	// Owner returns the tab the window was opened on.
	Owner() Session
	// NewTab opens an additional tab in the same window.
	NewTab(ctx context.Context) (Session, error)
	// Alive reports whether the window is still up.
	Alive() bool
	// Done is closed when the window dies (crash, kill, or Close).
	Done() <-chan struct{}
	// Close terminates the window and every tab in it.
	Close() error
}

// Group supervises ONE Chrome instance shared by several platform tabs: the
// default one-window-per-PROFILE layout, where Twitch drives the owner tab and
// Kick a second tab in the same window.
//
// Each platform holds a stable Session handle whose underlying tab is swapped in
// place when the browser is relaunched after a CDP loss - the same contract
// Managed gives a single-platform window, except that the rebuild recreates
// every registered tab together rather than each handle racing to relaunch.
type Group struct {
	logger zerolog.Logger
	launch func(ctx context.Context) (WindowHost, error)

	mu    sync.Mutex
	inst  WindowHost
	order []string
	slots map[string]*groupSlot

	closed atomic.Bool
}

// groupSlot is one platform's registration in the group: the stable handle its
// engine holds, plus what the rebuild needs to recreate its tab.
type groupSlot struct {
	sess  *Managed
	init  TabInit
	owner bool
}

// NewGroup launches the shared Chrome and starts supervising it. launch must
// return a fully started window (including any launch-time policy such as
// start_minimized); it is re-invoked on every relaunch.
func NewGroup(ctx context.Context, launch func(ctx context.Context) (WindowHost, error), logger zerolog.Logger) (*Group, error) {
	inst, err := launch(ctx)
	if err != nil {
		return nil, err
	}
	g := &Group{logger: logger, launch: launch, inst: inst, slots: map[string]*groupSlot{}}
	go g.supervise(inst)
	return g, nil
}

// Session returns the stable handle for key, opening its tab on the live browser
// the first time it is asked for. The FIRST key to register adopts the
// instance's own page and becomes the owner: closing it closes the window.
// Calling it again for a key already up returns the existing handle, so a
// reconcile pass never opens a duplicate tab.
func (g *Group) Session(ctx context.Context, key string, init TabInit) (Session, error) {
	g.mu.Lock()
	if s := g.slots[key]; s != nil {
		g.mu.Unlock()
		return s.sess, nil
	}
	inst, owner := g.inst, len(g.order) == 0
	g.mu.Unlock()

	if g.closed.Load() || inst == nil || !inst.Alive() {
		return nil, fmt.Errorf("browser: shared chrome window is not running")
	}

	var (
		t   Session
		err error
	)
	if owner {
		t = inst.Owner()
	} else if t, err = inst.NewTab(ctx); err != nil {
		return nil, err
	}
	if init != nil {
		if err := init(ctx, t); err != nil {
			g.logger.Warn().Err(err).Str("tab", key).Msg("browser: tab init failed")
		}
	}

	slot := &groupSlot{sess: NewSwappable(t, g.logger.With().Str("tab", key).Logger()), init: init, owner: owner}
	g.mu.Lock()
	g.slots[key] = slot
	g.order = append(g.order, key)
	g.mu.Unlock()
	g.logger.Info().Str("tab", key).Bool("owner", owner).Msg("browser: platform tab attached to the shared window")
	return slot.sess, nil
}

// CloseSession drops one platform's tab. Closing the OWNER's (Twitch's) closes
// the whole instance: the owner tab is the window, so the platforms sharing it
// cannot outlive it. Any other key closes just that tab, leaving the window and
// its siblings running.
func (g *Group) CloseSession(key string) error {
	g.mu.Lock()
	slot := g.slots[key]
	if slot == nil {
		g.mu.Unlock()
		return nil
	}
	delete(g.slots, key)
	out := g.order[:0]
	for _, k := range g.order {
		if k != key {
			out = append(out, k)
		}
	}
	g.order = out
	g.mu.Unlock()

	if slot.owner {
		return g.Close()
	}
	g.logger.Info().Str("tab", key).Msg("browser: closing one platform tab in the shared window")
	return slot.sess.Close()
}

// Closed reports whether the group has been shut down for good.
func (g *Group) Closed() bool { return g.closed.Load() }

// Close shuts the shared browser down permanently (no further relaunches).
func (g *Group) Close() error {
	g.closed.Store(true)
	g.mu.Lock()
	inst := g.inst
	slots := g.slots
	g.slots, g.order = map[string]*groupSlot{}, nil
	g.mu.Unlock()
	for _, s := range slots {
		s.sess.closed.Store(true)
	}
	if inst == nil {
		return nil
	}
	return inst.Close()
}

// supervise waits for the shared instance to die and rebuilds it. Recovery
// retries forever with capped backoff, exactly like Managed: a drops daemon must
// outlive a Chrome crash at 3am.
func (g *Group) supervise(inst WindowHost) {
	<-inst.Done()
	if g.closed.Load() {
		return
	}
	g.logger.Warn().Msg("shared browser window lost; relaunching chrome")
	_ = inst.Close() // reap the dead process/profile lock; idempotent
	backoff := 5 * time.Second
	for !g.closed.Load() {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		next, err := g.rebuild(ctx)
		cancel()
		if err == nil {
			g.logger.Info().Msg("shared browser relaunched; every platform tab restored")
			go g.supervise(next)
			return
		}
		g.logger.Warn().Err(err).Dur("retry_in", backoff).Msg("shared browser relaunch failed")
		time.Sleep(backoff)
		if backoff *= 2; backoff > relaunchMaxBackoff {
			backoff = relaunchMaxBackoff
		}
	}
}

// rebuild launches a replacement instance and recreates EVERY registered
// platform tab on it, in registration order (the owner adopting the instance's
// own page), re-running each tab's init so the capture shims come back. Only
// once every tab exists are they swapped into their stable handles, so an engine
// never sees a half-built browser. A failure part-way closes the doomed instance
// and leaves the old handles reporting "restarting" until the next attempt.
func (g *Group) rebuild(ctx context.Context) (WindowHost, error) {
	inst, err := g.launch(ctx)
	if err != nil {
		return nil, err
	}

	g.mu.Lock()
	order := append([]string(nil), g.order...)
	slots := make(map[string]*groupSlot, len(order))
	for _, k := range order {
		slots[k] = g.slots[k]
	}
	g.mu.Unlock()

	rebuilt := make(map[string]Session, len(order))
	for i, k := range order {
		var t Session
		if i == 0 {
			t = inst.Owner()
		} else if t, err = inst.NewTab(ctx); err != nil {
			_ = inst.Close()
			return nil, fmt.Errorf("browser: rebuild tab %s: %w", k, err)
		}
		if init := slots[k].init; init != nil {
			if err := init(ctx, t); err != nil {
				g.logger.Warn().Err(err).Str("tab", k).Msg("browser: tab init failed after relaunch")
			}
		}
		rebuilt[k] = t
	}

	g.mu.Lock()
	g.inst = inst
	g.mu.Unlock()
	if g.closed.Load() {
		// Shut down while the replacement was starting: do not leak the Chrome.
		_ = inst.Close()
		return nil, fmt.Errorf("browser: shared chrome window was closed during relaunch")
	}
	for k, t := range rebuilt {
		slots[k].sess.Swap(t)
	}
	return inst, nil
}
