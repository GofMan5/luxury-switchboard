package application

import (
	"context"
	"sync"
	"time"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/keypool/domain"
)

// PoolProber checks the provider's configured catalog using one concrete key.
// Only 2xx authenticates; 401/403 rejects; other statuses are inconclusive.
type PoolProber interface {
	ProbeKey(ctx context.Context, providerID string, key domain.Key) int
}

// PoolReport distinguishes actual work, answers, and credential verdicts. Pinned
// environment keys are excluded. Skipped keys were not dispatched before the
// safety deadline or were removed/replaced while waiting for a worker.
type PoolReport struct {
	Total        int  `json:"total"`
	Checked      int  `json:"checked"`
	Answered     int  `json:"answered"`
	Accepted     int  `json:"accepted"`
	Rejected     int  `json:"rejected"`
	Inconclusive int  `json:"inconclusive"`
	Skipped      int  `json:"skipped"`
	Reachable    bool `json:"reachable"`
}

const maxPoolCheckDuration = 2 * time.Minute

// poolProbeSettleGrace is how long a timed-out probe gets to honour its
// cancellation before its worker counts it as still running.
const poolProbeSettleGrace = time.Second

// runPoolProbe keeps the worker responsive when a PoolProber ignores cancellation.
// Only its buffered result channel can be touched after the worker stops.
// settled says no call is left behind, so the worker may start another one;
// waiting for it only when the whole check is still live keeps cancellation
// prompt.
func runPoolProbe(ctx, checkCtx context.Context, prober PoolProber, providerID string, key domain.Key) (status int, completed, settled bool) {
	result := make(chan int, 1)
	go func() {
		result <- prober.ProbeKey(ctx, providerID, key)
	}()
	select {
	case status := <-result:
		if ctx.Err() != nil {
			return 0, false, true
		}
		return status, true, true
	case <-ctx.Done():
	}
	if checkCtx.Err() != nil {
		return 0, false, false
	}
	select {
	case <-result:
		return 0, false, true
	case <-time.After(poolProbeSettleGrace):
		return 0, false, false
	}
}

// CheckPool bypasses quota/cooldowns deliberately: a cooled key is worth
// checking. Three workers bound network fan-out per check. At most three calls
// that ignore context can outlive each check; the real HTTP prober honors context.
// The probe phase has a safety deadline and reports incomplete checks honestly.
func (manager *Manager) CheckPool(ctx context.Context, providerID string, prober PoolProber) (PoolReport, error) {
	if err := ctx.Err(); err != nil {
		return PoolReport{}, err
	}
	manager.opMu.Lock()
	manager.mu.RLock()
	keys := manager.combinedLocked(manager.userKeys)
	_, known := manager.providerRates[providerID]
	manager.mu.RUnlock()
	if !known {
		manager.opMu.Unlock()
		return PoolReport{}, ErrUnknownProvider
	}
	leases := make([]*Lease, 0, len(keys))
	for _, key := range keys {
		if key.ProviderID == providerID && !key.Pinned {
			if lease := manager.scheduler.probeLease(providerID, key.ID); lease != nil {
				leases = append(leases, lease)
			}
		}
	}
	manager.opMu.Unlock()
	report := PoolReport{Total: len(leases), Skipped: len(leases)}
	if len(leases) == 0 || prober == nil {
		return report, ctx.Err()
	}
	checkCtx, cancel := context.WithTimeout(ctx, maxPoolCheckDuration)
	defer cancel()
	statuses := make([]int, len(leases))
	attempted := make([]bool, len(leases))
	jobs := make(chan int)
	var workers sync.WaitGroup
	for range min(3, len(leases)) {
		workers.Go(func() {
			for index := range jobs {
				if checkCtx.Err() != nil {
					return
				}
				lease := leases[index]
				current := manager.scheduler.probeLease(providerID, lease.keyID)
				if current == nil || current.generation != lease.generation {
					continue
				}
				probeCtx, probeCancel := context.WithTimeout(checkCtx, manager.probeTimeout)
				attempted[index] = true
				status, completed, settled := runPoolProbe(probeCtx, checkCtx, prober, providerID, lease.Key())
				probeCancel()
				if !completed {
					statuses[index] = 0
					if settled {
						continue
					}
					// A context-ignoring prober may still be running in its detached
					// call. Do not start another one on this worker: three workers
					// means three is the maximum number of calls left behind.
					return
				}
				statuses[index] = status
				if checkCtx.Err() != nil {
					statuses[index] = 0
					return
				}
				// File at probe completion, not after the slowest sibling: an old fast
				// refusal must not overwrite a newer traffic success during that wait.
				switch {
				case status == 401 || status == 403:
					lease.Finish(domain.Outcome{Kind: domain.OutcomeAuthentication})
				case status >= 200 && status < 300:
					lease.Finish(domain.Outcome{Kind: domain.OutcomeSuccess})
				}
			}
		})
	}
dispatch:
	for index := range leases {
		select {
		case <-checkCtx.Done():
			break dispatch
		case jobs <- index:
		}
	}
	close(jobs)
	workers.Wait()
	for index, status := range statuses {
		if !attempted[index] {
			continue
		}
		report.Checked++
		report.Skipped--
		if status != 0 {
			report.Answered++
		}
		switch {
		case status == 401 || status == 403:
			report.Rejected++
		case status >= 200 && status < 300:
			report.Accepted++
		default:
			report.Inconclusive++
		}
	}
	report.Reachable = report.Answered > 0
	// An internal safety deadline gives a partial report; caller cancellation is
	// terminal cancellation, never a misleading successful check response.
	if err := ctx.Err(); err != nil {
		return report, err
	}
	return report, nil
}
