package application

import (
	"context"
	"sync"
	"time"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/updates/domain"
)

// Releases answers "what is the newest published release" — one HTTP call away
// behind the port, so the service never names a host.
type Releases interface {
	LatestRelease(ctx context.Context) (domain.Latest, error)
}

// CheckResult is what the shell shows: the running version, the newest one,
// and whether the question could even be asked. An unreachable release feed is
// not an error state — the app works fine without knowing.
type CheckResult struct {
	Current   string `json:"current"`
	Latest    string `json:"latest"`
	URL       string `json:"url"`
	Newer     bool   `json:"newer"`
	Reachable bool   `json:"reachable"`
	CheckedAt string `json:"checkedAt"`
}

// cacheTTL keeps one launch from asking more than once and a long session from
// asking more than twice a day: release feeds answer with rate limits, and the
// answer is not urgent enough to pay them.
const cacheTTL = 6 * time.Hour

type Service struct {
	current  string
	releases Releases
	now      func() time.Time
	mu       sync.Mutex
	cached   *CheckResult
	cachedAt time.Time
}

func NewService(current string, releases Releases) *Service {
	return &Service{current: current, releases: releases, now: time.Now}
}

func (service *Service) Check(ctx context.Context) CheckResult {
	service.mu.Lock()
	if service.cached != nil && service.now().Sub(service.cachedAt) < cacheTTL {
		cached := *service.cached
		service.mu.Unlock()
		return cached
	}
	service.mu.Unlock()

	result := CheckResult{Current: service.current, CheckedAt: service.now().UTC().Format(time.RFC3339)}
	if service.releases != nil {
		if latest, err := service.releases.LatestRelease(ctx); err == nil && latest.Version != "" {
			result.Reachable = true
			result.Latest = latest.Version
			result.URL = latest.URL
			result.Newer = domain.NewerThan(latest.Version, service.current)
		}
	}
	service.mu.Lock()
	service.cached = &result
	service.cachedAt = service.now()
	service.mu.Unlock()
	return result
}
