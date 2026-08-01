package tunnelhttp

import (
	"context"
	"errors"
	"sync"
	"time"
)

const (
	// ponytail: fixed safety ceilings keep public queues memory-bounded; expose
	// them only if measured legitimate traffic reaches either limit.
	maxQueuedPerIP = 1_000
	maxQueuedTotal = 10_000
)

var errIPQueueFull = errors.New("tunnel request queue is full")

type ipLimiter struct {
	mu      sync.Mutex
	next    map[string]*ipLimit
	now     func() time.Time
	pending int
}

type ipLimit struct {
	next    time.Time
	pending int
	waiters []*ipWaiter
	notify  chan struct{}
}

type ipWaiter struct{}

func newIPLimiter() *ipLimiter {
	return &ipLimiter{next: make(map[string]*ipLimit), now: time.Now}
}

func (limiter *ipLimiter) Acquire(ctx context.Context, ip string, rpm int, onQueued func()) (func(), bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	limiter.mu.Lock()
	now := limiter.now()
	if limiter.pending >= maxQueuedTotal {
		limiter.mu.Unlock()
		return nil, false, errIPQueueFull
	}
	state := limiter.next[ip]
	if state == nil {
		if len(limiter.next) >= maxQueuedTotal {
			limiter.evictExpiredLocked(now)
			if len(limiter.next) >= maxQueuedTotal {
				limiter.mu.Unlock()
				return nil, false, errIPQueueFull
			}
		}
		state = &ipLimit{notify: make(chan struct{})}
		limiter.next[ip] = state
	}
	if state.pending >= maxQueuedPerIP {
		limiter.mu.Unlock()
		return nil, false, errIPQueueFull
	}
	waiter := &ipWaiter{}
	state.waiters = append(state.waiters, waiter)
	state.pending++
	limiter.pending++
	limiter.wakeLocked(state)
	queued := false

	for {
		now = limiter.now()
		if err := ctx.Err(); err != nil {
			if removeWaiter(state, waiter) {
				state.pending--
				limiter.pending--
			}
			limiter.wakeLocked(state)
			limiter.deleteExpiredLocked(ip, state, now)
			limiter.mu.Unlock()
			return nil, queued, err
		}
		first := len(state.waiters) > 0 && state.waiters[0] == waiter
		wait := max(state.next.Sub(now), 0)
		if first && (rpm == 0 || wait == 0) {
			state.waiters = state.waiters[1:]
			if rpm > 0 {
				state.next = maxTime(state.next, now).Add(time.Minute / time.Duration(rpm))
			}
			limiter.wakeLocked(state)
			released := false
			limiter.mu.Unlock()
			return func() {
				limiter.mu.Lock()
				defer limiter.mu.Unlock()
				if released {
					return
				}
				released = true
				state.pending--
				limiter.pending--
				limiter.wakeLocked(state)
				limiter.deleteExpiredLocked(ip, state, limiter.now())
			}, queued, nil
		}
		notify := state.notify
		limiter.mu.Unlock()
		if !queued {
			queued = true
			if onQueued != nil {
				onQueued()
			}
		}
		var timer *time.Timer
		var timerC <-chan time.Time
		if first && wait > 0 {
			timer = time.NewTimer(wait)
			timerC = timer.C
		}
		select {
		case <-ctx.Done():
			if timer != nil {
				timer.Stop()
			}
			limiter.mu.Lock()
			if removeWaiter(state, waiter) {
				state.pending--
				limiter.pending--
			}
			limiter.wakeLocked(state)
			limiter.deleteExpiredLocked(ip, state, limiter.now())
			limiter.mu.Unlock()
			return nil, queued, ctx.Err()
		case <-notify:
			if timer != nil {
				timer.Stop()
			}
		case <-timerC:
		}
		limiter.mu.Lock()
	}
}

func (limiter *ipLimiter) evictExpiredLocked(now time.Time) {
	for ip, state := range limiter.next {
		limiter.deleteExpiredLocked(ip, state, now)
	}
}

func (limiter *ipLimiter) deleteExpiredLocked(ip string, state *ipLimit, now time.Time) {
	if state.pending == 0 && len(state.waiters) == 0 && !state.next.After(now) {
		delete(limiter.next, ip)
	}
}

func (limiter *ipLimiter) wakeLocked(state *ipLimit) {
	close(state.notify)
	state.notify = make(chan struct{})
}

func removeWaiter(state *ipLimit, target *ipWaiter) bool {
	for index, waiter := range state.waiters {
		if waiter == target {
			state.waiters = append(state.waiters[:index], state.waiters[index+1:]...)
			return true
		}
	}
	return false
}

func maxTime(left, right time.Time) time.Time {
	if right.After(left) {
		return right
	}
	return left
}
