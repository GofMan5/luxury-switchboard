package application

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/routes/domain"
)

var ErrProviderUnavailable = errors.New("route provider is unavailable")

// Degradation TTL: how long a provider that answered a terminal verdict sits
// behind its chain siblings. Short on purpose — the verdict classes that set it
// (dead pool, shared batch quota, client rejection) resolve without notice, so
// the chain re-probes the provider rather than committing to its death. Worst
// case is one wasted attempt per TTL per model.
const degradeTTL = 5 * time.Minute

// Chain modes: "failover" serves strictly by priority, "balance" round-robins
// the healthy entries. The string values are the settings record's contract.
const (
	chainModeFailover = "failover"
	chainModeBalance  = "balance"
)

type Repository interface {
	Load(context.Context) ([]domain.Assignment, error)
	Save(context.Context, []domain.Assignment) error
}

type ProviderCatalog interface{ Exists(string) bool }

type Service struct {
	opMu        sync.Mutex
	mu          sync.RWMutex
	repository  Repository
	providers   ProviderCatalog
	assignments []domain.Assignment
	loadErr     error
	listeners   []func(domain.Target)
	// degraded records providers that answered a terminal verdict, and until
	// when. Routing prefers siblings while it lasts instead of sending every
	// request to a provider that just refused.
	degraded     map[string]time.Time
	degradeClock func() time.Time
	// chainMode is how a healthy chain shares requests: strict priority or
	// round-robin. Live-applied from settings.
	chainModeValue string
	// rotation advances on every balanced resolve; a plain counter per
	// service rather than per model keeps the hot path lock-cheap, and the
	// modulo of the eligible set is what makes the round fair.
	rotation uint64
}

func NewService(repository Repository, providers ProviderCatalog) (*Service, error) {
	if repository == nil || providers == nil {
		return nil, errors.New("route dependencies are invalid")
	}
	return &Service{
		repository: repository, providers: providers,
		degraded: map[string]time.Time{}, degradeClock: time.Now,
		chainModeValue: chainModeFailover,
	}, nil
}

// SetChainMode switches how a healthy chain shares requests. Applied live:
// requests in flight keep their already-resolved route.
func (service *Service) SetChainMode(mode string) {
	if mode != chainModeFailover && mode != chainModeBalance {
		return
	}
	service.mu.Lock()
	service.chainModeValue = mode
	service.mu.Unlock()
}

func (service *Service) chainMode() string {
	service.mu.RLock()
	defer service.mu.RUnlock()
	return service.chainModeValue
}

func (service *Service) Load(ctx context.Context) error {
	service.opMu.Lock()
	defer service.opMu.Unlock()
	assignments, err := service.repository.Load(ctx)
	if err != nil {
		service.setLoadError(err)
		return err
	}
	seen := make(map[string]struct{}, len(assignments))
	for _, assignment := range assignments {
		if assignment.Validate() != nil {
			err := errors.New("saved routes are invalid")
			service.setLoadError(err)
			return err
		}
		key := assignmentKey(assignment)
		if _, duplicate := seen[key]; duplicate {
			err := errors.New("saved routes are invalid")
			service.setLoadError(err)
			return err
		}
		seen[key] = struct{}{}
		// The tunnel target publishes one route per public model: two entries
		// for one name would make the public boundary ambiguous.
		if assignment.Target == domain.TargetTunnel {
			tunnelKey := string(domain.TargetTunnel) + "\x00" + assignment.PublicModel
			if _, duplicate := seen[tunnelKey]; duplicate {
				err := errors.New("saved routes are invalid")
				service.setLoadError(err)
				return err
			}
			seen[tunnelKey] = struct{}{}
		}
	}
	if aliasesCollide(assignments) {
		err := errors.New("saved routes are invalid")
		service.setLoadError(err)
		return err
	}
	service.mu.Lock()
	service.assignments = slices.Clone(assignments)
	service.loadErr = nil
	service.mu.Unlock()
	return nil
}

func (service *Service) Availability() error {
	service.mu.RLock()
	defer service.mu.RUnlock()
	return service.loadErr
}

