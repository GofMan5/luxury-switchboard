package application

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/providers/domain"
)

var (
	ErrBuiltinProvider = errors.New("builtin provider cannot be deleted")
	// ErrActiveProvider refuses to disable the provider the active route
	// depends on: switch the active route first, then retire the provider.
	// Deleting the active provider is NOT refused — it switches the active
	// route to a deterministic builtin instead — so the sentinel's text
	// names the disable path alone; the stdio surface and the UI match it
	// by identity, not by text.
	ErrActiveProvider = errors.New("active provider cannot be disabled: switch the active route first")
	// ErrProviderIDExists names a refused Add: the id the caller asked for is
	// already taken. The entry is never merged or overwritten, so the caller
	// decides between a fresh id and refusing the request.
	ErrProviderIDExists = errors.New("provider id is already in use")
)

type KeyPool interface {
	// EnsureProvider registers the provider's request budget: the limit and the
	// window it is counted over.
	EnsureProvider(providerID string, rpm int, window time.Duration) error
	// InvalidateProvider retires old profile verdicts without resetting quotas.
	InvalidateProvider(providerID string)
	RemoveProvider(string) error
	// DropProvider is the delete-side companion of EnsureProvider: it
	// deletes every key that belongs to the provider, then its budget.
	// A failed delete rolls nothing back — the cascade is the user's
	// decision, already made.
	DropProvider(ctx context.Context, providerID string) error
}

// RouteCascade retargets and removes the route assignments that name a
// provider, so merging or deleting a provider is one action instead of a
// scavenger hunt through Model Routes. Wired after construction because
// the routes slice reads the provider catalog, and the provider manager
// reads the routes service: the cycle breaks here, at a typed port, not
// at an import.
type RouteCascade interface {
	RemoveProvider(ctx context.Context, providerID string) error
	// ReassignProvider moves every assignment that names one provider onto
	// another, preserving the routes rather than deleting them.
	ReassignProvider(ctx context.Context, providerID, replacementID string) error
}

type Manager struct {
	opMu       sync.Mutex
	catalog    *Catalog
	repository Repository
	keys       KeyPool
	routes     RouteCascade
	loadMu     sync.RWMutex
	loadErr    error
}

func (manager *Manager) SetRouteCascade(routes RouteCascade) {
	manager.opMu.Lock()
	manager.routes = routes
	manager.opMu.Unlock()
}

func NewManager(catalog *Catalog, repository Repository, keys KeyPool) (*Manager, error) {
	if catalog == nil || repository == nil || keys == nil {
		return nil, errors.New("provider manager dependencies are invalid")
	}
	return &Manager{catalog: catalog, repository: repository, keys: keys}, nil
}

func (manager *Manager) Load(ctx context.Context) error {
	err := manager.load(ctx)
	manager.loadMu.Lock()
	manager.loadErr = err
	manager.loadMu.Unlock()
	return err
}

func (manager *Manager) load(ctx context.Context) error {
	state, err := manager.repository.Load(ctx)
	if err != nil {
		return err
	}
	if len(state.Providers) == 0 {
		return nil
	}
	if _, err := NewCatalog(state.Providers, state.ActiveID); err != nil {
		return err
	}
	for _, provider := range state.Providers {
		if err := manager.keys.EnsureProvider(provider.ID, provider.RPM, provider.RateWindow()); err != nil {
			return err
		}
	}
	return manager.catalog.Replace(state.Providers, state.ActiveID)
}

// Availability reports why the persisted provider state could not be read, or
// nil when it could. A write must refuse while this is set: saving runtime
// defaults over a file that merely failed to load atomically replaces it, and
// every provider definition the user ever entered is gone after the next
// start. Routes made the same call first; this is the same rule.
func (manager *Manager) Availability() error {
	manager.loadMu.RLock()
	defer manager.loadMu.RUnlock()
	return manager.loadErr
}

// ErrStoreUnavailable names a write refused because the store behind it could
// not be read: the caller's own message should say what to unlock rather than
// blaming the edit.
var ErrStoreUnavailable = errors.New("provider storage could not be read; saving now would replace it")

