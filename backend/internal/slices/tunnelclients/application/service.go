package application

import (
	"context"
	"fmt"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/tunnelclients/domain"
	"slices"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

type Service struct {
	mu        sync.RWMutex
	clients   map[string]*clientState
	requests  map[string]domain.Start
	sequence  atomic.Uint64
	now       func() time.Time
	listeners []func()
	history   History
}
type clientState struct {
	client domain.Client
	starts []time.Time
	events []domain.Event
}

func NewService(histories ...History) *Service {
	var history History
	if len(histories) > 0 {
		history = histories[0]
	}
	return &Service{clients: make(map[string]*clientState), requests: make(map[string]domain.Start), now: time.Now, history: history}
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
	id := fmt.Sprintf("tun_%016x", service.sequence.Add(1))
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
		state.client.State = "idle"
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
	result := make([]domain.Client, 0, len(service.clients))
	for _, state := range service.clients {
		state.starts = prune(state.starts, now)
		value := state.client
		value.ActualRPM = len(state.starts)
		result = append(result, value)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].LastSeen.After(result[j].LastSeen) })
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
		state = &clientState{client: domain.Client{IP: ip, State: "idle"}}
		service.clients[ip] = state
	}
	return state
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
	return slices.Clone(starts[index:])
}
func safeError(value string) string {
	switch value {
	case "upstream_rejected", "unsafe_response":
		return value
	default:
		return ""
	}
}
