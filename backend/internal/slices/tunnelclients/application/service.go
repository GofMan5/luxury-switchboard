package application

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/tunnelclients/domain"
)

const (
	clientIdleTTL        = time.Hour
	maxTrackedClients    = 10_000
	rejectNotifyInterval = time.Second
)

var serviceSequence atomic.Uint64

type Service struct {
	mu             sync.RWMutex
	clients        map[string]*clientState
	requests       map[string]domain.Start
	profiles       map[string]domain.Profile
	namespace      string
	sequence       atomic.Uint64
	now            func() time.Time
	listeners      []func()
	history        History
	store          Profiles
	rejectNotified time.Time
}
type clientState struct {
	client domain.Client
	starts []time.Time
	events []domain.Event
}

func NewService(history History) *Service {
	service := &Service{
		clients: make(map[string]*clientState), requests: make(map[string]domain.Start),
		profiles:  make(map[string]domain.Profile),
		namespace: fmt.Sprintf("tun_%016x_%x", uint64(time.Now().UnixNano()), serviceSequence.Add(1)),
		now:       time.Now, history: history,
	}
	if store, ok := history.(Profiles); ok {
		service.store = store
	}
	return service
}

// LoadProfiles restores owner decisions so a restart keeps every ban in force.
func (service *Service) LoadProfiles(ctx context.Context) error {
	if service.store == nil {
		return nil
	}
	stored, err := service.store.Profiles(ctx)
	if err != nil {
		return err
	}
	service.mu.Lock()
	for _, profile := range stored {
		canonical, err := profile.Canonical()
		if err != nil || canonical.Empty() {
			continue
		}
		service.profiles[canonical.IP] = canonical
	}
	service.mu.Unlock()
	return nil
}

// SetProfile bans, unbans or annotates one client address. Bans apply to new
// requests; generations already in flight finish normally.
func (service *Service) SetProfile(ctx context.Context, profile domain.Profile) error {
	canonical, err := profile.Canonical()
	if err != nil {
		return err
	}
	if service.store != nil {
		if canonical.Empty() {
			err = service.store.DeleteProfile(ctx, canonical.IP)
		} else {
			err = service.store.SaveProfile(ctx, canonical)
		}
		if err != nil {
			return err
		}
	}
	service.mu.Lock()
	if canonical.Empty() {
		delete(service.profiles, canonical.IP)
	} else {
		service.profiles[canonical.IP] = canonical
	}
	service.mu.Unlock()
	service.publish()
	return nil
}

func (service *Service) Banned(ip string) bool {
	if ip == "" {
		return false
	}
	service.mu.RLock()
	defer service.mu.RUnlock()
	return service.profiles[ip].Banned
}

// Reject counts a refused attempt. Refusals must stay cheaper than the requests
// they replace, so they never allocate history, per-minute samples or an event per
// attempt, and an address the owner never decided about is not tracked at all.
func (service *Service) Reject(ip string) {
	if ip == "" {
		return
	}
	now := service.now().UTC()
	service.mu.Lock()
	state := service.clients[ip]
	if state == nil {
		if _, known := service.profiles[ip]; !known {
			service.mu.Unlock()
			return
		}
		state = service.ensure(ip)
	}
	state.client.Refused++
	state.client.LastSeen = now
	notify := service.rejectNotified.IsZero() || now.Sub(service.rejectNotified) >= rejectNotifyInterval
	if notify {
		service.rejectNotified = now
	}
	service.mu.Unlock()
	if notify {
		service.publish()
	}
}

