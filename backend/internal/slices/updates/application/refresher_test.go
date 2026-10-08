package application

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/updates/domain"
)

func TestTheIntervalEnumMapsToDurations(t *testing.T) {
	cases := map[string]time.Duration{
		"1m":  time.Minute,
		"5m":  5 * time.Minute,
		"30m": 30 * time.Minute,
		"1h":  time.Hour,
		"off": 0,
		"":    0,
		"2m":  0,
	}
	for setting, want := range cases {
		if got := IntervalFromSetting(setting); got != want {
			t.Fatalf("IntervalFromSetting(%q) = %v, want %v", setting, got, want)
		}
	}
}

func TestTheBackoffLadderDoublesAndCaps(t *testing.T) {
	refresher := NewRefresher(nil)
	refresher.SetInterval(time.Minute)
	for _, want := range []time.Duration{
		time.Minute,
		2 * time.Minute,
		4 * time.Minute,
		8 * time.Minute,
		16 * time.Minute,
		32 * time.Minute,
		time.Hour, // 64 minutes is capped, not exceeded
		time.Hour,
		time.Hour,
	} {
		if got := refresher.delay(); got != want {
			t.Fatalf("delay after %d failures = %v, want %v", refresher.failures, got, want)
		}
		refresher.failures++
	}
}

func TestZeroIntervalMeansZeroDelay(t *testing.T) {
	refresher := NewRefresher(nil)
	if got := refresher.delay(); got != 0 {
		t.Fatalf("a fresh refresher delays: %v", got)
	}
	refresher.SetInterval(time.Minute)
	refresher.failures = 5
	if got := refresher.delay(); got != 32*time.Minute {
		t.Fatalf("five failures delayed %v, want 32m", got)
	}
	refresher.SetInterval(0)
	if got := refresher.delay(); got != 0 {
		t.Fatalf("a disabled refresher delayed: %v", got)
	}
}

func TestVerdictChangesAreNews(t *testing.T) {
	first := CheckResult{Current: "1.0.38", Reachable: true, Latest: "v1.0.39", Newer: true, CheckedAt: "2025-06-01T12:00:00Z"}
	cases := []struct {
		name   string
		before CheckResult
		after  CheckResult
		want   bool
	}{
		{"the first answer of a run", CheckResult{}, first, true},
		{"an identical answer", first, first, false},
		{"a new latest version", first, func() CheckResult { next := first; next.Latest = "v1.0.40"; return next }(), true},
		{"an update appearing", func() CheckResult { next := first; next.Newer = false; next.Latest = "1.0.38"; return next }(), first, true},
		{"the feed going dark", first, func() CheckResult { next := first; next.Reachable = false; return next }(), true},
		{"the feed coming back", func() CheckResult { next := first; next.Reachable = false; return next }(), first, true},
		{"a later timestamp alone", first, func() CheckResult { next := first; next.CheckedAt = "2025-06-01T12:01:00Z"; return next }(), false},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if got := verdictChanged(test.before, test.after); got != test.want {
				t.Fatalf("verdictChanged = %v, want %v", got, test.want)
			}
		})
	}
}

// refresherSpy answers whether the listener was told, and what.
type refresherSpy struct {
	mu    sync.Mutex
	tolds []CheckResult
}

func (spy *refresherSpy) listener(result CheckResult) {
	spy.mu.Lock()
	defer spy.mu.Unlock()
	spy.tolds = append(spy.tolds, result)
}

func (spy *refresherSpy) count() int {
	spy.mu.Lock()
	defer spy.mu.Unlock()
	return len(spy.tolds)
}

func (spy *refresherSpy) last() CheckResult {
	spy.mu.Lock()
	defer spy.mu.Unlock()
	if len(spy.tolds) == 0 {
		return CheckResult{}
	}
	return spy.tolds[len(spy.tolds)-1]
}

