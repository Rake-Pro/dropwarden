// Package browser owns the dedicated Chrome instance: locating the binary,
// launching it unfocused with a dedicated profile, attaching over CDP, and
// driving the single watcher tab with generic (Twitch-agnostic) primitives.
// Twitch-specific behavior (selectors, playback health, claim clicks) lives in
// the twitch package, which wraps a Session.
package browser

import (
	"context"
	"time"
)

// EventKind classifies an asynchronous browser notification.
type EventKind string

const (
	EventCrash      EventKind = "crash"      // renderer/tab crashed (Inspector.targetCrashed)
	EventDisconnect EventKind = "disconnect" // CDP connection lost (Chrome process died)
	EventNavigated  EventKind = "navigated"  // top-frame URL changed (raid/redirect)
)

// Event carries an asynchronous browser signal to the watcher.
type Event struct {
	Kind EventKind
	URL  string
	At   time.Time
}

// Session is the generic control surface for the single watcher tab. It never
// brings the window to the foreground. The twitch layer and the watcher depend
// on this interface, so both can be exercised with a fake.
type Session interface {
	// Navigate loads url and waits for the initial load event (bounded by ctx).
	Navigate(ctx context.Context, url string) error
	// Reload reloads the page; ignoreCache forces a full resync (hard refresh).
	Reload(ctx context.Context, ignoreCache bool) error
	// CurrentURL returns the top-frame URL.
	CurrentURL(ctx context.Context) (string, error)

	// Eval runs a JS expression and unmarshals its JSON result into out (which
	// may be nil to ignore the result). The ctx deadline doubles as a hang probe:
	// a timeout here is how the watcher detects an unresponsive renderer.
	Eval(ctx context.Context, js string, out any) error

	// Click performs a human-like click on the first element matching selector:
	// scroll into view, a short hover dwell, then a real mouse dispatch at a
	// random point inside the element box. It reports false (no error) when no
	// element matches, so callers can treat "absent" as an ordinary outcome.
	Click(ctx context.Context, selector string) (clicked bool, err error)

	// SeedOnNewDocument installs a script that runs on every subsequent
	// navigation before page scripts (Page.addScriptToEvaluateOnNewDocument),
	// used for the dim overlay and localStorage seeding.
	SeedOnNewDocument(ctx context.Context, js string) error

	// Events returns the async notification channel (crash/disconnect/navigation).
	Events() <-chan Event

	// ShiftWindow nudges the OS window position by (dx,dy) for burn-in avoidance,
	// without activating it.
	ShiftWindow(dx, dy int) error

	// SetMinimized minimizes (or restores) the watcher window via CDP
	// (Browser.setWindowBounds), so only the dedicated instance is affected and
	// no OS accessibility permissions are needed.
	SetMinimized(ctx context.Context, minimized bool) error

	// Alive reports whether the underlying Chrome/CDP connection is still up.
	Alive() bool

	// Close terminates the Chrome process and releases the profile lock.
	Close() error
}
