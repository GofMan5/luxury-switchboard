package application

import (
	"context"
	"errors"
	"slices"
	"sort"
	"sync"
	"time"
	_ "time/tzdata"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/keypool/domain"
)

var (
	ErrNoKeys    = errors.New("no keys configured")
	ErrQueueFull = errors.New("key queue is full")
)

const (
	defaultMaxQueued = 10_000
	rateWindow       = time.Minute
	modelCooldown    = 5 * time.Minute
)

type Scheduler struct {
	mu         sync.Mutex
	providers  map[string]*providerState
	waiters    []*waiter
	nextTicket uint64
	notify     chan struct{}
	now        func() time.Time
	maxQueued  int
}

type providerState struct {
	rpm    int
	starts []time.Time
	keys   []*keyState
}

type keyState struct {
	key           domain.Key
	starts        []time.Time
	cooldownUntil time.Time
	balanceUntil  time.Time
	blockedModels map[string]time.Time
	retries429    int
}

type waiter struct {
	ticket     uint64
	providerID string
	model      string
}

type Lease struct {
	scheduler  *Scheduler
	providerID string
	keyID      string
	key        domain.Key
	once       sync.Once
}

func NewScheduler(maxQueued int) *Scheduler {
	if maxQueued < 1 {
		maxQueued = defaultMaxQueued
	}
	return &Scheduler{
		providers: make(map[string]*providerState),
		notify:    make(chan struct{}),
		now:       time.Now,
		maxQueued: maxQueued,
	}
}

func (scheduler *Scheduler) Configure(providerID string, rpm int, keys []domain.Key) error {
	if providerID == "" || rpm < 0 {
		return errors.New("invalid provider rate settings")
	}
	states := make([]*keyState, 0, len(keys))
	seen := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		if key.ProviderID != providerID || key.RPM < 0 {
			return errors.New("invalid provider key")
		}
		if _, duplicate := seen[key.ID]; duplicate {
			return errors.New("duplicate key")
		}
		seen[key.ID] = struct{}{}
		states = append(states, &keyState{key: key, blockedModels: make(map[string]time.Time)})
	}
	sort.SliceStable(states, func(left, right int) bool {
		return states[left].key.Priority < states[right].key.Priority
	})

	scheduler.mu.Lock()
	previous := scheduler.providers[providerID]
	if previous != nil {
		for _, state := range states {
			if old := findKey(previous, state.key.ID); old != nil {
				state.starts = slices.Clone(old.starts)
				state.cooldownUntil = old.cooldownUntil
				state.balanceUntil = old.balanceUntil
				state.blockedModels = cloneBlocks(old.blockedModels)
				state.retries429 = old.retries429
			}
		}
	}
	provider := &providerState{rpm: rpm, keys: states}
	if previous != nil {
		provider.starts = slices.Clone(previous.starts)
	}
	scheduler.providers[providerID] = provider
	scheduler.broadcastLocked()
	scheduler.mu.Unlock()
	return nil
}

func (scheduler *Scheduler) Acquire(ctx context.Context, providerID, model string) (*Lease, time.Duration, error) {
	return scheduler.AcquireWithQueue(ctx, providerID, model, nil)
}

func (scheduler *Scheduler) AcquireWithQueue(ctx context.Context, providerID, model string, queued func()) (*Lease, time.Duration, error) {
	started := scheduler.now()
	reportedQueued := false
	scheduler.mu.Lock()
	if len(scheduler.waiters) >= scheduler.maxQueued {
		scheduler.mu.Unlock()
		return nil, 0, ErrQueueFull
	}
	scheduler.nextTicket++
	pending := &waiter{ticket: scheduler.nextTicket, providerID: providerID, model: model}
	scheduler.waiters = append(scheduler.waiters, pending)
	scheduler.broadcastLocked()
	scheduler.mu.Unlock()

	for {
		scheduler.mu.Lock()
		now := scheduler.now()
		provider := scheduler.providers[providerID]
		if provider == nil || len(provider.keys) == 0 {
			scheduler.removeWaiterLocked(pending)
			scheduler.mu.Unlock()
			return nil, now.Sub(started), ErrNoKeys
		}
		scheduler.pruneLocked(provider, now)
		candidate, key := scheduler.firstDispatchableLocked(now)
		if candidate == pending && key != nil {
			scheduler.removeWaiterLocked(pending)
			provider.starts = append(provider.starts, now)
			key.starts = append(key.starts, now)
			lease := &Lease{
				scheduler: scheduler, providerID: providerID,
				keyID: key.key.ID, key: key.key,
			}
			scheduler.mu.Unlock()
			return lease, now.Sub(started), nil
		}
		wait := scheduler.nextWakeLocked(now)
		notify := scheduler.notify
		scheduler.mu.Unlock()
		if !reportedQueued && queued != nil {
			reportedQueued = true
			queued()
		}

		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			scheduler.mu.Lock()
			scheduler.removeWaiterLocked(pending)
			scheduler.mu.Unlock()
			return nil, scheduler.now().Sub(started), ctx.Err()
		case <-notify:
			timer.Stop()
		case <-timer.C:
		}
	}
}