func (service *Service) List(target domain.Target) []domain.Assignment {
	service.mu.RLock()
	defer service.mu.RUnlock()
	if service.loadErr != nil {
		return nil
	}
	result := make([]domain.Assignment, 0)
	for _, assignment := range service.assignments {
		if assignment.Target == target {
			result = append(result, assignment)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].PublicModel != result[j].PublicModel {
			return result[i].PublicModel < result[j].PublicModel
		}
		return result[i].Priority < result[j].Priority
	})
	return result
}

// Resolve answers the assignment a request should try. The relay target
// depends on the chain mode:
//
//   - failover: strictly by priority, degraded siblings pushed behind healthy
//     ones, and an all-degraded chain still serves — degrading is preference,
//     not removal, so a chain never answers "no route" it would not have
//     answered before.
//   - balance: round-robin across the healthy entries, in priority order. A
//     provider's shared daily quota is spent per provider, so spreading
//     requests across the chain multiplies the quota by its healthy length;
//     degraded entries sit out the rotation and return when their TTL lapses.
//     An all-degraded chain serves by priority, same as failover.
//
// The tunnel target has one route per model and ignores the mode.
func (service *Service) Resolve(target domain.Target, model string) (domain.Assignment, bool) {
	if target != domain.TargetRelay || service.chainMode() != chainModeBalance {
		return service.resolveByPriority(target, model)
	}
	// Balance mode needs the write lock: the rotation counter moves on every
	// resolve. The critical section is a map increment, nothing more.
	service.mu.Lock()
	defer service.mu.Unlock()
	if service.loadErr != nil {
		return domain.Assignment{}, false
	}
	now := service.degradeClock()
	eligible := make([]domain.Assignment, 0, 2)
	for _, assignment := range service.assignments {
		if assignment.Target != target || !assignment.Enabled {
			continue
		}
		if assignment.PublicModel != model && !slices.Contains(assignment.Aliases, model) {
			continue
		}
		if !service.isDegradedLocked(assignment.ProviderID, now) {
			eligible = append(eligible, assignment)
		}
	}
	if len(eligible) == 0 {
		// Nothing healthy: the chain still serves, by its configured order.
		return service.resolveByPriorityLocked(target, model, now)
	}
	sort.SliceStable(eligible, func(i, j int) bool { return eligible[i].Priority < eligible[j].Priority })
	service.rotation++
	pick := eligible[int(service.rotation%uint64(len(eligible)))]
	return pick, true
}

func (service *Service) resolveByPriority(target domain.Target, model string) (domain.Assignment, bool) {
	service.mu.RLock()
	defer service.mu.RUnlock()
	if service.loadErr != nil {
		return domain.Assignment{}, false
	}
	return service.resolveByPriorityLocked(target, model, service.degradeClock())
}

func (service *Service) resolveByPriorityLocked(target domain.Target, model string, now time.Time) (domain.Assignment, bool) {
	var best domain.Assignment
	found := false
	bestRank := 0
	for _, assignment := range service.assignments {
		if assignment.Target != target || !assignment.Enabled {
			continue
		}
		if assignment.PublicModel != model && !slices.Contains(assignment.Aliases, model) {
			continue
		}
		rank := assignment.Priority
		if service.isDegradedLocked(assignment.ProviderID, now) {
			rank += 1_000_000
		}
		if !found || rank < bestRank {
			best, bestRank, found = assignment, rank, true
		}
	}
	return best, found
}

// Chain lists the relay assignments for a model in failover order, healthiest
// first, the order a request walks when the current one answers terminally.
func (service *Service) Chain(model string) []domain.Assignment {
	service.mu.RLock()
	defer service.mu.RUnlock()
	if service.loadErr != nil {
		return nil
	}
	now := service.degradeClock()
	chain := make([]domain.Assignment, 0, 2)
	for _, assignment := range service.assignments {
		if assignment.Target != domain.TargetRelay || !assignment.Enabled {
			continue
		}
		if assignment.PublicModel == model || slices.Contains(assignment.Aliases, model) {
			chain = append(chain, assignment)
		}
	}
	sort.SliceStable(chain, func(i, j int) bool {
		left, right := chain[i].Priority, chain[j].Priority
		if service.isDegradedLocked(chain[i].ProviderID, now) {
			left += 1_000_000
		}
		if service.isDegradedLocked(chain[j].ProviderID, now) {
			right += 1_000_000
		}
		return left < right
	})
	return chain
}

