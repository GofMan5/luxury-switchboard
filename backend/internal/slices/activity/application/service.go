package application

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/activity/domain"
)

const defaultCapacity = 2_000

const (
	// Real relay codes (see relay/adapters/http/server.go and the guardrail
	// verdict): dropping one stores StateFailed with an empty ErrorCode.
	errCancelled           = "cancelled"
	errProviderUnavailable = "provider_unavailable"
	errTransport           = "transport"
	errUpstreamStatus      = "upstream_status"
	errStreamIncomplete    = "stream_incomplete"
	errClientDisconnected  = "client_disconnected"
	errRequestRejected     = "request_rejected"
	errImageGeneration     = "image_generation"
	errPolicyRefusal       = "policy_refusal"
	errChatCompatibility   = "chat_compatibility"
	errGuardrailBlocked    = "guardrail_blocked"
)

var serviceSequence atomic.Uint64

type Service struct {
	mu        sync.RWMutex
	requests  map[string]domain.Request
	hidden    map[string]struct{}
	order     []string
	capacity  int
	namespace string
	sequence  atomic.Uint64
	now       func() time.Time
	listeners []func(domain.Request)
}

func NewService(capacity int) *Service {
	if capacity < 1 {
		capacity = defaultCapacity
	}
	return &Service{
		requests:  make(map[string]domain.Request),
		hidden:    make(map[string]struct{}),
		capacity:  capacity,
		namespace: fmt.Sprintf("req_%016x_%x", uint64(time.Now().UnixNano()), serviceSequence.Add(1)),
		now:       time.Now,
	}
}

func (service *Service) Start(value domain.Start) string {
	now := service.now().UTC()
	id := fmt.Sprintf("%s_%016x", service.namespace, service.sequence.Add(1))
	request := domain.Request{
		ID: id, StartedAt: now, UpdatedAt: now, State: domain.StateActive,
		Model: clean(value.Model, 128), ProviderID: clean(value.ProviderID, 64),
		ProviderName: clean(value.ProviderName, 80), Method: clean(value.Method, 16),
		Path: safePath(value.Path), BytesIn: max(value.BytesIn, 0),
	}
	service.mu.Lock()
	service.requests[id] = request
	service.order = append([]string{id}, service.order...)
	service.trimLocked()
	service.mu.Unlock()
	service.publish(request)
	return id
}

func (service *Service) Retry(id string, value domain.Retry) {
	service.update(id, func(request *domain.Request, now time.Time) {
		request.State = domain.StateRetrying
		request.Status = normalizedStatus(value.Status)
		request.Retries = max(request.Retries, value.Attempt)
		request.QueueMS += max(float64(value.Delay.Microseconds())/1_000, 0)
		request.UpdatedAt = now
	})
}

func (service *Service) Waiting(id string) {
	service.update(id, func(request *domain.Request, now time.Time) {
		request.State = domain.StateRetrying
		request.UpdatedAt = now
	})
}

func (service *Service) Resume(id string, waited time.Duration) {
	service.update(id, func(request *domain.Request, now time.Time) {
		request.State = domain.StateActive
		request.QueueMS += max(float64(waited.Microseconds())/1_000, 0)
		request.UpdatedAt = now
	})
}

func (service *Service) Finish(id string, value domain.Finish) {
	service.update(id, func(request *domain.Request, now time.Time) {
		request.UpdatedAt = now
		request.LatencyMS = max(float64(now.Sub(request.StartedAt).Microseconds())/1_000, 0)
		request.Status = normalizedStatus(value.Status)
		request.BytesOut = max(value.BytesOut, 0)
		request.ErrorCode = cleanError(value.ErrorCode)
		request.ErrorDetail = cleanErrorDetail(value.ErrorDetail)
		if request.ErrorCode == "" {
			// Unknown text must not persist without its code: a dropped
			// code with a kept detail desyncs history from the relay.
			request.ErrorDetail = ""
		}
		request.InputTokens = max(value.InputTokens, 0)
		request.OutputTokens = max(value.OutputTokens, 0)
		request.CachedTokens = min(max(value.CachedTokens, 0), request.InputTokens)
		request.ReasoningTokens = min(max(value.ReasoningTokens, 0), request.OutputTokens)
		request.ContextTokens = max(value.ContextTokens, request.InputTokens)
		request.TotalTokens = max(value.TotalTokens, request.ContextTokens+request.OutputTokens)
		request.GenerationMS = max(float64(value.Generation.Microseconds())/1_000, 0)
		if request.GenerationMS > 0 {
			request.TokensPerSecond = float64(request.OutputTokens) * 1_000 / request.GenerationMS
		}
		switch {
		case value.Cancelled:
			request.State = domain.StateCancelled
		case value.ErrorCode != "" || value.Status >= 400 || value.Status == 0:
			request.State = domain.StateFailed
		default:
			request.State = domain.StateCompleted
		}
	})
}

