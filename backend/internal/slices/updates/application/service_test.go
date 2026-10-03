package application

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/updates/domain"
)

type fakeReleases struct {
	latest domain.Latest
	err    error
	calls  int
}

func (releases *fakeReleases) LatestRelease(context.Context) (domain.Latest, error) {
	releases.calls++
	return releases.latest, releases.err
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

// The feed is asked once per cache window, not once per caller: release feeds
// rate-limit, and the answer does not change by the minute.
func TestTheAnswerIsCachedForTheWindow(t *testing.T) {
	releases := &fakeReleases{latest: domain.Latest{Version: "v1.0.39"}}
	now := time.Now()
	service := NewService("1.0.38", releases)
	service.now = func() time.Time { return now }
	service.Check(context.Background())
	service.Check(context.Background())
	if releases.calls != 1 {
		t.Fatalf("the cache did not hold: %d calls", releases.calls)
	}
	service.now = func() time.Time { return now.Add(cacheTTL + time.Minute) }
	service.Check(context.Background())
	if releases.calls != 2 {
		t.Fatalf("the cache never expired: %d calls", releases.calls)
	}
}
