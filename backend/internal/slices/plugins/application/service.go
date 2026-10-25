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
	// ErrBroken refuses every command but install and removal on a stored
	// plugin this version cannot use; its summary's Problem says why.
	ErrBroken = errors.New("plugin cannot be used by this version")
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
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Version       string   `json:"version"`
	Author        string   `json:"author"`
	Description   string   `json:"description"`
	Homepage      string   `json:"homepage"`
	MinAppVersion string   `json:"minAppVersion"`
	Permissions   []string `json:"permissions"`
	Granted       []string `json:"granted"`
	Enabled       bool     `json:"enabled"`
	// InstalledAt and UpdatedAt carry milliseconds: UpdatedAt changes on
	// every install, so the shell can use it as the code's revision.
	InstalledAt string `json:"installedAt"`
	UpdatedAt   string `json:"updatedAt"`
	SizeBytes   int    `json:"sizeBytes"`
	HasScript   bool   `json:"hasScript"`
	HasCSS      bool   `json:"hasCss"`
	HasTheme    bool   `json:"hasTheme"`
	HasSettings bool   `json:"hasSettings"`
	// Problem is empty for a usable plugin. Otherwise it tells the owner why
	// this version refuses the stored plugin and what to do; such a plugin
	// is listed disabled and can only be reinstalled or removed.
	Problem string `json:"problem"`
	// Secrets names the secret settings that have a value, in declared
	// order. Values never leave the control plane.
	Secrets []string `json:"secrets"`
	// Hosts is the package's network allowlist, empty without net:fetch, so
	// an update review can tell when it reaches somewhere new.
	Hosts []string `json:"hosts"`
	// Dev is set while the plugin is loaded from a source folder.
	Dev *domain.DevLink `json:"dev,omitempty"`
}

// Detail is the whole of one plugin, as plugins.get answers it.
type Detail struct {
	Plugin   Summary          `json:"plugin"`
	Script   string           `json:"script"`
	CSS      string           `json:"css"`
	Theme    *domain.Theme    `json:"theme"`
	Settings []domain.Setting `json:"settings"`
}

// Values is a plugin's storage, as the storage commands answer it.
type Values struct {
	Values map[string]string `json:"values"`
}

// installed is a plugin with its encoded package size, measured once.
type installed struct {
	plugin    domain.Plugin
	sizeBytes int
	// problem is why this version refuses a stored record, empty when it is
	// usable. A refused record's plugin is kept exactly as it was loaded so
	// every save writes it back unchanged until it is reinstalled or removed.
	problem string
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
	// appVersion is the running release, checked against each package's
	// minimum.
	appVersion string
	now        func() time.Time
	// changed hears the new list after every install, update and removal.
	// It runs under opMu, so notifications arrive in mutation order and the
	// last one always describes the current state.
	changed func([]Summary)
	// removed hears each plugin id that stopped being installed, after the
	// removal was saved. It runs under opMu, outside mu.
	removed []func(id string)
}

// NewService creates the service. budget bounds the encoded plugins.get and
// storage answers; the composition root passes the transport's payload limit
// and the running app version.
func NewService(repository Repository, budget int, appVersion string) (*Service, error) {
	if repository == nil {
		return nil, errors.New("plugin repository is required")
	}
	if budget < 1 {
		return nil, errors.New("plugin response budget is required")
	}
	if !domain.ValidAppVersion(appVersion) {
		return nil, errors.New("plugin service needs the running app version")
	}
	return &Service{repository: repository, budget: budget, appVersion: appVersion, now: time.Now, plugins: map[string]installed{}}, nil
}

// OnChanged sets the listener told the new list after every install, update
// and removal; storage writes are not changes to the list. Set before serving.
func (service *Service) OnChanged(listener func([]Summary)) {
	service.opMu.Lock()
	service.changed = listener
	service.opMu.Unlock()
}

// OnRemoved adds a listener told the id of every plugin once its removal is
// saved, so other slices can drop what that plugin registered. Listeners run
// in the order added, synchronously, under the service's mutation lock: they
// must not call back into this service. Set before serving.
func (service *Service) OnRemoved(listener func(id string)) {
	service.opMu.Lock()
	service.removed = append(service.removed, listener)
	service.opMu.Unlock()
}

