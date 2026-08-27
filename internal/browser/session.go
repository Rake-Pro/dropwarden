package browser

import (
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
	"github.com/rake-pro/dropwarden/internal/config"
	"github.com/rs/zerolog"
)

const (
	// portWaitTimeout bounds the poll for DevToolsActivePort.
	//
	// verify live: Chrome writes the file once the browser process is far enough
	// along to have bound the debugging socket. 15s is generous for a warm
	// profile but has not been measured on Windows with a cold profile on a slow
	// disk; if launches start timing out, raise this before suspecting CDP.
	portWaitTimeout = 15 * time.Second
	portPollEvery   = 100 * time.Millisecond

	// loadTimeout bounds waiting for window.onload after a navigation.
	loadTimeout = 45 * time.Second
	// queryTimeout bounds locating an element for a click.
	queryTimeout = 10 * time.Second
	// closeTimeout bounds waiting for the event pump to drain on Close.
	closeTimeout = 2 * time.Second

	// eventBuffer is deep enough that a watcher busy handling one event does not
	// lose the next. Overflow is logged, never blocked on.
	eventBuffer = 32
)

// session is the rod-backed implementation of Session. It owns exactly one
// Chrome process and exactly one page inside it for its whole lifetime.
type session struct {
	logger  zerolog.Logger
	chrome  *launchedChrome
	browser *rod.Browser
	page    *rod.Page
	events  chan Event

	cancel   context.CancelFunc
	pumpDone chan struct{}

	alive     atomic.Bool
	closing   atomic.Bool
	closeOnce sync.Once
}

// Launch starts a dedicated Chrome, attaches to it over CDP, and adopts its
// single page. ctx bounds the startup handshake only: once Launch returns, the
// session owns the browser until Close, independent of ctx.
func Launch(ctx context.Context, cfg *config.Config, logger zerolog.Logger) (Session, error) {
	chromePath, err := FindChrome(cfg.Browser.ChromePath)
	if err != nil {
		return nil, err
	}
	profileDir, err := cfg.ProfileDir()
	if err != nil {
		return nil, err
	}
	if err := ensureProfile(profileDir, logger); err != nil {
		return nil, err
	}

	flags := chromeFlags(cfg.Browser, profileDir, cfg.Browser.Window)
	logger.Info().Str("chrome", chromePath).Str("profile", profileDir).Msg("launching chrome")

	proc, err := launchChrome(chromePath, flags)
	if err != nil {
		return nil, err
	}

	port, wsPath, err := waitDevToolsPort(ctx, profileDir)
	if err != nil {
		_ = proc.Kill()
		return nil, err
	}
	// Chrome binds the debug socket to loopback only; the path carries the
	// per-launch token, so this URL is not guessable by other local processes.
	ws := fmt.Sprintf("ws://127.0.0.1:%s%s", port, wsPath)

	// The rod context deliberately does not derive from ctx: the browser must
	// outlive the startup call.
	sessCtx, cancel := context.WithCancel(context.Background())
	br := rod.New().ControlURL(ws).Context(sessCtx)
	if err := br.Connect(); err != nil {
		cancel()
		_ = proc.Kill()
		return nil, fmt.Errorf("browser: connect to cdp at %s: %w", ws, err)
	}

	page, err := adoptPage(br)
	if err != nil {
		cancel()
		_ = br.Close()
		_ = proc.Kill()
		return nil, err
	}

	s := &session{
		logger:   logger,
		chrome:   proc,
		browser:  br,
		page:     page,
		events:   make(chan Event, eventBuffer),
		cancel:   cancel,
		pumpDone: make(chan struct{}),
	}
	s.alive.Store(true)

	if err := s.applyDarkEmulation(); err != nil {
		logger.Warn().Err(err).Msg("could not force dark color scheme")
	}
	if op := cfg.Browser.Burnin.DimOpacity; op > 0 {
		script := dimOverlayScript(op)
		if err := s.SeedOnNewDocument(ctx, script); err != nil {
			logger.Warn().Err(err).Msg("could not install dim overlay")
		} else if _, err := s.page.Context(ctx).Eval(script); err != nil {
			// The seeded copy only runs on the next document; paint the current
			// one too so about:blank is not left bright.
			logger.Debug().Err(err).Msg("dim overlay not applied to the current document")
		}
	}

	go s.pump()
	return s, nil
}