func (lease *Lease) Key() domain.Key {
	return lease.key
}

func (lease *Lease) Finish(outcome domain.Outcome) {
	lease.once.Do(func() {
		lease.scheduler.finish(lease.providerID, lease.keyID, outcome)
	})
}

func (scheduler *Scheduler) Reset(providerID, keyID string) bool {
	scheduler.mu.Lock()
	defer scheduler.mu.Unlock()
	provider := scheduler.providers[providerID]
	key := findKey(provider, keyID)
	if key == nil {
		return false
	}
	key.cooldownUntil = time.Time{}
	key.balanceUntil = time.Time{}
	clear(key.blockedModels)
	scheduler.broadcastLocked()
	return true
}

func (scheduler *Scheduler) Snapshot(providerID string) []domain.PublicKey {
	scheduler.mu.Lock()
	defer scheduler.mu.Unlock()
	provider := scheduler.providers[providerID]
	if provider == nil {
		return nil
	}
	now := scheduler.now()
	scheduler.pruneLocked(provider, now)
	result := make([]domain.PublicKey, 0, len(provider.keys))
	for _, state := range provider.keys {
		until := maxTime(state.cooldownUntil, state.balanceUntil)
		cooldown := max(until.Sub(now).Milliseconds(), 0)
		result = append(result, domain.PublicKey{
			ID: state.key.ID, ProviderID: state.key.ProviderID,
			Label: state.key.Label, Priority: state.key.Priority, RPM: state.key.RPM,
			Pinned: state.key.Pinned, ProxyConfigured: state.key.ProxyURL != "",
			CooldownMS: cooldown, BlockedModels: activeBlocks(state.blockedModels, now),
			Retries429: state.retries429, StartsInWindow: len(state.starts),
		})
	}
	return result
}

func (scheduler *Scheduler) Count(providerID string) int {
	scheduler.mu.Lock()
	defer scheduler.mu.Unlock()
	provider := scheduler.providers[providerID]
	if provider == nil {
		return 0
	}
	return len(provider.keys)
}

func (scheduler *Scheduler) RemoveProvider(providerID string) {
	scheduler.mu.Lock()
	delete(scheduler.providers, providerID)
	scheduler.broadcastLocked()
	scheduler.mu.Unlock()
}

func (scheduler *Scheduler) finish(providerID, keyID string, outcome domain.Outcome) {
	scheduler.mu.Lock()
	defer scheduler.mu.Unlock()
	key := findKey(scheduler.providers[providerID], keyID)
	if key == nil {
		return
	}
	now := scheduler.now()
	switch outcome.Kind {
	case domain.OutcomeRateLimited:
		delay := outcome.RetryAfter
		if delay <= 0 {
			delay = time.Second
		}
		key.cooldownUntil = maxTime(key.cooldownUntil, now.Add(min(delay, time.Minute)))
		key.retries429++
	case domain.OutcomeModelUnavailable:
		if outcome.Model != "" {
			key.blockedModels[outcome.Model] = now.Add(modelCooldown)
		}
	case domain.OutcomeBalanceExhausted:
		key.balanceUntil = nextMoscowMidnight(now)
	case domain.OutcomeAuthentication:
		key.cooldownUntil = maxTime(key.cooldownUntil, now.Add(modelCooldown))
	case domain.OutcomeSuccess, domain.OutcomeServerError, domain.OutcomeTransport, domain.OutcomeRequestError:
		// Request-scoped outcomes must not cool an otherwise working key.
	}
	scheduler.broadcastLocked()
}

