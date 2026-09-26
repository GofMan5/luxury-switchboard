package application

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	providerdomain "github.com/luxuryprivate/switchboard/backend/internal/slices/providers/domain"
)

// ReachabilityProber answers whether a provider's endpoint is alive. Any HTTP
// answer at all — including a 401 with no credential — means reachable: the
// question is transport, not entitlement, so a dead token or a suspicious edge
// must not paint a live provider red.
type ReachabilityProber interface {
	ProbeReachability(ctx context.Context, provider providerdomain.Provider) (up bool, reason string)
}

// HealthState is one provider's reachability, as the sidebar shows it. The
// reason is a short neutral sentence, never the endpoint or an error with a
// URL inside; the latency is the probe's own round trip, for the operator's
// sense of the provider, not a routing input.
type HealthState struct {
	ProviderID string    `json:"providerId"`
	Up         bool      `json:"up"`
	Reason     string    `json:"reason"`
	LatencyMS  int64     `json:"latencyMs"`
	Since      time.Time `json:"since"`
}

// HealthMonitor probes every enabled provider on an interval and reports the
// state that changed. The probe costs one anonymous catalog request per
// provider per interval: no credentials, no relay path, no activity rows —
// reachability is transport, and the question is whether anything answers.
type HealthMonitor struct {
	catalog *Catalog
	prober  ReachabilityProber
	now     func() time.Time

	mu        sync.Mutex
	states    map[string]HealthState
	listeners []func(changed HealthState, snapshot []HealthState)

	enabled atomic.Bool
}

func NewHealthMonitor(catalog *Catalog, prober ReachabilityProber) (*HealthMonitor, error) {
	if catalog == nil || prober == nil {
		return nil, errors.New("health monitor dependencies are invalid")
	}
	monitor := &HealthMonitor{catalog: catalog, prober: prober, states: map[string]HealthState{}, now: time.Now}
	monitor.enabled.Store(true)
	return monitor, nil
}

// SetEnabled gates the loop live: a settings change takes effect on the next
// tick, not after a restart. Disabled leaves the last snapshot in place — the
// sidebar shows configured state rather than pretending to know liveness.
func (monitor *HealthMonitor) SetEnabled(enabled bool) { monitor.enabled.Store(enabled) }

func (monitor *HealthMonitor) Enabled() bool { return monitor.enabled.Load() }

// OnChanged registers a listener for state transitions. The snapshot argument
// is the full ordered state, so one event carries everything a surface needs.
func (monitor *HealthMonitor) OnChanged(listener func(changed HealthState, snapshot []HealthState)) {
	if listener == nil {
		return
	}
	monitor.mu.Lock()
	monitor.listeners = append(monitor.listeners, listener)
	monitor.mu.Unlock()
}

// Run probes until the context ends. The first pass happens immediately so a
// freshly started app shows liveness without waiting a full interval.
func (monitor *HealthMonitor) Run(ctx context.Context, interval time.Duration) {
	if interval < 30*time.Second {
		interval = 30 * time.Second
	}
	monitor.pass(ctx)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if monitor.enabled.Load() {
				monitor.pass(ctx)
			}
		}
	}
}

type probeOutcome struct {
	id        string
	up        bool
	reason    string
	latencyMS int64
}

// pass probes all enabled providers concurrently — a provider that times out
// must not delay the verdict on the rest — and reports only what changed.
func (monitor *HealthMonitor) pass(ctx context.Context) {
	enabled := make([]providerdomain.Provider, 0)
	for _, provider := range monitor.catalog.List() {
		if provider.Enabled {
			enabled = append(enabled, provider)
		}
	}
	outcomes := make([]probeOutcome, len(enabled))
	var group sync.WaitGroup
	// One generous budget for the whole pass: every probe has its own 10s
	// timeout, and a stalled one cannot hold the pass hostage beyond it.
	probeCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	for index, provider := range enabled {
		group.Add(1)
		go func(index int, provider providerdomain.Provider) {
			defer group.Done()
			started := time.Now()
			up, reason := monitor.prober.ProbeReachability(probeCtx, provider)
			outcomes[index] = probeOutcome{id: provider.ID, up: up, reason: reason, latencyMS: time.Since(started).Milliseconds()}
		}(index, provider)
	}
	group.Wait()

	monitor.mu.Lock()
	now := monitor.now()
	changed := make([]HealthState, 0, len(outcomes))
	probed := make(map[string]struct{}, len(outcomes))
	for _, outcome := range outcomes {
		probed[outcome.id] = struct{}{}
		previous, existed := monitor.states[outcome.id]
		next := HealthState{ProviderID: outcome.id, Up: outcome.up, Reason: outcome.reason, LatencyMS: outcome.latencyMS}
		switch {
		case !existed || previous.Up != outcome.up:
			// A transition resets the clock; the first sighting counts as one
			// so the sidebar can age it.
			next.Since = now
			monitor.states[outcome.id] = next
			changed = append(changed, next)
		case previous.Reason != outcome.reason || previous.LatencyMS != outcome.latencyMS:
			// Same verdict, fresher numbers: the snapshot updates, nobody
			// is told.
			next.Since = previous.Since
			monitor.states[outcome.id] = next
		default:
			monitor.states[outcome.id] = HealthState{
				ProviderID: outcome.id, Up: outcome.up, Reason: outcome.reason, LatencyMS: outcome.latencyMS, Since: previous.Since,
			}
		}
	}
	// Providers that vanished from the catalog keep no state.
	for id := range monitor.states {
		if _, stillThere := probed[id]; !stillThere {
			delete(monitor.states, id)
		}
	}
	snapshot := monitor.snapshotLocked()
	listeners := append([]func(changed HealthState, snapshot []HealthState){}, monitor.listeners...)
	monitor.mu.Unlock()
	for _, listener := range listeners {
		for _, transition := range changed {
			listener(transition, snapshot)
		}
	}
}

func (monitor *HealthMonitor) Snapshot() []HealthState {
	monitor.mu.Lock()
	defer monitor.mu.Unlock()
	return monitor.snapshotLocked()
}

func (monitor *HealthMonitor) snapshotLocked() []HealthState {
	result := make([]HealthState, 0, len(monitor.states))
	for _, state := range monitor.states {
		result = append(result, state)
	}
	slices.SortFunc(result, func(left, right HealthState) int {
		return strings.Compare(left.ProviderID, right.ProviderID)
	})
	return result
}
