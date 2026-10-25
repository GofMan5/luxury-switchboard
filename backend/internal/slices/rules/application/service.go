// Package application runs the operator's relay rules: it persists them,
// evaluates request rules for the local relay, answers retry-rule lookups for
// every relay path, and keeps the in-memory hit and window counters.
package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/rules/domain"
)

var (
	// ErrStoreUnavailable names a write refused because the rules file could
	// not be read: saving over it would replace every rule with this one edit.
	ErrStoreUnavailable = errors.New("rules storage could not be read; saving now would replace it")
	ErrNotFound         = errors.New("rule not found")
	// ErrPermissionDenied refuses a plugin editing a rule it does not own, or
	// reordering at all: order is the operator's decision.
	ErrPermissionDenied = errors.New("rule belongs to another owner")
)

// maxSetBytes keeps the whole set — the rules file and the rules.list answer
// — inside one stdio payload with room to spare. Measured on the encoded
// form, because the per-field caps alone allow a set several times larger.
const maxSetBytes = 192 * 1024

type Repository interface {
	Load(context.Context) (domain.Set, bool, error)
	Save(context.Context, domain.Set) error
}

type Service struct {
	opMu       sync.Mutex
	repository Repository
	history    FailureHistory
	// changed hears each saved set under opMu, so notifications arrive in
	// save order.
	changed func()

	// mu guards the current set and its counters. Evaluate takes it on the
	// relay's hot path; it holds for a scan of at most 64 rules.
	mu       sync.Mutex
	set      domain.Set
	loadErr  error
	counters map[string]*counter
	now      func() time.Time
}

// NewService builds the service. history may be nil: recentErrors then
// reports that history is unavailable rather than answering empty.
func NewService(repository Repository, history FailureHistory) (*Service, error) {
	if repository == nil {
		return nil, errors.New("rules repository is required")
	}
	return &Service{
		repository: repository,
		history:    history,
		set:        domain.Set{RequestRules: []domain.RequestRule{}, RetryRules: []domain.RetryRule{}},
		counters:   make(map[string]*counter),
		now:        time.Now,
	}, nil
}

// OnChanged sets the listener told after each saved change. Set before serving.
func (service *Service) OnChanged(listener func()) {
	service.opMu.Lock()
	service.changed = listener
	service.opMu.Unlock()
}

