package browser

import (
	"context"
	"fmt"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

// Clock is the time source a Pacer uses. Injecting it is what makes the pacing
// rules testable without real sleeps.
type Clock interface {
	Now() time.Time
	Sleep(time.Duration)
}

type realClock struct{}

func (realClock) Now() time.Time        { return time.Now() }
func (realClock) Sleep(d time.Duration) { time.Sleep(d) }

// RealClock returns the wall-clock implementation of Clock.
func RealClock() Clock { return realClock{} }

// ActionClass names a category of browser action that gets its own minimum
// spacing. Two actions of different classes do not pace each other.
type ActionClass int

const (
	Navigation ActionClass = iota
	Click
	HardRefresh
	DiscoveryCycle
	DirectoryPoll
)

func (c ActionClass) String() string {
	switch c {
	case Navigation:
		return "navigation"
	case Click:
		return "click"
	case HardRefresh:
		return "hard_refresh"
	case DiscoveryCycle:
		return "discovery_cycle"
	case DirectoryPoll:
		return "directory_poll"
	default:
		return fmt.Sprintf("class(%d)", int(c))
	}
}

type pacingRule struct {
	min    time.Duration
	jitter time.Duration
}

// pacingRules is the rate budget. These numbers are the whole point of the
// package's politeness story: they are floors on how often dropwarden may touch
// Twitch, chosen to stay well under what a human viewer would generate.
var pacingRules = map[ActionClass]pacingRule{
	Navigation:     {8 * time.Second, 12 * time.Second},
	Click:          {3 * time.Second, 2500 * time.Millisecond},
	HardRefresh:    {4 * time.Minute, 90 * time.Second},
	DiscoveryCycle: {30 * time.Minute, 30 * time.Minute},
	DirectoryPoll:  {4 * time.Minute, 3 * time.Minute},
}

// Navigation ceiling: no more than navCeiling navigations in any navWindow.
// This is a backstop on top of the per-class minimum, so a burst of retries
// (stall recovery, raid chasing, channel churn) cannot add up to a request rate
// that no human produces.
const (
	navCeiling = 12
	navWindow  = 10 * time.Minute
)

// Pacer enforces a minimum gap plus jitter between successive actions of the
// same class, and a rolling ceiling on navigations. It is safe for concurrent
// use.
type Pacer struct {
	mu    sync.Mutex
	clock Clock
	rnd   func() float64
	last  map[ActionClass]time.Time
	navs  []time.Time
}

// NewPacer builds a Pacer over the given clock and random source. rnd must
// return a value in [0,1); pass nil for the default source. A deterministic rnd
// plus a fake clock makes the spacing rules fully testable.
func NewPacer(clock Clock, rnd func() float64) *Pacer {
	if clock == nil {
		clock = RealClock()
	}
	if rnd == nil {
		rnd = rand.Float64
	}
	return &Pacer{clock: clock, rnd: rnd, last: make(map[ActionClass]time.Time)}
}

// Wait blocks until the next action of the given class is allowed to run. It
// returns ctx.Err() if the context is cancelled before or during the wait, in
// which case the caller must not perform the action.
func (p *Pacer) Wait(ctx context.Context, class ActionClass) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	wait, err := p.plan(class)
	if err != nil {
		return err
	}
	if wait > 0 {
		p.clock.Sleep(wait)
		if err := ctx.Err(); err != nil {
			return err
		}
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.clock.Now()
	p.last[class] = now
	if class == Navigation {
		p.navs = append(p.navs, now)
	}
	return nil
}

// plan computes how long the caller must sleep before the next action of class
// may run.
func (p *Pacer) plan(class ActionClass) (time.Duration, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	rule, ok := pacingRules[class]
	if !ok {
		return 0, fmt.Errorf("browser: pacer: unknown action class %s", class)
	}

	now := p.clock.Now()
	var wait time.Duration
	if last, seen := p.last[class]; seen {
		if gap := rule.min - now.Sub(last); gap > 0 {
			wait = gap
		}
	}
	wait += time.Duration(p.rnd() * float64(rule.jitter))

	if class == Navigation {
		wait += p.navCeilingWaitLocked(now.Add(wait))
	}
	return wait, nil
}

// navCeilingWaitLocked returns the extra delay needed so that performing a
// navigation at time at would not exceed navCeiling navigations per navWindow.
// The caller must hold p.mu.
func (p *Pacer) navCeilingWaitLocked(at time.Time) time.Duration {
	cutoff := at.Add(-navWindow)
	recent := p.navs[:0:0]
	for _, t := range p.navs {
		if t.After(cutoff) {
			recent = append(recent, t)
		}
	}
	p.navs = recent

	if len(recent) < navCeiling {
		return 0
	}
	// The oldest navigation still counting against the budget has to age out of
	// the window before another one may run.
	oldest := recent[len(recent)-navCeiling]
	extra := oldest.Add(navWindow).Sub(at)
	if extra <= 0 {
		return 0
	}
	log.Warn().
		Dur("delay", extra).
		Int("ceiling", navCeiling).
		Dur("window", navWindow).
		Msg("navigation rate ceiling reached, throttling")
	return extra
}
