package application

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/plugins/domain"
)

const (
	DefaultJournalList = 50
	MaxJournalList     = 200
)

// JournalStore keeps the activity journal in one protected file.
type JournalStore interface {
	// Load returns the stored entries, oldest first; a missing file is empty.
	Load(ctx context.Context) ([]domain.JournalEntry, error)
	// Save replaces the stored entries atomically.
	Save(ctx context.Context, entries []domain.JournalEntry) error
	// Delete removes the file; a missing file is not an error.
	Delete(ctx context.Context) error
}

// JournalPage is one page of entries, newest first. Next is the cursor for
// older entries, zero when there are none. Incomplete reports that the newest
// entries could not be saved and live only in this process.
type JournalPage struct {
	Entries    []domain.JournalEntry `json:"entries"`
	Next       int64                 `json:"next,omitempty"`
	Incomplete bool                  `json:"incomplete"`
}

// Journal records what happened to each plugin: installs, updates,
// permission changes, enabling, folder links and removals. It is written
// from the plugins service after every saved mutation and keeps the newest
// MaxJournalEntries.
type Journal struct {
	store  JournalStore
	budget int
	mu     sync.Mutex
	// loaded is false until the file was read; a failed read is retried on
	// the next use and never overwritten, so history is not lost to a
	// locked keyring. Clear is the way out of an unreadable file.
	loaded  bool
	entries []domain.JournalEntry
	seq     int64
	unsaved bool
}

func NewJournal(plugins *Service, store JournalStore) (*Journal, error) {
	if plugins == nil || store == nil {
		return nil, errors.New("plugin journal needs the plugins service and a store")
	}
	journal := &Journal{store: store, budget: plugins.budget}
	plugins.opMu.Lock()
	plugins.persisted = journal.record
	plugins.opMu.Unlock()
	return journal, nil
}

// List answers entries older than before (all when zero), newest first,
// for one plugin or every plugin when pluginID is empty.
func (journal *Journal) List(ctx context.Context, pluginID string, before int64, limit int) (JournalPage, error) {
	switch {
	case pluginID != "" && !domain.ValidID(pluginID):
		return JournalPage{}, domain.ValidationError{Field: "pluginId", Reason: "must be a plugin id"}
	case before < 0:
		return JournalPage{}, domain.ValidationError{Field: "before", Reason: "must not be negative"}
	case limit < 0 || limit > MaxJournalList:
		return JournalPage{}, domain.ValidationError{Field: "limit", Reason: fmt.Sprintf("must be at most %d", MaxJournalList)}
	case limit == 0:
		limit = DefaultJournalList
	}
	journal.mu.Lock()
	defer journal.mu.Unlock()
	if err := journal.load(ctx); err != nil {
		return JournalPage{}, err
	}
	page := JournalPage{Entries: []domain.JournalEntry{}, Incomplete: journal.unsaved}
	for index := len(journal.entries) - 1; index >= 0; index-- {
		entry := journal.entries[index]
		if (before > 0 && entry.Seq >= before) || (pluginID != "" && entry.PluginID != pluginID) {
			continue
		}
		if len(page.Entries) == limit {
			page.Next = page.Entries[len(page.Entries)-1].Seq
			break
		}
		page.Entries = append(page.Entries, entry)
		// The transport bounds the answer, not the count: an entry that does
		// not fit starts the next page.
		if fits, err := fitsBudget(page, journal.budget); err != nil || !fits {
			page.Entries = page.Entries[:len(page.Entries)-1]
			if len(page.Entries) == 0 {
				return JournalPage{}, ErrTooLarge
			}
			page.Next = page.Entries[len(page.Entries)-1].Seq
			break
		}
	}
	return page, nil
}

// Clear deletes the journal, readable or not.
func (journal *Journal) Clear(ctx context.Context) error {
	journal.mu.Lock()
	defer journal.mu.Unlock()
	if err := journal.store.Delete(ctx); err != nil {
		return fmt.Errorf("plugin journal could not be cleared: %w", err)
	}
	journal.loaded, journal.entries, journal.unsaved = true, nil, false
	return nil
}

func (journal *Journal) load(ctx context.Context) error {
	if journal.loaded {
		return nil
	}
	entries, err := journal.store.Load(ctx)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrStoreUnavailable, err)
	}
	for _, entry := range entries {
		if !entry.Valid() || entry.Seq <= journal.seq {
			return fmt.Errorf("%w: plugin journal is invalid", ErrStoreUnavailable)
		}
		journal.seq = entry.Seq
	}
	journal.entries, journal.loaded = entries, true
	return nil
}