// waitDevToolsPort polls the profile for the port file Chrome writes after
// binding its debugging socket. Line 1 is the port, line 2 the websocket path.
func waitDevToolsPort(ctx context.Context, profileDir string) (port, wsPath string, err error) {
	path := filepath.Join(profileDir, devToolsPortFile)
	deadline := time.Now().Add(portWaitTimeout)

	for {
		if b, rerr := os.ReadFile(path); rerr == nil {
			lines := strings.Split(strings.TrimSpace(string(b)), "\n")
			if len(lines) >= 2 {
				port, wsPath = strings.TrimSpace(lines[0]), strings.TrimSpace(lines[1])
				if port != "" && wsPath != "" {
					return port, wsPath, nil
				}
			}
		}
		if time.Now().After(deadline) {
			return "", "", fmt.Errorf("browser: chrome did not publish %s within %s (did it fail to start?)",
				path, portWaitTimeout)
		}
		select {
		case <-ctx.Done():
			return "", "", fmt.Errorf("browser: wait for devtools port: %w", ctx.Err())
		case <-time.After(portPollEvery):
		}
	}
}

// adoptPage takes over the single page Chrome opened for about:blank, creating
// one only if the target list somehow has none. Exactly one page is adopted and
// held for the session's lifetime; the watcher never opens tabs.
func adoptPage(br *rod.Browser) (*rod.Page, error) {
	pages, err := br.Pages()
	if err != nil {
		return nil, fmt.Errorf("browser: list pages: %w", err)
	}
	for _, p := range pages {
		info, ierr := p.Info()
		if ierr != nil {
			continue
		}
		if info.Type == "page" {
			return p, nil
		}
	}
	p, err := br.Page(proto.TargetCreateTarget{URL: "about:blank"})
	if err != nil {
		return nil, fmt.Errorf("browser: create page: %w", err)
	}
	return p, nil
}

// pump translates CDP events into Session events. It subscribes at the browser
// level (rather than the page level) because that channel closes when the CDP
// transport dies, which is how a Chrome crash or kill is detected. It owns
// s.events and is the only closer.
func (s *session) pump() {
	defer func() {
		close(s.events)
		close(s.pumpDone)
	}()

	// Enable the domains whose events we want. rod records these in its own
	// domain-state map, so its internal EachEvent helpers will not disable them
	// again when they finish.
	if err := (proto.PageEnable{}).Call(s.page); err != nil {
		s.logger.Warn().Err(err).Msg("could not enable Page domain")
	}
	if err := (proto.InspectorEnable{}).Call(s.page); err != nil {
		s.logger.Warn().Err(err).Msg("could not enable Inspector domain")
	}

	var (
		sid       = s.page.SessionID
		crashed   = (proto.InspectorTargetCrashed{}).ProtoEvent()
		navigated = (proto.PageFrameNavigated{}).ProtoEvent()
		topFrame  = s.page.FrameID
	)

	for msg := range s.browser.Event() {
		if msg.SessionID != sid {
			continue
		}
		switch msg.Method {
		case crashed:
			s.logger.Error().Msg("renderer crashed")
			s.emit(Event{Kind: EventCrash, At: time.Now()})
		case navigated:
			evt := &proto.PageFrameNavigated{}
			if !msg.Load(evt) || evt.Frame == nil {
				continue
			}
			// Ignore iframes: only a top-frame navigation is a raid or redirect.
			if evt.Frame.ParentID != "" || evt.Frame.ID != topFrame {
				continue
			}
			s.emit(Event{Kind: EventNavigated, URL: evt.Frame.URL, At: time.Now()})
			// The emulation override does not always survive a cross-document
			// navigation. Reassert off the pump goroutine so event delivery is
			// never blocked on a CDP round trip.
			go func() {
				if err := s.applyDarkEmulation(); err != nil {
					s.logger.Debug().Err(err).Msg("could not reapply dark color scheme after navigation")
				}
			}()
		}
	}

	// The browser event channel only closes when the CDP transport is gone.
	s.alive.Store(false)
	if !s.closing.Load() {
		s.logger.Error().Msg("cdp connection lost")
		s.emit(Event{Kind: EventDisconnect, At: time.Now()})
	}
}

