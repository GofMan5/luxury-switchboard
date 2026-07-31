package application

import (
	"context"
	"errors"
	"net/url"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/keypool/domain"
)

var (
	ErrKeyNotFound     = errors.New("key not found")
	ErrPinnedKey       = errors.New("pinned key cannot be changed this way")
	ErrDuplicateKey    = errors.New("key already exists")
	ErrUnknownProvider = errors.New("unknown provider")
	ErrProviderHasKeys = errors.New("provider still has keys")
)

type Manager struct {
	opMu        sync.Mutex
	mu          sync.RWMutex
	scheduler   *Scheduler
	repository  Repository
	providerRPM map[string]int
	builtins    []domain.Key
	userKeys    []domain.Key
}

type Update struct {
	Label    string
	RPM      int
	ProxyURL *string
	Secret   *string
}

func NewManager(scheduler *Scheduler, repository Repository, providerRPM map[string]int, builtins []domain.Key) (*Manager, error) {
	if scheduler == nil || repository == nil || len(providerRPM) == 0 {
		return nil, errors.New("key manager dependencies are invalid")
	}
	manager := &Manager{
		scheduler: scheduler, repository: repository,
		providerRPM: cloneRates(providerRPM), builtins: slices.Clone(builtins),
	}
	if err := manager.apply(nil); err != nil {
		return nil, err
	}
	return manager, nil
}

func (manager *Manager) Load(ctx context.Context) error {
	keys, err := manager.repository.Load(ctx)
	if err != nil {
		return err
	}
	loadedBuiltins := slices.Clone(manager.builtins)
	loadedUsers := make([]domain.Key, 0, len(keys))
	for _, key := range keys {
		if _, exists := manager.providerRPM[key.ProviderID]; !exists {
			return ErrUnknownProvider
		}
		if key.Pinned {
			for index := range loadedBuiltins {
				if loadedBuiltins[index].ProviderID == key.ProviderID {
					loadedBuiltins[index].Label = key.Label
					loadedBuiltins[index].RPM = key.RPM
					loadedBuiltins[index].Priority = key.Priority
					break
				}
			}
			// A persisted pinned row carries metadata only. Without the current
			// environment credential it must never become a usable key.
			continue
		}
		loadedUsers = append(loadedUsers, key)
	}
	manager.opMu.Lock()
	defer manager.opMu.Unlock()
	manager.mu.Lock()
	manager.builtins = loadedBuiltins
	manager.userKeys = loadedUsers
	manager.mu.Unlock()
	return manager.apply(loadedUsers)
}

func (manager *Manager) List(providerID string) []domain.PublicKey {
	return manager.scheduler.Snapshot(providerID)
}

func (manager *Manager) Count(providerID string) int {
	return manager.scheduler.Count(providerID)
}

func (manager *Manager) SensitiveValues(providerID string) []string {
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	values := make([]string, 0)
	for _, key := range manager.combinedLocked(manager.userKeys) {
		if key.ProviderID != providerID {
			continue
		}
		values = append(values, key.Credential.Reveal())
		if key.ProxyURL != "" {
			values = append(values, key.ProxyURL)
			if proxy, err := url.Parse(key.ProxyURL); err == nil {
				values = append(values, proxy.Hostname())
				if proxy.User != nil {
					values = append(values, proxy.User.Username())
					if password, configured := proxy.User.Password(); configured {
						values = append(values, password)
					}
				}
			}
		}
	}
	return values
}

func (manager *Manager) EnsureProvider(providerID string, rpm int) error {
	if providerID == "" || rpm < 0 {
		return ErrUnknownProvider
	}
	manager.opMu.Lock()
	defer manager.opMu.Unlock()
	manager.mu.Lock()
	manager.providerRPM[providerID] = rpm
	manager.mu.Unlock()
	return manager.apply(nil)
}

func (manager *Manager) RemoveProvider(providerID string) error {
	manager.opMu.Lock()
	defer manager.opMu.Unlock()
	manager.mu.RLock()
	all := manager.combinedLocked(manager.userKeys)
	manager.mu.RUnlock()
	for _, key := range all {
		if key.ProviderID == providerID {
			return ErrProviderHasKeys
		}
	}
	manager.mu.Lock()
	if _, exists := manager.providerRPM[providerID]; !exists {
		manager.mu.Unlock()
		return ErrUnknownProvider
	}
	delete(manager.providerRPM, providerID)
	manager.mu.Unlock()
	manager.scheduler.RemoveProvider(providerID)
	return nil
}