// record runs under the service's opMu after next was saved and must not
// call back into the service.
func (journal *Journal) record(ctx context.Context, previous, next map[string]installed, at time.Time) {
	changes := journalChanges(previous, next, at)
	if len(changes) == 0 {
		return
	}
	// The plugin change is already saved: a caller giving up now must not
	// cost its record.
	ctx = context.WithoutCancel(ctx)
	journal.mu.Lock()
	defer journal.mu.Unlock()
	if journal.load(ctx) != nil {
		// ponytail: changes made while the file is unreadable are not kept;
		// List reports the journal unavailable meanwhile, never empty.
		return
	}
	for _, change := range changes {
		journal.seq++
		change.Seq = journal.seq
		journal.entries = append(journal.entries, change)
	}
	if overflow := len(journal.entries) - domain.MaxJournalEntries; overflow > 0 {
		journal.entries = slices.Clone(journal.entries[overflow:])
	}
	journal.unsaved = journal.store.Save(ctx, journal.entries) != nil
}

func fitsBudget(page JournalPage, budget int) (bool, error) {
	size, err := encodedSize(page)
	return err == nil && size <= budget, err
}

// journalChanges describes what a saved mutation did, in plugin id order.
func journalChanges(previous, next map[string]installed, at time.Time) []domain.JournalEntry {
	ids := slices.Sorted(maps.Keys(next))
	for id := range previous {
		if _, kept := next[id]; !kept {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	var changes []domain.JournalEntry
	for _, id := range ids {
		before, had := previous[id]
		after, has := next[id]
		var plugin domain.Plugin
		if has {
			plugin = after.plugin
		} else {
			plugin = before.plugin
		}
		entry := domain.JournalEntry{At: at, PluginID: id, Name: domain.JournalName(id, plugin.Package.Name), Version: domain.JournalVersion(plugin.Package.Version)}
		switch {
		case !had:
			entry.Kind, entry.Granted, entry.Origin = domain.JournalInstalled, sortedCopy(plugin.Granted), origin(plugin)
			if plugin.Dev != nil {
				entry.Kind = domain.JournalLinked
			}
			changes = append(changes, entry)
			continue
		case !has:
			entry.Kind = domain.JournalRemoved
			changes = append(changes, entry)
			continue
		}
		old := before.plugin
		if fromVersion := domain.JournalVersion(old.Package.Version); fromVersion != entry.Version {
			entry.FromVersion = fromVersion
		}
		entry.Granted, entry.Revoked = difference(plugin.Granted, old.Granted), difference(old.Granted, plugin.Granted)
		switch {
		case old.Dev == nil && plugin.Dev != nil:
			entry.Kind, entry.Origin = domain.JournalLinked, domain.OriginFolder
		// Only installs and folder reloads move UpdatedAt: an install over a
		// linked folder is an update, and a folder reload with the same
		// version is the developer saving a file, not an update.
		case !plugin.UpdatedAt.Equal(old.UpdatedAt) && (plugin.Dev == nil || entry.FromVersion != ""):
			entry.Kind, entry.Origin = domain.JournalUpdated, origin(plugin)
		case old.Dev != nil && plugin.Dev == nil:
			entry.Kind = domain.JournalUnlinked
		case len(entry.Granted) > 0 || len(entry.Revoked) > 0:
			entry.Kind = domain.JournalPermissions
		}
		if entry.Kind != "" {
			changes = append(changes, entry)
		}
		if old.Enabled != plugin.Enabled {
			toggled := domain.JournalEntry{At: at, PluginID: id, Name: entry.Name, Version: entry.Version, Kind: domain.JournalDisabled}
			if plugin.Enabled {
				toggled.Kind = domain.JournalEnabled
			}
			changes = append(changes, toggled)
		}
	}
	return changes
}

func origin(plugin domain.Plugin) string {
	switch {
	case plugin.Dev != nil:
		return domain.OriginFolder
	case plugin.Source != "":
		return domain.OriginLink
	}
	return domain.OriginFile
}

// difference lists what a holds and b does not, sorted.
func difference(a, b []string) []string {
	var out []string
	for _, value := range a {
		if !slices.Contains(b, value) {
			out = append(out, value)
		}
	}
	slices.Sort(out)
	return out
}

func sortedCopy(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	copied := slices.Clone(values)
	slices.Sort(copied)
	return copied
}