// A run that starts with checks on asks immediately: a launch deserves its
// own answer, not an interval of silence.
func TestTheFirstPassIsImmediate(t *testing.T) {
	releases := &fakeReleases{latest: domain.Latest{Version: "v1.0.39"}}
	service := NewService("1.0.38", releases)
	refresher := NewRefresher(service)
	spy := &refresherSpy{}
	refresher.OnChanged(spy.listener)
	refresher.SetInterval(time.Hour)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { refresher.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()

	for deadline := time.Now().Add(2 * time.Second); spy.count() == 0 && time.Now().Before(deadline); {
		time.Sleep(5 * time.Millisecond)
	}
	if spy.count() == 0 {
		t.Fatal("the first pass never happened")
	}
	if got := spy.last(); !got.Newer || got.Latest != "v1.0.39" {
		t.Fatalf("the first answer was not the verdict: %+v", got)
	}
	if releases.callCount() != 1 {
		t.Fatalf("the immediate pass asked more than once: %d calls", releases.callCount())
	}
}

// A run that starts disabled asks nothing, and a disabled loop stays parked
// on a nil tick channel rather than spinning.
func TestADisabledRunAsksNothing(t *testing.T) {
	releases := &fakeReleases{latest: domain.Latest{Version: "v1.0.39"}}
	service := NewService("1.0.38", releases)
	refresher := NewRefresher(service)
	refresher.SetInterval(0)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { refresher.Run(ctx); close(done) }()
	time.Sleep(100 * time.Millisecond)
	cancel()
	<-done

	if releases.callCount() != 0 {
		t.Fatalf("a disabled refresher asked the feed: %d calls", releases.callCount())
	}
}

// The cadence ticks while the feed is healthy, and a failure's verdict
// change is announced exactly once: dark then still-dark is not news.
func TestPassesAnnounceVerdictChangesOnlyWhenTheyChange(t *testing.T) {
	releases := &fakeReleases{latest: domain.Latest{Version: "v1.0.39"}}
	service := NewService("1.0.38", releases)
	refresher := NewRefresher(service)
	spy := &refresherSpy{}
	refresher.OnChanged(spy.listener)
	// Fast enough for a test, slow enough not to spin the loop.
	refresher.SetInterval(20 * time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { refresher.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()

	// Three identical passes: the first answer is news, the rest are not.
	for deadline := time.Now().Add(2 * time.Second); releases.callCount() < 3 && time.Now().Before(deadline); {
		time.Sleep(5 * time.Millisecond)
	}
	if releases.callCount() < 3 {
		t.Fatalf("the cadence never ticked: %d passes", releases.callCount())
	}
	time.Sleep(60 * time.Millisecond)
	if spy.count() != 1 {
		t.Fatalf("identical answers were announced %d times, want 1", spy.count())
	}

	// The feed goes dark: exactly one announcement for the flip.
	releases.mu.Lock()
	releases.err = errors.New("offline")
	releases.mu.Unlock()
	for deadline := time.Now().Add(2 * time.Second); service.Status().Reachable && time.Now().Before(deadline); {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(80 * time.Millisecond)
	if spy.count() != 2 {
		t.Fatalf("the feed going dark was announced %d times, want 2 total", spy.count())
	}

	// And the backoff grew: the fourth+ failure's delay is the interval
	// doubled, visible only through the ladder it produces.
	if refresher.delay() <= 20*time.Millisecond {
		t.Fatalf("failures did not grow the delay: %v", refresher.delay())
	}
}

// Re-arming on a live SetInterval re-arms the timer: the new cadence is
// measured from the change, and a mid-pass change coalesces into one
// re-arm rather than piling wakes.
func TestTheCadenceChangesLive(t *testing.T) {
	releases := &fakeReleases{latest: domain.Latest{Version: "v1.0.39"}}
	service := NewService("1.0.38", releases)
	refresher := NewRefresher(service)
	refresher.SetInterval(time.Hour) // parked far away

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { refresher.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()

	// The immediate first pass happens on Run.
	for deadline := time.Now().Add(2 * time.Second); releases.callCount() < 1 && time.Now().Before(deadline); {
		time.Sleep(5 * time.Millisecond)
	}

	// A save that speeds the cadence up: the wake re-arms the timer and the
	// next pass arrives on the new pace, not the hour that was armed.
	before := releases.callCount()
	refresher.SetInterval(10 * time.Millisecond)
	for deadline := time.Now().Add(2 * time.Second); releases.callCount() <= before && time.Now().Before(deadline); {
		time.Sleep(5 * time.Millisecond)
	}
	if releases.callCount() == before {
		t.Fatal("re-enabling a faster cadence never asked immediately")
	}
	for deadline := time.Now().Add(2 * time.Second); releases.callCount() < before+3 && time.Now().Before(deadline); {
		time.Sleep(5 * time.Millisecond)
	}
	if releases.callCount() < before+3 {
		t.Fatalf("the new cadence never ticked: %d passes", releases.callCount()-before)
	}
}

// Turning checks off stops the asking, and turning them back on asks at
// once: the operator chose to know, not to wait an interval first.
func TestOffThenOnAsksImmediately(t *testing.T) {
	releases := &fakeReleases{latest: domain.Latest{Version: "v1.0.39"}}
	service := NewService("1.0.38", releases)
	refresher := NewRefresher(service)
	refresher.SetInterval(time.Hour)
	refresher.SetInterval(0)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { refresher.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()

	time.Sleep(100 * time.Millisecond)
	if releases.callCount() != 0 {
		t.Fatalf("checks-off still asked: %d calls", releases.callCount())
	}

	refresher.SetInterval(time.Hour)
	for deadline := time.Now().Add(2 * time.Second); releases.callCount() == 0 && time.Now().Before(deadline); {
		time.Sleep(5 * time.Millisecond)
	}
	if releases.callCount() == 0 {
		t.Fatal("re-enabling never asked immediately")
	}
}

// The listener list is copied before the call, so a listener that
// registers its sibling still cannot race the iteration.
func TestListenersAreCopiedBeforeTheCall(t *testing.T) {
	service := NewService("1.0.38", nil)
	refresher := NewRefresher(service)
	var first, second refresherSpy
	refresher.OnChanged(first.listener)
	refresher.OnChanged(func(result CheckResult) {
		// Registering from inside a listener must not deadlock or panic.
		refresher.OnChanged(second.listener)
	})
	refresher.notify(CheckResult{Current: "1.0.38"})
	if first.count() != 1 {
		t.Fatal("the first listener was not told")
	}
	if second.count() != 0 {
		t.Fatal("a sibling registered mid-notify was told by the same pass")
	}
	refresher.notify(CheckResult{Current: "1.0.38"})
	if second.count() != 1 {
		t.Fatal("the late-registered listener was never told")
	}
}