// Degrade parks a provider behind its chain siblings after a terminal verdict:
// dead credentials, an exhausted shared quota, a client-level rejection. The
// TTL is short because these verdicts also lift without notice, and a chain
// that never re-probed would strand a provider that recovered.
func (service *Service) Degrade(providerID string) {
	service.mu.Lock()
	if service.degraded == nil {
		service.degraded = map[string]time.Time{}
	}
	service.degraded[providerID] = service.degradeClock().Add(degradeTTL)
	service.mu.Unlock()
}

func (service *Service) isDegradedLocked(providerID string, now time.Time) bool {
	until, degraded := service.degraded[providerID]
	return degraded && now.Before(until)
}

func (service *Service) Upsert(ctx context.Context, assignment domain.Assignment) error {
	if err := assignment.Validate(); err != nil {
		return err
	}
	service.opMu.Lock()
	defer service.opMu.Unlock()
	if err := service.Availability(); err != nil {
		return err
	}
	if !service.providers.Exists(assignment.ProviderID) {
		return ErrProviderUnavailable
	}
	service.mu.RLock()
	candidate := slices.Clone(service.assignments)
	service.mu.RUnlock()
	index := chainIndex(candidate, assignment)
	if index < 0 {
		candidate = append(candidate, assignment)
	} else {
		candidate[index] = assignment
	}
	if aliasesCollide(candidate) {
		return errors.New("route alias collides with another route")
	}
	return service.persist(ctx, candidate, assignment.Target)
}

func (service *Service) UpsertMany(ctx context.Context, assignments []domain.Assignment) error {
	if len(assignments) == 0 || len(assignments) > 500 {
		return errors.New("invalid route batch")
	}
	target := assignments[0].Target
	seen := make(map[string]struct{}, len(assignments))
	for _, assignment := range assignments {
		if assignment.Target != target || assignment.Validate() != nil {
			return errors.New("invalid route batch")
		}
		key := assignmentKey(assignment)
		if _, duplicate := seen[key]; duplicate {
			return errors.New("invalid route batch")
		}
		seen[key] = struct{}{}
	}
	service.opMu.Lock()
	defer service.opMu.Unlock()
	if err := service.Availability(); err != nil {
		return err
	}
	for _, assignment := range assignments {
		if !service.providers.Exists(assignment.ProviderID) {
			return ErrProviderUnavailable
		}
	}
	service.mu.RLock()
	candidate := slices.Clone(service.assignments)
	service.mu.RUnlock()
	for _, assignment := range assignments {
		index := chainIndex(candidate, assignment)
		if index < 0 {
			candidate = append(candidate, assignment)
		} else {
			candidate[index] = assignment
		}
	}
	if aliasesCollide(candidate) {
		return errors.New("route alias collides with another route")
	}
	return service.persist(ctx, candidate, target)
}

// Delete removes a public model whole: on the relay target that is the entire
// failover chain, because a chain with a hole the operator did not ask for is
// a route that silently behaves differently from the one configured.
func (service *Service) Delete(ctx context.Context, target domain.Target, publicModel string) error {
	if err := domain.ValidateIdentity(target, publicModel); err != nil {
		return err
	}
	service.opMu.Lock()
	defer service.opMu.Unlock()
	if err := service.Availability(); err != nil {
		return err
	}
	service.mu.RLock()
	candidate := slices.Clone(service.assignments)
	service.mu.RUnlock()
	kept := make([]domain.Assignment, 0, len(candidate))
	for _, item := range candidate {
		if item.Target == target && item.PublicModel == publicModel {
			continue
		}
		kept = append(kept, item)
	}
	return service.persist(ctx, kept, target)
}

