package application

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/keypool/domain"
)

var (
	ErrKeyNotFound     = errors.New("key not found")
	ErrPinnedKey       = errors.New("pinned key cannot be changed this way")
	ErrDuplicateKey    = errors.New("key already exists")
	ErrUnknownProvider = errors.New("unknown provider")
	ErrProviderHasKeys = errors.New("provider still has keys")
	ErrImportTooLarge  = errors.New("import batch is too large")
	// ErrStoreUnavailable names a write refused because the store behind it
	// could not be read: saving the runtime pool over an unreadable file would
	// atomically replace it, and every key the user ever entered is gone after
	// the next start. Routes and providers made the same call first; this is
	// the same rule.
	ErrStoreUnavailable = errors.New("key storage could not be read; saving now would replace it")
)

// MaxImportBatch bounds one bulk import. This many realistic keys still fit the
// control-plane frame, so an oversized paste meets a readable limit here instead of
// a frame the transport drops without saying why.
const MaxImportBatch = 500

type Manager struct {
	opMu          sync.Mutex
	mu            sync.RWMutex
	scheduler     *Scheduler
	repository    Repository
	providerRates map[string]Rate
	builtins      []domain.Key
	userKeys      []domain.Key
	loadMu        sync.RWMutex
	loadErr       error
}

// Rate is a provider's request budget: how many requests fit in one window. A
// zero window means the scheduler's per-minute default.
type Rate struct {
	Limit  int
	Window time.Duration
}

type Update struct {
	Label    string
	RPM      int
	ProxyURL *string
	Secret   *string
}

// ImportEntry is one pasted key. Everything else about it comes from the batch.
type ImportEntry struct {
	Label  string
	Secret string
}

// Import adds several keys under one shared setting: the provider, the request limit
// and the proxy apply to every entry, and each entry carries only its own label and
// secret.
type Import struct {
	ProviderID string
	RPM        int
	ProxyURL   string
	Entries    []ImportEntry
}

// ImportReport says what became of each entry, by its position in the submitted
// batch. Positions rather than labels, so nothing a caller pasted travels back.
type ImportReport struct {
	Added     int   `json:"added"`
	Duplicate []int `json:"duplicate"`
	Rejected  []int `json:"rejected"`
}

func NewManager(scheduler *Scheduler, repository Repository, providerRates map[string]Rate, builtins []domain.Key) (*Manager, error) {
	if scheduler == nil || repository == nil || len(providerRates) == 0 {
		return nil, errors.New("key manager dependencies are invalid")
	}
	manager := &Manager{
		scheduler: scheduler, repository: repository,
		providerRates: cloneRates(providerRates), builtins: slices.Clone(builtins),
	}
	if err := manager.apply(nil); err != nil {
		return nil, err
	}
	return manager, nil
}

func (manager *Manager) Load(ctx context.Context) error {
	err := manager.load(ctx)
	manager.loadMu.Lock()
	manager.loadErr = err
	manager.loadMu.Unlock()
	return err
}

// Availability reports why the persisted key pool could not be read, or nil
// when it could. Mutations refuse while it is set: a save would replace the
// unreadable file with the runtime pool plus one edit.
func (manager *Manager) Availability() error {
	manager.loadMu.RLock()
	defer manager.loadMu.RUnlock()
	return manager.loadErr
}

