// Package application runs plugin relay hooks: it keeps the registrations,
// asks request hooks for a decision while the relay waits, and hands finished
// requests to response observers without making anyone wait.
package application

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"slices"
	"sync"
	"time"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/relayhooks/domain"
)

const (
	// MaxPendingCalls bounds the request-hook calls waiting for an answer
	// across every request; a call over it is skipped (the request continues).
	MaxPendingCalls = 256
	// MaxQueuedObservations bounds the finished requests waiting for their
	// observers; a request over it is not observed.
	MaxQueuedObservations = 32
)

var (
	// ErrNotGranted means the plugin is not installed, not enabled, or not
	// granted relay:content.
	ErrNotGranted = errors.New("plugin may not hook relay requests")
	// ErrLimit means the hook would go over MaxHooks or MaxHooksPerPhase.
	ErrLimit = errors.New("relay hook limit reached")
	// ErrUnavailable means the service was not connected yet.
	ErrUnavailable = errors.New("relay hooks are unavailable")
)

// GrantChecker answers whether a plugin is installed, enabled and holds a
// permission. Implemented by the plugins service.
type GrantChecker interface {
	Granted(pluginID, permission string) bool
}

// Removal reports a hook the service removed on its own; the plugin host did
// not ask for it.
type Removal struct {
	PluginID string
	HookID   string
	Phase    string
	Reason   string
}

// RemovalTimeouts: the hook missed MaxConsecutiveTimeouts calls in a row.
const RemovalTimeouts = "timeouts"

// Events delivers hook traffic to the plugin host.
type Events interface {
	// Invoke asks a request hook for its decision. bodyOmitted reports that
	// the body was left out to fit the transport; the hook then cannot
	// rewrite it. An error means the hook was not asked.
	Invoke(invocation domain.Invocation) (bodyOmitted bool, err error)
	// Observe tells a response observer how a request ended.
	Observe(observation domain.Observation) error
	// Removed reports a hook the service removed on its own.
	Removed(removal Removal) error
}

// Service keeps the registered hooks in registration order and runs them.
type Service struct {
	mu      sync.Mutex
	grants  GrantChecker
	events  Events
	hooks   []*registration
	pending map[string]*pendingCall
	// queue holds finished requests for their observers; draining says a
	// goroutine is delivering it and exits once it is empty.
	queue    []observation
	draining bool
	// budget bounds every request hook of one request together.
	budget time.Duration
}

type registration struct {
	hook domain.Hook
	// timeouts counts the calls in a row that the hook left unanswered.
	timeouts int
}

type pendingCall struct {
	owner  *registration
	answer chan callResult
}

// callResult is what ends a pending call: a decision, or the hook's removal.
type callResult struct {
	decision domain.Decision
	answered bool
}

type observation struct {
	hooks    []*registration
	request  domain.Request
	response domain.Response
}

// NewService returns a service with no hooks. It refuses registrations until
// Connect.
func NewService() *Service {
	return &Service{pending: make(map[string]*pendingCall), budget: domain.TotalBudget}
}

// Connect wires the grant checks and the delivery of hook traffic.
func (service *Service) Connect(grants GrantChecker, events Events) {
	service.mu.Lock()
	service.grants, service.events = grants, events
	service.mu.Unlock()
}

// Register adds hook, or replaces the plugin's hook with the same id in its
// place. The plugin must be granted relay:content.
func (service *Service) Register(hook domain.Hook) error {
	if err := hook.Validate(); err != nil {
		return err
	}
	hook.Models = slices.Clone(hook.Models)
	hook.Endpoints = slices.Clone(hook.Endpoints)
	service.mu.Lock()
	defer service.mu.Unlock()
	if service.grants == nil {
		return ErrUnavailable
	}
	// The grant is checked under mu so an uninstall cannot slip between the
	// check and the insert: the plugins service drops a plugin's hooks
	// (UnregisterPlugin, which takes mu) after it stops granting anything.
	// Granted takes only the plugins service's read lock, never its mutation
	// lock, so this order cannot deadlock with UnregisterPlugin.
	if !service.grants.Granted(hook.PluginID, domain.PermissionRelayContent) {
		return ErrNotGranted
	}
	replaced := slices.IndexFunc(service.hooks, func(entry *registration) bool {
		return entry.hook.PluginID == hook.PluginID && entry.hook.HookID == hook.HookID
	})
	total, samePhase := len(service.hooks), 0
	for index, entry := range service.hooks {
		if index != replaced && entry.hook.PluginID == hook.PluginID && entry.hook.Phase == hook.Phase {
			samePhase++
		}
	}
	if replaced < 0 {
		total++
	}
	if total > domain.MaxHooks || samePhase >= domain.MaxHooksPerPhase {
		return ErrLimit
	}
	entry := &registration{hook: hook}
	if replaced < 0 {
		service.hooks = append(service.hooks, entry)
		return nil
	}
	service.release(service.hooks[replaced])
	service.hooks[replaced] = entry
	return nil
}

