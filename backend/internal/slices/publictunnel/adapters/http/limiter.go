package tunnelhttp

import (
	"sync"
	"time"
)

type ipLimiter struct {
	mu   sync.Mutex
	next map[string]time.Time
	now  func() time.Time
}

func newIPLimiter() *ipLimiter {
	return &ipLimiter{next: make(map[string]time.Time), now: time.Now}
}
func (limiter *ipLimiter) Reserve(ip string, rpm int) time.Duration {
	if rpm == 0 {
		return 0
	}
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	now := limiter.now()
	slot := limiter.next[ip]
	if slot.Before(now) {
		slot = now
	}
	limiter.next[ip] = slot.Add(time.Minute / time.Duration(rpm))
	if len(limiter.next) > 10000 {
		for key, next := range limiter.next {
			if next.Before(now) {
				delete(limiter.next, key)
			}
		}
	}
	return max(slot.Sub(now), 0)
}