func (manager *Manager) Add(ctx context.Context, params domain.Params) (domain.PublicKey, error) {
	manager.opMu.Lock()
	defer manager.opMu.Unlock()
	if _, exists := manager.providerRPM[params.ProviderID]; !exists {
		return domain.PublicKey{}, ErrUnknownProvider
	}
	params.Pinned = false
	manager.mu.RLock()
	candidate := slices.Clone(manager.userKeys)
	all := manager.combinedLocked(candidate)
	manager.mu.RUnlock()
	params.Priority = nextPriority(all, params.ProviderID)
	key, err := domain.NewKey(params)
	if err != nil {
		return domain.PublicKey{}, err
	}
	if containsKey(all, key.ID) {
		return domain.PublicKey{}, ErrDuplicateKey
	}
	candidate = append(candidate, key)
	if err := manager.persistAndApply(ctx, candidate); err != nil {
		return domain.PublicKey{}, err
	}
	return publicByID(manager.scheduler.Snapshot(key.ProviderID), key.ID)
}

func (manager *Manager) Update(ctx context.Context, providerID, keyID string, update Update) (domain.PublicKey, error) {
	manager.opMu.Lock()
	defer manager.opMu.Unlock()
	if update.RPM < 0 || strings.TrimSpace(update.Label) == "" {
		return domain.PublicKey{}, errors.New("invalid key update")
	}
	manager.mu.RLock()
	all := manager.combinedLocked(manager.userKeys)
	candidate := slices.Clone(manager.userKeys)
	builtinCandidate := slices.Clone(manager.builtins)
	manager.mu.RUnlock()
	current, pinned := keyByID(all, keyID)
	if current == nil || current.ProviderID != providerID {
		return domain.PublicKey{}, ErrKeyNotFound
	}
	proxyURL := current.ProxyURL
	if update.ProxyURL != nil {
		if pinned && strings.TrimSpace(*update.ProxyURL) != current.ProxyURL {
			return domain.PublicKey{}, ErrPinnedKey
		}
		proxyURL = strings.TrimSpace(*update.ProxyURL)
	}
	secret := current.Credential.Reveal()
	if update.Secret != nil {
		if pinned {
			return domain.PublicKey{}, ErrPinnedKey
		}
		secret = *update.Secret
	}
	updated, err := domain.NewKey(domain.Params{
		ProviderID: providerID, Label: update.Label, Secret: secret,
		Priority: current.Priority, RPM: update.RPM,
		Pinned: current.Pinned, ProxyURL: proxyURL,
	})
	if err != nil {
		return domain.PublicKey{}, err
	}
	if pinned {
		for index := range builtinCandidate {
			if builtinCandidate[index].ID == keyID {
				builtinCandidate[index] = updated
				break
			}
		}
		if err := manager.persistState(ctx, builtinCandidate, candidate); err != nil {
			return domain.PublicKey{}, err
		}
		return publicByID(manager.scheduler.Snapshot(providerID), updated.ID)
	}
	for index := range candidate {
		if candidate[index].ID == keyID {
			candidate[index] = updated
			break
		}
	}
	if updated.ID != keyID && containsKey(all, updated.ID) {
		return domain.PublicKey{}, ErrDuplicateKey
	}
	if err := manager.persistAndApply(ctx, candidate); err != nil {
		return domain.PublicKey{}, err
	}
	return publicByID(manager.scheduler.Snapshot(providerID), updated.ID)
}

func (manager *Manager) Remove(ctx context.Context, providerID, keyID string) error {
	manager.opMu.Lock()
	defer manager.opMu.Unlock()
	manager.mu.RLock()
	all := manager.combinedLocked(manager.userKeys)
	candidate := slices.Clone(manager.userKeys)
	manager.mu.RUnlock()
	key, pinned := keyByID(all, keyID)
	if key == nil || key.ProviderID != providerID {
		return ErrKeyNotFound
	}
	if pinned {
		return ErrPinnedKey
	}
	for index := range candidate {
		if candidate[index].ID == keyID {
			candidate = append(candidate[:index], candidate[index+1:]...)
			return manager.persistAndApply(ctx, candidate)
		}
	}
	return ErrKeyNotFound
}