// Unregister removes the plugin's hook hookID, or every hook of the plugin
// when hookID is empty, and returns how many it removed.
func (service *Service) Unregister(pluginID, hookID string) (int, error) {
	if !domain.ValidPluginID(pluginID) {
		return 0, domain.ValidationError{Field: "pluginId", Reason: "must be 3-64 lowercase letters, digits, dots or dashes"}
	}
	if hookID != "" && !domain.ValidHookID(hookID) {
		return 0, domain.ValidationError{Field: "hookId", Reason: "must be 1-32 lowercase letters, digits, underscores or dashes"}
	}
	return service.remove(func(hook domain.Hook) bool {
		return hook.PluginID == pluginID && (hookID == "" || hook.HookID == hookID)
	}), nil
}

// UnregisterPlugin removes every hook of a plugin that was uninstalled. It
// runs inside the plugins service's mutation, so it never calls back into it.
func (service *Service) UnregisterPlugin(pluginID string) {
	service.remove(func(hook domain.Hook) bool { return hook.PluginID == pluginID })
}

// Reset removes every hook; the plugin host calls it when it starts.
func (service *Service) Reset() int {
	return service.remove(func(domain.Hook) bool { return true })
}

func (service *Service) remove(match func(domain.Hook) bool) int {
	service.mu.Lock()
	defer service.mu.Unlock()
	removed := 0
	service.hooks = slices.DeleteFunc(service.hooks, func(entry *registration) bool {
		if !match(entry.hook) {
			return false
		}
		service.release(entry)
		removed++
		return true
	})
	return removed
}

// release ends the pending calls of a hook that is going away: their requests
// continue at once instead of waiting out the timeout. Callers hold mu.
func (service *Service) release(owner *registration) {
	for callID, call := range service.pending {
		if call.owner == owner {
			delete(service.pending, callID)
			call.answer <- callResult{}
		}
	}
}

