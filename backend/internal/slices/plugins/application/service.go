package application

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/plugins/domain"
)

var (
	ErrNotFound    = errors.New("plugin is not installed")
	ErrLimit       = fmt.Errorf("at most %d plugins can be installed", domain.MaxPlugins)
	ErrTooLarge    = errors.New("plugin is too large")
	ErrStorageFull = domain.ErrStorageFull
	// ErrStoreUnavailable names every command refused because the plugins file
	// could not be read: an empty list would claim the owner has no plugins,
	// and a save would replace the file the owner's plugins are still in.
	ErrStoreUnavailable = errors.New("plugin storage could not be read")
	// ErrCapacity is returned by a Repository whose encoded document would
	// exceed what it can protect. Nothing was written.
	ErrCapacity = errors.New("plugin storage file is full")
)

type Repository interface {
	// Load returns every saved plugin; a missing file is an empty list.
	Load(context.Context) ([]domain.Plugin, error)
	// Save replaces every saved plugin atomically, or returns ErrCapacity
	// (possibly wrapped) when the document would be too large to protect.
	Save(context.Context, []domain.Plugin) error
}

// Summary is a plugin's metadata, without the code and styles it carries.
type Summary struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Version     string   `json:"version"`
	Author      string   `json:"author"`
	Description string   `json:"description"`
	Permissions []string `json:"permissions"`
	Granted     []string `json:"granted"`
	Enabled     bool     `json:"enabled"`
	InstalledAt string   `json:"installedAt"`
	UpdatedAt   string   `json:"updatedAt"`
	SizeBytes   int      `json:"sizeBytes"`
	HasScript   bool     `json:"hasScript"`
	HasCSS      bool     `json:"hasCss"`
	HasTheme    bool     `json:"hasTheme"`
}

// Detail is the whole of one plugin, as plugins.get answers it.
type Detail struct {
	Plugin Summary       `json:"plugin"`
	Script string        `json:"script"`
	CSS    string        `json:"css"`
	Theme  *domain.Theme `json:"theme"`
}

// Values is a plugin's storage, as the storage commands answer it.
type Values struct {
	Values map[string]string `json:"values"`
}

// installed is a plugin with its encoded package size, measured once.
type installed struct {
	plugin    domain.Plugin
	sizeBytes int
}

type Service struct {
	// opMu serializes mutations end to end: build, persist, swap.
	opMu sync.Mutex
	// mu guards the current state. Every mutation replaces the map and the
	// changed record, never edits them in place, so readers can hold a
	// record after unlocking.
	mu         sync.RWMutex
	plugins    map[string]installed
	loadErr    error
	repository Repository
	// budget is the largest encoded Detail and Values a plugin may have: the
	// response that carries either has to fit the transport.
	budget int
	now    func() time.Time
	// changed hears the new list after every install, update and removal.
	// It runs under opMu, so notifications arrive in mutation order and the
	// last one always describes the current state.
	changed func([]Summary)
}

// NewService creates the service. budget bounds the encoded plugins.get and
// storage answers; the composition root passes the transport's payload limit.
func NewService(repository Repository, budget int) (*Service, error) {
	if repository == nil {
		return nil, errors.New("plugin repository is required")
	}
	if budget < 1 {
		return nil, errors.New("plugin response budget is required")
	}
	return &Service{repository: repository, budget: budget, now: time.Now, plugins: map[string]installed{}}, nil
}

// OnChanged sets the listener told the new list after every install, update
// and removal; storage writes are not changes to the list. Set before serving.
func (service *Service) OnChanged(listener func([]Summary)) {
	service.opMu.Lock()
	service.changed = listener
	service.opMu.Unlock()
}