// RemoveProvider is the cascade behind deleting a provider: every assignment
// that names it, relay and tunnel alike, goes with the provider in one save,
// and the listeners hear about each target that actually lost a row. Delete
// stays the operator's single-model tool; this is the hand-off the providers
// manager calls while the provider entry still exists.
func (service *Service) RemoveProvider(ctx context.Context, providerID string) error {
	service.opMu.Lock()
	defer service.opMu.Unlock()
	if err := service.Availability(); err != nil {
		return err
	}
	service.mu.RLock()
	candidate := slices.Clone(service.assignments)
	service.mu.RUnlock()
	kept := make([]domain.Assignment, 0, len(candidate))
	affected := map[domain.Target]struct{}{}
	for _, item := range candidate {
		if item.ProviderID == providerID {
			affected[item.Target] = struct{}{}
			continue
		}
		kept = append(kept, item)
	}
	if len(affected) == 0 {
		return nil
	}
	targets := make([]domain.Target, 0, len(affected))
	for _, target := range []domain.Target{domain.TargetRelay, domain.TargetTunnel} {
		if _, hit := affected[target]; hit {
			targets = append(targets, target)
		}
	}
	return service.persistTargets(ctx, kept, targets)
}

func assignmentKey(assignment domain.Assignment) string {
	// The relay target is a chain: one public model may sit on several
	// providers, so a row is identified by the provider too. The tunnel target
	// publishes one route per model and keeps its stricter uniqueness in Load.
	return string(assignment.Target) + "\x00" + assignment.PublicModel + "\x00" + assignment.ProviderID
}

// chainIndex finds the stored row an upsert replaces: the relay row for this
// model on this provider, or the tunnel's single row for the model.
func chainIndex(candidate []domain.Assignment, assignment domain.Assignment) int {
	return slices.IndexFunc(candidate, func(item domain.Assignment) bool {
		if item.Target != assignment.Target || item.PublicModel != assignment.PublicModel {
			return false
		}
		if assignment.Target == domain.TargetRelay {
			return item.ProviderID == assignment.ProviderID
		}
		return true
	})
}

// aliasesCollide reports whether any public model or alias name is claimed by
// more than one route on the same target. Ambiguous routing must fail loudly,
// while the same name on relay and tunnel stays independent.
func aliasesCollide(assignments []domain.Assignment) bool {
	owners := make(map[string]string, len(assignments)*2)
	for _, assignment := range assignments {
		names := make([]string, 0, 1+len(assignment.Aliases))
		names = append(names, assignment.PublicModel)
		names = append(names, assignment.Aliases...)
		for _, name := range names {
			key := string(assignment.Target) + "\x00" + name
			if owner, exists := owners[key]; exists && owner != assignment.PublicModel {
				return true
			}
			owners[key] = assignment.PublicModel
		}
	}
	return false
}

func (service *Service) OnChanged(listener func(domain.Target)) {
	if listener == nil {
		return
	}
	service.mu.Lock()
	service.listeners = append(service.listeners, listener)
	service.mu.Unlock()
}

func (service *Service) persist(ctx context.Context, candidate []domain.Assignment, target domain.Target) error {
	return service.persistTargets(ctx, candidate, []domain.Target{target})
}

func (service *Service) persistTargets(ctx context.Context, candidate []domain.Assignment, targets []domain.Target) error {
	if err := service.repository.Save(ctx, candidate); err != nil {
		return fmt.Errorf("routes could not be saved: %w", err)
	}
	service.mu.Lock()
	service.assignments = slices.Clone(candidate)
	listeners := append([]func(domain.Target){}, service.listeners...)
	service.mu.Unlock()
	for _, listener := range listeners {
		for _, target := range targets {
			listener(target)
		}
	}
	return nil
}

func (service *Service) setLoadError(err error) {
	service.mu.Lock()
	service.assignments = nil
	service.loadErr = err
	service.mu.Unlock()
}
