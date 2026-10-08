package application

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/updates/domain"
)

type fakeReleases struct {
	latest domain.Latest
	err    error
	calls  int
	// block, when set, parks LatestRelease until the channel closes or
	// receives: the single-flight tests need a check in flight to join.
	block <-chan struct{}
	mu    sync.Mutex
}

func (releases *fakeReleases) LatestRelease(ctx context.Context) (domain.Latest, error) {
	releases.mu.Lock()
	releases.calls++
	releases.mu.Unlock()
	if releases.block != nil {
		select {
		case <-releases.block:
		case <-ctx.Done():
		}
	}
	releases.mu.Lock()
	defer releases.mu.Unlock()
	return releases.latest, releases.err
}

func (releases *fakeReleases) callCount() int {
	releases.mu.Lock()
	defer releases.mu.Unlock()
	return releases.calls
}

func (releases *fakeReleases) Open(context.Context, string) (io.ReadCloser, int64, error) {
	return nil, 0, errors.New("no assets in this fixture")
}

func TestAnUpdateIsReportedOnlyWhenTheTagIsNewer(t *testing.T) {
	service := NewService("1.0.38", &fakeReleases{latest: domain.Latest{Version: "v1.0.39", URL: "https://example.test/release"}})
	result := service.Check(context.Background())
	if !result.Newer || result.Latest != "v1.0.39" || !result.Reachable {
		t.Fatalf("a newer release was not reported: %+v", result)
	}
}

func TestAUnreachableFeedIsQuiet(t *testing.T) {
	service := NewService("1.0.38", &fakeReleases{err: errors.New("offline")})
	result := service.Check(context.Background())
	if result.Newer || result.Reachable {
		t.Fatalf("an unreachable feed read as an update: %+v", result)
	}
	if result.Current != "1.0.38" {
		t.Fatalf("the running version was lost: %+v", result)
	}
}

// The refresher owns freshness now, so Check is a real round trip every time:
// a manual "Check now" that arrives a second after the last pass must not be
// swallowed by a time window the caller did not ask for.
func TestEveryCheckAsksTheFeed(t *testing.T) {
	releases := &fakeReleases{latest: domain.Latest{Version: "v1.0.39"}}
	service := NewService("1.0.38", releases)
	service.Check(context.Background())
	service.Check(context.Background())
	if releases.callCount() != 2 {
		t.Fatalf("a check did not ask the feed: %d calls", releases.callCount())
	}
}

// Status is the cached read the interface polls: it answers without a round
// trip, so a settings page or shell pill never pays the feed for asking.
func TestStatusAnswersWithoutTheFeed(t *testing.T) {
	releases := &fakeReleases{latest: domain.Latest{Version: "v1.0.39"}}
	service := NewService("1.0.38", releases)
	if result := service.Status(); result.Latest != "" {
		t.Fatalf("a fresh service answered with a verdict: %+v", result)
	}
	service.Check(context.Background())
	for reads := 0; reads < 3; reads++ {
		if result := service.Status(); !result.Newer || result.Latest != "v1.0.39" {
			t.Fatalf("the last answer was lost: %+v", result)
		}
	}
	if releases.callCount() != 1 {
		t.Fatalf("Status asked the feed: %d calls", releases.callCount())
	}
}

// A caller that arrives while a check is running joins it rather than
// starting a second round trip: the refresher's pass and an operator's manual
// click are one request, even when the feed is slow.
func TestConcurrentChecksShareOneRoundTrip(t *testing.T) {
	block := make(chan struct{})
	releases := &fakeReleases{latest: domain.Latest{Version: "v1.0.39"}, block: block}
	service := NewService("1.0.38", releases)

	const joiners = 4
	results := make([]CheckResult, joiners)
	var started sync.WaitGroup
	var joined sync.WaitGroup
	joined.Add(joiners)
	for i := 0; i < joiners; i++ {
		started.Add(1)
		go func() {
			started.Done()
			results[i] = service.Check(context.Background())
			joined.Done()
		}()
	}
	// Wait until every joiner is inside the flight (or already waiting on
	// its result) before letting the feed answer.
	started.Wait()
	time.Sleep(50 * time.Millisecond)
	close(block)
	joined.Wait()

	for i, result := range results {
		if !result.Newer || result.Latest != "v1.0.39" {
			t.Fatalf("a joiner missed the shared answer %d: %+v", i, result)
		}
	}
	if releases.callCount() != 1 {
		t.Fatalf("concurrent checks did not share one round trip: %d calls", releases.callCount())
	}
}

// A joiner that gives up waiting still gets the freshest completed answer
// rather than a verdict it cannot use: cancellation cancels the waiting, not
// the truth.
func TestACancelledJoinerStillGetsTheLastAnswer(t *testing.T) {
	// A first, fast flight completes so there is a real answer to fall
	// back on while the slow one runs.
	releases := &fakeReleases{latest: domain.Latest{Version: "v1.0.39"}}
	service := NewService("1.0.38", releases)
	service.Check(context.Background())

	// A second, slow flight starts and parks inside the feed.
	block := make(chan struct{})
	releases.mu.Lock()
	releases.block = block
	releases.mu.Unlock()
	go func() { service.Check(context.Background()) }()
	for deadline := time.Now().Add(2 * time.Second); releases.callCount() < 2 && time.Now().Before(deadline); {
		time.Sleep(5 * time.Millisecond)
	}
	if releases.callCount() < 2 {
		t.Fatal("the slow flight never started")
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	result := service.Check(cancelled)
	if result.Latest != "v1.0.39" {
		t.Fatalf("the cancelled joiner was not told the last answer: %+v", result)
	}

	// Let the slow flight finish so the test leaves nothing in flight.
	close(block)
	flightOngoing := func() bool {
		service.mu.Lock()
		defer service.mu.Unlock()
		return service.inFlight != nil
	}
	for deadline := time.Now().Add(2 * time.Second); flightOngoing() && time.Now().Before(deadline); {
		time.Sleep(5 * time.Millisecond)
	}
	if flightOngoing() {
		t.Fatal("the slow flight never finished")
	}
}

// The timestamp records the asking, not the answer: an operator reading
// "last checked" must see that a check happened even when the feed was dark.
func TestAnUnreachableFeedIsStillRecordedAsChecked(t *testing.T) {
	now := time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)
	releases := &fakeReleases{err: errors.New("offline")}
	service := NewService("1.0.38", releases)
	service.now = func() time.Time { return now }
	result := service.Check(context.Background())
	if result.Reachable {
		t.Fatalf("the offline feed read as reachable: %+v", result)
	}
	if result.CheckedAt != now.Format(time.RFC3339) {
		t.Fatalf("the attempt was not recorded: %+v", result)
	}
}

func TestTheStatusSurvivesRefetch(t *testing.T) {
	releases := &fakeReleases{latest: domain.Latest{Version: "v1.0.39"}}
	service := NewService("1.0.38", releases)
	service.Check(context.Background())
	releases.mu.Lock()
	releases.latest = domain.Latest{Version: "v1.0.40"}
	releases.mu.Unlock()
	result := service.Check(context.Background())
	if result.Latest != "v1.0.40" || !result.Newer {
		t.Fatalf("the second fetch kept the first answer: %+v", result)
	}
	if stale := service.Status(); stale.Latest != "v1.0.40" {
		t.Fatalf("the status did not follow the fetch: %+v", stale)
	}
}
