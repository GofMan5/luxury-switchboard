package tunnelhttp

import (
	"sync"
	"time"
)

const (
	// ponytail: fixed safety ceilings keep public queues memory-bounded; expose
	// them only if measured legitimate traffic reaches either limit.
	maxQueuedPerIP = 1_000
	maxQueuedTotal = 10_000
)

type ipLimiter struct {
	mu      sync.Mutex
	next    map[string]*ipLimit
	now     func() time.Time
	pending int
}

type ipLimit struct {
	next    time.Time
	pending int
}

func newIPLimiter() *ipLimiter {
	return &ipLimiter{next: make(map[string]*ipLimit), now: time.Now}
}
func (limiter *ipLimiter) Reserve(ip string, rpm int) (time.Duration, func(), bool) {
	limiter.mu.Lock()
	now := limiter.now()
	if limiter.pending >= maxQueuedTotal {
		limiter.mu.Unlock()
		return 0, nil, false
	}
	state := limiter.next[ip]
	if state == nil {
		state = &ipLimit{}
		limiter.next[ip] = state
	}
	if state.pending >= maxQueuedPerIP {
		limiter.mu.Unlock()
		return 0, nil, false
	}
	state.pending++
	limiter.pending++
	slot := state.next
	if slot.Before(now) {
		slot = now
	}
	if rpm > 0 {
		state.next = slot.Add(time.Minute / time.Duration(rpm))
	}
	if len(limiter.next) > 10000 {
		for key, candidate := range limiter.next {
			if candidate.pending == 0 && candidate.next.Before(now) {
				delete(limiter.next, key)
			}
		}
	}
	limiter.mu.Unlock()
	return max(slot.Sub(now), 0), func() { limiter.release(ip) }, true
}

func (limiter *ipLimiter) release(ip string) {
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	if state := limiter.next[ip]; state != nil && state.pending > 0 {
		state.pending--
		limiter.pending--
	}
}