func (manager *Manager) load(ctx context.Context) error {
	keys, err := manager.repository.Load(ctx)
	if err != nil {
		return err
	}
	loadedBuiltins := slices.Clone(manager.builtins)
	loadedUsers := make([]domain.Key, 0, len(keys))
	for _, key := range keys {
		if _, exists := manager.providerRates[key.ProviderID]; !exists {
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
	if duplicateKeyID(append(slices.Clone(loadedBuiltins), loadedUsers...)) {
		return ErrDuplicateKey
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

func (manager *Manager) EnsureProvider(providerID string, rpm int, window time.Duration) error {
	if providerID == "" || rpm < 0 || window < 0 {
		return ErrUnknownProvider
	}
	manager.opMu.Lock()
	defer manager.opMu.Unlock()
	manager.mu.Lock()
	manager.providerRates[providerID] = Rate{Limit: rpm, Window: window}
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
	if _, exists := manager.providerRates[providerID]; !exists {
		manager.mu.Unlock()
		return ErrUnknownProvider
	}
	delete(manager.providerRates, providerID)
	manager.mu.Unlock()
	manager.scheduler.RemoveProvider(providerID)
	return nil
}

func (manager *Manager) Add(ctx context.Context, params domain.Params) (domain.PublicKey, error) {
	manager.opMu.Lock()
	defer manager.opMu.Unlock()
	if err := manager.Availability(); err != nil {
		return domain.PublicKey{}, ErrStoreUnavailable
	}
	if _, exists := manager.providerRates[params.ProviderID]; !exists {
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

// AddMany imports a batch of keys in a single save.
//
// An entry whose secret is already configured for this provider is skipped instead
// of failing the import - pasting a list that overlaps the pool is the normal case,
// not a mistake - and so is an entry repeated inside the batch itself. An entry the
// domain refuses is skipped the same way, so one malformed line does not cost the
// caller the rest of the paste. Every skip is reported by position, so nothing is
// dropped quietly.
func (manager *Manager) AddMany(ctx context.Context, request Import) (ImportReport, error) {
	if len(request.Entries) == 0 {
		return ImportReport{}, errors.New("empty key import")
	}
	if len(request.Entries) > MaxImportBatch {
		return ImportReport{}, ErrImportTooLarge
	}
	manager.opMu.Lock()
	defer manager.opMu.Unlock()
	if err := manager.Availability(); err != nil {
		return ImportReport{}, ErrStoreUnavailable
	}
	if _, exists := manager.providerRates[request.ProviderID]; !exists {
		return ImportReport{}, ErrUnknownProvider
	}
	manager.mu.RLock()
	candidate := slices.Clone(manager.userKeys)
	all := manager.combinedLocked(candidate)
	manager.mu.RUnlock()
	priority := nextPriority(all, request.ProviderID)
	configured := make(map[string]struct{}, len(all)+len(request.Entries))
	for _, key := range all {
		configured[key.ID] = struct{}{}
	}
	report := ImportReport{Duplicate: []int{}, Rejected: []int{}}
	for position, entry := range request.Entries {
		key, err := domain.NewKey(domain.Params{
			ProviderID: request.ProviderID, Label: entry.Label, Secret: entry.Secret,
			Priority: priority, RPM: request.RPM, ProxyURL: request.ProxyURL,
		})
		if err != nil {
			report.Rejected = append(report.Rejected, position)
			continue
		}
		if _, duplicate := configured[key.ID]; duplicate {
			report.Duplicate = append(report.Duplicate, position)
			continue
		}
		configured[key.ID] = struct{}{}
		candidate = append(candidate, key)
		report.Added++
		priority++
	}
	// A batch that added nothing leaves the pool exactly as it was, so saving it
	// would rewrite the encrypted store and wake every listener for no change.
	if report.Added == 0 {
		return report, nil
	}
	if err := manager.persistAndApply(ctx, candidate); err != nil {
		return ImportReport{}, err
	}
	return report, nil
}

func (manager *Manager) Update(ctx context.Context, providerID, keyID string, update Update) (domain.PublicKey, error) {
	manager.opMu.Lock()
	defer manager.opMu.Unlock()
	if err := manager.Availability(); err != nil {
		return domain.PublicKey{}, ErrStoreUnavailable
	}
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
	if err := manager.Availability(); err != nil {
		return ErrStoreUnavailable
	}
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
	if err := manager.Availability(); err != nil {
		return ErrStoreUnavailable
	}
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
		return fmt.Errorf("key settings could not be saved: %w", err)
	}
	manager.mu.Lock()
	manager.builtins = slices.Clone(builtins)
	manager.userKeys = slices.Clone(candidate)
	manager.mu.Unlock()
	return manager.apply(candidate)
}

func persistedBuiltinMetadata(keys []domain.Key) []domain.Key {
	result := slices.Clone(keys)
	placeholder, _ := domain.NewCredential("managed-credential")
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
	rates := cloneRates(manager.providerRates)
	manager.mu.RUnlock()
	for providerID, rate := range rates {
		keys := make([]domain.Key, 0)
		for _, key := range all {
			if key.ProviderID == providerID {
				keys = append(keys, key)
			}
		}
		if err := manager.scheduler.Configure(providerID, rate.Limit, rate.Window, keys); err != nil {
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

func duplicateKeyID(keys []domain.Key) bool {
	seen := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		if _, duplicate := seen[key.ID]; duplicate {
			return true
		}
		seen[key.ID] = struct{}{}
	}
	return false
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

func cloneRates(source map[string]Rate) map[string]Rate {
	result := make(map[string]Rate, len(source))
	for id, rate := range source {
		result[id] = rate
	}
	return result
}