func (service *Service) List(limit int) []domain.Request {
	service.mu.RLock()
	defer service.mu.RUnlock()
	limit = min(max(limit, 1), 500)
	limit = min(limit, len(service.order))
	result := make([]domain.Request, 0, limit)
	for _, id := range service.order[:limit] {
		result = append(result, service.requests[id])
	}
	return result
}

func (service *Service) Summary(window time.Duration) domain.Summary {
	service.mu.RLock()
	defer service.mu.RUnlock()
	cutoff := service.now().UTC().Add(-window)
	latencies := make([]float64, 0, len(service.order))
	completed := 0
	failed := 0
	summary := domain.Summary{}
	for _, id := range service.order {
		request := service.requests[id]
		if request.StartedAt.Before(cutoff) {
			continue
		}
		summary.Requests++
		switch request.State {
		case domain.StateActive:
			summary.Active++
		case domain.StateRetrying:
			summary.Queued++
		case domain.StateCompleted:
			completed++
			latencies = append(latencies, request.LatencyMS)
		case domain.StateFailed:
			failed++
		}
	}
	terminal := completed + failed
	if terminal > 0 {
		summary.SuccessRate = float64(completed) * 100 / float64(terminal)
	}
	if len(latencies) > 0 {
		sort.Float64s(latencies)
		summary.P95MS = latencies[P95Index(len(latencies))]
	}
	if window > 0 {
		summary.RPM = float64(summary.Requests) / window.Minutes()
	}
	return summary
}

func (service *Service) OnChanged(listener func(domain.Request)) {
	if listener == nil {
		return
	}
	service.mu.Lock()
	service.listeners = append(service.listeners, listener)
	service.mu.Unlock()
}

func (service *Service) update(id string, mutate func(*domain.Request, time.Time)) {
	service.mu.Lock()
	request, exists := service.requests[id]
	if !exists {
		service.mu.Unlock()
		return
	}
	mutate(&request, service.now().UTC())
	if _, hidden := service.hidden[id]; hidden && domain.IsTerminal(request.State) {
		delete(service.hidden, id)
		delete(service.requests, id)
	} else {
		service.requests[id] = request
	}
	service.mu.Unlock()
	service.publish(request)
}

func (service *Service) publish(request domain.Request) {
	service.mu.RLock()
	listeners := slices.Clone(service.listeners)
	service.mu.RUnlock()
	for _, listener := range listeners {
		listener(request)
	}
}

func (service *Service) trimLocked() {
	for len(service.order) > service.capacity {
		index := len(service.order) - 1
		for candidate := len(service.order) - 1; candidate >= 0; candidate-- {
			if domain.IsTerminal(service.requests[service.order[candidate]].State) {
				index = candidate
				break
			}
		}
		id := service.order[index]
		service.order = append(service.order[:index], service.order[index+1:]...)
		if domain.IsTerminal(service.requests[id].State) {
			delete(service.requests, id)
		} else {
			service.hidden[id] = struct{}{}
		}
	}
}

func clean(value string, limit int) string {
	value = strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return -1
		}
		return r
	}, strings.TrimSpace(value))
	runes := []rune(value)
	if len(runes) > limit {
		value = string(runes[:limit])
	}
	return value
}

func safePath(value string) string {
	value, _, _ = strings.Cut(value, "?")
	value, _, _ = strings.Cut(value, "#")
	return clean(value, 160)
}

func normalizedStatus(value int) int {
	if value < 100 || value > 599 {
		return 0
	}
	return value
}

func cleanError(value string) string {
	switch value {
	case errCancelled, errProviderUnavailable, errTransport, errUpstreamStatus, errStreamIncomplete,
		errClientDisconnected, errRequestRejected, errImageGeneration,
		errPolicyRefusal, errChatCompatibility, errGuardrailBlocked:
		return value
	default:
		return ""
	}
}

func cleanErrorDetail(value string) string {
	value = strings.Map(func(r rune) rune {
		if (r < 32 && r != '\n' && r != '\r' && r != '\t') || r == 127 {
			return -1
		}
		return r
	}, strings.TrimSpace(value))
	runes := []rune(value)
	if len(runes) > 4096 {
		return string(runes[:4096])
	}
	return value
}
