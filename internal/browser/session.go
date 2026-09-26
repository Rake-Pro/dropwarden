package browser

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/cdp"
	"github.com/go-rod/rod/lib/proto"
	"github.com/rs/zerolog"
)

// tab is the rod-backed implementation of Session: exactly one page inside an
// Instance, driven for its whole lifetime by exactly one platform engine.
type tab struct {
	inst   *Instance
	logger zerolog.Logger
	page   *rod.Page
	sid    proto.TargetSessionID
	frame  proto.PageFrameID
	events chan Event

	// owner marks the tab Chrome opened at launch. Closing it closes the whole
	// Instance (see Close): the owner's tab is the window, so the platforms
	// sharing it cannot outlive it.
	owner bool

	alive     atomic.Bool
	closing   atomic.Bool
	closeOnce sync.Once
	evOnce    sync.Once
}

// emit delivers an event without ever blocking the pump. A full buffer means
// the watcher is wedged, which is worth a loud log but must not stall event
// processing (and with it disconnect detection).
func (t *tab) emit(e Event) {
	select {
	case t.events <- e:
	default:
		t.logger.Warn().Str("kind", string(e.Kind)).Msg("browser event dropped: consumer not keeping up")
	}
}

// closeEvents closes the tab's event channel exactly once. Only the instance
// pump and Close call it.
func (t *tab) closeEvents() { t.evOnce.Do(func() { close(t.events) }) }

// applyDarkEmulation forces prefers-color-scheme:dark so Twitch renders its dark
// theme regardless of the account setting, which keeps the window dim.
func (t *tab) applyDarkEmulation() error {
	err := proto.EmulationSetEmulatedMedia{
		Features: []*proto.EmulationMediaFeature{
			{Name: "prefers-color-scheme", Value: "dark"},
		},
	}.Call(t.page)
	if err != nil {
		return fmt.Errorf("browser: emulate dark color scheme: %w", err)
	}
	return nil
}

// dimOverlayScript builds the burn-in overlay: a fixed, full-viewport black
// layer at the configured opacity plus a dark document background.
//
// verify live: the overlay must not intercept player controls (pointer-events
// is none) and must not trip Twitch's visibility heuristics into pausing
// playback. Opacity is a paint-only effect and the element never covers the
// video's intersection ratio, so it should be inert, but confirm against a real
// stream before trusting it.
func dimOverlayScript(opacity float64) string {
	return fmt.Sprintf(`(() => {
  const ID = '__dropwarden_dim__';
  const OPACITY = %.4f;
  const install = () => {
    const root = document.documentElement;
    if (root) { root.style.background = '#000'; }
    if (!document.body || document.getElementById(ID)) { return false; }
    const el = document.createElement('div');
    el.id = ID;
    el.style.cssText = 'position:fixed;top:0;left:0;width:100vw;height:100vh;' +
      'background:#000;pointer-events:none;z-index:2147483647;opacity:' + OPACITY;
    document.body.appendChild(el);
    return true;
  };
  if (!install()) {
    const obs = new MutationObserver(() => { if (install()) { obs.disconnect(); } });
    obs.observe(document.documentElement, { childList: true, subtree: false });
  }
})();`, opacity)
}

// arrowOrFunc matches JS source that already evaluates to a function.
var arrowOrFunc = regexp.MustCompile(`^(async\s+)?(function\b|\(\s*[\w$,\s]*\)\s*=>|[\w$]+\s*=>)`)

// asJSFunc adapts caller JS to what rod's evaluator expects. rod wraps the
// source as `function() { return (SRC).apply(this, arguments) }`, so SRC must be
// a function; a bare expression like `location.href` would blow up. Callers may
// pass either form and get the expected result.
func asJSFunc(js string) string {
	trimmed := strings.TrimSpace(strings.Trim(js, "\t\n\v\f\r ;"))
	if arrowOrFunc.MatchString(trimmed) {
		return trimmed
	}
	return "() => (" + trimmed + ")"
}

