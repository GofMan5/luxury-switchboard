package application

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/plugins/domain"
)

// DevPollInterval is how often linked folders are read again.
const DevPollInterval = time.Second

var (
	ErrDevLimit = fmt.Errorf("at most %d plugins can be loaded from folders", domain.MaxDevLinks)
	// ErrDevLinked refuses linking a folder that another plugin is already
	// loaded from.
	ErrDevLinked = errors.New("that folder is already linked to another plugin")
	// ErrNotLinked refuses unlinking a plugin that is not loaded from a
	// folder.
	ErrNotLinked = errors.New("plugin is not loaded from a folder")
)

// DevProblemError refuses a link whose folder does not hold a usable
// package. Problem is the owner's own folder's report, safe to show them.
type DevProblemError struct{ Problem string }

func (err DevProblemError) Error() string { return err.Problem }

// DevSource packs a plugin source folder. It returns the package JSON, nil
// when plugin.json could not be read as an object, and every problem found.
type DevSource interface {
	Pack(path string) (json.RawMessage, []string)
}

// DevLinks loads plugins from source folders and reloads them as their
// authors edit them.
type DevLinks struct {
	plugins *Service
	source  DevSource
}

func NewDevLinks(plugins *Service, source DevSource) (*DevLinks, error) {
	if plugins == nil || source == nil {
		return nil, errors.New("dev links need the plugins service and a folder source")
	}
	return &DevLinks{plugins: plugins, source: source}, nil
}

// Link loads the folder at path and installs or replaces the plugin it
// names. The folder must load cleanly the first time. Permissions the
// installed version was not granted arrive ungranted.
func (links *DevLinks) Link(ctx context.Context, path string) (Summary, error) {
	if !domain.ValidDevPath(path) {
		return Summary{}, domain.ValidationError{Field: "path", Reason: "must be an absolute folder path"}
	}
	pkg, problem := links.load(path)
	if problem != "" {
		return Summary{}, DevProblemError{Problem: problem}
	}
	service := links.plugins
	service.opMu.Lock()
	defer service.opMu.Unlock()
	current, err := service.current()
	if err != nil {
		return Summary{}, err
	}
	linked := 0
	for id, entry := range current {
		if entry.plugin.Dev == nil || id == pkg.ID {
			continue
		}
		if entry.plugin.Dev.Path == path {
			return Summary{}, ErrDevLinked
		}
		linked++
	}
	if linked >= domain.MaxDevLinks {
		return Summary{}, ErrDevLimit
	}
	previous, exists := current[pkg.ID]
	if !exists && len(current) >= domain.MaxPlugins {
		return Summary{}, ErrLimit
	}
	now := service.timestamp()
	plugin := domain.Plugin{Package: pkg, Enabled: true, Granted: []string{}, InstalledAt: now, UpdatedAt: now, Storage: map[string]string{}}
	if exists {
		plugin = reloaded(previous, pkg, now)
	}
	plugin.Dev = &domain.DevLink{Path: path, LoadedAt: now}
	return links.save(ctx, current, plugin)
}

// Unlink stops loading the plugin from its folder. The last loaded version
// stays installed as it is.
func (links *DevLinks) Unlink(ctx context.Context, id string) (Summary, error) {
	service := links.plugins
	service.opMu.Lock()
	defer service.opMu.Unlock()
	current, err := service.current()
	if err != nil {
		return Summary{}, err
	}
	entry, exists := current[id]
	switch {
	case !exists:
		return Summary{}, ErrNotFound
	case entry.plugin.Dev == nil:
		return Summary{}, ErrNotLinked
	}
	entry.plugin.Dev = nil
	return links.save(ctx, current, entry.plugin)
}

// Watch reloads every linked folder each DevPollInterval until ctx ends.
func (links *DevLinks) Watch(ctx context.Context) {
	ticker := time.NewTicker(DevPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			links.poll(ctx)
		}
	}
}

// poll reads every linked folder once. Folders are read without the
// mutation lock; each result is applied only if the link is still the one
// that was read.
func (links *DevLinks) poll(ctx context.Context) {
	service := links.plugins
	service.mu.RLock()
	folders := map[string]string{}
	if service.loadErr == nil {
		for id, entry := range service.plugins {
			if entry.plugin.Dev != nil {
				folders[id] = entry.plugin.Dev.Path
			}
		}
	}
	service.mu.RUnlock()
	for _, id := range slices.Sorted(maps.Keys(folders)) {
		if ctx.Err() != nil {
			return
		}
		pkg, problem := links.load(folders[id])
		if problem == "" && pkg.ID != id {
			problem = fmt.Sprintf("plugin.json now names %q; unlink this plugin and link the folder again to load it as another plugin.", pkg.ID)
		}
		links.apply(ctx, id, folders[id], pkg, problem)
	}
}

