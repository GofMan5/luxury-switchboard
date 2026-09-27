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
	// authCooldown is how long a key that answered 401/403 sits out. An auth
	// verdict never resolves on a timer — the token is valid or it is not — so
	// the cooldown buys exactly one thing: keeping a client's own retry loop
	// off the provider's back. Five minutes asked too much for that (measured:
	// a dead single-key pool made every request queue the full five minutes
	// before failing again, nineteen times in a row); thirty seconds keeps the
	// pace gentle while the refusal stays fast and honest.
	authCooldown = 30 * time.Second
	// deadKeyThreshold is how many authentication refusals in a row read as a
	// dead credential (the notifications domain carries the same number for
	// its wording): two might be a flaky provider, three is a verdict.
	deadKeyThreshold = 3
	// balanceCooldown is the re-check interval for a 402: the verdict is
	// honest, but money returns when the operator tops up, not on a schedule
	// the relay could name. Ten minutes bounds a broken account to one cheap
	// 402 per interval while letting a top-up matter almost immediately.
	balanceCooldown = 10 * time.Minute
)

type Scheduler struct {
	mu         sync.Mutex
	providers  map[string]*providerState
	waiters    []*waiter
	notify     chan struct{}
	nextTicket uint64
	now        func() time.Time
	maxQueued  int
	// deadKeyListeners hear about a key whose authentication streak crossed the
	// dead threshold: the notification feed is the consumer, and it wants the
	// label, not the internal state.
	deadKeyListeners []func(providerID, label string)
}

type providerState struct {
	rpm int
	// window is the period the provider and its keys count requests over: a
	// minute for the usual quota, a second for providers that cap bursts.
	window time.Duration
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
	// authStreak counts consecutive authentication refusals; any other
	// outcome resets it. Three in a row is the dead-credential signal the
	// operator is told about.
	authStreak int
	// lastOutcome is the outcome of the key's last finished attempt, nil
	// before the first: the kind's zero value is a real outcome (success),
	// so an untouched key must not read as one.
	lastOutcome *domain.OutcomeKind
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

// Configure replaces a provider's rate settings and key set. window is the
// period the limit is counted over; a zero value keeps the per-minute default.
func (scheduler *Scheduler) Configure(providerID string, rpm int, window time.Duration, keys []domain.Key) error {
	if providerID == "" || rpm < 0 || window < 0 {
		return errors.New("invalid provider rate settings")
	}
	if window == 0 {
		window = rateWindow
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
				state.authStreak = old.authStreak
				state.lastOutcome = old.lastOutcome
			}
		}
	}
	provider := &providerState{rpm: rpm, window: window, keys: states}
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
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
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
		if err := ctx.Err(); err != nil {
			scheduler.removeWaiterLocked(pending)
			scheduler.mu.Unlock()
			return nil, now.Sub(started), err
		}
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

// TryAcquire takes a key only if one is dispatchable right now, without
// queueing. The relay uses it when it is already rotating on a credential
// rejection: the first attempt waited its fair turn, but parking every
// rotation on the cooldowns other requests left behind turned one refused
// request into tens of minutes of queueing (measured: 11 keys, 41 minutes).
// A miss means the request ends on the rejection it already holds instead of
// waiting for another key.
func (scheduler *Scheduler) TryAcquire(providerID, model string) (*Lease, bool) {
	scheduler.mu.Lock()
	defer scheduler.mu.Unlock()
	now := scheduler.now()
	provider := scheduler.providers[providerID]
	if provider == nil || len(provider.keys) == 0 {
		return nil, false
	}
	// Never jump the fair queue: a rotation takes a free key only when nobody
	// else is waiting on this provider.
	for _, pending := range scheduler.waiters {
		if pending.providerID == providerID {
			return nil, false
		}
	}
	scheduler.pruneLocked(provider, now)
	if !rateAvailable(provider.starts, provider.rpm) {
		return nil, false
	}
	key := selectKey(provider, model, now)
	if key == nil {
		return nil, false
	}
	provider.starts = append(provider.starts, now)
	key.starts = append(key.starts, now)
	return &Lease{
		scheduler: scheduler, providerID: providerID,
		keyID: key.key.ID, key: key.key,
	}, true
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
		lastOutcome := ""
		if state.lastOutcome != nil {
			lastOutcome = outcomeName(*state.lastOutcome)
		}
		result = append(result, domain.PublicKey{
			ID: state.key.ID, ProviderID: state.key.ProviderID,
			Label: state.key.Label, Priority: state.key.Priority, RPM: state.key.RPM,
			Pinned: state.key.Pinned, ProxyConfigured: state.key.ProxyURL != "",
			CooldownMS: cooldown, BlockedModels: activeBlocks(state.blockedModels, now),
			Retries429: state.retries429, StartsInWindow: len(state.starts),
			AuthStreak: state.authStreak, LastOutcome: lastOutcome,
		})
	}
	return result
}

