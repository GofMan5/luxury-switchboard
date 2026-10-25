package application

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/plugins/domain"
)

type memory struct {
	mu       sync.Mutex
	plugins  []domain.Plugin
	loadErr  error
	fail     bool
	capacity bool
	saves    int
}

func (store *memory) Load(context.Context) ([]domain.Plugin, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	return deepCopy(store.plugins), store.loadErr
}

func (store *memory) Save(_ context.Context, plugins []domain.Plugin) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	switch {
	case store.capacity:
		return fmt.Errorf("document: %w", ErrCapacity)
	case store.fail:
		return errors.New("disk is full")
	}
	store.saves++
	store.plugins = deepCopy(plugins)
	return nil
}

func deepCopy(plugins []domain.Plugin) []domain.Plugin {
	copied := make([]domain.Plugin, 0, len(plugins))
	for _, plugin := range plugins {
		plugin.Package = plugin.Package.Normalized()
		plugin.Granted = slices.Clone(plugin.Granted)
		plugin.Storage = maps.Clone(plugin.Storage)
		plugin.Secrets = maps.Clone(plugin.Secrets)
		if plugin.Dev != nil {
			dev := *plugin.Dev
			plugin.Dev = &dev
		}
		copied = append(copied, plugin)
	}
	return copied
}

type clock struct{ now time.Time }

func (c *clock) tick() time.Time { c.now = c.now.Add(90 * time.Second); return c.now }

const runningVersion = "1.2.3"

func newService(t *testing.T, store *memory, budget int) (*Service, *clock, *[][]Summary) {
	t.Helper()
	service, err := NewService(store, budget, runningVersion)
	if err != nil {
		t.Fatal(err)
	}
	fake := &clock{now: time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)}
	service.now = func() time.Time { return fake.tick().Add(123 * time.Millisecond) }
	var notifications [][]Summary
	service.OnChanged(func(list []Summary) { notifications = append(notifications, list) })
	if err := service.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	return service, fake, &notifications
}

func pkg(id, name string, permissions ...string) domain.Package {
	p := domain.Package{Schema: 1, ID: id, Name: name, Version: "1.0.0", Permissions: permissions}
	if slices.Contains(permissions, domain.PermissionTheme) {
		p.Theme = &domain.Theme{Name: name, Scheme: "dark", Tokens: map[string]string{"--bg": "#000000"}}
	}
	if slices.Contains(permissions, domain.PermissionCSS) {
		p.CSS = ".x{}"
	}
	if slices.ContainsFunc(permissions, func(permission string) bool {
		return permission != domain.PermissionTheme && permission != domain.PermissionCSS
	}) {
		p.Script = "export default 1"
	}
	return p
}

func TestInstallAnswersTheSummary(t *testing.T) {
	store := &memory{}
	service, _, notifications := newService(t, store, 1<<20)
	summary, err := service.Install(context.Background(), pkg("acme.dusk", "Dusk", domain.PermissionTheme, domain.PermissionStorage), []string{domain.PermissionStorage})
	if err != nil {
		t.Fatal(err)
	}
	if summary.ID != "acme.dusk" || !summary.Enabled || !slices.Equal(summary.Granted, []string{domain.PermissionStorage}) ||
		summary.InstalledAt != "2026-03-04T05:07:37.123Z" || summary.UpdatedAt != summary.InstalledAt ||
		!summary.HasScript || summary.HasCSS || !summary.HasTheme || summary.HasSettings || summary.SizeBytes <= 0 || summary.Author != "" ||
		summary.Problem != "" || summary.Homepage != "" || summary.MinAppVersion != "" {
		t.Fatalf("unexpected summary %+v", summary)
	}
	encoded, _ := encodedSize(store.plugins[0].Package)
	if summary.SizeBytes != encoded {
		t.Fatalf("sizeBytes %d is not the encoded package size %d", summary.SizeBytes, encoded)
	}
	if len(*notifications) != 1 || (*notifications)[0][0].ID != "acme.dusk" {
		t.Fatalf("install was not announced: %+v", *notifications)
	}
	detail, err := service.Get("acme.dusk")
	if err != nil || detail.Script != "export default 1" || detail.Theme == nil || detail.CSS != "" {
		t.Fatalf("unexpected detail %+v %v", detail, err)
	}
}

