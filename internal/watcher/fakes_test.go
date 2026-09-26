package watcher

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

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
func (f *fakeSession) SetMinimized(context.Context, bool) error        { return nil }
func (f *fakeSession) Alive() bool                                     { return true }
func (f *fakeSession) Close() error                                    { return nil }

// fakeController implements twitch.Controller with scriptable outcomes.
type fakeController struct {
	sess         browser.Session
	health       twitch.Health
	probeErr     error
	claimInv     bool   // ClaimInInventory result
	claimInvSel  string // selector ClaimInInventory was last called with
	claimInvHits int    // ClaimInInventory calls
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
func (f *fakeController) ClaimInInventory(_ context.Context, selector, _ string) (bool, error) {
	f.claimInvHits++
	f.claimInvSel = selector
	return f.claimInv, nil
}

// fakePager implements Pager.
type fakePager struct {
	channels    []string
	live        []domain.Channel // LiveChannels result; liveErr wins when set
	liveErr     error
	liveCalls   int
	heartbeat   domain.Heartbeat
	progress    []domain.Progress
	currentDrop *domain.Progress
	inventory   []domain.Progress
	pollCalls   int

	// Claim scripting: claimStatus/claimErr are ClaimDrop's result (claimErr
	// may be errors.ErrUnsupported); claimed lists the claim ids sent;
	// afterClaim, when set, replaces inventory once a claim succeeds (the
	// verification re-read).
	claimStatus  string
	claimErr     error
	claimed      []string
	afterClaim   []domain.Progress
	claimMarks   bool  // a successful claim marks the matching inventory row isClaimed (when afterClaim is nil)
	invErr       error // FetchInventoryProgress error (e.g. no currentUser, errors.ErrUnsupported)
	openInvCalls int
	buttonsCalls int
}

func (f *fakePager) OpenChannel(context.Context, string, twitch.PlayerPolicy) error { return nil }
func (f *fakePager) ReadInventory(context.Context) ([]domain.Progress, error) {
	return f.progress, nil
}
func (f *fakePager) DirectoryChannels(context.Context, string) ([]string, error) {
	return f.channels, nil
}
func (f *fakePager) DrainHeartbeat(context.Context) (domain.Heartbeat, error) {
	return f.heartbeat, nil
}
func (f *fakePager) LiveChannels(_ context.Context, logins []string) ([]domain.Channel, error) {
	f.liveCalls++
	if f.liveErr != nil {
		return nil, f.liveErr
	}
	var out []domain.Channel
	for _, ch := range f.live {
		for _, l := range logins {
			if strings.EqualFold(l, ch.Login) {
				out = append(out, ch)
			}
		}
	}
	return out, nil
}
func (f *fakePager) FetchCurrentDrop(context.Context) (*domain.Progress, error) {
	f.pollCalls++
	return f.currentDrop, nil
}
func (f *fakePager) FetchInventoryProgress(context.Context) ([]domain.Progress, error) {
	if f.invErr != nil {
		return nil, f.invErr
	}
	return f.inventory, nil
}

// fakeDisco implements Discoverer.
type fakeDisco struct {
	res   discovery.Result
	calls int
}

func (f *fakeDisco) RunOnce(context.Context) (discovery.Result, error) {
	f.calls++
	return f.res, nil
}

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

func (f *fakePager) DrainProgress(context.Context) ([]domain.Progress, error) {
	return nil, nil
}

func (f *fakePager) ClaimDrop(_ context.Context, claimID string) (string, string, error) {
	f.claimed = append(f.claimed, claimID)
	if f.claimErr != nil {
		return f.claimStatus, "", f.claimErr
	}
	if f.afterClaim != nil {
		f.inventory = f.afterClaim
	} else if f.claimMarks {
		for i := range f.inventory {
			if f.inventory[i].ClaimID == claimID {
				at := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
				f.inventory[i].ClaimedAt = &at
				f.inventory[i].Claimable = false
			}
		}
	}
	return f.claimStatus, `{"data":{}}`, nil
}

func (f *fakePager) OpenInventory(context.Context) error {
	f.openInvCalls++
	return nil
}

func (f *fakePager) InventoryButtons(context.Context) ([]byte, int, error) {
	f.buttonsCalls++
	return nil, 0, errors.ErrUnsupported // no debug file writes from tests
}