// emit delivers an event without ever blocking the pump. A full buffer means
// the watcher is wedged, which is worth a loud log but must not stall event
// processing (and with it disconnect detection).
func (s *session) emit(e Event) {
	select {
	case s.events <- e:
	default:
		s.logger.Warn().Str("kind", string(e.Kind)).Msg("browser event dropped: consumer not keeping up")
	}
}

// applyDarkEmulation forces prefers-color-scheme:dark so Twitch renders its dark
// theme regardless of the account setting, which keeps the window dim.
func (s *session) applyDarkEmulation() error {
	err := proto.EmulationSetEmulatedMedia{
		Features: []*proto.EmulationMediaFeature{
			{Name: "prefers-color-scheme", Value: "dark"},
		},
	}.Call(s.page)
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

func (s *session) Navigate(ctx context.Context, url string) error {
	p := s.page.Context(ctx)
	if err := p.Navigate(url); err != nil {
		return fmt.Errorf("browser: navigate to %s: %w", url, err)
	}
	if err := p.Timeout(loadTimeout).WaitLoad(); err != nil {
		return fmt.Errorf("browser: wait for load of %s: %w", url, err)
	}
	if err := s.applyDarkEmulation(); err != nil {
		s.logger.Debug().Err(err).Msg("could not reapply dark color scheme after navigate")
	}
	return nil
}

func (s *session) Reload(ctx context.Context, ignoreCache bool) error {
	p := s.page.Context(ctx)

	// rod's own Reload runs location.reload(), which cannot express a
	// cache-busting hard refresh, so drive Page.reload directly and wait for the
	// top frame to renavigate ourselves. Subscribing before issuing the reload
	// avoids missing the event.
	waiter, cancel := p.WithCancel()
	defer cancel()
	topFrame := s.page.FrameID
	wait := waiter.EachEvent(func(e *proto.PageFrameNavigated) bool {
		return e.Frame != nil && e.Frame.ID == topFrame
	})

	if err := (proto.PageReload{IgnoreCache: ignoreCache}).Call(p); err != nil {
		return fmt.Errorf("browser: reload (ignore_cache=%t): %w", ignoreCache, err)
	}
	wait()

	if err := p.Timeout(loadTimeout).WaitLoad(); err != nil {
		return fmt.Errorf("browser: wait for load after reload: %w", err)
	}
	if err := s.applyDarkEmulation(); err != nil {
		s.logger.Debug().Err(err).Msg("could not reapply dark color scheme after reload")
	}
	return nil
}

func (s *session) CurrentURL(ctx context.Context) (string, error) {
	info, err := s.page.Context(ctx).Info()
	if err != nil {
		return "", fmt.Errorf("browser: read current url: %w", err)
	}
	return info.URL, nil
}

func (s *session) Eval(ctx context.Context, js string, out any) error {
	res, err := s.page.Context(ctx).Eval(asJSFunc(js))
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

func (s *session) Click(ctx context.Context, selector string) (bool, error) {
	p := s.page.Context(ctx).Timeout(queryTimeout)

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
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return fmt.Errorf("browser: click dwell: %w", ctx.Err())
	case <-t.C:
		return nil
	}
}

func (s *session) SeedOnNewDocument(ctx context.Context, js string) error {
	_, err := proto.PageAddScriptToEvaluateOnNewDocument{Source: js}.Call(s.page.Context(ctx))
	if err != nil {
		return fmt.Errorf("browser: seed script on new document: %w", err)
	}
	return nil
}

func (s *session) Events() <-chan Event { return s.events }

func (s *session) ShiftWindow(dx, dy int) error {
	return shiftWindow(s.chrome.pid, dx, dy)
}

func (s *session) SetMinimized(ctx context.Context, minimized bool) error {
	p := s.page.Context(ctx)
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

func (s *session) Alive() bool { return s.alive.Load() }

func (s *session) Close() error {
	var err error
	s.closeOnce.Do(func() {
		// Set closing first so the pump reports a clean shutdown rather than a
		// spurious disconnect event.
		s.closing.Store(true)
		s.alive.Store(false)

		if cerr := s.browser.Close(); cerr != nil {
			s.logger.Debug().Err(cerr).Msg("cdp browser close")
		}
		s.cancel()

		if kerr := s.chrome.Kill(); kerr != nil {
			err = kerr
		}

		select {
		case <-s.pumpDone:
		case <-time.After(closeTimeout):
			s.logger.Warn().Msg("browser event pump did not stop in time")
		}
	})
	return err
}