// Granted reports whether plugin id is installed, usable, enabled and holds
// permission. Other slices ask it before acting on a plugin's behalf.
func (service *Service) Granted(id, permission string) bool {
	service.mu.RLock()
	defer service.mu.RUnlock()
	if service.loadErr != nil {
		return false
	}
	entry, exists := service.plugins[id]
	return exists && entry.problem == "" && entry.plugin.Enabled && slices.Contains(entry.plugin.Granted, permission)
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

// restore checks every stored record. One that this version refuses is kept
// as a broken entry rather than failing the store, so one stale plugin does
// not hide the others. The store as a whole still fails when it cannot be
// one set of addressable plugins: too many records, a repeated id, or an id
// no command could name.
func (service *Service) restore(restored []domain.Plugin) (map[string]installed, error) {
	if len(restored) > domain.MaxPlugins {
		return nil, errors.New("stored plugins exceed the plugin limit")
	}
	plugins := make(map[string]installed, len(restored))
	for _, record := range restored {
		id := record.Package.ID
		if !domain.ValidID(id) {
			return nil, errors.New("a stored plugin has no usable id")
		}
		if _, duplicate := plugins[id]; duplicate {
			return nil, errors.New("stored plugins repeat an id")
		}
		entry, err := service.restoreOne(record)
		if err != nil {
			return nil, err
		}
		plugins[id] = entry
	}
	return plugins, nil
}

func (service *Service) restoreOne(record domain.Plugin) (installed, error) {
	plugin := record
	plugin.Package = record.Package.Normalized()
	plugin.Granted = append([]string{}, record.Granted...)
	plugin.Storage = maps.Clone(record.Storage)
	if plugin.Storage == nil {
		plugin.Storage = map[string]string{}
	}
	plugin.Secrets = maps.Clone(record.Secrets)
	if plugin.Dev != nil {
		link := *record.Dev
		plugin.Dev = &link
	}
	size, sizeErr := encodedSize(plugin.Package)
	broken := func(problem string) installed {
		if sizeErr != nil {
			size = 0
		}
		return installed{plugin: record, sizeBytes: size, problem: problem}
	}
	if err := plugin.Validate(); err != nil {
		// Validation messages name the field and the rule, never the value.
		return broken(fmt.Sprintf("This plugin no longer passes this version's checks (%v). Reinstall a fixed version or remove it.", err)), nil
	}
	if plugin.Package.NeedsNewerApp(service.appVersion) {
		return broken(fmt.Sprintf("Needs Luxury Switchboard %s or later; this is %s. Update the app or install an older version of the plugin.", plugin.Package.MinAppVersion, service.appVersion)), nil
	}
	if sizeErr != nil {
		return installed{}, sizeErr
	}
	return installed{plugin: plugin, sizeBytes: size}, nil
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
// keeps the plugin's storage, the secrets the new version still declares and
// the install time, enables it and replaces the grant. A manual install ends
// a dev link. Installing over a broken entry replaces it, keeping only the
// stored install time and storage that still pass.
func (service *Service) Install(ctx context.Context, pkg domain.Package, granted []string) (Summary, error) {
	pkg = pkg.Normalized()
	if err := pkg.Validate(); err != nil {
		return Summary{}, err
	}
	if pkg.NeedsNewerApp(service.appVersion) {
		// The version was validated above, so it is safe to repeat.
		return Summary{}, domain.ValidationError{Field: "minAppVersion", Reason: "needs Luxury Switchboard " + pkg.MinAppVersion + " or later"}
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
		plugin.InstalledAt, plugin.UpdatedAt, plugin.Storage = upgraded(previous, now)
		plugin.Secrets = domain.KeptSecrets(pkg.Settings, previous.plugin.Secrets)
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

// upgraded carries an installed plugin's install time and storage into its
// replacement. The update time always moves forward, even within one
// millisecond or after the clock went back, so every install is a new
// revision. A broken record's values are carried only where they still pass.
func upgraded(previous installed, now time.Time) (installedAt, updatedAt time.Time, storage map[string]string) {
	old := previous.plugin
	if previous.problem != "" {
		installedAt, storage = now, map[string]string{}
		if !old.InstalledAt.IsZero() && !old.InstalledAt.After(now) {
			installedAt = old.InstalledAt
		}
		if len(old.Storage) > 0 && domain.CheckStorage(old.Storage) == nil {
			storage = maps.Clone(old.Storage)
		}
		return installedAt, now, storage
	}
	updatedAt = now
	if !updatedAt.After(old.UpdatedAt) {
		updatedAt = old.UpdatedAt.Add(time.Millisecond)
	}
	return old.InstalledAt, updatedAt, old.Storage
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
	if entry.problem != "" {
		return Summary{}, ErrBroken
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
	for _, listener := range service.removed {
		listener(id)
	}
	return nil
}

// SetSecret sets the secret setting key, or clears it when value is nil, and
// returns the names that have a value afterwards. The value is never
// returned; a change is announced so the shell can show "Set" or "Not set".
func (service *Service) SetSecret(ctx context.Context, id, key string, value *string) ([]string, error) {
	service.opMu.Lock()
	defer service.opMu.Unlock()
	current, err := service.current()
	if err != nil {
		return nil, err
	}
	entry, exists := current[id]
	if !exists {
		return nil, ErrNotFound
	}
	if entry.problem != "" {
		return nil, ErrBroken
	}
	secrets, err := domain.WithSecret(entry.plugin.Package.Settings, entry.plugin.Secrets, key, value)
	if err != nil {
		return nil, err
	}
	if !maps.Equal(secrets, entry.plugin.Secrets) {
		entry.plugin.Secrets = secrets
		if err := service.commit(ctx, current, entry, ErrStorageFull); err != nil {
			return nil, err
		}
	}
	return secretNames(entry.plugin), nil
}

// StorageSet sets key, or deletes it when value is nil, and returns the
// plugin's storage afterwards. A write that changes nothing is not saved.
func (service *Service) StorageSet(ctx context.Context, id, key string, value *string) (Values, error) {
	return service.writeStorage(ctx, id, func(values map[string]string) (map[string]string, error) {
		return domain.WithStorage(values, key, value)
	})
}

// StorageUpdate applies a batch of storage changes in one save, after first
// removing every key when clear is set, and returns the plugin's storage
// afterwards. Nothing changes unless the whole batch fits; a batch that
// changes nothing is not saved.
func (service *Service) StorageUpdate(ctx context.Context, id string, changes map[string]*string, clear bool) (Values, error) {
	return service.writeStorage(ctx, id, func(values map[string]string) (map[string]string, error) {
		return domain.WithStorageChanges(values, changes, clear)
	})
}

func (service *Service) writeStorage(ctx context.Context, id string, change func(map[string]string) (map[string]string, error)) (Values, error) {
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
	if entry.problem != "" {
		return Values{}, ErrBroken
	}
	values, err := change(entry.plugin.Storage)
	if err != nil {
		return Values{}, err
	}
	answer := Values{Values: values}
	if fits, err := service.fits(answer); err != nil || !fits {
		return Values{}, errors.Join(ErrStorageFull, err)
	}
	if maps.Equal(values, entry.plugin.Storage) {
		return answer, nil
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
	if entry.problem != "" {
		return installed{}, ErrBroken
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
	return service.now().UTC().Truncate(time.Millisecond)
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

func hosts(network *domain.Network) []string {
	if network == nil {
		return []string{}
	}
	return slices.Clone(network.Hosts)
}

func summary(entry installed) Summary {
	plugin := entry.plugin
	if entry.problem != "" {
		plugin = listed(plugin)
	}
	pkg := plugin.Package
	return Summary{
		ID:            pkg.ID,
		Name:          pkg.Name,
		Version:       pkg.Version,
		Author:        pkg.Author,
		Description:   pkg.Description,
		Homepage:      pkg.Homepage,
		MinAppVersion: pkg.MinAppVersion,
		Permissions:   append([]string{}, pkg.Permissions...),
		Granted:       append([]string{}, plugin.Granted...),
		Enabled:       plugin.Enabled && entry.problem == "",
		InstalledAt:   plugin.InstalledAt.UTC().Format(time.RFC3339Nano),
		UpdatedAt:     plugin.UpdatedAt.UTC().Format(time.RFC3339Nano),
		SizeBytes:     entry.sizeBytes,
		HasScript:     pkg.Script != "",
		HasCSS:        pkg.CSS != "",
		HasTheme:      pkg.Theme != nil,
		HasSettings:   len(pkg.Settings) > 0,
		Problem:       entry.problem,
		Secrets:       secretNames(plugin),
		Hosts:         hosts(pkg.Network),
		Dev:           devCopy(plugin.Dev),
	}
}

// secretNames lists the secret settings of plugin that have a value.
func secretNames(plugin domain.Plugin) []string {
	names := []string{}
	for _, key := range domain.SecretKeys(plugin.Package.Settings) {
		if plugin.Secrets[key] != "" {
			names = append(names, key)
		}
	}
	return names
}

func devCopy(link *domain.DevLink) *domain.DevLink {
	if link == nil {
		return nil
	}
	copied := *link
	copied.LoadedAt = copied.LoadedAt.UTC()
	return &copied
}

// listed is what of a refused record can be shown: fields that break their
// own rule are blank and the grant is kept only if it still fits.
func listed(record domain.Plugin) domain.Plugin {
	plugin := record
	plugin.Package = record.Package.Listed()
	granted, err := plugin.Package.Grant(record.Granted)
	if err != nil || !slices.Equal(granted, record.Granted) {
		granted = []string{}
	}
	plugin.Granted = granted
	return plugin
}

func detail(entry installed) Detail {
	pkg := entry.plugin.Package.Normalized()
	settings := pkg.Settings
	if settings == nil {
		settings = []domain.Setting{}
	}
	return Detail{Plugin: summary(entry), Script: pkg.Script, CSS: pkg.CSS, Theme: pkg.Theme, Settings: settings}
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
