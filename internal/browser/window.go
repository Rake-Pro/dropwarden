package browser

import (
	"context"
	"sync"
	"time"

	"github.com/rs/zerolog"
)

// WindowCoordinator owns the minimize/restore state of ONE Chrome window and
// arbitrates between the platform tabs that share it.
//
// Restore is REFCOUNTED. An engine entering Opening/Validating/Claiming needs a
// visible, foreground player, so it takes a hold; it drops that hold once
// playback is confirmed (beginWatching). The window is only re-minimized when
// the LAST hold is gone, so Twitch validating a channel can never bury Kick
// mid-validation, and vice versa.
//
// A coordinator with a single registered tab (separate-window mode, or shared
// mode with Kick off) never activates anything: there is nothing to switch
// between, and the window state alone is the whole policy.
type WindowCoordinator struct {
	logger zerolog.Logger

	// manage mirrors browser.window.start_minimized: only then does this window
	// live minimized between watches and need restoring at all.
	manage bool

	// opMu serializes whole hold operations (decision AND the CDP calls that
	// apply it). With the CDP calls outside the state lock alone, a release of
	// the last hold racing a fresh take could apply its minimize AFTER the
	// take's restore, leaving the window minimized while held with the
	// `restored` flag out of sync (so the restore would never re-fire). Each
	// CDP call is individually time-bounded (windowOpTimeout), so a wedged
	// renderer cannot hold this lock forever.
	opMu sync.Mutex

	mu       sync.Mutex
	tabs     map[string]Session
	order    []string
	holds    map[string]bool
	restored bool
}

// windowOpTimeout bounds each window-state CDP call made under opMu.
const windowOpTimeout = 15 * time.Second

// NewWindowCoordinator returns the coordinator for one Chrome window.
// manageMinimized is the window's start_minimized policy.
func NewWindowCoordinator(manageMinimized bool, logger zerolog.Logger) *WindowCoordinator {
	return &WindowCoordinator{
		logger: logger,
		manage: manageMinimized,
		tabs:   map[string]Session{},
		holds:  map[string]bool{},
	}
}

// Register adds one platform tab to this window and returns its handle. sess is
// the platform's STABLE handle (a *Managed): the coordinator re-reads the live
// tab behind it on every call, so a relaunch swap is picked up automatically.
func (w *WindowCoordinator) Register(key string, sess Session) *WindowHandle {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, ok := w.tabs[key]; !ok {
		w.order = append(w.order, key)
	}
	w.tabs[key] = sess
	return &WindowHandle{w: w, key: key}
}

// WindowHandle is one platform tab's view of its window. The watcher engine
// holds one and never touches window state directly.
type WindowHandle struct {
	w   *WindowCoordinator
	key string
}

// Restore takes this tab's restore hold: the window comes out of the minimized
// state (on the first hold) and this tab is brought to the front of it. It is
// idempotent - re-entering Validating after a refresh does not double-count.
func (h *WindowHandle) Restore(ctx context.Context) {
	if h == nil || h.w == nil {
		return
	}
	h.w.hold(ctx, h.key, true)
}

// Release drops this tab's restore hold. The window re-minimizes only when no
// other platform is still holding one.
func (h *WindowHandle) Release(ctx context.Context) {
	if h == nil || h.w == nil {
		return
	}
	h.w.hold(ctx, h.key, false)
}

// Close unregisters this tab (dropping its hold), for a platform runtime that is
// being torn down while the window lives on.
func (h *WindowHandle) Close(ctx context.Context) {
	if h == nil || h.w == nil {
		return
	}
	h.w.unregister(ctx, h.key)
}