func TestUpgradeKeepsStorageAndInstallTime(t *testing.T) {
	store := &memory{}
	service, _, _ := newService(t, store, 1<<20)
	ctx := context.Background()
	first, _ := service.Install(ctx, pkg("acme.tool", "Tool", domain.PermissionStorage, domain.PermissionRead), []string{domain.PermissionStorage, domain.PermissionRead})
	value := "kept"
	if _, err := service.StorageSet(ctx, "acme.tool", "k", &value); err != nil {
		t.Fatal(err)
	}
	disabled := false
	if _, err := service.Update(ctx, "acme.tool", &disabled, nil); err != nil {
		t.Fatal(err)
	}
	upgrade := pkg("acme.tool", "Tool 2", domain.PermissionStorage)
	upgrade.Version = "2.0.0"
	second, err := service.Install(ctx, upgrade, nil)
	if err != nil {
		t.Fatal(err)
	}
	if second.InstalledAt != first.InstalledAt || second.UpdatedAt == first.UpdatedAt || !second.Enabled ||
		len(second.Granted) != 0 || second.Version != "2.0.0" {
		t.Fatalf("unexpected upgrade %+v after %+v", second, first)
	}
	if values, _ := service.StorageGet("acme.tool"); values.Values["k"] != "kept" {
		t.Fatalf("an upgrade lost storage: %+v", values)
	}
}