// Load restores the saved plugins. A failure is remembered and every command
// refuses with ErrStoreUnavailable until a restart reads the file.
func (service *Service) Load(ctx context.Context) error {
	service.opMu.Lock()
	defer service.opMu.Unlock()
	restored, err := service.repository.Load(ctx)
	plugins := map[string]installed{}
	if err == nil {
		plugins, err = service.restore(restored)
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	service.loadErr = err
	if err == nil {
		service.plugins = plugins
	}
	return err
}

func (service *Service) restore(restored []domain.Plugin) (map[string]installed, error) {
	if len(restored) > domain.MaxPlugins {
		return nil, errors.New("stored plugins exceed the plugin limit")
	}
	plugins := make(map[string]installed, len(restored))
	for _, plugin := range restored {
		plugin.Package = plugin.Package.Normalized()
		plugin.Granted = append([]string{}, plugin.Granted...)
		plugin.Storage = maps.Clone(plugin.Storage)
		if plugin.Storage == nil {
			plugin.Storage = map[string]string{}
		}
		if err := plugin.Validate(); err != nil {
			return nil, fmt.Errorf("a stored plugin is invalid: %w", err)
		}
		if _, duplicate := plugins[plugin.Package.ID]; duplicate {
			return nil, errors.New("stored plugins repeat an id")
		}
		size, err := encodedSize(plugin.Package)
		if err != nil {
			return nil, err
		}
		plugins[plugin.Package.ID] = installed{plugin: plugin, sizeBytes: size}
	}
	return plugins, nil
}

// List returns every plugin's metadata, by name then id.
func (service *Service) List() ([]Summary, error) {
	service.mu.RLock()
	defer service.mu.RUnlock()
	if service.loadErr != nil {
		return nil, ErrStoreUnavailable
	}
	return summaries(service.plugins), nil
}

func (service *Service) Get(id string) (Detail, error) {
	entry, err := service.lookup(id)
	if err != nil {
		return Detail{}, err
	}
	return detail(entry), nil
}

func (service *Service) StorageGet(id string) (Values, error) {
	entry, err := service.lookup(id)
	if err != nil {
		return Values{}, err
	}
	return Values{Values: maps.Clone(entry.plugin.Storage)}, nil
}

// Install adds pkg, or upgrades the installed plugin with its id: an upgrade
// keeps the plugin's storage and install time, enables it and replaces the
// grant.
func (service *Service) Install(ctx context.Context, pkg domain.Package, granted []string) (Summary, error) {
	pkg = pkg.Normalized()
	if err := pkg.Validate(); err != nil {
		return Summary{}, err
	}
	granted, err := pkg.Grant(granted)
	if err != nil {
		return Summary{}, err
	}
	size, err := encodedSize(pkg)
	if err != nil {
		return Summary{}, err
	}
	service.opMu.Lock()
	defer service.opMu.Unlock()
	current, err := service.current()
	if err != nil {
		return Summary{}, err
	}
	now := service.timestamp()
	plugin := domain.Plugin{Package: pkg, Enabled: true, Granted: granted, InstalledAt: now, UpdatedAt: now, Storage: map[string]string{}}
	if previous, exists := current[pkg.ID]; exists {
		plugin.InstalledAt = previous.plugin.InstalledAt
		plugin.Storage = previous.plugin.Storage
		if plugin.UpdatedAt.Before(plugin.InstalledAt) {
			plugin.UpdatedAt = plugin.InstalledAt
		}
	} else if len(current) >= domain.MaxPlugins {
		return Summary{}, ErrLimit
	}
	entry := installed{plugin: plugin, sizeBytes: size}
	if fits, err := service.fits(detail(entry)); err != nil || !fits {
		return Summary{}, errors.Join(ErrTooLarge, err)
	}
	if err := service.commit(ctx, current, entry, ErrTooLarge); err != nil {
		return Summary{}, err
	}
	return summary(entry), nil
}

// Update changes whether a plugin is enabled and what it is granted. A nil
// field is left as it is.
func (service *Service) Update(ctx context.Context, id string, enabled *bool, granted *[]string) (Summary, error) {
	service.opMu.Lock()
	defer service.opMu.Unlock()
	current, err := service.current()
	if err != nil {
		return Summary{}, err
	}
	entry, exists := current[id]
	if !exists {
		return Summary{}, ErrNotFound
	}
	if enabled != nil {
		entry.plugin.Enabled = *enabled
	}
	if granted != nil {
		ordered, err := entry.plugin.Package.Grant(*granted)
		if err != nil {
			return Summary{}, err
		}
		entry.plugin.Granted = ordered
	}
	if fits, err := service.fits(detail(entry)); err != nil || !fits {
		return Summary{}, errors.Join(ErrTooLarge, err)
	}
	if err := service.commit(ctx, current, entry, ErrTooLarge); err != nil {
		return Summary{}, err
	}
	return summary(entry), nil
}

func (service *Service) Remove(ctx context.Context, id string) error {
	service.opMu.Lock()
	defer service.opMu.Unlock()
	current, err := service.current()
	if err != nil {
		return err
	}
	if _, exists := current[id]; !exists {
		return ErrNotFound
	}
	next := maps.Clone(current)
	delete(next, id)
	if err := service.persist(ctx, next, ErrTooLarge); err != nil {
		return err
	}
	service.notify(next)
	return nil
}

// StorageSet sets key, or deletes it when value is nil, and returns the
// plugin's storage afterwards.
func (service *Service) StorageSet(ctx context.Context, id, key string, value *string) (Values, error) {
	service.opMu.Lock()
	defer service.opMu.Unlock()
	current, err := service.current()
	if err != nil {
		return Values{}, err
	}
	entry, exists := current[id]
	if !exists {
		return Values{}, ErrNotFound
	}
	values, err := domain.WithStorage(entry.plugin.Storage, key, value)
	if err != nil {
		return Values{}, err
	}
	answer := Values{Values: values}
	if fits, err := service.fits(answer); err != nil || !fits {
		return Values{}, errors.Join(ErrStorageFull, err)
	}
	entry.plugin.Storage = values
	next := maps.Clone(current)
	next[id] = entry
	if err := service.persist(ctx, next, ErrStorageFull); err != nil {
		return Values{}, err
	}
	return Values{Values: maps.Clone(values)}, nil
}

func (service *Service) lookup(id string) (installed, error) {
	service.mu.RLock()
	defer service.mu.RUnlock()
	if service.loadErr != nil {
		return installed{}, ErrStoreUnavailable
	}
	entry, exists := service.plugins[id]
	if !exists {
		return installed{}, ErrNotFound
	}
	return entry, nil
}

// current returns the live map for a mutation to copy. Callers hold opMu, so
// nothing replaces it underneath them.
func (service *Service) current() (map[string]installed, error) {
	service.mu.RLock()
	defer service.mu.RUnlock()
	if service.loadErr != nil {
		return nil, ErrStoreUnavailable
	}
	return service.plugins, nil
}

func (service *Service) commit(ctx context.Context, current map[string]installed, entry installed, full error) error {
	next := maps.Clone(current)
	next[entry.plugin.Package.ID] = entry
	if err := service.persist(ctx, next, full); err != nil {
		return err
	}
	service.notify(next)
	return nil
}

func (service *Service) notify(plugins map[string]installed) {
	if service.changed != nil {
		service.changed(summaries(plugins))
	}
}

// persist saves next and only then makes it current, so a failed save leaves
// both the file and memory as they were. full is the error a full document
// reports as.
func (service *Service) persist(ctx context.Context, next map[string]installed, full error) error {
	plugins := make([]domain.Plugin, 0, len(next))
	for _, id := range slices.Sorted(maps.Keys(next)) {
		plugins = append(plugins, next[id].plugin)
	}
	if err := service.repository.Save(ctx, plugins); err != nil {
		if errors.Is(err, ErrCapacity) {
			return errors.Join(full, err)
		}
		return fmt.Errorf("plugins could not be saved: %w", err)
	}
	service.mu.Lock()
	service.plugins = next
	service.mu.Unlock()
	return nil
}

func (service *Service) fits(answer any) (bool, error) {
	size, err := encodedSize(answer)
	return err == nil && size <= service.budget, err
}

func (service *Service) timestamp() time.Time {
	return service.now().UTC().Truncate(time.Second)
}

func summaries(plugins map[string]installed) []Summary {
	list := make([]Summary, 0, len(plugins))
	for _, entry := range plugins {
		list = append(list, summary(entry))
	}
	slices.SortFunc(list, func(left, right Summary) int {
		return cmp.Or(cmp.Compare(left.Name, right.Name), cmp.Compare(left.ID, right.ID))
	})
	return list
}

func summary(entry installed) Summary {
	pkg := entry.plugin.Package
	return Summary{
		ID:          pkg.ID,
		Name:        pkg.Name,
		Version:     pkg.Version,
		Author:      pkg.Author,
		Description: pkg.Description,
		Permissions: slices.Clone(pkg.Permissions),
		Granted:     append([]string{}, entry.plugin.Granted...),
		Enabled:     entry.plugin.Enabled,
		InstalledAt: entry.plugin.InstalledAt.UTC().Format(time.RFC3339),
		UpdatedAt:   entry.plugin.UpdatedAt.UTC().Format(time.RFC3339),
		SizeBytes:   entry.sizeBytes,
		HasScript:   pkg.Script != "",
		HasCSS:      pkg.CSS != "",
		HasTheme:    pkg.Theme != nil,
	}
}

func detail(entry installed) Detail {
	pkg := entry.plugin.Package
	var theme *domain.Theme
	if pkg.Theme != nil {
		copied := *pkg.Theme
		copied.Tokens = maps.Clone(copied.Tokens)
		theme = &copied
	}
	return Detail{Plugin: summary(entry), Script: pkg.Script, CSS: pkg.CSS, Theme: theme}
}

// encodedSize measures value exactly as the stdio server writes it: compact,
// without HTML escaping, without the encoder's trailing newline.
func encodedSize(value any) (int, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return 0, err
	}
	return buffer.Len() - 1, nil
}
