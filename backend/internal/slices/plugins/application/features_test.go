package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/plugins/domain"
)

func tuned() domain.Package {
	p := pkg("acme.tuned", "Tuned", domain.PermissionStorage)
	p.Homepage = "https://example.test/tuned"
	p.MinAppVersion = runningVersion
	p.Settings = []domain.Setting{
		{Key: "mode", Label: "Mode", Type: domain.SettingSelect, Default: json.RawMessage(`"a"`), Options: []domain.SettingOption{{Value: "a", Label: "A"}, {Value: "b", Label: "B"}}},
		{Key: "greeting", Label: "Greeting", Type: domain.SettingText, Default: json.RawMessage(`"<hi>"`), Placeholder: "Hello"},
	}
	return p
}

func TestSettingsHomepageAndMinimumReachTheAnswers(t *testing.T) {
	store := &memory{}
	service, _, _ := newService(t, store, 1<<20)
	summary, err := service.Install(context.Background(), tuned(), nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if !summary.HasSettings || summary.Homepage != "https://example.test/tuned" || summary.MinAppVersion != runningVersion || summary.Problem != "" {
		t.Fatalf("unexpected summary %+v", summary)
	}
	detail, err := service.Get("acme.tuned")
	if err != nil || !reflect.DeepEqual(detail.Settings, tuned().Normalized().Settings) {
		t.Fatalf("unexpected settings %+v %v", detail.Settings, err)
	}
	restarted, _, _ := newService(t, store, 1<<20)
	again, err := restarted.Get("acme.tuned")
	if err != nil || !reflect.DeepEqual(again, detail) {
		t.Fatalf("settings did not survive a restart:\n%+v\n%+v", again, detail)
	}

	if _, err := service.Install(context.Background(), pkg("acme.plain", "Plain", domain.PermissionTheme), nil, ""); err != nil {
		t.Fatal(err)
	}
	plain, _ := service.Get("acme.plain")
	encoded, _ := json.Marshal(plain)
	if plain.Settings == nil || !strings.Contains(string(encoded), `"settings":[]`) {
		t.Fatalf("a plugin without settings does not answer an empty list: %s", encoded)
	}
}

func TestAPackageForANewerAppIsRefused(t *testing.T) {
	store := &memory{}
	service, _, notifications := newService(t, store, 1<<20)
	future := tuned()
	future.MinAppVersion = "1.2.4"
	want := domain.ValidationError{Field: "minAppVersion", Reason: "needs Luxury Switchboard 1.2.4 or later"}
	if _, err := service.Install(context.Background(), future, nil, ""); err != want {
		t.Fatalf("want %v, got %v", want, err)
	}
	if store.saves != 0 || len(*notifications) != 0 {
		t.Fatal("a refused install reached storage or listeners")
	}
	future.MinAppVersion = "1.2.03"
	if _, err := service.Install(context.Background(), future, nil, ""); err != nil {
		t.Fatalf("an equal minimum written with a leading zero: %v", err)
	}
}

func TestEveryInstallIsANewRevision(t *testing.T) {
	service, _, _ := newService(t, &memory{}, 1<<20)
	ctx := context.Background()
	at := time.Date(2026, 3, 4, 5, 6, 7, 100_000_000, time.FixedZone("fixture", 3*3600))
	service.now = func() time.Time { return at }
	install := func(id string) Summary {
		t.Helper()
		summary, err := service.Install(ctx, pkg(id, "A", domain.PermissionTheme), nil, "")
		if err != nil {
			t.Fatal(err)
		}
		return summary
	}
	first := install("acme.a")
	at = at.Add(150*time.Millisecond + 999*time.Microsecond)
	second := install("acme.a")
	third := install("acme.a")
	at = at.Add(-time.Hour)
	fourth := install("acme.a")
	other := install("acme.b")
	got := []string{first.UpdatedAt, second.UpdatedAt, third.UpdatedAt, fourth.UpdatedAt}
	want := []string{"2026-03-04T02:06:07.1Z", "2026-03-04T02:06:07.25Z", "2026-03-04T02:06:07.251Z", "2026-03-04T02:06:07.252Z"}
	if !slices.Equal(got, want) {
		t.Fatalf("update times %v, want %v", got, want)
	}
	for _, summary := range []Summary{second, third, fourth} {
		if summary.InstalledAt != first.InstalledAt {
			t.Fatalf("an upgrade moved the install time: %+v", summary)
		}
	}
	if other.InstalledAt != "2026-03-04T01:06:07.25Z" || other.UpdatedAt != other.InstalledAt {
		t.Fatalf("a new plugin after the clock went back: %+v", other)
	}
	for _, value := range []string{first.UpdatedAt, fourth.UpdatedAt} {
		if parsed, err := time.Parse(time.RFC3339Nano, value); err != nil || parsed.Location() != time.UTC {
			t.Fatalf("%q is not an RFC 3339 UTC time: %v", value, err)
		}
	}
}

// brokenStore holds one healthy record and three this version refuses for
// different reasons.
func brokenStore() (*memory, map[string]domain.Plugin) {
	installedAt := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	healthy := domain.Plugin{Package: pkg("acme.ok", "OK", domain.PermissionStorage), Enabled: true, Granted: []string{}, InstalledAt: installedAt, UpdatedAt: installedAt, Storage: map[string]string{}}
	stale := domain.Plugin{Package: pkg("acme.stale", "Stale", domain.PermissionStorage), Enabled: true, Granted: []string{domain.PermissionStorage},
		InstalledAt: installedAt, UpdatedAt: installedAt.Add(time.Hour), Storage: map[string]string{"kept": "1"}}
	stale.Package.Settings = []domain.Setting{{Key: "old", Label: "Old", Type: "switch", Default: json.RawMessage(`true`)}}
	future := healthy
	future.Package = pkg("acme.future", "Future", domain.PermissionTheme)
	future.Package.MinAppVersion = "9.0.0"
	mangled := domain.Plugin{Package: pkg("acme.mangled", "", domain.PermissionStorage), Enabled: true, Granted: []string{domain.PermissionWrite},
		Storage: map[string]string{"bad key": "v"}}
	mangled.Package.Version = "not a version"
	records := map[string]domain.Plugin{"acme.ok": healthy, "acme.stale": stale, "acme.future": future, "acme.mangled": mangled}
	store := &memory{}
	for _, id := range slices.Sorted(maps.Keys(records)) {
		store.plugins = append(store.plugins, records[id])
	}
	return store, records
}

func TestAStoredPluginThisVersionRefusesIsListedBroken(t *testing.T) {
	store, records := brokenStore()
	service, _, _ := newService(t, store, 1<<20)
	list, err := service.List()
	if err != nil || len(list) != 4 {
		t.Fatalf("unexpected list %+v %v", list, err)
	}
	byID := map[string]Summary{}
	for _, summary := range list {
		byID[summary.ID] = summary
	}
	if ok := byID["acme.ok"]; ok.Problem != "" || !ok.Enabled {
		t.Fatalf("a healthy plugin is affected: %+v", ok)
	}
	stale := byID["acme.stale"]
	if stale.Problem != "This plugin no longer passes this version's checks (settings[0].type must be text, number, toggle, select or secret). Reinstall a fixed version or remove it." ||
		stale.Enabled || stale.Name != "Stale" || stale.Version != "1.0.0" || !slices.Equal(stale.Granted, []string{domain.PermissionStorage}) ||
		stale.InstalledAt != "2026-01-01T00:00:00Z" || stale.UpdatedAt != "2026-01-01T01:00:00Z" || stale.SizeBytes <= 0 {
		t.Fatalf("unexpected stale summary %+v", stale)
	}
	if future := byID["acme.future"]; future.Problem != "Needs Luxury Switchboard 9.0.0 or later; this is 1.2.3. Update the app or install an older version of the plugin." ||
		future.Enabled || future.MinAppVersion != "9.0.0" {
		t.Fatalf("unexpected future summary %+v", future)
	}
	mangled := byID["acme.mangled"]
	if mangled.Problem != "This plugin no longer passes this version's checks (name must be 1-64 characters without control characters). Reinstall a fixed version or remove it." ||
		mangled.Name != "acme.mangled" || mangled.Version != "" || mangled.Granted == nil || len(mangled.Granted) != 0 || mangled.Enabled ||
		mangled.InstalledAt != "0001-01-01T00:00:00Z" {
		t.Fatalf("unexpected mangled summary %+v", mangled)
	}

	ctx := context.Background()
	value := "v"
	enabled := true
	for _, id := range []string{"acme.stale", "acme.future", "acme.mangled"} {
		_, get := service.Get(id)
		_, storageGet := service.StorageGet(id)
		_, storageSet := service.StorageSet(ctx, id, "k", &value)
		_, storageUpdate := service.StorageUpdate(ctx, id, map[string]*string{"k": &value}, true)
		_, update := service.Update(ctx, id, &enabled, nil)
		for command, err := range map[string]error{"get": get, "storage.get": storageGet, "storage.set": storageSet, "storage.update": storageUpdate, "update": update} {
			if !errors.Is(err, ErrBroken) {
				t.Errorf("%s %s: %v", id, command, err)
			}
		}
	}
	if store.saves != 0 {
		t.Fatal("a refused command was saved")
	}

	// A save for another plugin writes every broken record back as it was.
	if _, err := service.StorageSet(ctx, "acme.ok", "k", &value); err != nil {
		t.Fatal(err)
	}
	for _, record := range store.plugins {
		if record.Package.ID == "acme.ok" {
			continue
		}
		want := records[record.Package.ID]
		want.Package = want.Package.Normalized()
		if !reflect.DeepEqual(record, want) {
			t.Errorf("a broken record changed on save:\n%+v\n%+v", record, want)
		}
	}
	restarted, _, _ := newService(t, store, 1<<20)
	if again, _ := restarted.List(); fmt.Sprint(again) != fmt.Sprint(list) {
		t.Fatalf("a restart changed the broken list:\n%+v\n%+v", list, again)
	}
}

func TestABrokenPluginCanBeReinstalledOrRemoved(t *testing.T) {
	store, _ := brokenStore()
	service, _, notifications := newService(t, store, 1<<20)
	ctx := context.Background()

	fixed := pkg("acme.stale", "Stale", domain.PermissionStorage)
	fixed.Version = "2.0.0"
	summary, err := service.Install(ctx, fixed, []string{domain.PermissionStorage}, "")
	if err != nil {
		t.Fatal(err)
	}
	if summary.Problem != "" || !summary.Enabled || summary.InstalledAt != "2026-01-01T00:00:00Z" || summary.UpdatedAt != "2026-03-04T05:07:37.123Z" {
		t.Fatalf("unexpected reinstall %+v", summary)
	}
	if values, err := service.StorageGet("acme.stale"); err != nil || !maps.Equal(values.Values, map[string]string{"kept": "1"}) {
		t.Fatalf("a reinstall lost storage that still passes: %+v %v", values, err)
	}

	// Nothing of the mangled record passes, so its reinstall starts fresh.
	summary, err = service.Install(ctx, pkg("acme.mangled", "Mangled", domain.PermissionStorage), nil, "")
	if err != nil || summary.Problem != "" || summary.InstalledAt != summary.UpdatedAt || summary.InstalledAt != "2026-03-04T05:09:07.123Z" {
		t.Fatalf("unexpected fresh reinstall %+v %v", summary, err)
	}
	if values, _ := service.StorageGet("acme.mangled"); values.Values == nil || len(values.Values) != 0 {
		t.Fatalf("a reinstall kept storage that does not pass: %+v", values)
	}

	if err := service.Remove(ctx, "acme.future"); err != nil {
		t.Fatalf("a broken plugin could not be removed: %v", err)
	}
	list, _ := service.List()
	if len(list) != 3 || slices.ContainsFunc(list, func(summary Summary) bool { return summary.Problem != "" }) {
		t.Fatalf("unexpected list %+v", list)
	}
	if len(*notifications) != 3 {
		t.Fatalf("unexpected notifications %d", len(*notifications))
	}
	restarted, _, _ := newService(t, store, 1<<20)
	if again, _ := restarted.List(); fmt.Sprint(again) != fmt.Sprint(list) {
		t.Fatalf("the repaired list did not survive a restart:\n%+v\n%+v", list, again)
	}
}

func TestAWriteThatChangesNothingIsNotSaved(t *testing.T) {
	store := &memory{}
	service, _, _ := newService(t, store, 1<<20)
	ctx := context.Background()
	_, _ = service.Install(ctx, pkg("acme.a", "A", domain.PermissionStorage), nil, "")
	value := "v"
	if _, err := service.StorageSet(ctx, "acme.a", "k", &value); err != nil {
		t.Fatal(err)
	}
	saves := store.saves
	same := "v"
	if values, err := service.StorageSet(ctx, "acme.a", "k", &same); err != nil || values.Values["k"] != "v" {
		t.Fatalf("an unchanged write: %+v %v", values, err)
	}
	if _, err := service.StorageSet(ctx, "acme.a", "absent", nil); err != nil {
		t.Fatal(err)
	}
	if values, err := service.StorageUpdate(ctx, "acme.a", map[string]*string{"k": &same, "absent": nil}, false); err != nil || !maps.Equal(values.Values, map[string]string{"k": "v"}) {
		t.Fatalf("an unchanged batch: %+v %v", values, err)
	}
	if _, err := service.StorageUpdate(ctx, "acme.a", map[string]*string{"k": &same}, true); err != nil {
		t.Fatal(err)
	}
	if store.saves != saves {
		t.Fatalf("%d writes that changed nothing were saved", store.saves-saves)
	}
	store.fail = true
	if _, err := service.StorageSet(ctx, "acme.a", "k", &same); err != nil {
		t.Fatalf("an unchanged write reached a failing store: %v", err)
	}
}

func TestAStorageBatchAppliesWhole(t *testing.T) {
	store := &memory{}
	service, _, notifications := newService(t, store, 1<<20)
	ctx := context.Background()
	_, _ = service.Install(ctx, pkg("acme.a", "A", domain.PermissionStorage), nil, "")
	announced := len(*notifications)
	text := func(value string) *string { return &value }

	saves := store.saves
	values, err := service.StorageUpdate(ctx, "acme.a", map[string]*string{"a": text("1"), "b": text("2"), "c": text("3")}, false)
	if err != nil || !maps.Equal(values.Values, map[string]string{"a": "1", "b": "2", "c": "3"}) || store.saves != saves+1 {
		t.Fatalf("a batch: %+v %v, %d saves", values, err, store.saves-saves)
	}
	values.Values["a"] = "changed"
	if read, _ := service.StorageGet("acme.a"); read.Values["a"] != "1" {
		t.Fatal("the answer shares state with the service")
	}

	values, err = service.StorageUpdate(ctx, "acme.a", map[string]*string{"a": nil, "b": text("two"), "d": text("4")}, false)
	if err != nil || !maps.Equal(values.Values, map[string]string{"b": "two", "c": "3", "d": "4"}) {
		t.Fatalf("null deletes within a batch: %+v %v", values, err)
	}
	values, err = service.StorageUpdate(ctx, "acme.a", map[string]*string{"setting:mode": text("b"), "c": nil}, true)
	if err != nil || !maps.Equal(values.Values, map[string]string{"setting:mode": "b"}) {
		t.Fatalf("clear then values: %+v %v", values, err)
	}
	if values, err := service.StorageUpdate(ctx, "acme.a", map[string]*string{}, true); err != nil || values.Values == nil || len(values.Values) != 0 {
		t.Fatalf("clearing alone: %+v %v", values, err)
	}
	_, _ = service.StorageUpdate(ctx, "acme.a", map[string]*string{"keep": text("1")}, false)

	saves = store.saves
	before, _ := service.StorageGet("acme.a")
	var validation domain.ValidationError
	if _, err := service.StorageUpdate(ctx, "acme.a", map[string]*string{"ok": text("1"), "big": text(strings.Repeat("v", domain.MaxStorageValueBytes+1))}, true); !errors.Is(err, ErrStorageFull) {
		t.Fatalf("an oversized value in a batch: %v", err)
	}
	if _, err := service.StorageUpdate(ctx, "acme.a", map[string]*string{"ok": text("1"), "bad key": text("1")}, true); !errors.As(err, &validation) {
		t.Fatalf("a bad key in a batch: %v", err)
	}
	if _, err := service.StorageUpdate(ctx, "acme.a", map[string]*string{}, false); !errors.As(err, &validation) || validation.Field != "values" {
		t.Fatalf("an empty batch: %v", err)
	}
	tooMany := map[string]*string{}
	for index := range domain.MaxStorageKeys + 1 {
		tooMany[fmt.Sprintf("k%02d", index)] = text("v")
	}
	if _, err := service.StorageUpdate(ctx, "acme.a", tooMany, true); !errors.As(err, &validation) || validation.Field != "values" {
		t.Fatalf("%d entries: %v", domain.MaxStorageKeys+1, err)
	}
	if after, _ := service.StorageGet("acme.a"); !maps.Equal(after.Values, before.Values) || store.saves != saves {
		t.Fatalf("a refused batch changed storage: %+v -> %+v, %d saves", before, after, store.saves-saves)
	}
	if _, err := service.StorageUpdate(ctx, "acme.missing", map[string]*string{"k": text("v")}, false); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a missing plugin: %v", err)
	}

	delete(tooMany, "k00")
	if values, err := service.StorageUpdate(ctx, "acme.a", tooMany, true); err != nil || len(values.Values) != domain.MaxStorageKeys || store.saves != saves+1 {
		t.Fatalf("%d entries: %d values, %v", domain.MaxStorageKeys, len(values.Values), err)
	}
	if len(*notifications) != announced {
		t.Fatal("a storage batch was announced")
	}
}

func TestAStorageBatchMustFitItsAnswer(t *testing.T) {
	store := &memory{}
	values := map[string]string{"a": strings.Repeat("v", 100)}
	service, _, _ := newService(t, store, 1<<20)
	ctx := context.Background()
	if _, err := service.Install(ctx, pkg("acme.a", "A", domain.PermissionStorage), nil, ""); err != nil {
		t.Fatal(err)
	}
	// Only storage answers are measured from here on.
	service.budget, _ = encodedSize(Values{Values: values})
	large := values["a"]
	if _, err := service.StorageUpdate(ctx, "acme.a", map[string]*string{"a": &large}, false); err != nil {
		t.Fatalf("an answer at the budget: %v", err)
	}
	more := large + "v"
	saves := store.saves
	if _, err := service.StorageUpdate(ctx, "acme.a", map[string]*string{"a": &more}, false); !errors.Is(err, ErrStorageFull) || store.saves != saves {
		t.Fatalf("an answer past the budget: %v", err)
	}
}