// hold takes or drops one tab's restore hold and applies the resulting window
// state. All decisions are made under the lock; the CDP calls are made outside
// it so a wedged renderer cannot block the other platform's engine.
func (w *WindowCoordinator) hold(ctx context.Context, key string, take bool) {
	w.opMu.Lock()
	defer w.opMu.Unlock()

	w.mu.Lock()
	sess, known := w.tabs[key]
	if !known {
		w.mu.Unlock()
		return
	}
	if take {
		w.holds[key] = true
	} else {
		delete(w.holds, key)
	}
	var restore, minimize bool
	if w.manage {
		if take && !w.restored {
			restore, w.restored = true, true
		}
		if !take && len(w.holds) == 0 && w.restored {
			minimize, w.restored = true, false
		}
	}
	// Activation only matters when this window actually hosts more than one
	// platform; a single-tab window is already showing the right thing.
	activate := take && len(w.order) > 1
	// A release while another platform still holds must put THAT tab back in
	// front: the released tab may be covering it mid-validation.
	var nextKey string
	var nextSess Session
	if !take && len(w.holds) > 0 && len(w.order) > 1 {
		for _, k := range w.order {
			if w.holds[k] {
				nextKey, nextSess = k, w.tabs[k]
				break
			}
		}
	}
	held := len(w.holds)
	w.mu.Unlock()

	if restore {
		w.setMinimized(ctx, sess, false)
	}
	if activate {
		w.activate(ctx, key, sess)
	}
	if nextSess != nil {
		w.activate(ctx, nextKey, nextSess)
	}
	if minimize {
		w.setMinimized(ctx, sess, true)
	}
	w.logger.Debug().Str("tab", key).Bool("hold", take).Int("holds", held).Msg("window: restore holds")
}

// unregister removes a tab from the window, releasing any hold it still had so
// a torn-down platform cannot pin the window open forever.
func (w *WindowCoordinator) unregister(ctx context.Context, key string) {
	w.hold(ctx, key, false)
	w.mu.Lock()
	delete(w.tabs, key)
	out := w.order[:0]
	for _, k := range w.order {
		if k != key {
			out = append(out, k)
		}
	}
	w.order = out
	w.mu.Unlock()
}

func (w *WindowCoordinator) setMinimized(ctx context.Context, sess Session, minimized bool) {
	ctx, cancel := context.WithTimeout(ctx, windowOpTimeout)
	defer cancel()
	if err := live(sess).SetMinimized(ctx, minimized); err != nil {
		w.logger.Warn().Err(err).Bool("minimized", minimized).Msg("set window state failed")
	}
}

// activatable is the tab-selection capability the coordinator needs. It is
// deliberately NOT part of Session: only the concrete tab implements it, and
// only this file ever calls it.
type activatable interface {
	Activate(ctx context.Context) error
}

// activate brings one platform's tab to the front of the shared window.
//
// THIS IS THE ONE SANCTIONED FOCUS-TOUCHING CALL IN THE CODEBASE. Everything
// else in internal/browser, internal/twitch and internal/watcher is banned from
// the tab-activation and raise-the-window family by the Taskfile `forbidden`
// task, which exempts only this file (and only for activation).
//
// Owner decision 2026-08-30: tab activation is accepted as the PROOF-OF-CONCEPT
// mechanism for one-Chrome-window-per-profile, because a muted background tab is
// the known silent-failure family for playback (Chrome throttles it and drop
// minutes stop accruing) and the engine must be able to put the tab it is
// validating in front.
//
// RELEASE-BLOCKING BACKLOG (docs/STATUS.md): replace this with a no-activation
// solution, or prove muted background-tab playback reliable on real hardware.
// Until then the stall watchdogs are the only backstop.
func (w *WindowCoordinator) activate(ctx context.Context, key string, sess Session) {
	a, ok := live(sess).(activatable)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, windowOpTimeout)
	defer cancel()
	if err := a.Activate(ctx); err != nil {
		w.logger.Warn().Err(err).Str("tab", key).Msg("activate tab failed")
		return
	}
	w.logger.Debug().Str("tab", key).Msg("window: tab activated for validation")
}

// live unwraps a stable handle to the tab currently behind it, so a browser
// relaunch that swapped the tab is picked up without re-registering.
func live(s Session) Session {
	if m, ok := s.(*Managed); ok {
		if cur := m.session(); cur != nil {
			return cur
		}
	}
	return s
}