func (t *tab) Navigate(ctx context.Context, url string) error {
	p := t.page.Context(ctx)
	if err := p.Navigate(url); err != nil {
		return fmt.Errorf("browser: navigate to %s: %w", url, err)
	}
	if err := waitLoad(p); err != nil {
		return fmt.Errorf("browser: wait for load of %s: %w", url, err)
	}
	if err := t.applyDarkEmulation(); err != nil {
		t.logger.Debug().Err(err).Msg("could not reapply dark color scheme after navigate")
	}
	return nil
}

// waitLoad waits for window.onload, riding out a page that renavigates while
// the wait is in flight. rod's WaitLoad is a JS promise in the page's
// execution context; a client-side redirect (kick.com/drops bouncing through
// its Cloudflare check, an SPA re-route) destroys that context and CDP answers
// -32000 "Inspected target navigated or closed" (live 2026-09-20, every few
// Kick discovery passes). The document that replaced it is the one to wait
// on, so re-arm the wait; a target that really closed fails again with a
// different error and is returned. Bounded by loadTimeout overall.
func waitLoad(p *rod.Page) error {
	deadline := time.Now().Add(loadTimeout)
	for attempt := 0; ; attempt++ {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return fmt.Errorf("load did not settle within %s", loadTimeout)
		}
		err := p.Timeout(remaining).WaitLoad()
		if err == nil {
			return nil
		}
		if !isNavigatedAway(err) || attempt >= maxRenavigations {
			return err
		}
	}
}

// maxRenavigations caps how many mid-wait renavigations waitLoad tolerates
// before giving up on a page that never settles (a redirect loop).
const maxRenavigations = 5

// isNavigatedAway reports the CDP error a JS-promise wait gets when the frame it
// was evaluating in navigated away underneath it.
func isNavigatedAway(err error) bool {
	var cerr *cdp.Error
	if !errors.As(err, &cerr) {
		return false
	}
	return cerr.Code == -32000 && strings.Contains(cerr.Message, "navigated")
}

func (t *tab) Reload(ctx context.Context, ignoreCache bool) error {
	p := t.page.Context(ctx)

	// rod's own Reload runs location.reload(), which cannot express a
	// cache-busting hard refresh, so drive Page.reload directly and wait for the
	// top frame to renavigate ourselves. Subscribing before issuing the reload
	// avoids missing the event.
	waiter, cancel := p.WithCancel()
	defer cancel()
	topFrame := t.frame
	wait := waiter.EachEvent(func(e *proto.PageFrameNavigated) bool {
		return e.Frame != nil && e.Frame.ID == topFrame
	})

	if err := (proto.PageReload{IgnoreCache: ignoreCache}).Call(p); err != nil {
		return fmt.Errorf("browser: reload (ignore_cache=%t): %w", ignoreCache, err)
	}
	wait()

	if err := waitLoad(p); err != nil {
		return fmt.Errorf("browser: wait for load after reload: %w", err)
	}
	if err := t.applyDarkEmulation(); err != nil {
		t.logger.Debug().Err(err).Msg("could not reapply dark color scheme after reload")
	}
	return nil
}

func (t *tab) CurrentURL(ctx context.Context) (string, error) {
	info, err := t.page.Context(ctx).Info()
	if err != nil {
		return "", fmt.Errorf("browser: read current url: %w", err)
	}
	return info.URL, nil
}

func (t *tab) Eval(ctx context.Context, js string, out any) error {
	res, err := t.page.Context(ctx).Eval(asJSFunc(js))
	if err != nil {
		return fmt.Errorf("browser: eval: %w", err)
	}
	if out == nil {
		return nil
	}
	if err := res.Value.Unmarshal(out); err != nil {
		return fmt.Errorf("browser: decode eval result: %w", err)
	}
	return nil
}

func (t *tab) Click(ctx context.Context, selector string) (bool, error) {
	p := t.page.Context(ctx).Timeout(queryTimeout)

	// Has uses rod's not-found sleeper, so a missing element returns
	// immediately as (false, nil, nil) instead of retrying until the timeout.
	present, el, err := p.Has(selector)
	if err != nil {
		return false, fmt.Errorf("browser: look for %s: %w", selector, err)
	}
	if !present {
		return false, nil
	}

	if err := el.ScrollIntoView(); err != nil {
		return false, fmt.Errorf("browser: scroll %s into view: %w", selector, err)
	}
	if err := el.Hover(); err != nil {
		return false, fmt.Errorf("browser: hover %s: %w", selector, err)
	}
	if err := dwell(ctx); err != nil {
		return false, err
	}
	// rod's Click dispatches a real Input.dispatchMouseEvent at a random point
	// inside the element box, not a synthetic DOM click.
	if err := el.Click(proto.InputMouseButtonLeft, 1); err != nil {
		return false, fmt.Errorf("browser: click %s: %w", selector, err)
	}
	return true, nil
}

