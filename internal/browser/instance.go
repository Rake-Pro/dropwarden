package browser

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/devices"
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

// Instance is one Chrome process plus its CDP connection. It hosts one or more
// tabs: the OWNER tab (the page Chrome opens at launch) and any extra tab a
// second platform asks for.
//
// One Chrome per PROFILE is the default layout: Twitch drives the owner tab and
// Kick a second tab in the SAME window, so four people cost four Chrome windows
// instead of eight. The legacy one-window-per-platform layout is still reachable
// (kick.browser.separate_window), which is simply two Instances with one tab
// each.
type Instance struct {
	logger     zerolog.Logger
	chrome     *launchedChrome
	browser    *rod.Browser
	ctx        context.Context
	cancel     context.CancelFunc
	dimOpacity float64

	mu    sync.Mutex
	tabs  map[proto.TargetSessionID]*tab
	owner *tab

	pumpDone chan struct{}
	done     chan struct{}

	alive     atomic.Bool
	closing   atomic.Bool
	closeOnce sync.Once
}

// Launch starts a dedicated Chrome and returns its owner tab. Closing that
// session closes the browser. ctx bounds the startup handshake only: once Launch
// returns, the instance owns the browser until Close, independent of ctx.
func Launch(ctx context.Context, cfg *config.Config, logger zerolog.Logger) (Session, error) {
	profileDir, err := cfg.ProfileDir()
	if err != nil {
		return nil, err
	}
	return LaunchWith(ctx, cfg.Browser, profileDir, cfg.Browser.Burnin.DimOpacity, logger)
}

// LaunchWith starts a dedicated Chrome for ONE platform instance and returns its
// single owner tab: the separate-window path (kick.browser.separate_window) and
// the CLI harness.
func LaunchWith(ctx context.Context, bcfg config.BrowserConfig, profileDir string, dimOpacity float64, logger zerolog.Logger) (Session, error) {
	in, err := LaunchInstance(ctx, bcfg, profileDir, dimOpacity, logger)
	if err != nil {
		return nil, err
	}
	return in.Owner(), nil
}

// LaunchInstance starts a Chrome that can host several platform tabs, attaches
// to it over CDP, and adopts the page Chrome opened as the owner tab.
func LaunchInstance(ctx context.Context, bcfg config.BrowserConfig, profileDir string, dimOpacity float64, logger zerolog.Logger) (*Instance, error) {
	chromePath, err := FindChrome(bcfg.ChromePath)
	if err != nil {
		return nil, err
	}
	if err := ensureProfile(profileDir, logger); err != nil {
		return nil, err
	}

	if bcfg.ChromePath == "" && IsEdge(chromePath) {
		logger.Warn().Str("path", chromePath).Msg("browser: Chrome not found; falling back to Microsoft Edge (install Chrome or set browser.chrome_path)")
	}
	flags := chromeFlags(bcfg, profileDir, bcfg.Window)
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
	// rod emulates its "laptop" device on every page it hands out, and that
	// device carries a fixed Mac Chrome/114 user agent plus an "en" language,
	// while the real Chrome is a different build on a different OS (and
	// navigator.platform still says so): a mismatched user agent. Keep the
	// viewport emulation the watcher is tuned for, but report the browser's
	// own user agent and language.
	dev := devices.LaptopWithMDPIScreen.Landscape()
	if ver, err := br.Version(); err == nil && ver.UserAgent != "" {
		dev.UserAgent = ver.UserAgent
	}
	dev.AcceptLanguage = "en-US,en"
	br.DefaultDevice(dev)

	page, err := adoptPage(br)
	if err != nil {
		cancel()
		_ = br.Close()
		_ = proc.Kill()
		return nil, err
	}

	in := &Instance{
		logger:     logger,
		chrome:     proc,
		browser:    br,
		ctx:        sessCtx,
		cancel:     cancel,
		dimOpacity: dimOpacity,
		tabs:       map[proto.TargetSessionID]*tab{},
		pumpDone:   make(chan struct{}),
		done:       make(chan struct{}),
	}
	in.alive.Store(true)
	in.adopt(page, true)
	go in.pump()
	if bcfg.Window.AlwaysOnBottom {
		// Launch and every relaunch go through here, so a replacement Chrome
		// starts behind other windows too. The coordinator and the watch loop
		// keep re-asserting afterwards.
		if err := in.SendToBottom(ctx); err != nil {
			logBottomErr(logger, err)
		}
	}
	return in, nil
}

