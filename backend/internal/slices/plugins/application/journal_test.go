package application

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/plugins/domain"
)

type journalMemory struct {
	mu      sync.Mutex
	entries []domain.JournalEntry
	loadErr error
	fail    bool
	saves   int
	deletes int
}

func (store *journalMemory) Load(context.Context) ([]domain.JournalEntry, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	return slices.Clone(store.entries), store.loadErr
}

func (store *journalMemory) Save(_ context.Context, entries []domain.JournalEntry) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.fail {
		return errors.New("disk is full")
	}
	store.saves++
	store.entries = slices.Clone(entries)
	return nil
}

func (store *journalMemory) Delete(context.Context) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.fail {
		return errors.New("disk is full")
	}
	store.deletes++
	store.entries, store.loadErr = nil, nil
	return nil
}

func newJournal(t *testing.T, service *Service, store *journalMemory) *Journal {
	t.Helper()
	journal, err := NewJournal(service, store)
	if err != nil {
		t.Fatal(err)
	}
	return journal
}

// kinds lists a page oldest first as "kind id version" lines.
func kinds(page JournalPage) []string {
	var out []string
	for index := len(page.Entries) - 1; index >= 0; index-- {
		entry := page.Entries[index]
		out = append(out, strings.TrimSpace(fmt.Sprintf("%s %s %s", entry.Kind, entry.PluginID, entry.Version)))
	}
	return out
}

func list(t *testing.T, journal *Journal, pluginID string, before int64, limit int) JournalPage {
	t.Helper()
	page, err := journal.List(context.Background(), pluginID, before, limit)
	if err != nil {
		t.Fatal(err)
	}
	return page
}