// dwell pauses between hover and click. A real pointer rests on a control for a
// moment before pressing; an instantaneous hover-then-click is a bot tell.
func dwell(ctx context.Context) error {
	d := 200*time.Millisecond + rand.N(600*time.Millisecond)
	tm := time.NewTimer(d)
	defer tm.Stop()
	select {
	case <-ctx.Done():
		return fmt.Errorf("browser: click dwell: %w", ctx.Err())
	case <-tm.C:
		return nil
	}
}

func (t *tab) SeedOnNewDocument(ctx context.Context, js string) error {
	_, err := proto.PageAddScriptToEvaluateOnNewDocument{Source: js}.Call(t.page.Context(ctx))
	if err != nil {
		return fmt.Errorf("browser: seed script on new document: %w", err)
	}
	return nil
}

func (t *tab) Events() <-chan Event { return t.events }

func (t *tab) ShiftWindow(dx, dy int) error {
	return shiftWindow(t.inst.chrome.pid, dx, dy)
}

// SendToBottom pushes this tab's Chrome window(s) to the bottom of the OS
// z-order without activating them (browser.window.always_on_bottom). It is not
// part of Session: WindowCoordinator and Managed reach it through bottomable.
func (t *tab) SendToBottom(context.Context) error {
	n, err := sendToBottom(uint32(t.inst.chrome.pid))
	if err != nil {
		return err
	}
	t.logger.Debug().Int("windows", n).Msg("window: pushed to bottom of z-order")
	return nil
}

func (t *tab) SetMinimized(ctx context.Context, minimized bool) error {
	p := t.page.Context(ctx)
	// Called on the page session, getWindowForTarget resolves our own window.
	win, err := proto.BrowserGetWindowForTarget{}.Call(p)
	if err != nil {
		return fmt.Errorf("browser: get window for target: %w", err)
	}
	state := proto.BrowserWindowStateMinimized
	if !minimized {
		state = proto.BrowserWindowStateNormal
	}
	err = proto.BrowserSetWindowBounds{
		WindowID: win.WindowID,
		Bounds:   &proto.BrowserBounds{WindowState: state},
	}.Call(p)
	if err != nil {
		return fmt.Errorf("browser: set window state %s: %w", state, err)
	}
	return nil
}

// Activate selects this tab in its window (Target.activateTarget). It is NOT
// part of the Session interface: activation is the one focus-touching operation
// in the codebase and WindowCoordinator is its only sanctioned caller (see
// window.go and the Taskfile `forbidden` guard).
func (t *tab) Activate(ctx context.Context) error {
	err := proto.TargetActivateTarget{TargetID: t.page.TargetID}.Call(t.inst.browser.Context(ctx))
	if err != nil {
		return fmt.Errorf("browser: activate tab: %w", err)
	}
	return nil
}

func (t *tab) Alive() bool { return t.alive.Load() }

// Close releases this tab. The OWNER tab is the window, so closing it closes the
// whole Chrome instance (and every other platform's tab with it); any other tab
// is closed on its own, leaving the window and its siblings running.
func (t *tab) Close() error {
	var err error
	t.closeOnce.Do(func() {
		// Set closing first so the pump reports a clean shutdown rather than a
		// spurious disconnect event.
		t.closing.Store(true)
		t.alive.Store(false)
		if t.owner {
			err = t.inst.Close()
			return
		}
		t.inst.forget(t)
		if _, cerr := (proto.TargetCloseTarget{TargetID: t.page.TargetID}).Call(t.inst.browser); cerr != nil {
			t.logger.Debug().Err(cerr).Msg("close tab")
			err = cerr
		}
		t.closeEvents()
	})
	return err
}