func TestOnlyNewIDsCountAgainstTheLimit(t *testing.T) {
	service, _, _ := newService(t, &memory{}, 1<<20)
	ctx := context.Background()
	for index := range domain.MaxPlugins {
		if _, err := service.Install(ctx, pkg(fmt.Sprintf("plugin-%02d", index), "P", domain.PermissionTheme), nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := service.Install(ctx, pkg("plugin-extra", "P", domain.PermissionTheme), nil); !errors.Is(err, ErrLimit) {
		t.Fatalf("the 25th plugin: %v", err)
	}
	if _, err := service.Install(ctx, pkg("plugin-00", "Upgraded", domain.PermissionTheme), nil); err != nil {
		t.Fatalf("an upgrade at the limit was refused: %v", err)
	}
}

func TestAPluginThatCannotBeAnsweredIsRefused(t *testing.T) {
	store := &memory{}
	probe := pkg("acme.big", "Big", domain.PermissionPages)
	small, _ := encodedSize(detail(installed{plugin: domain.Plugin{Package: probe.Normalized(), Granted: []string{}, Storage: map[string]string{}}}))
	service, _, _ := newService(t, store, small+64)
	probe.Script = strings.Repeat("a", 200)
	if _, err := service.Install(context.Background(), probe, nil); !errors.Is(err, ErrTooLarge) || store.saves != 0 {
		t.Fatalf("an unanswerable plugin: %v, saves %d", err, store.saves)
	}
	probe.Script = "x"
	if _, err := service.Install(context.Background(), probe, nil); err != nil {
		t.Fatalf("a plugin within the budget: %v", err)
	}
}

func TestInvalidInstallsAreRefusedBeforeSaving(t *testing.T) {
	store := &memory{}
	service, _, notifications := newService(t, store, 1<<20)
	var validation domain.ValidationError
	if _, err := service.Install(context.Background(), domain.Package{Schema: 1, ID: "x"}, nil); !errors.As(err, &validation) {
		t.Fatalf("an invalid package: %v", err)
	}
	if _, err := service.Install(context.Background(), pkg("acme.a", "A", domain.PermissionTheme), []string{domain.PermissionWrite}); !errors.As(err, &validation) {
		t.Fatalf("an undeclared grant: %v", err)
	}
	if store.saves != 0 || len(*notifications) != 0 {
		t.Fatal("an invalid install reached storage or listeners")
	}
}

func TestUpdateAndRemove(t *testing.T) {
	store := &memory{}
	service, _, notifications := newService(t, store, 1<<20)
	ctx := context.Background()
	installed, _ := service.Install(ctx, pkg("acme.a", "A", domain.PermissionRead, domain.PermissionWrite), []string{domain.PermissionRead})
	granted := []string{domain.PermissionWrite, domain.PermissionRead}
	updated, err := service.Update(ctx, "acme.a", nil, &granted)
	if err != nil || !updated.Enabled || !slices.Equal(updated.Granted, []string{domain.PermissionRead, domain.PermissionWrite}) || updated.UpdatedAt != installed.UpdatedAt {
		t.Fatalf("unexpected update %+v %v", updated, err)
	}
	disabled := false
	if updated, _ := service.Update(ctx, "acme.a", &disabled, nil); updated.Enabled || len(updated.Granted) != 2 {
		t.Fatalf("enabled update touched the grant: %+v", updated)
	}
	if _, err := service.Update(ctx, "acme.missing", &disabled, nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("update of a missing plugin: %v", err)
	}
	bad := []string{domain.PermissionSensitive}
	if _, err := service.Update(ctx, "acme.a", nil, &bad); err == nil {
		t.Fatal("an undeclared grant was accepted")
	}
	if err := service.Remove(ctx, "acme.a"); err != nil {
		t.Fatal(err)
	}
	if err := service.Remove(ctx, "acme.a"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second removal: %v", err)
	}
	if _, err := service.Get("acme.a"); !errors.Is(err, ErrNotFound) || len(store.plugins) != 0 {
		t.Fatalf("removal did not persist: %v %+v", err, store.plugins)
	}
	if len(*notifications) != 4 || len((*notifications)[3]) != 0 {
		t.Fatalf("unexpected notifications: %+v", *notifications)
	}
}

func TestStorageReadsAndWritesQuietly(t *testing.T) {
	store := &memory{}
	service, _, notifications := newService(t, store, 1<<20)
	ctx := context.Background()
	_, _ = service.Install(ctx, pkg("acme.a", "A", domain.PermissionStorage), nil)
	before := len(*notifications)
	value := "v"
	values, err := service.StorageSet(ctx, "acme.a", "k", &value)
	if err != nil || values.Values["k"] != "v" {
		t.Fatalf("unexpected storage %+v %v", values, err)
	}
	values.Values["k"] = "changed"
	if read, _ := service.StorageGet("acme.a"); read.Values["k"] != "v" {
		t.Fatal("the answer shares state with the service")
	}
	if values, err := service.StorageSet(ctx, "acme.a", "k", nil); err != nil || len(values.Values) != 0 || values.Values == nil {
		t.Fatalf("delete: %+v %v", values, err)
	}
	if _, err := service.StorageSet(ctx, "acme.a", "absent", nil); err != nil {
		t.Fatalf("deleting a missing key: %v", err)
	}
	big := strings.Repeat("v", domain.MaxStorageValueBytes+1)
	if _, err := service.StorageSet(ctx, "acme.a", "k", &big); !errors.Is(err, ErrStorageFull) {
		t.Fatalf("an oversized value: %v", err)
	}
	var validation domain.ValidationError
	if _, err := service.StorageSet(ctx, "acme.a", "bad key", &value); !errors.As(err, &validation) {
		t.Fatalf("a bad key: %v", err)
	}
	if _, err := service.StorageSet(ctx, "acme.missing", "k", &value); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a missing plugin: %v", err)
	}
	if _, err := service.StorageGet("acme.missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a missing plugin: %v", err)
	}
	if len(*notifications) != before {
		t.Fatal("a storage write was announced")
	}
}