// Respond delivers a request hook's decision. It reports false for a call
// that is unknown or already over. An invalid decision is refused, and the
// call it answers continues unchanged.
func (service *Service) Respond(callID string, decision domain.Decision) (bool, error) {
	err := decision.Validate()
	if !domain.ValidCallID(callID) {
		return false, err
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	call, ok := service.pending[callID]
	if !ok {
		return false, err
	}
	delete(service.pending, callID)
	if err != nil {
		call.answer <- callResult{}
		return false, err
	}
	decision.Tags = slices.Clone(decision.Tags)
	call.answer <- callResult{decision: decision, answered: true}
	return true, nil
}

// Active reports whether a granted hook of phase would see a request for
// model at endpoint. It is cheap, so the relay asks before copying bodies.
func (service *Service) Active(phase, model, endpoint string) bool {
	var owners [domain.MaxHooks]string
	count := 0
	service.mu.Lock()
	grants, connected := service.grants, service.events != nil
	for _, entry := range service.hooks {
		if entry.hook.Phase == phase && entry.hook.Matches(model, endpoint) {
			owners[count] = entry.hook.PluginID
			count++
		}
	}
	service.mu.Unlock()
	if grants == nil || !connected {
		return false
	}
	for _, owner := range owners[:count] {
		if grants.Granted(owner, domain.PermissionRelayContent) {
			return true
		}
	}
	return false
}

// Before asks every matching request hook in registration order, each seeing
// the request as the hooks before it left it. It stops at the first block.
// A hook that does not answer in its timeout, or within the budget all hooks
// share, lets the request continue; ctx ends every wait.
func (service *Service) Before(ctx context.Context, request domain.Request) domain.Decision {
	result := domain.Decision{Action: domain.ActionContinue}
	service.mu.Lock()
	hooks := service.matching(domain.PhaseRequest, nil)
	grants, budget := service.grants, service.budget
	service.mu.Unlock()
	if len(hooks) == 0 || grants == nil {
		return result
	}
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	for _, entry := range hooks {
		if ctx.Err() != nil {
			break
		}
		// Each hook filters the request as the hooks before it left it: a
		// rewritten model can move it into or out of a filter.
		if !entry.hook.Matches(request.Model, request.Endpoint) || !grants.Granted(entry.hook.PluginID, domain.PermissionRelayContent) {
			continue
		}
		decision, ok := service.call(ctx, entry, request)
		if !ok {
			continue
		}
		result.Tags = domain.MergeTags(result.Tags, decision.Tags)
		switch decision.Action {
		case domain.ActionBlock:
			return domain.Decision{Action: domain.ActionBlock, Tags: result.Tags, Block: decision.Block}
		case domain.ActionRewrite:
			if decision.Model != "" {
				request.Model, result.Model = decision.Model, decision.Model
				result.Action = domain.ActionRewrite
			}
			if decision.Body != "" && !request.BodyOmitted && domain.UsableBody(decision.Body) {
				request.Body, result.Body = decision.Body, decision.Body
				request.Bytes = int64(len(decision.Body))
				result.Action = domain.ActionRewrite
			}
		}
	}
	return result
}

// call asks one hook and waits for its answer, its timeout or ctx.
func (service *Service) call(ctx context.Context, entry *registration, request domain.Request) (domain.Decision, bool) {
	callID, err := newCallID()
	if err != nil {
		return domain.Decision{}, false
	}
	answer := make(chan callResult, 1)
	service.mu.Lock()
	events := service.events
	if events == nil || len(service.pending) >= MaxPendingCalls || !slices.Contains(service.hooks, entry) {
		service.mu.Unlock()
		return domain.Decision{}, false
	}
	service.pending[callID] = &pendingCall{owner: entry, answer: answer}
	service.mu.Unlock()

	bodyOmitted, err := events.Invoke(domain.Invocation{
		CallID: callID, PluginID: entry.hook.PluginID, HookID: entry.hook.HookID, Request: request,
	})
	if err != nil {
		service.forget(callID)
		return domain.Decision{}, false
	}
	timer := time.NewTimer(entry.hook.Timeout())
	defer timer.Stop()
	var result callResult
	select {
	case result = <-answer:
	case <-timer.C:
		if service.forget(callID) {
			service.timedOut(entry)
			return domain.Decision{}, false
		}
		result = <-answer
	case <-ctx.Done():
		if service.forget(callID) {
			return domain.Decision{}, false
		}
		result = <-answer
	}
	if !result.answered {
		return domain.Decision{}, false
	}
	service.answered(entry)
	if bodyOmitted {
		result.decision.Body = ""
	}
	return result.decision, true
}

// forget drops a pending call and reports whether it was still pending. When
// it was not, its answer is already in the channel: Respond and release send
// under mu, while the call is removed.
func (service *Service) forget(callID string) bool {
	service.mu.Lock()
	defer service.mu.Unlock()
	if _, ok := service.pending[callID]; !ok {
		return false
	}
	delete(service.pending, callID)
	return true
}

func (service *Service) answered(entry *registration) {
	service.mu.Lock()
	entry.timeouts = 0
	service.mu.Unlock()
}

// timedOut counts a missed call and removes the hook after
// MaxConsecutiveTimeouts in a row.
func (service *Service) timedOut(entry *registration) {
	service.mu.Lock()
	index := slices.Index(service.hooks, entry)
	if index < 0 {
		service.mu.Unlock()
		return
	}
	entry.timeouts++
	if entry.timeouts < domain.MaxConsecutiveTimeouts {
		service.mu.Unlock()
		return
	}
	service.hooks = slices.Delete(service.hooks, index, index+1)
	service.release(entry)
	events := service.events
	service.mu.Unlock()
	if events != nil {
		_ = events.Removed(Removal{
			PluginID: entry.hook.PluginID, HookID: entry.hook.HookID, Phase: entry.hook.Phase, Reason: RemovalTimeouts,
		})
	}
}

// After queues a finished request for every matching response observer and
// returns at once. When the queue is full the request is not observed.
func (service *Service) After(request domain.Request, response domain.Response) {
	request.Body, request.BodyOmitted = "", false
	service.mu.Lock()
	defer service.mu.Unlock()
	if service.events == nil || service.grants == nil {
		return
	}
	hooks := service.matching(domain.PhaseResponse, &request)
	if len(hooks) == 0 || len(service.queue) >= MaxQueuedObservations {
		return
	}
	service.queue = append(service.queue, observation{hooks: hooks, request: request, response: response})
	if !service.draining {
		service.draining = true
		go service.drain()
	}
}

// drain delivers queued observations until the queue is empty, then exits.
func (service *Service) drain() {
	for {
		service.mu.Lock()
		if len(service.queue) == 0 {
			service.draining = false
			service.mu.Unlock()
			return
		}
		next := service.queue[0]
		service.queue[0] = observation{}
		service.queue = service.queue[1:]
		if len(service.queue) == 0 {
			service.queue = nil
		}
		grants, events := service.grants, service.events
		service.mu.Unlock()
		for _, entry := range next.hooks {
			if !service.registered(entry) || !grants.Granted(entry.hook.PluginID, domain.PermissionRelayContent) {
				continue
			}
			_ = events.Observe(domain.Observation{
				PluginID: entry.hook.PluginID, HookID: entry.hook.HookID, Request: next.request, Response: next.response,
			})
		}
	}
}

func (service *Service) registered(entry *registration) bool {
	service.mu.Lock()
	defer service.mu.Unlock()
	return slices.Contains(service.hooks, entry)
}

// matching lists the hooks of phase that take request (every hook of phase
// for a nil request). Callers hold mu.
func (service *Service) matching(phase string, request *domain.Request) []*registration {
	var hooks []*registration
	for _, entry := range service.hooks {
		if entry.hook.Phase == phase && (request == nil || entry.hook.Matches(request.Model, request.Endpoint)) {
			hooks = append(hooks, entry)
		}
	}
	return hooks
}

// newCallID returns 128 random bits in lower-case hex.
func newCallID() (string, error) {
	var buffer [16]byte
	if _, err := rand.Read(buffer[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(buffer[:]), nil
}