func (scheduler *Scheduler) firstDispatchableLocked(now time.Time) (*waiter, *keyState) {
	for _, pending := range scheduler.waiters {
		provider := scheduler.providers[pending.providerID]
		if provider == nil || !rateAvailable(provider.starts, provider.rpm) {
			continue
		}
		if key := selectKey(provider, pending.model, now); key != nil {
			return pending, key
		}
	}
	return nil, nil
}

func selectKey(provider *providerState, model string, now time.Time) *keyState {
	for _, key := range provider.keys {
		if now.Before(maxTime(key.cooldownUntil, key.balanceUntil)) {
			continue
		}
		if until := key.blockedModels[model]; model != "" && now.Before(until) {
			continue
		}
		if rateAvailable(key.starts, key.key.RPM) {
			return key
		}
	}
	return nil
}

func rateAvailable(starts []time.Time, rpm int) bool {
	return rpm == 0 || len(starts) < rpm
}

func (scheduler *Scheduler) pruneLocked(provider *providerState, now time.Time) {
	provider.starts = pruneStarts(provider.starts, now)
	for _, key := range provider.keys {
		key.starts = pruneStarts(key.starts, now)
		for model, until := range key.blockedModels {
			if !now.Before(until) {
				delete(key.blockedModels, model)
			}
		}
	}
}

func pruneStarts(starts []time.Time, now time.Time) []time.Time {
	cutoff := now.Add(-rateWindow)
	index := 0
	for index < len(starts) && !starts[index].After(cutoff) {
		index++
	}
	return slices.Clone(starts[index:])
}

func (scheduler *Scheduler) nextWakeLocked(now time.Time) time.Duration {
	next := now.Add(time.Minute)
	for _, provider := range scheduler.providers {
		if provider.rpm > 0 && len(provider.starts) >= provider.rpm {
			next = minTime(next, provider.starts[0].Add(rateWindow))
		}
		for _, key := range provider.keys {
			if key.key.RPM > 0 && len(key.starts) >= key.key.RPM {
				next = minTime(next, key.starts[0].Add(rateWindow))
			}
			for _, until := range []time.Time{key.cooldownUntil, key.balanceUntil} {
				if now.Before(until) {
					next = minTime(next, until)
				}
			}
			for _, until := range key.blockedModels {
				if now.Before(until) {
					next = minTime(next, until)
				}
			}
		}
	}
	return max(next.Sub(now), 10*time.Millisecond)
}

func (scheduler *Scheduler) removeWaiterLocked(target *waiter) {
	for index, pending := range scheduler.waiters {
		if pending == target {
			scheduler.waiters = append(scheduler.waiters[:index], scheduler.waiters[index+1:]...)
			scheduler.broadcastLocked()
			return
		}
	}
}

func (scheduler *Scheduler) broadcastLocked() {
	close(scheduler.notify)
	scheduler.notify = make(chan struct{})
}

func findKey(provider *providerState, id string) *keyState {
	if provider == nil {
		return nil
	}
	for _, key := range provider.keys {
		if key.key.ID == id {
			return key
		}
	}
	return nil
}

func cloneBlocks(source map[string]time.Time) map[string]time.Time {
	result := make(map[string]time.Time, len(source))
	for model, until := range source {
		result[model] = until
	}
	return result
}

func activeBlocks(blocks map[string]time.Time, now time.Time) int {
	count := 0
	for _, until := range blocks {
		if now.Before(until) {
			count++
		}
	}
	return count
}

func nextMoscowMidnight(now time.Time) time.Time {
	location, err := time.LoadLocation("Europe/Moscow")
	if err != nil {
		location = time.FixedZone("MSK", 3*60*60)
	}
	local := now.In(location)
	return time.Date(local.Year(), local.Month(), local.Day()+1, 0, 0, 0, 0, location)
}

func minTime(left, right time.Time) time.Time {
	if right.Before(left) {
		return right
	}
	return left
}

func maxTime(left, right time.Time) time.Time {
	if right.After(left) {
		return right
	}
	return left
}