func (manager *Manager) Add(ctx context.Context, params domain.Params) (domain.Provider, error) {
	manager.opMu.Lock()
	defer manager.opMu.Unlock()
	if err := manager.Availability(); err != nil {
		return domain.Provider{}, ErrStoreUnavailable
	}
	// A requested id is honored verbatim — callers like the backup import and
	// the codex provisioner rely on the entry landing under the id they
	// named, and domain.New validates the shape. Surrounding whitespace is
	// not part of the id. An empty id still gets a minted one.
	id := strings.TrimSpace(params.ID)
	if id != "" {
		if _, exists := manager.catalog.Lookup(id); exists {
			return domain.Provider{}, ErrProviderIDExists
		}
	} else {
		minted, err := randomProviderID()
		if err != nil {
			return domain.Provider{}, err
		}
		id = minted
	}
	params.ID = id
	params.Builtin = false
	provider, err := domain.New(params)
	if err != nil {
		return domain.Provider{}, err
	}
	active, err := manager.catalog.Active()
	if err != nil {
		return domain.Provider{}, err
	}
	providers := append(manager.catalog.List(), provider)
	// Admission is published only after the provider is durable. Otherwise a
	// concurrent restore/key command can save a key during this write, and a
	// failed write cannot roll the budget back while that orphan key exists.
	if err := manager.repository.Save(ctx, SavedState{Providers: providers, ActiveID: active.ID}); err != nil {
		return domain.Provider{}, fmt.Errorf("provider settings could not be saved: %w", err)
	}
	if err := manager.keys.EnsureProvider(provider.ID, provider.RPM, provider.RateWindow()); err != nil {
		return domain.Provider{}, err
	}
	if err := manager.catalog.Replace(providers, active.ID); err != nil {
		return domain.Provider{}, err
	}
	return provider, nil
}

// Get reads one provider by id. The boolean is existence, not admission: a
// disabled entry is still returned, because write paths — the codex
// provisioner deciding whether to relink a retired entry — must distinguish
// "missing" from "off". The in-memory catalog answers even when the store
// could not be loaded; ctx is accepted for port symmetry with the write
// methods and is not otherwise consulted.
func (manager *Manager) Get(ctx context.Context, id string) (domain.Provider, bool) {
	return manager.catalog.Lookup(id)
}

// List reads every provider entry — enabled and disabled alike — in
// catalog order, as a copy. Read paths that provision and relink preset
// rows (the codex slice deciding whether a parked entry can be
// re-pointed at a new account) must see retired entries too, and the
// in-memory catalog answers even when the store could not be loaded;
// ctx is accepted for port symmetry with the write methods and is not
// otherwise consulted.
func (manager *Manager) List(_ context.Context) []domain.Provider {
	return manager.catalog.List()
}

func (manager *Manager) Update(ctx context.Context, id string, params domain.Params) (domain.Provider, error) {
	manager.opMu.Lock()
	defer manager.opMu.Unlock()
	if err := manager.Availability(); err != nil {
		return domain.Provider{}, ErrStoreUnavailable
	}
	providers := manager.catalog.List()
	index := slices.IndexFunc(providers, func(provider domain.Provider) bool { return provider.ID == id })
	if index < 0 {
		return domain.Provider{}, ErrProviderUnavailable
	}
	current := providers[index]
	params.ID = id
	params.Builtin = current.Builtin
	// Preset identity is owned by the slice that provisioned the provider; an
	// update through the generic provider form must not strip it. The codex
	// slice changes the account through its own command, which carries the new
	// identity explicitly.
	params.Preset = current.Preset
	params.AccountID = current.AccountID
	updated, err := domain.New(params)
	if err != nil {
		return domain.Provider{}, err
	}
	// OAuth credentials belong to the managed destination, not an editable
	// endpoint. Canonical repair and retirement use this same path.
	if current.Preset == domain.PresetCodex && (updated.BaseURL.String() != "https://chatgpt.com/backend-api/codex" || updated.AuthMode != domain.AuthBearer || updated.AuthHeader != "" || updated.ModelsPath != "/models" || updated.Format != domain.FormatResponses || updated.ChatPath != "/v1/chat/completions") {
		return domain.Provider{}, errors.New("managed codex destination cannot be edited")
	}
	active, err := manager.catalog.Active()
	if err != nil {
		return domain.Provider{}, err
	}
	if active.ID == id && !updated.Enabled {
		return domain.Provider{}, ErrActiveProvider
	}
	providers[index] = updated
	// A provisional shorter window can prune consumed starts irreversibly.
	// Publish the validated runtime budget only after the durable save succeeds.
	if err := manager.repository.Save(ctx, SavedState{Providers: providers, ActiveID: active.ID}); err != nil {
		return domain.Provider{}, fmt.Errorf("provider settings could not be saved: %w", err)
	}
	if err := manager.keys.EnsureProvider(id, updated.RPM, updated.RateWindow()); err != nil {
		return domain.Provider{}, err
	}
	if err := manager.catalog.Replace(providers, active.ID); err != nil {
		return domain.Provider{}, err
	}
	if !sameCredentialProfile(current, updated) {
		manager.keys.InvalidateProvider(id)
	}
	return updated, nil
}

