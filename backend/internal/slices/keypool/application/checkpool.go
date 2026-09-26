package application

import (
	"context"
	"sync"
	"time"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/keypool/domain"
)

// PoolProber answers whether one concrete credential still authenticates at
// its provider. The cheap honest probe is the provider's own catalog endpoint:
// an answer with the key attached says the key is alive, a 401/403 says it is
// not, and anything else says nothing.
type PoolProber interface {
	// ProbeKey reports the HTTP status the provider answered for this key's
	// catalog request. A zero status means the probe never got an answer —
	// the provider, not the key, is unreachable — and must not count against
	// the credential.
	ProbeKey(ctx context.Context, providerID string, key domain.Key) int
}

// PoolReport says what a pool check found, by counting rather than by naming
// keys: the rows above already carry their own badges.
type PoolReport struct {
	Checked   int  `json:"checked"`
	Rejected  int  `json:"rejected"`
	Reachable bool `json:"reachable"`
}

// CheckPool probes every key of a provider concurrently and files each answer
// through the scheduler: a rejected key earns its authentication streak (the
// dead-key badge and its notification ride on the same counter), an accepted
// one resets it. Probes bypass the scheduler on purpose — a cooled key is
// exactly the one worth checking.
func (manager *Manager) CheckPool(ctx context.Context, providerID string, prober PoolProber) (PoolReport, error) {
	manager.mu.RLock()
	userKeys := manager.combinedLocked(manager.userKeys)
	manager.mu.RUnlock()
	if _, exists := manager.providerRates[providerID]; !exists {
		return PoolReport{}, ErrUnknownProvider
	}
	candidates := make([]domain.Key, 0, len(userKeys))
	for _, key := range userKeys {
		if key.ProviderID == providerID && !key.Pinned {
			candidates = append(candidates, key)
		}
	}
	if len(candidates) == 0 || prober == nil {
		return PoolReport{Reachable: true}, nil
	}
	report := PoolReport{Checked: len(candidates)}
	statuses := make([]int, len(candidates))
	var group sync.WaitGroup
	// Bounded fan-out: three at a time keeps a large pool from looking like an
	// attack burst to a provider that counts requests.
	bounded := make(chan struct{}, 3)
	for index, key := range candidates {
		group.Add(1)
		go func(index int, key domain.Key) {
			defer group.Done()
			bounded <- struct{}{}
			defer func() { <-bounded }()
			probeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
			defer cancel()
			statuses[index] = prober.ProbeKey(probeCtx, providerID, key)
		}(index, key)
	}
	group.Wait()
	for index, key := range candidates {
		switch {
		case statuses[index] == 0:
			// No answer at all: the provider was unreachable for this probe
			// round, which says nothing about the credential.
			continue
		case statuses[index] == 401 || statuses[index] == 403:
			report.Rejected++
			manager.scheduler.finish(providerID, key.ID, domain.Outcome{Kind: domain.OutcomeAuthentication})
		default:
			manager.scheduler.finish(providerID, key.ID, domain.Outcome{Kind: domain.OutcomeSuccess})
		}
		if statuses[index] != 0 {
			report.Reachable = true
		}
	}
	return report, nil
}