func TestJournalRecordsWhatHappenedToEachPlugin(t *testing.T) {
	service, links, folders, _, _ := newDevLinks(t)
	store := &journalMemory{}
	journal := newJournal(t, service, store)
	ctx := context.Background()

	dusk := pkg("acme.dusk", "Dusk", domain.PermissionTheme, domain.PermissionStorage)
	if _, err := service.Install(ctx, dusk, []string{domain.PermissionStorage, domain.PermissionTheme}, "https://example.test/dusk.json"); err != nil {
		t.Fatal(err)
	}
	// Storage and secrets are the plugin's data, not its history.
	if _, err := service.StorageSet(ctx, "acme.dusk", "fixture-key", new("fixture-value")); err != nil {
		t.Fatal(err)
	}
	upgrade := dusk
	upgrade.Version = "1.1.0"
	if _, err := service.Install(ctx, upgrade, []string{domain.PermissionTheme}, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Update(ctx, "acme.dusk", new(false), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Update(ctx, "acme.dusk", new(true), &[]string{domain.PermissionStorage}); err != nil {
		t.Fatal(err)
	}
	path := devPath("dawn")
	dawn := pkg("acme.dawn", "Dawn", domain.PermissionTheme)
	folders.set(path, dawn)
	if _, err := links.Link(ctx, path); err != nil {
		t.Fatal(err)
	}
	// Saving a file with the same version is not an update.
	dawn.Description = "Edited"
	folders.set(path, dawn)
	links.poll(ctx)
	dawn.Version = "2.0.0"
	folders.set(path, dawn)
	links.poll(ctx)
	if _, err := links.Unlink(ctx, "acme.dawn"); err != nil {
		t.Fatal(err)
	}
	if err := service.Remove(ctx, "acme.dusk"); err != nil {
		t.Fatal(err)
	}

	page := list(t, journal, "", 0, 0)
	want := []string{
		"installed acme.dusk 1.0.0",
		"updated acme.dusk 1.1.0",
		"disabled acme.dusk 1.1.0",
		"permissions acme.dusk 1.1.0",
		"enabled acme.dusk 1.1.0",
		"linked acme.dawn 1.0.0",
		"updated acme.dawn 2.0.0",
		"unlinked acme.dawn 2.0.0",
		"removed acme.dusk 1.1.0",
	}
	if got := kinds(page); !slices.Equal(got, want) {
		t.Fatalf("journal\n got %q\nwant %q", got, want)
	}
	byKind := map[string]domain.JournalEntry{}
	for _, entry := range page.Entries {
		byKind[entry.Kind+" "+entry.PluginID] = entry
	}
	installed := byKind["installed acme.dusk"]
	if installed.Origin != domain.OriginLink || !slices.Equal(installed.Granted, []string{domain.PermissionStorage, domain.PermissionTheme}) || installed.Name != "Dusk" {
		t.Fatalf("install %+v", installed)
	}
	updated := byKind["updated acme.dusk"]
	if updated.FromVersion != "1.0.0" || updated.Origin != domain.OriginFile || !slices.Equal(updated.Revoked, []string{domain.PermissionStorage}) || len(updated.Granted) != 0 {
		t.Fatalf("update %+v", updated)
	}
	permissions := byKind["permissions acme.dusk"]
	if !slices.Equal(permissions.Granted, []string{domain.PermissionStorage}) || !slices.Equal(permissions.Revoked, []string{domain.PermissionTheme}) {
		t.Fatalf("permissions %+v", permissions)
	}
	if reload := byKind["updated acme.dawn"]; reload.Origin != domain.OriginFolder || reload.FromVersion != "1.0.0" {
		t.Fatalf("folder reload %+v", reload)
	}
	for index, entry := range page.Entries {
		if !entry.Valid() || (index > 0 && entry.Seq >= page.Entries[index-1].Seq) {
			t.Fatalf("entry %d is not a newest-first valid entry: %+v", index, entry)
		}
	}
	saved := fmt.Sprintf("%+v", store.entries)
	for _, private := range []string{"example.test", "fixture-key", "fixture-value", path} {
		if strings.Contains(saved, private) {
			t.Errorf("the journal recorded %q", private)
		}
	}
	if len(store.entries) != len(want) || page.Incomplete || page.Next != 0 {
		t.Fatalf("saved %d entries, incomplete %v, next %d", len(store.entries), page.Incomplete, page.Next)
	}
	if got := kinds(list(t, journal, "acme.dawn", 0, 0)); !slices.Equal(got, want[5:8]) {
		t.Fatalf("filtered %q", got)
	}
}

func TestJournalSurvivesARestartAndContinuesItsSequence(t *testing.T) {
	store := &journalMemory{}
	service, _, _ := newService(t, &memory{}, 1<<20)
	newJournal(t, service, store)
	ctx := context.Background()
	if _, err := service.Install(ctx, pkg("acme.dusk", "Dusk", domain.PermissionTheme), nil, ""); err != nil {
		t.Fatal(err)
	}
	restarted, _, _ := newService(t, &memory{}, 1<<20)
	journal := newJournal(t, restarted, store)
	if _, err := restarted.Install(ctx, pkg("acme.dawn", "Dawn", domain.PermissionTheme), nil, ""); err != nil {
		t.Fatal(err)
	}
	page := list(t, journal, "", 0, 0)
	if got := kinds(page); !slices.Equal(got, []string{"installed acme.dusk 1.0.0", "installed acme.dawn 1.0.0"}) || page.Entries[0].Seq != 2 {
		t.Fatalf("after restart %q %+v", got, page.Entries)
	}
}

func TestJournalPagesAndKeepsTheNewest(t *testing.T) {
	service, _, _ := newService(t, &memory{}, 1<<20)
	store := &journalMemory{}
	journal := newJournal(t, service, store)
	ctx := context.Background()
	if _, err := service.Install(ctx, pkg("acme.dusk", "Dusk", domain.PermissionTheme), nil, ""); err != nil {
		t.Fatal(err)
	}
	for index := range domain.MaxJournalEntries {
		if _, err := service.Update(ctx, "acme.dusk", new(index%2 == 1), nil); err != nil {
			t.Fatal(err)
		}
	}
	if len(store.entries) != domain.MaxJournalEntries || store.entries[0].Seq != 2 {
		t.Fatalf("kept %d entries from seq %d", len(store.entries), store.entries[0].Seq)
	}
	var seen []int64
	for before := int64(0); ; {
		page := list(t, journal, "", before, MaxJournalList)
		for _, entry := range page.Entries {
			seen = append(seen, entry.Seq)
		}
		if page.Next == 0 {
			break
		}
		before = page.Next
	}
	if len(seen) != domain.MaxJournalEntries || seen[0] != domain.MaxJournalEntries+1 || seen[len(seen)-1] != 2 {
		t.Fatalf("paged %d entries, %d..%d", len(seen), seen[0], seen[len(seen)-1])
	}
	if page := list(t, journal, "", 0, 0); len(page.Entries) != DefaultJournalList || page.Next != page.Entries[DefaultJournalList-1].Seq {
		t.Fatalf("default page %d next %d", len(page.Entries), page.Next)
	}
	if page := list(t, journal, "acme.other", 0, 0); len(page.Entries) != 0 || page.Next != 0 {
		t.Fatalf("an unknown plugin answered %+v", page)
	}
}

func TestJournalPagesStayWithinTheBudget(t *testing.T) {
	service, _, _ := newService(t, &memory{}, 1<<20)
	journal := newJournal(t, service, &journalMemory{})
	ctx := context.Background()
	for index := range 6 {
		if _, err := service.Install(ctx, pkg(fmt.Sprintf("acme.p%d", index), strings.Repeat("N", 40), domain.PermissionTheme), nil, ""); err != nil {
			t.Fatal(err)
		}
	}
	one, err := encodedSize(JournalPage{Entries: list(t, journal, "", 0, 1).Entries})
	if err != nil {
		t.Fatal(err)
	}
	journal.budget = one*2 + 10
	page := list(t, journal, "", 0, 0)
	if size, _ := encodedSize(page); size > journal.budget || len(page.Entries) < 1 || len(page.Entries) > 2 || page.Next != page.Entries[len(page.Entries)-1].Seq {
		t.Fatalf("page of %d entries, %d bytes, next %d", len(page.Entries), size, page.Next)
	}
	journal.budget = 10
	if _, err := journal.List(ctx, "", 0, 0); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("an entry over the budget answered %v", err)
	}
}

func TestJournalRefusesInvalidQueries(t *testing.T) {
	service, _, _ := newService(t, &memory{}, 1<<20)
	journal := newJournal(t, service, &journalMemory{})
	for _, query := range []struct {
		field, id string
		before    int64
		limit     int
	}{
		{"pluginId", "Not An Id", 0, 0},
		{"before", "", -1, 0},
		{"limit", "", 0, -1},
		{"limit", "", 0, MaxJournalList + 1},
	} {
		var validation domain.ValidationError
		if _, err := journal.List(context.Background(), query.id, query.before, query.limit); !errors.As(err, &validation) || validation.Field != query.field {
			t.Errorf("%+v: %v", query, err)
		}
	}
}

func TestAnUnreadableJournalIsUnavailableNotEmpty(t *testing.T) {
	service, _, _ := newService(t, &memory{}, 1<<20)
	kept := []domain.JournalEntry{{Seq: 7, At: service.timestamp(), Kind: domain.JournalInstalled, PluginID: "acme.kept", Name: "Kept"}}
	store := &journalMemory{entries: kept, loadErr: errors.New("keyring is locked")}
	journal := newJournal(t, service, store)
	ctx := context.Background()
	if _, err := journal.List(ctx, "", 0, 0); !errors.Is(err, ErrStoreUnavailable) {
		t.Fatalf("an unreadable journal answered %v", err)
	}
	// The plugin change still succeeds; the unreadable file is not replaced.
	if _, err := service.Install(ctx, pkg("acme.dusk", "Dusk", domain.PermissionTheme), nil, ""); err != nil {
		t.Fatal(err)
	}
	if store.saves != 0 || len(store.entries) != 1 || store.entries[0].Seq != 7 {
		t.Fatal("an unreadable journal was overwritten")
	}
	store.loadErr = nil
	if got := kinds(list(t, journal, "", 0, 0)); !slices.Equal(got, []string{"installed acme.kept"}) {
		t.Fatalf("after the keyring came back %q", got)
	}

	for _, broken := range [][]domain.JournalEntry{
		{{Seq: 1, At: kept[0].At, Kind: "exploded", PluginID: "acme.kept", Name: "Kept"}},
		{kept[0], kept[0]},
	} {
		other, _, _ := newService(t, &memory{}, 1<<20)
		invalid := newJournal(t, other, &journalMemory{entries: broken})
		if _, err := invalid.List(ctx, "", 0, 0); !errors.Is(err, ErrStoreUnavailable) {
			t.Errorf("%+v answered %v", broken, err)
		}
	}
	// Clear is the way out of an unreadable journal.
	store.loadErr = errors.New("keyring is locked")
	other, _, _ := newService(t, &memory{}, 1<<20)
	cleared := newJournal(t, other, store)
	if err := cleared.Clear(ctx); err != nil {
		t.Fatal(err)
	}
	if page := list(t, cleared, "", 0, 0); len(page.Entries) != 0 || store.deletes != 1 {
		t.Fatalf("after clear %+v", page)
	}
}

func TestAnUnsavedJournalIsReportedIncomplete(t *testing.T) {
	service, _, _ := newService(t, &memory{}, 1<<20)
	store := &journalMemory{fail: true}
	journal := newJournal(t, service, store)
	ctx := context.Background()
	if _, err := service.Install(ctx, pkg("acme.dusk", "Dusk", domain.PermissionTheme), nil, ""); err != nil {
		t.Fatal(err)
	}
	if page := list(t, journal, "", 0, 0); !page.Incomplete || len(page.Entries) != 1 {
		t.Fatalf("an unsaved entry %+v", page)
	}
	if err := journal.Clear(ctx); err == nil {
		t.Fatal("a failed clear reported success")
	}
	store.fail = false
	if _, err := service.Update(ctx, "acme.dusk", new(false), nil); err != nil {
		t.Fatal(err)
	}
	if page := list(t, journal, "", 0, 0); page.Incomplete || len(store.entries) != 2 {
		t.Fatalf("after a good save %+v, stored %d", page, len(store.entries))
	}
	if err := journal.Clear(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Update(ctx, "acme.dusk", new(true), nil); err != nil {
		t.Fatal(err)
	}
	// Sequence numbers keep going after a clear, so a stale cursor cannot
	// skip new entries.
	if page := list(t, journal, "", 0, 0); len(page.Entries) != 1 || page.Entries[0].Seq != 3 {
		t.Fatalf("after clear %+v", page)
	}
}

func TestAFailedPluginSaveRecordsNothing(t *testing.T) {
	plugins := &memory{}
	service, _, _ := newService(t, plugins, 1<<20)
	store := &journalMemory{}
	newJournal(t, service, store)
	plugins.fail = true
	if _, err := service.Install(context.Background(), pkg("acme.dusk", "Dusk", domain.PermissionTheme), nil, ""); err == nil {
		t.Fatal("a failed save installed")
	}
	if len(store.entries) != 0 || store.saves != 0 {
		t.Fatal("a failed install was recorded")
	}
}