func TestAFailedSaveChangesNothing(t *testing.T) {
	store := &memory{}
	service, _, notifications := newService(t, store, 1<<20)
	ctx := context.Background()
	_, _ = service.Install(ctx, pkg("acme.a", "A", domain.PermissionStorage), []string{domain.PermissionStorage})
	before, _ := service.List()
	announced := len(*notifications)
	store.fail = true
	value := "v"
	disabled := false
	failures := map[string]error{}
	_, failures["install"] = service.Install(ctx, pkg("acme.b", "B", domain.PermissionTheme), nil)
	_, failures["upgrade"] = service.Install(ctx, pkg("acme.a", "A2", domain.PermissionStorage), nil)
	_, failures["update"] = service.Update(ctx, "acme.a", &disabled, nil)
	failures["remove"] = service.Remove(ctx, "acme.a")
	_, failures["storage"] = service.StorageSet(ctx, "acme.a", "k", &value)
	_, failures["storage batch"] = service.StorageUpdate(ctx, "acme.a", map[string]*string{"k": &value}, false)
	for name, err := range failures {
		if err == nil || errors.Is(err, ErrStoreUnavailable) || errors.Is(err, ErrTooLarge) || errors.Is(err, ErrStorageFull) {
			t.Errorf("%s: unexpected failure %v", name, err)
		}
	}
	after, _ := service.List()
	values, _ := service.StorageGet("acme.a")
	if !slices.EqualFunc(before, after, func(a, b Summary) bool { return fmt.Sprint(a) == fmt.Sprint(b) }) || len(values.Values) != 0 || len(*notifications) != announced {
		t.Fatalf("memory moved ahead of the file: %+v -> %+v, %+v", before, after, values)
	}
}

func TestAFullDocumentIsReportedAsTooLargeOrStorageFull(t *testing.T) {
	store := &memory{}
	service, _, _ := newService(t, store, 1<<20)
	ctx := context.Background()
	_, _ = service.Install(ctx, pkg("acme.a", "A", domain.PermissionStorage), nil)
	store.capacity = true
	if _, err := service.Install(ctx, pkg("acme.b", "B", domain.PermissionTheme), nil); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("install into a full document: %v", err)
	}
	value := "v"
	if _, err := service.StorageSet(ctx, "acme.a", "k", &value); !errors.Is(err, ErrStorageFull) {
		t.Fatalf("storage into a full document: %v", err)
	}
	if _, err := service.StorageUpdate(ctx, "acme.a", map[string]*string{"k": &value}, false); !errors.Is(err, ErrStorageFull) {
		t.Fatalf("a storage batch into a full document: %v", err)
	}
}

// A store fails whole only when it cannot be one set of addressable
// plugins; one record this version refuses is a broken entry instead.
func TestAStoreThatIsNotOneSetOfPluginsRefusesEverything(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	valid := domain.Plugin{Package: pkg("acme.a", "A", domain.PermissionTheme), Granted: []string{}, InstalledAt: now, UpdatedAt: now}
	duplicate := []domain.Plugin{valid, valid}
	unnamed := valid
	unnamed.Package.ID = "Bad Id"
	tooMany := make([]domain.Plugin, 0, domain.MaxPlugins+1)
	for index := range domain.MaxPlugins + 1 {
		record := valid
		record.Package.ID = fmt.Sprintf("acme.p%02d", index)
		tooMany = append(tooMany, record)
	}
	for name, store := range map[string]*memory{
		"unreadable":   {loadErr: errors.New("locked")},
		"unusable id":  {plugins: []domain.Plugin{valid, unnamed}},
		"duplicate id": {plugins: duplicate},
		"too many":     {plugins: tooMany},
	} {
		service, _ := NewService(store, 1<<20, runningVersion)
		if service.Load(context.Background()) == nil {
			t.Fatalf("%s: load succeeded", name)
		}
		ctx := context.Background()
		value := "v"
		enabled := true
		_, list := service.List()
		_, get := service.Get("acme.a")
		_, install := service.Install(ctx, pkg("acme.b", "B", domain.PermissionTheme), nil)
		_, update := service.Update(ctx, "acme.a", &enabled, nil)
		remove := service.Remove(ctx, "acme.a")
		_, storageGet := service.StorageGet("acme.a")
		_, storageSet := service.StorageSet(ctx, "acme.a", "k", &value)
		_, storageUpdate := service.StorageUpdate(ctx, "acme.a", map[string]*string{"k": &value}, true)
		for command, err := range map[string]error{
			"list": list, "get": get, "install": install, "update": update, "remove": remove,
			"storage.get": storageGet, "storage.set": storageSet, "storage.update": storageUpdate,
		} {
			if !errors.Is(err, ErrStoreUnavailable) {
				t.Errorf("%s %s: %v", name, command, err)
			}
		}
		if store.saves != 0 {
			t.Errorf("%s: an unreadable store was overwritten", name)
		}
	}
}

