package watcher

import (
	"context"
	"sync"

	"github.com/rake-pro/dropwarden/internal/browser"
	"github.com/rake-pro/dropwarden/internal/discovery"
	"github.com/rake-pro/dropwarden/internal/domain"
	"github.com/rake-pro/dropwarden/internal/twitch"
)

// fakeSession is a no-op browser.Session for handler tests.
type fakeSession struct {
	events chan browser.Event
}

func newFakeSession() *fakeSession { return &fakeSession{events: make(chan browser.Event, 4)} }

func (f *fakeSession) Navigate(context.Context, string) error          { return nil }
func (f *fakeSession) Reload(context.Context, bool) error              { return nil }
func (f *fakeSession) CurrentURL(context.Context) (string, error)      { return "", nil }
func (f *fakeSession) Eval(context.Context, string, any) error         { return nil }
func (f *fakeSession) Click(context.Context, string) (bool, error)     { return true, nil }
func (f *fakeSession) SeedOnNewDocument(context.Context, string) error { return nil }
func (f *fakeSession) Events() <-chan browser.Event                    { return f.events }
func (f *fakeSession) ShiftWindow(int, int) error                      { return nil }
func (f *fakeSession) Alive() bool                                     { return true }
func (f *fakeSession) Close() error                                    { return nil }

// fakeController implements twitch.Controller with scriptable outcomes.
type fakeController struct {
	sess         browser.Session
	health       twitch.Health
	probeErr     error
	claimVisible bool
	claimInv     bool
}

func newFakeController() *fakeController {
	return &fakeController{sess: newFakeSession()}
}

func (f *fakeController) Session() browser.Session { return f.sess }
func (f *fakeController) Probe(context.Context) (twitch.Health, error) {
	return f.health, f.probeErr
}
func (f *fakeController) ApplyPlayerPolicy(context.Context, twitch.PlayerPolicy) error { return nil }
func (f *fakeController) ReassertVolume(context.Context, twitch.PlayerPolicy) error    { return nil }
func (f *fakeController) ClickPlay(context.Context) error                              { return nil }
func (f *fakeController) ToggleDropsPanel(context.Context) error                       { return nil }
func (f *fakeController) CancelRaid(context.Context) (bool, error)                     { return false, nil }
func (f *fakeController) ClaimVisible(context.Context) (bool, error)                   { return f.claimVisible, nil }
func (f *fakeController) ClaimInInventory(context.Context, string) (bool, error) {
	return f.claimInv, nil
}

// fakePager implements Pager.
type fakePager struct {
	channels    []string
	progress    []domain.Progress
	currentDrop *domain.Progress
	inventory   []domain.Progress
	pollCalls   int
}

func (f *fakePager) OpenChannel(context.Context, string, twitch.PlayerPolicy) error { return nil }
func (f *fakePager) ReadInventory(context.Context) ([]domain.Progress, error) {
	return f.progress, nil
}
func (f *fakePager) DirectoryChannels(context.Context, string) ([]string, error) {
	return f.channels, nil
}
func (f *fakePager) FetchCurrentDrop(context.Context) (*domain.Progress, error) {
	f.pollCalls++
	return f.currentDrop, nil
}
func (f *fakePager) FetchInventoryProgress(context.Context) ([]domain.Progress, error) {
	return f.inventory, nil
}

// fakeDisco implements Discoverer.
type fakeDisco struct{ res discovery.Result }

func (f *fakeDisco) RunOnce(context.Context) (discovery.Result, error) { return f.res, nil }

// recordingEmitter captures emitted events for assertions.
type recordingEmitter struct {
	mu     sync.Mutex
	events map[string]int
	last   map[string]any
}

func newRecordingEmitter() *recordingEmitter {
	return &recordingEmitter{events: map[string]int{}, last: map[string]any{}}
}

func (r *recordingEmitter) Emit(event string, payload any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events[event]++
	r.last[event] = payload
}

func (r *recordingEmitter) count(event string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.events[event]
}