// outcomeName gives the operator-facing name of an outcome kind, empty before
// the key's first finished attempt.
func outcomeName(kind domain.OutcomeKind) string {
	switch kind {
	case domain.OutcomeSuccess:
		return "success"
	case domain.OutcomeRateLimited:
		return "rate-limited"
	case domain.OutcomeServerError:
		return "server-error"
	case domain.OutcomeTransport:
		return "transport"
	case domain.OutcomeRequestError:
		return "request-error"
	case domain.OutcomeModelUnavailable:
		return "model-unavailable"
	case domain.OutcomeBalanceExhausted:
		return "balance-exhausted"
	case domain.OutcomeAuthentication:
		return "authentication"
	default:
		return ""
	}
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

// OnDeadKey registers a listener told once per streak when a key crosses the
// dead-credential threshold: three consecutive authentication refusals. The
// listener runs outside the scheduler lock.
func (scheduler *Scheduler) OnDeadKey(listener func(providerID, label string)) {
	if listener == nil {
		return
	}
	scheduler.mu.Lock()
	scheduler.deadKeyListeners = append(scheduler.deadKeyListeners, listener)
	scheduler.mu.Unlock()
}

func (scheduler *Scheduler) finish(providerID, keyID string, outcome domain.Outcome) {
	scheduler.mu.Lock()
	key := findKey(scheduler.providers[providerID], keyID)
	if key == nil {
		scheduler.mu.Unlock()
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
		// The balance verdict is honest but not scheduled: money comes back
		// when the operator tops up, not at any hour the relay could name.
		// Until-midnight used to park a key for most of a day after a single
		// 402 — measured: 1414 minutes, straight through an evening of use —
		// and a top-up five minutes later changed nothing until the clock
		// said so. Ten minutes is a re-check interval: an empty account costs
		// one cheap 402 per interval, and a topped-up one is serving again
		// almost immediately.
		key.balanceUntil = now.Add(balanceCooldown)
	case domain.OutcomeAuthentication:
		key.cooldownUntil = maxTime(key.cooldownUntil, now.Add(authCooldown))
	default:
	}
	// Health bookkeeping: the streak survives a rate limit or a server error
	// (those say nothing about the credential) and breaks on anything else.
	kind := outcome.Kind
	key.lastOutcome = &kind
	deadNow := false
	if outcome.Kind == domain.OutcomeAuthentication {
		key.authStreak++
		deadNow = key.authStreak == deadKeyThreshold
	} else {
		key.authStreak = 0
	}
	deadLabel := ""
	if deadNow {
		deadLabel = key.key.Label
	}
	listeners := append([]func(providerID, label string){}, scheduler.deadKeyListeners...)
	scheduler.broadcastLocked()
	scheduler.mu.Unlock()
	if deadLabel != "" {
		for _, listener := range listeners {
			listener(providerID, deadLabel)
		}
	}
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
	provider.starts = pruneStarts(provider.starts, now, provider.window)
	for _, key := range provider.keys {
		key.starts = pruneStarts(key.starts, now, provider.window)
		for model, until := range key.blockedModels {
			if !now.Before(until) {
				delete(key.blockedModels, model)
			}
		}
	}
}

func pruneStarts(starts []time.Time, now time.Time, window time.Duration) []time.Time {
	if window <= 0 {
		window = rateWindow
	}
	cutoff := now.Add(-window)
	index := 0
	for index < len(starts) && !starts[index].After(cutoff) {
		index++
	}
	return slices.Clone(starts[index:])
}

func (scheduler *Scheduler) nextWakeLocked(now time.Time) time.Duration {
	next := now.Add(time.Minute)
	for _, provider := range scheduler.providers {
		window := provider.window
		if window <= 0 {
			window = rateWindow
		}
		if provider.rpm > 0 && len(provider.starts) >= provider.rpm {
			next = minTime(next, provider.starts[0].Add(window))
		}
		for _, key := range provider.keys {
			if key.key.RPM > 0 && len(key.starts) >= key.key.RPM {
				next = minTime(next, key.starts[0].Add(window))
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
