package application

import (
	"sync"
	"time"
)

// limiter bounds what each plugin may start per sliding minute and, when
// concurrent is positive, how many it may have running at once. A plugin's
// entry is dropped once it holds nothing, so the map only ever names plugins
// that acted within the last minute.
type limiter struct {
	mu         sync.Mutex
	perMinute  int
	concurrent int
	now        func() time.Time
	plugins    map[string]*rate
}

type rate struct {
	recent []time.Time
	active int
}

func newLimiter(perMinute, concurrent int) *limiter {
	return &limiter{perMinute: perMinute, concurrent: concurrent, now: time.Now, plugins: map[string]*rate{}}
}

// acquire counts one action for id and returns its release, or false when
// the plugin is over either limit; a refused action is not counted.
func (limits *limiter) acquire(id string) (func(), bool) {
	limits.mu.Lock()
	defer limits.mu.Unlock()
	now := limits.now()
	entry := limits.plugins[id]
	if entry == nil {
		entry = &rate{}
		limits.plugins[id] = entry
	}
	kept := entry.recent[:0]
	for _, at := range entry.recent {
		if now.Sub(at) < time.Minute {
			kept = append(kept, at)
		}
	}
	entry.recent = kept
	if len(entry.recent) >= limits.perMinute || (limits.concurrent > 0 && entry.active >= limits.concurrent) {
		limits.forget(id, entry)
		return nil, false
	}
	entry.recent = append(entry.recent, now)
	entry.active++
	var once sync.Once
	return func() {
		once.Do(func() {
			limits.mu.Lock()
			defer limits.mu.Unlock()
			entry.active--
			limits.forget(id, entry)
		})
	}, true
}

func (limits *limiter) forget(id string, entry *rate) {
	if entry.active == 0 && len(entry.recent) == 0 && limits.plugins[id] == entry {
		delete(limits.plugins, id)
	}
}