// SetPreset replaces the preset identity a provider carries. The generic
// update path preserves that identity on purpose, so this method exists for
// the slice that owns the preset: it re-points the provider at a new OAuth
// account after a re-login, or hands the entry back to manual management on
// logout. Everything else about the provider stays as configured.
func (manager *Manager) SetPreset(ctx context.Context, id string, preset domain.Preset, accountID domain.AccountID) (domain.Provider, error) {
	manager.opMu.Lock()
	defer manager.opMu.Unlock()
	if err := manager.Availability(); err != nil {
		return domain.Provider{}, ErrStoreUnavailable
	}
	providers := manager.catalog.List()
	index := slices.IndexFunc(providers, func(provider domain.Provider) bool { return provider.ID == id })
	if index < 0 {
		return domain.Provider{}, ErrProviderUnavailable
	}
	current := providers[index]
	params := paramsOf(current)
	params.Preset = preset
	params.AccountID = accountID
	updated, err := domain.New(params)
	if err != nil {
		return domain.Provider{}, err
	}
	active, err := manager.catalog.Active()
	if err != nil {
		return domain.Provider{}, err
	}
	providers[index] = updated
	if err := manager.repository.Save(ctx, SavedState{Providers: providers, ActiveID: active.ID}); err != nil {
		return domain.Provider{}, fmt.Errorf("provider settings could not be saved: %w", err)
	}
	if err := manager.catalog.Replace(providers, active.ID); err != nil {
		return domain.Provider{}, err
	}
	if !sameCredentialProfile(current, updated) {
		manager.keys.InvalidateProvider(id)
	}
	return updated, nil
}

func paramsOf(provider domain.Provider) domain.Params {
	return domain.Params{
		ID: provider.ID, Name: provider.Name, BaseURL: provider.BaseURL.String(),
		AuthMode: provider.AuthMode, AuthHeader: provider.AuthHeader, Dialect: provider.Dialect,
		ModelsPath: provider.ModelsPath, Format: provider.Format, ChatPath: provider.ChatPath,
		ImageCompat: provider.ImageCompat, RPM: provider.RPM, RateUnit: provider.RateUnit,
		CacheTTL: provider.CacheTTL, Enabled: provider.Enabled, Builtin: provider.Builtin,
	}
}

func (manager *Manager) Delete(ctx context.Context, id string) error {
	manager.opMu.Lock()
	defer manager.opMu.Unlock()
	if err := manager.Availability(); err != nil {
		return ErrStoreUnavailable
	}
	providers := manager.catalog.List()
	index := slices.IndexFunc(providers, func(provider domain.Provider) bool { return provider.ID == id })
	if index < 0 {
		return ErrProviderUnavailable
	}
	if providers[index].Builtin {
		return ErrBuiltinProvider
	}
	active, err := manager.catalog.Active()
	if err != nil {
		return err
	}
	// Deleting the provider the active route points at moves the route
	// rather than refusing the delete: the decision is already made. The
	// one refusal is having nowhere to move — checked before anything is
	// cascaded, so a refused delete changes nothing.
	nextActive := active.ID
	if active.ID == id {
		fallback, found := fallbackActiveProvider(providers, id)
		if !found {
			return errors.New("no enabled provider remains: enable another provider before deleting the active one")
		}
		nextActive = fallback.ID
	}
	// The cascade runs before the entry leaves the store. Delete is one
	// deliberate action: the routes and keys that name the provider go
	// with it, not ahead of it by hand. Ordering matters for failure too —
	// a failed entry save leaves the provider still listed but empty of
	// keys and routes, which is consistent and retryable. Rolling the
	// cascade back would mean re-adding keys this manager never held.
	if manager.routes != nil {
		if err := manager.routes.RemoveProvider(ctx, id); err != nil {
			return fmt.Errorf("provider model routes could not be deleted: %w", err)
		}
	}
	if err := manager.keys.DropProvider(ctx, id); err != nil {
		return fmt.Errorf("provider keys could not be deleted: %w", err)
	}
	providers = append(providers[:index], providers[index+1:]...)
	if err := manager.repository.Save(ctx, SavedState{Providers: providers, ActiveID: nextActive}); err != nil {
		return fmt.Errorf("provider settings could not be saved: %w", err)
	}
	return manager.catalog.Replace(providers, nextActive)
}

