package application

import (
	"context"
	"sync"
	"time"
)

// backoffCap is the ceiling the failure ladder climbs to: a feed that stays
// dark is asked hourly at most, which is the politeness an unauthenticated
// release feed deserves even from a one-minute cadence.
const backoffCap = time.Hour

// IntervalFromSetting maps the settings enum onto the cadence the refresher
// runs at. "off", an empty value and anything unrecognized answer zero: the
// poller stops rather than guessing a pace its owner did not choose.
func IntervalFromSetting(setting string) time.Duration {
	switch setting {
	case "1m":
		return time.Minute
	case "5m":
		return 5 * time.Minute
	case "30m":
		return 30 * time.Minute
	case "1h":
		return time.Hour
	default:
		return 0
	}
}

// Refresher keeps the update answer fresh on a cadence the settings own.
// The Service holds the answer; this is only the asking. It re-checks on a
// timer, backs off when the feed refuses, and tells listeners when the
// verdict itself changes — "still current" one minute later is not news,
// a new latest version is.
type Refresher struct {
	service *Service

	listenersMu sync.Mutex
	listeners   []func(CheckResult)

	mu       sync.Mutex
	interval time.Duration
	failures int
	// kick is armed when the cadence turns back on: the operator just
	// chose to know, and the first answer after a silence is owed now,
	// not one interval from now.
	kick bool
	wake chan struct{}
}

func NewRefresher(service *Service) *Refresher {
	// Buffered by one so a SetInterval during a pass coalesces into the
	// single re-arm the loop performs after the pass returns.
	return &Refresher{service: service, wake: make(chan struct{}, 1)}
}

// SetInterval changes the cadence in place: a save in Settings re-arms the
// running loop without a restart. Zero stops the asking; coming back from
// zero asks immediately.
func (refresher *Refresher) SetInterval(interval time.Duration) {
	refresher.mu.Lock()
	wasAsking := refresher.interval > 0
	refresher.interval = interval
	if !wasAsking && interval > 0 {
		refresher.kick = true
	}
	refresher.mu.Unlock()
	refresher.wakeLoop()
}

// OnChanged registers a listener told after a pass whose verdict differs
// from the previous one: a version or reachability flip, or the first
// answer of the run. Listeners are copied before the call, so registering
// from inside a listener is safe.
func (refresher *Refresher) OnChanged(listener func(CheckResult)) {
	refresher.listenersMu.Lock()
	refresher.listeners = append(refresher.listeners, listener)
	refresher.listenersMu.Unlock()
}

// Interval answers the cadence in force; zero means not asking.
func (refresher *Refresher) Interval() time.Duration {
	refresher.mu.Lock()
	defer refresher.mu.Unlock()
	return refresher.interval
}

// Run asks until the context ends. The first pass happens immediately when
// checks are on — a launch deserves its own answer, not an interval of
// silence — and the timer follows. Run returns only with its context.
func (refresher *Refresher) Run(ctx context.Context) {
	// The initial duration is irrelevant: arm stops and re-arms the timer
	// before it can ever fire. Run's goroutine owns the timer alone, so
	// re-arming never races another hand.
	timer := time.NewTimer(time.Hour)
	defer timer.Stop()
	if refresher.Interval() > 0 {
		// A kick armed by a SetInterval that ran before Run owes this very
		// pass: consume it, and the wake that carries it, so a launch asks
		// once — not once for Run and once for the leftover signal.
		refresher.takeKick()
		refresher.drainWake()
		refresher.pass(ctx)
	}
	tick := refresher.arm(timer)
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick:
			refresher.pass(ctx)
			tick = refresher.arm(timer)
		case <-refresher.wake:
			if refresher.takeKick() && refresher.Interval() > 0 {
				refresher.pass(ctx)
			}
			tick = refresher.arm(timer)
		}
	}
}

// pass performs one check, records the outcome for the backoff ladder, and
// notifies listeners when the verdict changed.
func (refresher *Refresher) pass(ctx context.Context) {
	before := refresher.service.Status()
	result := refresher.service.Check(ctx)
	refresher.mu.Lock()
	if result.Reachable {
		refresher.failures = 0
	} else {
		refresher.failures++
	}
	refresher.mu.Unlock()
	if verdictChanged(before, result) {
		refresher.notify(result)
	}
}

// verdictChanged answers whether the second answer is news against the
// first: a new latest version, a newly available (or vanished) update, or
// the feed becoming reachable or dark. The first answer of a run is news
// too — an interface that connects before the first pass learns the
// verdict from the event, not from polling.
func verdictChanged(before, after CheckResult) bool {
	if before.CheckedAt == "" {
		return true
	}
	return before.Latest != after.Latest ||
		before.Newer != after.Newer ||
		before.Reachable != after.Reachable
}

func (refresher *Refresher) notify(result CheckResult) {
	refresher.listenersMu.Lock()
	listeners := make([]func(CheckResult), len(refresher.listeners))
	copy(listeners, refresher.listeners)
	refresher.listenersMu.Unlock()
	for _, listener := range listeners {
		listener(result)
	}
}

// arm re-arms the timer for the next pass and answers the channel that
// fires it; nil means disabled — a select on a nil channel blocks forever,
// which is exactly the semantics of "not asking".
func (refresher *Refresher) arm(timer *time.Timer) <-chan time.Time {
	if !timer.Stop() {
		// The timer fired while we were elsewhere; drain the stale value
		// so the next Reset measures from now, not from the old expiry.
		select {
		case <-timer.C:
		default:
		}
	}
	delay := refresher.delay()
	if delay <= 0 {
		return nil
	}
	timer.Reset(delay)
	return timer.C
}

// wakeLoop nudges the loop without blocking the caller: a buffered send
// that coalesces when the loop is mid-pass.
func (refresher *Refresher) wakeLoop() {
	select {
	case refresher.wake <- struct{}{}:
	default:
	}
}

// drainWake empties the wake a SetInterval left behind, pairing with
// takeKick when a pass is about to happen anyway.
func (refresher *Refresher) drainWake() {
	select {
	case <-refresher.wake:
	default:
	}
}

func (refresher *Refresher) takeKick() bool {
	refresher.mu.Lock()
	defer refresher.mu.Unlock()
	kick := refresher.kick
	refresher.kick = false
	return kick
}

// delay answers how long to wait before the next pass: the configured
// cadence doubled once per consecutive failure, capped at an hour. A feed
// that answers resets the ladder — the backoff is politeness, not
// punishment.
func (refresher *Refresher) delay() time.Duration {
	refresher.mu.Lock()
	defer refresher.mu.Unlock()
	if refresher.interval <= 0 {
		return 0
	}
	delay := refresher.interval
	for step := 0; step < refresher.failures; step++ {
		delay *= 2
		if delay >= backoffCap {
			return backoffCap
		}
	}
	return delay
}