func TestListIsSortedAndSurvivesARestart(t *testing.T) {
	store := &memory{}
	service, _, _ := newService(t, store, 1<<20)
	ctx := context.Background()
	for _, p := range []domain.Package{pkg("zeta.b", "Same", domain.PermissionTheme), pkg("alpha.z", "Zed", domain.PermissionTheme), pkg("alpha.a", "Same", domain.PermissionTheme)} {
		if _, err := service.Install(ctx, p, []string{domain.PermissionTheme}); err != nil {
			t.Fatal(err)
		}
	}
	value := "v"
	_, _ = service.StorageSet(ctx, "alpha.z", "k", &value)
	list, _ := service.List()
	ids := []string{list[0].ID, list[1].ID, list[2].ID}
	if !slices.Equal(ids, []string{"alpha.a", "zeta.b", "alpha.z"}) {
		t.Fatalf("unexpected order %v", ids)
	}
	restarted, _, _ := newService(t, store, 1<<20)
	again, _ := restarted.List()
	if fmt.Sprint(again) != fmt.Sprint(list) {
		t.Fatalf("a restart changed the list:\n%+v\n%+v", list, again)
	}
	if values, _ := restarted.StorageGet("alpha.z"); values.Values["k"] != "v" {
		t.Fatal("storage did not survive a restart")
	}
}

func TestConcurrentMutationsStaySerialized(t *testing.T) {
	store := &memory{}
	service, _, _ := newService(t, store, 1<<20)
	ctx := context.Background()
	_, _ = service.Install(ctx, pkg("acme.shared", "Shared", domain.PermissionStorage), nil)
	var group sync.WaitGroup
	for index := range 12 {
		group.Add(3)
		go func() {
			defer group.Done()
			if _, err := service.Install(ctx, pkg(fmt.Sprintf("acme.p%02d", index), "P", domain.PermissionTheme), nil); err != nil {
				t.Error(err)
			}
		}()
		go func() {
			defer group.Done()
			value := fmt.Sprint(index)
			key := fmt.Sprintf("k%d", index)
			var err error
			if index%2 == 0 {
				_, err = service.StorageSet(ctx, "acme.shared", key, &value)
			} else {
				_, err = service.StorageUpdate(ctx, "acme.shared", map[string]*string{key: &value, "absent": nil}, false)
			}
			if err != nil {
				t.Error(err)
			}
		}()
		go func() {
			defer group.Done()
			_, _ = service.List()
			_, _ = service.Get("acme.shared")
			_, _ = service.StorageGet("acme.shared")
		}()
	}
	group.Wait()
	list, _ := service.List()
	values, _ := service.StorageGet("acme.shared")
	if len(list) != 13 || len(values.Values) != 12 || len(store.plugins) != 13 || len(store.plugins[0].Storage)+len(store.plugins[12].Storage) != 12 {
		t.Fatalf("lost a mutation: %d plugins, %d values, %d stored", len(list), len(values.Values), len(store.plugins))
	}
}