// fallbackActiveProvider picks where the active route lands when its
// provider is deleted: the first enabled builtin in catalog order — a
// builtin always answers, even with no keys — then any other enabled
// provider. Deterministic on purpose: two identical deletes must pick
// the same target, or the second one would behave differently than the
// first for no visible reason.
func fallbackActiveProvider(providers []domain.Provider, deletedID string) (domain.Provider, bool) {
	for _, provider := range providers {
		if provider.ID == deletedID {
			continue
		}
		if provider.Enabled && provider.Builtin {
			return provider, true
		}
	}
	for _, provider := range providers {
		if provider.ID == deletedID {
			continue
		}
		if provider.Enabled {
			return provider, true
		}
	}
	return domain.Provider{}, false
}

// Fold merges one provider entry into another without deleting what the
// entry carried: every route that names the folded provider is retargeted
// at the survivor, and the folded entry leaves the catalog. It is the
// data-preserving migration Delete refuses to be — a preset the product
// stopped creating must not take the operator's routes down with it.
//
// If the folded provider was the active one, the active route moves to the
// survivor (or the same deterministic fallback Delete uses, when the
// survivor is disabled). The order mirrors Delete's cascade: routes first,
// then keys, then the entry list — each step is a separate save that
// leaves consistent, retryable state behind, and the whole fold is
// idempotent, so a caller that failed partway runs it again next chance.
func (manager *Manager) Fold(ctx context.Context, fromID, toID string) error {
	manager.opMu.Lock()
	defer manager.opMu.Unlock()
	if err := manager.Availability(); err != nil {
		return ErrStoreUnavailable
	}
	providers := manager.catalog.List()
	index := slices.IndexFunc(providers, func(provider domain.Provider) bool { return provider.ID == fromID })
	if index < 0 {
		return ErrProviderUnavailable
	}
	if providers[index].Builtin {
		return ErrBuiltinProvider
	}
	if fromID == toID {
		return errors.New("a provider cannot be folded into itself")
	}
	if _, exists := manager.catalog.Lookup(toID); !exists {
		return errors.New("fold target provider does not exist")
	}
	active, err := manager.catalog.Active()
	if err != nil {
		return err
	}
	nextActive := active.ID
	if active.ID == fromID {
		if survivor, exists := manager.catalog.Lookup(toID); exists && survivor.Enabled {
			nextActive = toID
		} else {
			fallback, found := fallbackActiveProvider(providers, fromID)
			if !found {
				return errors.New("no enabled provider remains to take the active route")
			}
			nextActive = fallback.ID
		}
	}
	if manager.routes != nil {
		if err := manager.routes.ReassignProvider(ctx, fromID, toID); err != nil {
			return fmt.Errorf("provider model routes could not be folded: %w", err)
		}
	}
	if err := manager.keys.DropProvider(ctx, fromID); err != nil {
		return fmt.Errorf("provider keys could not be deleted: %w", err)
	}
	providers = append(providers[:index], providers[index+1:]...)
	if err := manager.repository.Save(ctx, SavedState{Providers: providers, ActiveID: nextActive}); err != nil {
		return fmt.Errorf("provider settings could not be saved: %w", err)
	}
	return manager.catalog.Replace(providers, nextActive)
}

func (manager *Manager) Activate(ctx context.Context, id string) (domain.Provider, error) {
	manager.opMu.Lock()
	defer manager.opMu.Unlock()
	if err := manager.Availability(); err != nil {
		return domain.Provider{}, ErrStoreUnavailable
	}
	providers := manager.catalog.List()
	provider, exists := providerByID(providers, id)
	if !exists || !provider.Enabled {
		return domain.Provider{}, ErrProviderUnavailable
	}
	if err := manager.repository.Save(ctx, SavedState{Providers: providers, ActiveID: id}); err != nil {
		return domain.Provider{}, fmt.Errorf("provider settings could not be saved: %w", err)
	}
	return manager.catalog.Activate(id)
}

func providerByID(providers []domain.Provider, id string) (domain.Provider, bool) {
	for _, provider := range providers {
		if provider.ID == id {
			return provider, true
		}
	}
	return domain.Provider{}, false
}

func randomProviderID() (string, error) {
	value := make([]byte, 8)
	if _, err := rand.Read(value); err != nil {
		return "", errors.New("secure random source unavailable")
	}
	return "provider_" + hex.EncodeToString(value), nil
}