func (manager *Manager) Move(ctx context.Context, providerID, keyID string, direction int) error {
	manager.opMu.Lock()
	defer manager.opMu.Unlock()
	if direction != -1 && direction != 1 {
		return errors.New("invalid move direction")
	}
	manager.mu.RLock()
	candidate := slices.Clone(manager.userKeys)
	manager.mu.RUnlock()
	indices := providerIndices(candidate, providerID)
	position := slices.IndexFunc(indices, func(index int) bool { return candidate[index].ID == keyID })
	if position < 0 {
		if _, pinned := keyByID(manager.builtins, keyID); pinned {
			return ErrPinnedKey
		}
		return ErrKeyNotFound
	}
	target := position + direction
	if target < 0 || target >= len(indices) {
		return nil
	}
	left, right := indices[position], indices[target]
	candidate[left], candidate[right] = candidate[right], candidate[left]
	manager.normalizePriorities(candidate, providerID)
	return manager.persistAndApply(ctx, candidate)
}

func (manager *Manager) Reset(providerID, keyID string) error {
	if !manager.scheduler.Reset(providerID, keyID) {
		return ErrKeyNotFound
	}
	return nil
}

func (manager *Manager) persistAndApply(ctx context.Context, candidate []domain.Key) error {
	manager.mu.RLock()
	builtins := slices.Clone(manager.builtins)
	manager.mu.RUnlock()
	return manager.persistState(ctx, builtins, candidate)
}

func (manager *Manager) persistState(ctx context.Context, builtins, candidate []domain.Key) error {
	persisted := append(persistedBuiltinMetadata(builtins), candidate...)
	if err := manager.repository.Save(ctx, persisted); err != nil {
		return errors.New("key settings could not be saved")
	}
	manager.mu.Lock()
	manager.builtins = slices.Clone(builtins)
	manager.userKeys = slices.Clone(candidate)
	manager.mu.Unlock()
	return manager.apply(candidate)
}

func persistedBuiltinMetadata(keys []domain.Key) []domain.Key {
	result := slices.Clone(keys)
	placeholder, _ := domain.NewCredential("environment-managed")
	for index := range result {
		result[index].Credential = placeholder
		result[index].ProxyURL = ""
	}
	return result
}

func (manager *Manager) apply(candidate []domain.Key) error {
	manager.mu.RLock()
	if candidate == nil {
		candidate = manager.userKeys
	}
	all := manager.combinedLocked(candidate)
	rates := cloneRates(manager.providerRPM)
	manager.mu.RUnlock()
	for providerID, rpm := range rates {
		keys := make([]domain.Key, 0)
		for _, key := range all {
			if key.ProviderID == providerID {
				keys = append(keys, key)
			}
		}
		if err := manager.scheduler.Configure(providerID, rpm, keys); err != nil {
			return err
		}
	}
	return nil
}

func (manager *Manager) combinedLocked(user []domain.Key) []domain.Key {
	all := append(slices.Clone(manager.builtins), user...)
	sort.SliceStable(all, func(left, right int) bool {
		if all[left].ProviderID != all[right].ProviderID {
			return all[left].ProviderID < all[right].ProviderID
		}
		return all[left].Priority < all[right].Priority
	})
	return all
}

func (manager *Manager) normalizePriorities(keys []domain.Key, providerID string) {
	priority := 1
	for index := range keys {
		if keys[index].ProviderID == providerID {
			keys[index].Priority = priority
			priority++
		}
	}
}

func providerIndices(keys []domain.Key, providerID string) []int {
	result := make([]int, 0)
	for index, key := range keys {
		if key.ProviderID == providerID {
			result = append(result, index)
		}
	}
	return result
}

func keyByID(keys []domain.Key, id string) (*domain.Key, bool) {
	for index := range keys {
		if keys[index].ID == id {
			return &keys[index], keys[index].Pinned
		}
	}
	return nil, false
}

func containsKey(keys []domain.Key, id string) bool {
	key, _ := keyByID(keys, id)
	return key != nil
}

func nextPriority(keys []domain.Key, providerID string) int {
	priority := 0
	for _, key := range keys {
		if key.ProviderID == providerID {
			priority = max(priority, key.Priority+1)
		}
	}
	return priority
}

func publicByID(keys []domain.PublicKey, id string) (domain.PublicKey, error) {
	for _, key := range keys {
		if key.ID == id {
			return key, nil
		}
	}
	return domain.PublicKey{}, ErrKeyNotFound
}

func cloneRates(source map[string]int) map[string]int {
	result := make(map[string]int, len(source))
	for id, rpm := range source {
		result[id] = rpm
	}
	return result
}