func (service *Service) Queue(ip string, delta int) {
	if ip == "" || delta == 0 {
		return
	}
	service.mu.Lock()
	state := service.ensure(ip)
	state.client.Queued = max(state.client.Queued+delta, 0)
	state.client.LastSeen = service.now().UTC()
	if state.client.Active == 0 {
		if state.client.Queued > 0 {
			state.client.State = "queued"
		} else {
			state.client.State = "idle"
		}
	}
	service.mu.Unlock()
	service.publish()
}
func (service *Service) Begin(start domain.Start) string {
	now := service.now().UTC()
	id := fmt.Sprintf("%s_%016x", service.namespace, service.sequence.Add(1))
	service.mu.Lock()
	state := service.ensure(start.IP)
	state.client.Active++
	state.client.Count++
	state.client.LastSeen = now
	state.client.State = "active"
	state.starts = append(state.starts, now)
	state.starts = prune(state.starts, now)
	service.requests[id] = start
	service.mu.Unlock()
	service.publish()
	return id
}
func (service *Service) Finish(id string, finish domain.Finish) {
	now := service.now().UTC()
	service.mu.Lock()
	start, ok := service.requests[id]
	if !ok {
		service.mu.Unlock()
		return
	}
	delete(service.requests, id)
	state := service.ensure(start.IP)
	state.client.Active = max(state.client.Active-1, 0)
	state.client.LastSeen = now
	if state.client.Active == 0 {
		if state.client.Queued > 0 {
			state.client.State = "queued"
		} else {
			state.client.State = "idle"
		}
	}
	event := domain.Event{ID: id, IP: start.IP, Time: now, State: "completed", Method: start.Method, Path: start.Path, Model: start.Model, Status: finish.Status, LatencyMS: float64(finish.Duration.Microseconds()) / 1000, BytesIn: start.BytesIn, BytesOut: finish.BytesOut, ErrorCode: safeError(finish.ErrorCode)}
	if finish.ErrorCode != "" || finish.Status >= 400 {
		event.State = "error"
	}
	state.events = append([]domain.Event{event}, state.events...)
	if len(state.events) > 80 {
		state.events = state.events[:80]
	}
	service.mu.Unlock()
	if service.history != nil {
		service.history.Record(event)
	}
	service.publish()
}
func (service *Service) List() []domain.Client {
	service.mu.Lock()
	defer service.mu.Unlock()
	now := service.now().UTC()
	service.evictIdleLocked(now)
	result := make([]domain.Client, 0, len(service.clients)+len(service.profiles))
	for _, state := range service.clients {
		state.starts = prune(state.starts, now)
		value := state.client
		value.ActualRPM = len(state.starts)
		profile := service.profiles[value.IP]
		value.Banned, value.Note = profile.Banned, profile.Note
		result = append(result, value)
	}
	// Owner decisions stay visible after the client goes idle, otherwise a ban
	// could never be lifted from the interface.
	for ip, profile := range service.profiles {
		if _, live := service.clients[ip]; live {
			continue
		}
		result = append(result, domain.Client{IP: ip, State: "idle", Banned: profile.Banned, Note: profile.Note})
	}
	// Idle profiles share one zero timestamp, so the address breaks the tie and the
	// rows the owner is acting on stay put between refreshes.
	sort.Slice(result, func(first, second int) bool {
		if !result[first].LastSeen.Equal(result[second].LastSeen) {
			return result[first].LastSeen.After(result[second].LastSeen)
		}
		return result[first].IP < result[second].IP
	})
	return result
}
func (service *Service) Events(ctx context.Context, ip string) []domain.Event {
	service.mu.RLock()
	state := service.clients[ip]
	var memory []domain.Event
	if state != nil {
		memory = slices.Clone(state.events)
	}
	service.mu.RUnlock()
	if service.history == nil {
		return memory
	}
	persisted, err := service.history.Recent(ctx, ip, 80)
	if err != nil {
		return memory
	}
	seen := make(map[string]struct{}, len(memory))
	for _, event := range memory {
		seen[event.ID] = struct{}{}
	}
	for _, event := range persisted {
		if _, exists := seen[event.ID]; !exists {
			memory = append(memory, event)
		}
	}
	sort.Slice(memory, func(i, j int) bool { return memory[i].Time.After(memory[j].Time) })
	return memory[:min(len(memory), 80)]
}
func (service *Service) OnChanged(listener func()) {
	if listener == nil {
		return
	}
	service.mu.Lock()
	service.listeners = append(service.listeners, listener)
	service.mu.Unlock()
}
func (service *Service) ensure(ip string) *clientState {
	state := service.clients[ip]
	if state == nil {
		service.evictIdleLocked(service.now().UTC())
		if len(service.clients) >= maxTrackedClients {
			service.evictOldestIdleLocked()
		}
		state = &clientState{client: domain.Client{IP: ip, State: "idle"}}
		service.clients[ip] = state
	}
	return state
}

func (service *Service) evictIdleLocked(now time.Time) {
	cutoff := now.Add(-clientIdleTTL)
	for ip, state := range service.clients {
		if state.client.Active == 0 && state.client.Queued == 0 && state.client.LastSeen.Before(cutoff) {
			delete(service.clients, ip)
		}
	}
}

func (service *Service) evictOldestIdleLocked() {
	oldestIP := ""
	var oldest time.Time
	for ip, state := range service.clients {
		if state.client.Active != 0 || state.client.Queued != 0 {
			continue
		}
		if oldestIP == "" || state.client.LastSeen.Before(oldest) {
			oldestIP, oldest = ip, state.client.LastSeen
		}
	}
	if oldestIP != "" {
		delete(service.clients, oldestIP)
	}
}
func (service *Service) publish() {
	service.mu.RLock()
	listeners := append([]func(){}, service.listeners...)
	service.mu.RUnlock()
	for _, listener := range listeners {
		listener()
	}
}
func prune(starts []time.Time, now time.Time) []time.Time {
	cutoff := now.Add(-time.Minute)
	index := 0
	for index < len(starts) && !starts[index].After(cutoff) {
		index++
	}
	if index == 0 {
		return starts
	}
	return slices.Clone(starts[index:])
}
func safeError(value string) string {
	switch value {
	case "upstream_rejected", "unsafe_response", "context_limit", "client_disconnected":
		return value
	default:
		return ""
	}
}