func (links *DevLinks) apply(ctx context.Context, id, path string, pkg domain.Package, problem string) {
	service := links.plugins
	service.opMu.Lock()
	defer service.opMu.Unlock()
	current, err := service.current()
	if err != nil {
		return
	}
	entry, exists := current[id]
	if !exists || entry.plugin.Dev == nil || entry.plugin.Dev.Path != path {
		return
	}
	plugin := entry.plugin
	link := *plugin.Dev
	changed := false
	if problem == "" && (entry.problem != "" || !samePackage(plugin.Package, pkg)) {
		candidate := reloaded(entry, pkg, service.timestamp())
		if _, err := links.answerable(candidate); err != nil {
			problem = "The changed plugin is too large to load."
		} else {
			plugin, changed = candidate, true
			link.LoadedAt = plugin.UpdatedAt
		}
	}
	if !changed && link.Problem == problem {
		return
	}
	link.Problem = problem
	plugin.Dev = &link
	// A failed save leaves the last good version; the next poll tries again.
	_, _ = links.save(ctx, current, plugin)
}

// load packs and checks the folder as an install would, returning the
// package or the problem that stops it.
func (links *DevLinks) load(path string) (domain.Package, string) {
	raw, problems := links.source.Pack(path)
	var pkg domain.Package
	if raw != nil {
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&pkg); err != nil {
			problems = append(problems, "plugin.json does not match the package format: "+err.Error())
		} else {
			pkg = pkg.Normalized()
			if err := pkg.Validate(); err != nil {
				problems = append(problems, "Plugin "+err.Error())
			} else if pkg.NeedsNewerApp(links.plugins.appVersion) {
				problems = append(problems, fmt.Sprintf("Needs Luxury Switchboard %s or later; this is %s.", pkg.MinAppVersion, links.plugins.appVersion))
			}
		}
	}
	if len(problems) > 0 {
		return domain.Package{}, domain.DevProblem(strings.Join(problems, "\n"))
	}
	return pkg, ""
}

// save checks that plugin can still be answered and commits it. Callers hold
// opMu.
func (links *DevLinks) save(ctx context.Context, current map[string]installed, plugin domain.Plugin) (Summary, error) {
	entry, err := links.answerable(plugin)
	if err != nil {
		return Summary{}, err
	}
	if err := links.plugins.commit(ctx, current, entry, ErrTooLarge); err != nil {
		return Summary{}, err
	}
	return summary(entry), nil
}

// answerable measures plugin and refuses it when plugins.get could not
// answer it.
func (links *DevLinks) answerable(plugin domain.Plugin) (installed, error) {
	size, err := encodedSize(plugin.Package)
	if err != nil {
		return installed{}, err
	}
	entry := installed{plugin: plugin, sizeBytes: size}
	if fits, err := links.plugins.fits(detail(entry)); err != nil || !fits {
		return installed{}, errors.Join(ErrTooLarge, err)
	}
	return entry, nil
}

// reloaded is the installed plugin with pkg in place of its package. The
// grant keeps only what the owner already granted and pkg still declares,
// less net:fetch when pkg adds a network host;
// storage, enabled state and the secrets pkg still declares stay. A broken
// record is replaced as an install over it would be.
func reloaded(previous installed, pkg domain.Package, now time.Time) domain.Plugin {
	plugin := previous.plugin
	plugin.Package = pkg
	plugin.InstalledAt, plugin.UpdatedAt, plugin.Storage = upgraded(previous, now)
	plugin.Secrets = domain.KeptSecrets(pkg.Settings, previous.plugin.Secrets)
	if previous.problem != "" {
		plugin.Enabled, plugin.Granted = true, []string{}
		return plugin
	}
	// New hosts were never reviewed: net:fetch waits for the owner again.
	widened := previous.plugin.Package.Network.Widens(pkg.Network)
	kept := slices.DeleteFunc(slices.Clone(previous.plugin.Granted), func(permission string) bool {
		return !slices.Contains(pkg.Permissions, permission) || (widened && permission == domain.PermissionNetFetch)
	})
	plugin.Granted, _ = pkg.Grant(kept)
	if plugin.Granted == nil {
		plugin.Granted = []string{}
	}
	return plugin
}

func samePackage(left, right domain.Package) bool {
	a, errA := json.Marshal(left)
	b, errB := json.Marshal(right)
	return errA == nil && errB == nil && bytes.Equal(a, b)
}