// Load restores the saved rules. A failure is remembered: the service keeps
// running with no rules and refuses to save until a restart reads the file.
func (service *Service) Load(ctx context.Context) error {
	service.opMu.Lock()
	defer service.opMu.Unlock()
	set, found, err := service.repository.Load(ctx)
	if err == nil && found {
		set = set.Normalized()
		if invalid := set.Validate(); invalid != nil {
			err = fmt.Errorf("stored rules are invalid: %w", invalid)
		}
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	service.loadErr = err
	if err == nil && found {
		service.set = set
		service.pruneCountersLocked()
	}
	return err
}

// List returns a copy of every rule in evaluation order.
func (service *Service) List() domain.Set {
	service.mu.Lock()
	defer service.mu.Unlock()
	return service.set.Clone()
}

// SaveRequestRule inserts a rule, or replaces the one with its id in place.
// An actor ("plugin:<id>") may only replace rules it owns and gives a new
// rule its own ownership; the operator (no actor) may replace any rule and
// keeps the existing owner, so toggling a plugin's rule does not take it over.
func (service *Service) SaveRequestRule(ctx context.Context, rule domain.RequestRule, actor string) (domain.RequestRule, error) {
	if err := validActor(actor); err != nil {
		return domain.RequestRule{}, err
	}
	rule = rule.Normalized()
	rule.Owner = actor
	if err := rule.Validate(); err != nil {
		return domain.RequestRule{}, err
	}
	var saved domain.RequestRule
	err := service.mutate(ctx, func(set *domain.Set) (bool, error) {
		if slices.ContainsFunc(set.RetryRules, func(other domain.RetryRule) bool { return other.ID == rule.ID }) {
			return false, domain.ValidationError{Field: "Rule id", Reason: "is used by a retry rule"}
		}
		index := slices.IndexFunc(set.RequestRules, func(other domain.RequestRule) bool { return other.ID == rule.ID })
		if index < 0 {
			if len(set.RequestRules) >= domain.MaxRequestRules {
				return false, domain.ValidationError{Field: "Request rules", Reason: "are limited to 64"}
			}
			set.RequestRules = append(set.RequestRules, rule)
			saved = rule
			return true, nil
		}
		existing := set.RequestRules[index].Owner
		if actor != "" && existing != actor {
			return false, ErrPermissionDenied
		}
		rule.Owner = existing
		set.RequestRules[index] = rule
		saved = rule
		return true, nil
	})
	if err != nil {
		return domain.RequestRule{}, err
	}
	return saved.Normalized(), nil
}

// DeleteRequestRule removes the rule with id.
func (service *Service) DeleteRequestRule(ctx context.Context, id, actor string) error {
	if err := validActor(actor); err != nil {
		return err
	}
	return service.mutate(ctx, func(set *domain.Set) (bool, error) {
		index := slices.IndexFunc(set.RequestRules, func(rule domain.RequestRule) bool { return rule.ID == id })
		if index < 0 {
			return false, ErrNotFound
		}
		if actor != "" && set.RequestRules[index].Owner != actor {
			return false, ErrPermissionDenied
		}
		set.RequestRules = slices.Delete(set.RequestRules, index, index+1)
		return true, nil
	})
}

// ReorderRequestRules puts the request rules in the order ids names; ids
// must name every rule exactly once. Operator only.
func (service *Service) ReorderRequestRules(ctx context.Context, ids []string, actor string) ([]domain.RequestRule, error) {
	if actor != "" {
		return nil, ErrPermissionDenied
	}
	var result []domain.RequestRule
	err := service.mutate(ctx, func(set *domain.Set) (bool, error) {
		ordered, err := reorder(set.RequestRules, ids, func(rule domain.RequestRule) string { return rule.ID })
		if err != nil {
			return false, domain.ValidationError{Field: "Reorder", Reason: "must list every request rule exactly once"}
		}
		set.RequestRules = ordered
		return true, nil
	})
	if err != nil {
		return nil, err
	}
	result = service.List().RequestRules
	return result, nil
}

// SaveRetryRule inserts or replaces a custom retry rule, with the same
// ownership rules as SaveRequestRule.
func (service *Service) SaveRetryRule(ctx context.Context, rule domain.RetryRule, actor string) (domain.RetryRule, error) {
	if err := validActor(actor); err != nil {
		return domain.RetryRule{}, err
	}
	rule = rule.Normalized()
	rule.Owner = actor
	if err := rule.Validate(); err != nil {
		return domain.RetryRule{}, err
	}
	var saved domain.RetryRule
	err := service.mutate(ctx, func(set *domain.Set) (bool, error) {
		if slices.ContainsFunc(set.RequestRules, func(other domain.RequestRule) bool { return other.ID == rule.ID }) {
			return false, domain.ValidationError{Field: "Rule id", Reason: "is used by a request rule"}
		}
		index := slices.IndexFunc(set.RetryRules, func(other domain.RetryRule) bool { return other.ID == rule.ID })
		if index < 0 {
			if len(set.RetryRules) >= domain.MaxRetryRules {
				return false, domain.ValidationError{Field: "Retry rules", Reason: "are limited to 64"}
			}
			set.RetryRules = append(set.RetryRules, rule)
			saved = rule
			return true, nil
		}
		existing := set.RetryRules[index].Owner
		if actor != "" && existing != actor {
			return false, ErrPermissionDenied
		}
		rule.Owner = existing
		set.RetryRules[index] = rule
		saved = rule
		return true, nil
	})
	if err != nil {
		return domain.RetryRule{}, err
	}
	return saved.Normalized(), nil
}

// DeleteRetryRule removes the retry rule with id.
func (service *Service) DeleteRetryRule(ctx context.Context, id, actor string) error {
	if err := validActor(actor); err != nil {
		return err
	}
	return service.mutate(ctx, func(set *domain.Set) (bool, error) {
		index := slices.IndexFunc(set.RetryRules, func(rule domain.RetryRule) bool { return rule.ID == id })
		if index < 0 {
			return false, ErrNotFound
		}
		if actor != "" && set.RetryRules[index].Owner != actor {
			return false, ErrPermissionDenied
		}
		set.RetryRules = slices.Delete(set.RetryRules, index, index+1)
		return true, nil
	})
}

// ReorderRetryRules puts the retry rules in the order ids names. Operator only.
func (service *Service) ReorderRetryRules(ctx context.Context, ids []string, actor string) ([]domain.RetryRule, error) {
	if actor != "" {
		return nil, ErrPermissionDenied
	}
	err := service.mutate(ctx, func(set *domain.Set) (bool, error) {
		ordered, err := reorder(set.RetryRules, ids, func(rule domain.RetryRule) string { return rule.ID })
		if err != nil {
			return false, domain.ValidationError{Field: "Reorder", Reason: "must list every retry rule exactly once"}
		}
		set.RetryRules = ordered
		return true, nil
	})
	if err != nil {
		return nil, err
	}
	return service.List().RetryRules, nil
}

// DropOwner deletes every rule owner holds — the plugin host calls it when a
// plugin is removed. Nothing is saved when the owner held no rules.
func (service *Service) DropOwner(ctx context.Context, owner string) error {
	if owner == "" {
		return nil
	}
	return service.mutate(ctx, func(set *domain.Set) (bool, error) {
		before := len(set.RequestRules) + len(set.RetryRules)
		set.RequestRules = slices.DeleteFunc(set.RequestRules, func(rule domain.RequestRule) bool { return rule.Owner == owner })
		set.RetryRules = slices.DeleteFunc(set.RetryRules, func(rule domain.RetryRule) bool { return rule.Owner == owner })
		return len(set.RequestRules)+len(set.RetryRules) != before, nil
	})
}

// mutate applies edit to a copy of the current set and, when edit reports a
// change, validates, persists and only then publishes it.
func (service *Service) mutate(ctx context.Context, edit func(*domain.Set) (bool, error)) error {
	service.opMu.Lock()
	defer service.opMu.Unlock()
	service.mu.Lock()
	next := service.set.Clone()
	loadErr := service.loadErr
	service.mu.Unlock()
	changed, err := edit(&next)
	if err != nil {
		return err
	}
	if !changed {
		return nil
	}
	if loadErr != nil {
		return ErrStoreUnavailable
	}
	next = next.Normalized()
	if err := next.Validate(); err != nil {
		return err
	}
	if encoded, err := json.Marshal(next); err != nil || len(encoded) > maxSetBytes {
		return domain.ValidationError{Field: "Rules", Reason: "are too large to store; remove some rules or patterns"}
	}
	if err := service.repository.Save(ctx, next); err != nil {
		return fmt.Errorf("rules could not be saved: %w", err)
	}
	service.mu.Lock()
	service.set = next
	service.pruneCountersLocked()
	service.mu.Unlock()
	if service.changed != nil {
		service.changed()
	}
	return nil
}

func (service *Service) pruneCountersLocked() {
	live := make(map[string]bool, len(service.set.RequestRules)+len(service.set.RetryRules))
	for _, rule := range service.set.RequestRules {
		live[rule.ID] = true
	}
	for _, rule := range service.set.RetryRules {
		live[rule.ID] = true
	}
	for id := range service.counters {
		if !live[id] {
			delete(service.counters, id)
		}
	}
}

func validActor(actor string) error {
	if actor != "" && !domain.ValidActor(actor) {
		return domain.ValidationError{Field: "Actor", Reason: "is invalid"}
	}
	return nil
}

var errNotPermutation = errors.New("not a permutation")

func reorder[T any](rules []T, ids []string, id func(T) string) ([]T, error) {
	if len(ids) != len(rules) {
		return nil, errNotPermutation
	}
	byID := make(map[string]T, len(rules))
	for _, rule := range rules {
		byID[id(rule)] = rule
	}
	ordered := make([]T, 0, len(ids))
	for _, value := range ids {
		rule, ok := byID[value]
		if !ok {
			return nil, errNotPermutation
		}
		delete(byID, value)
		ordered = append(ordered, rule)
	}
	return ordered, nil
}