// SendToBottom pushes this Chrome's window(s) to the bottom of the OS z-order
// without activating them. errors.ErrUnsupported off Windows.
func (in *Instance) SendToBottom(context.Context) error {
	n, err := sendToBottom(uint32(in.chrome.pid))
	if err != nil {
		return err
	}
	in.logger.Debug().Int("windows", n).Msg("window: pushed to bottom of z-order")
	return nil
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
// one only if the target list somehow has none.
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

// adopt wraps a rod page as a tab of this instance: it registers the tab so the
// event pump can route to it, enables the domains whose events the watcher
// needs, and applies the per-page policy (dark emulation, burn-in dim overlay).
func (in *Instance) adopt(p *rod.Page, owner bool) *tab {
	t := &tab{
		inst:   in,
		logger: in.logger,
		page:   p,
		sid:    p.SessionID,
		frame:  p.FrameID,
		events: make(chan Event, eventBuffer),
		owner:  owner,
	}
	t.alive.Store(true)

	in.mu.Lock()
	in.tabs[t.sid] = t
	if owner {
		in.owner = t
	}
	in.mu.Unlock()

	// rod records these in its own domain-state map, so its internal EachEvent
	// helpers will not disable them again when they finish.
	if err := (proto.PageEnable{}).Call(p); err != nil {
		in.logger.Warn().Err(err).Msg("could not enable Page domain")
	}
	if err := (proto.InspectorEnable{}).Call(p); err != nil {
		in.logger.Warn().Err(err).Msg("could not enable Inspector domain")
	}
	if err := t.applyDarkEmulation(); err != nil {
		in.logger.Warn().Err(err).Msg("could not force dark color scheme")
	}
	if op := in.dimOpacity; op > 0 {
		script := dimOverlayScript(op)
		if err := t.SeedOnNewDocument(in.ctx, script); err != nil {
			in.logger.Warn().Err(err).Msg("could not install dim overlay")
		} else if _, err := t.page.Eval(script); err != nil {
			// The seeded copy only runs on the next document; paint the current
			// one too so about:blank is not left bright.
			in.logger.Debug().Err(err).Msg("dim overlay not applied to the current document")
		}
	}
	return t
}

// Owner returns the tab Chrome opened at launch. Closing it closes the whole
// instance: that tab IS the window, and no other tab can outlive it.
func (in *Instance) Owner() Session {
	in.mu.Lock()
	defer in.mu.Unlock()
	return in.owner
}

// NewTab opens an additional tab in this Chrome WINDOW (Target.createTarget
// without newWindow) and returns it as a Session. Closing that tab closes only
// the tab; the window and the other platforms' tabs are untouched. ctx bounds
// the create call only - the page itself is bound to the instance lifetime.
func (in *Instance) NewTab(ctx context.Context) (Session, error) {
	if !in.alive.Load() {
		return nil, fmt.Errorf("browser: cannot open a tab: chrome instance is not running")
	}
	// Background: a tab opened while another platform's tab is mid-watch must
	// never steal the foreground (a backgrounded player reads "hidden" and stops
	// crediting); the window coordinator activates tabs explicitly when needed.
	p, err := in.browser.Context(ctx).Page(proto.TargetCreateTarget{URL: "about:blank", Background: true})
	if err != nil {
		return nil, fmt.Errorf("browser: create tab: %w", err)
	}
	t := in.adopt(p.Context(in.ctx), false)
	in.logger.Info().Msg("browser: opened an extra tab in the shared window")
	return t, nil
}

// CloseInitialTab closes the page Chrome opened at launch (the owner/keeper),
// leaving the window running on the tabs opened since. It is a no-op once the
// keeper is gone. The caller must have opened at least one other tab first, or
// closing the last page would close the whole window.
func (in *Instance) CloseInitialTab() {
	in.mu.Lock()
	keeper := in.owner
	in.owner = nil
	in.mu.Unlock()
	if keeper == nil {
		return
	}
	keeper.closing.Store(true)
	keeper.alive.Store(false)
	in.forget(keeper)
	if _, err := (proto.TargetCloseTarget{TargetID: keeper.page.TargetID}).Call(in.browser); err != nil {
		in.logger.Debug().Err(err).Msg("browser: close keeper tab")
	}
	keeper.closeEvents()
}

// SetMinimized minimizes or restores the whole Chrome window through any live
// tab (the keeper at launch, or a platform tab afterwards). Window bounds are a
// window property, so which tab drives the call does not matter.
func (in *Instance) SetMinimized(ctx context.Context, v bool) error {
	in.mu.Lock()
	t := in.owner
	if t == nil {
		for _, any := range in.tabs {
			t = any
			break
		}
	}
	in.mu.Unlock()
	if t == nil {
		return fmt.Errorf("browser: no tab to minimize the window through")
	}
	return t.SetMinimized(ctx, v)
}

// Done is closed when the CDP transport is gone (Chrome died, crashed, or was
// closed). A supervisor waits on it to rebuild the instance and its tabs.
func (in *Instance) Done() <-chan struct{} { return in.done }

// Alive reports whether the Chrome/CDP connection is still up.
func (in *Instance) Alive() bool { return in.alive.Load() }

func (in *Instance) tabFor(sid proto.TargetSessionID) *tab {
	in.mu.Lock()
	defer in.mu.Unlock()
	return in.tabs[sid]
}

func (in *Instance) snapshotTabs() []*tab {
	in.mu.Lock()
	defer in.mu.Unlock()
	out := make([]*tab, 0, len(in.tabs))
	for _, t := range in.tabs {
		out = append(out, t)
	}
	return out
}

func (in *Instance) forget(t *tab) {
	in.mu.Lock()
	delete(in.tabs, t.sid)
	in.mu.Unlock()
}

// pump translates CDP events into per-tab Session events. It subscribes at the
// browser level (rather than the page level) because that channel closes when
// the CDP transport dies, which is how a Chrome crash or kill is detected, and
// because one subscription serves every tab. It owns each tab's event channel
// and is the only closer.
func (in *Instance) pump() {
	defer close(in.pumpDone)

	var (
		crashed   = (proto.InspectorTargetCrashed{}).ProtoEvent()
		navigated = (proto.PageFrameNavigated{}).ProtoEvent()
	)

	for msg := range in.browser.Event() {
		t := in.tabFor(msg.SessionID)
		if t == nil {
			continue
		}
		switch msg.Method {
		case crashed:
			t.logger.Error().Msg("renderer crashed")
			t.emit(Event{Kind: EventCrash, At: time.Now()})
		case navigated:
			evt := &proto.PageFrameNavigated{}
			if !msg.Load(evt) || evt.Frame == nil {
				continue
			}
			// Ignore iframes: only a top-frame navigation is a raid or redirect.
			if evt.Frame.ParentID != "" || evt.Frame.ID != t.frame {
				continue
			}
			t.emit(Event{Kind: EventNavigated, URL: evt.Frame.URL, At: time.Now()})
			// The emulation override does not always survive a cross-document
			// navigation. Reassert off the pump goroutine so event delivery is
			// never blocked on a CDP round trip.
			go func(t *tab) {
				if err := t.applyDarkEmulation(); err != nil {
					t.logger.Debug().Err(err).Msg("could not reapply dark color scheme after navigation")
				}
			}(t)
		}
	}

	// The browser event channel only closes when the CDP transport is gone: the
	// whole instance died, and every tab with it.
	in.alive.Store(false)
	closing := in.closing.Load()
	if !closing {
		in.logger.Error().Msg("cdp connection lost")
	}
	for _, t := range in.snapshotTabs() {
		t.alive.Store(false)
		if !closing && !t.closing.Load() {
			t.emit(Event{Kind: EventDisconnect, At: time.Now()})
		}
		t.closeEvents()
	}
	close(in.done)
}

// Close terminates the Chrome process and releases the profile lock. Every tab
// goes with it.
func (in *Instance) Close() error {
	var err error
	in.closeOnce.Do(func() {
		// Set closing first so the pump reports a clean shutdown rather than a
		// spurious disconnect event.
		in.closing.Store(true)
		in.alive.Store(false)

		if cerr := in.browser.Close(); cerr != nil {
			in.logger.Debug().Err(cerr).Msg("cdp browser close")
		}
		in.cancel()

		if kerr := in.chrome.Kill(); kerr != nil {
			err = kerr
		}

		select {
		case <-in.pumpDone:
		case <-time.After(closeTimeout):
			in.logger.Warn().Msg("browser event pump did not stop in time")
		}
	})
	return err
}

// OpenBackgroundTab opens url as a new tab in the Chrome window that s drives,
// in the BACKGROUND (Target.createTarget Background: true): the session's own
// tab keeps the foreground and keeps playing. The new tab is not adopted -
// nothing drives it; the user reads it and closes it. It errors when s is not
// backed by a live rod tab (a fake, or a Managed handle mid-relaunch).
func OpenBackgroundTab(ctx context.Context, s Session, url string) error {
	for {
		switch v := s.(type) {
		case *Managed:
			s = v.session()
			continue
		case *tab:
			if !v.inst.alive.Load() {
				return fmt.Errorf("browser: cannot open a tab: chrome instance is not running")
			}
			_, err := proto.TargetCreateTarget{URL: url, Background: true}.Call(v.inst.browser.Context(ctx))
			if err != nil {
				return fmt.Errorf("browser: open background tab: %w", err)
			}
			return nil
		default:
			return fmt.Errorf("browser: session has no chrome window to open a tab in")
		}
	}
}
