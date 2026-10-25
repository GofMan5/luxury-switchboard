package domain

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

func validPackage() Package {
	return Package{
		Schema:      SchemaVersion,
		ID:          "acme.dusk",
		Name:        "Dusk",
		Version:     "1.2.0-beta+3",
		Author:      "Fixture Author",
		Description: "A fixture theme.\nSecond line.",
		Permissions: []string{PermissionTheme, PermissionCSS, PermissionStorage},
		Theme:       &Theme{Name: "Dusk", Scheme: "dark", Tokens: map[string]string{"--bg": "#101010"}},
		CSS:         ".fixture { color: var(--bg); }",
		Script:      "export default () => {}",
	}
}

func TestAValidPackageIsAccepted(t *testing.T) {
	if err := validPackage().Normalized().Validate(); err != nil {
		t.Fatal(err)
	}
	minimal := Package{Schema: SchemaVersion, ID: "abc", Name: "A", Version: "1"}.Normalized()
	if err := minimal.Validate(); err != nil || minimal.Permissions == nil {
		t.Fatalf("a minimal package was refused: %v %+v", err, minimal)
	}
	themeOnly := Package{Schema: 1, ID: "theme.only", Name: "T", Version: "1", Permissions: []string{PermissionTheme}, Theme: &Theme{Name: "T", Scheme: "light"}}
	if err := themeOnly.Normalized().Validate(); err != nil {
		t.Fatalf("a theme with no tokens was refused: %v", err)
	}
}

func TestNormalizedSharesNothing(t *testing.T) {
	original := validPackage()
	normalized := original.Normalized()
	normalized.Permissions[0] = "changed"
	normalized.Theme.Tokens["--bg"] = "changed"
	normalized.Theme.Name = "changed"
	if original.Permissions[0] != PermissionTheme || original.Theme.Tokens["--bg"] != "#101010" || original.Theme.Name != "Dusk" {
		t.Fatal("Normalized shares state with its receiver")
	}
}

func TestEachPackageRuleIsEnforced(t *testing.T) {
	for name, test := range map[string]struct {
		field  string
		mutate func(*Package)
	}{
		"schema":              {"schema", func(p *Package) { p.Schema = 2 }},
		"id too short":        {"id", func(p *Package) { p.ID = "ab" }},
		"id uppercase":        {"id", func(p *Package) { p.ID = "Acme.dusk" }},
		"id trailing dot":     {"id", func(p *Package) { p.ID = "acme." }},
		"id too long":         {"id", func(p *Package) { p.ID = strings.Repeat("a", 65) }},
		"blank name":          {"name", func(p *Package) { p.Name = "   " }},
		"long name":           {"name", func(p *Package) { p.Name = strings.Repeat("é", MaxNameRunes+1) }},
		"control in name":     {"name", func(p *Package) { p.Name = "A\x00" }},
		"newline in name":     {"name", func(p *Package) { p.Name = "A\nB" }},
		"bad version":         {"version", func(p *Package) { p.Version = "1 0" }},
		"long version":        {"version", func(p *Package) { p.Version = strings.Repeat("1", MaxVersionBytes+1) }},
		"long author":         {"author", func(p *Package) { p.Author = strings.Repeat("a", MaxAuthorRunes+1) }},
		"control in author":   {"author", func(p *Package) { p.Author = "a\tb" }},
		"long description":    {"description", func(p *Package) { p.Description = strings.Repeat("a", MaxDescriptionRunes+1) }},
		"tab in description":  {"description", func(p *Package) { p.Description = "a\tb" }},
		"unknown permission":  {"permissions", func(p *Package) { p.Permissions = append(p.Permissions, "fs:write") }},
		"repeated permission": {"permissions", func(p *Package) { p.Permissions = append(p.Permissions, PermissionCSS) }},
		"theme name":          {"theme.name", func(p *Package) { p.Theme.Name = "" }},
		"theme scheme":        {"theme.scheme", func(p *Package) { p.Theme.Scheme = "dim" }},
		"theme tokens":        {"theme.tokens", func(p *Package) { p.Theme.Tokens = map[string]string{"--bg": "url(x)"} }},
		"css size":            {"css", func(p *Package) { p.CSS = strings.Repeat("a", MaxCSSBytes+1) }},
		"css import":          {"css", func(p *Package) { p.CSS = "@import 'x';" }},
		"script size":         {"script", func(p *Package) { p.Script = strings.Repeat("a", MaxScriptBytes+1) }},
		"theme without grant": {"theme", func(p *Package) { p.Permissions = []string{PermissionCSS, PermissionStorage} }},
		"css without grant":   {"css", func(p *Package) { p.Permissions = []string{PermissionTheme, PermissionStorage} }},
		"script capabilities": {"permissions", func(p *Package) { p.Script = "" }},
		"control without code": {"permissions", func(p *Package) {
			p.Script = ""
			p.Permissions = []string{PermissionTheme, PermissionCSS, PermissionRead}
		}},
	} {
		pkg := validPackage().Normalized()
		test.mutate(&pkg)
		var validation ValidationError
		if err := pkg.Validate(); !errors.As(err, &validation) || validation.Field != test.field {
			t.Errorf("%s: want a %s error, got %v", name, test.field, err)
		}
	}
	tooMany := validPackage()
	tooMany.Permissions = slices.Repeat([]string{PermissionStorage}, MaxPermissions+1)
	if err := tooMany.Validate(); err == nil {
		t.Fatal("too many permissions were accepted")
	}
	atLimits := validPackage()
	atLimits.Name = strings.Repeat("é", MaxNameRunes)
	atLimits.CSS = strings.Repeat("a", MaxCSSBytes)
	atLimits.Script = strings.Repeat("a", MaxScriptBytes)
	if err := atLimits.Validate(); err != nil {
		t.Fatalf("values at their limits were refused: %v", err)
	}
}

func TestGrantKeepsDeclaredOrderAndNothingMore(t *testing.T) {
	pkg := validPackage()
	granted, err := pkg.Grant([]string{PermissionStorage, PermissionTheme})
	if err != nil || !slices.Equal(granted, []string{PermissionTheme, PermissionStorage}) {
		t.Fatalf("unexpected grant %v %v", granted, err)
	}
	if granted, err := pkg.Grant(nil); err != nil || granted == nil || len(granted) != 0 {
		t.Fatalf("an empty grant is not an empty list: %v %v", granted, err)
	}
	for name, grant := range map[string][]string{
		"undeclared": {PermissionWrite},
		"repeated":   {PermissionTheme, PermissionTheme},
		"too many":   {PermissionTheme, PermissionCSS, PermissionStorage, PermissionTheme},
	} {
		if _, err := pkg.Grant(grant); err == nil {
			t.Errorf("%s grant was accepted", name)
		}
	}
}

func TestStorageBounds(t *testing.T) {
	value := func(text string) *string { return &text }
	values, err := WithStorage(nil, "prefs:theme", value("dark"))
	if err != nil || values["prefs:theme"] != "dark" {
		t.Fatalf("unexpected storage %v %v", values, err)
	}
	next, err := WithStorage(values, "prefs:theme", nil)
	if err != nil || len(next) != 0 || values["prefs:theme"] != "dark" {
		t.Fatalf("delete changed the input or failed: %v %v %v", next, values, err)
	}
	if next, err := WithStorage(values, "missing", nil); err != nil || len(next) != 1 {
		t.Fatalf("deleting a missing key is not a no-op: %v %v", next, err)
	}
	var validation ValidationError
	if _, err := WithStorage(nil, "bad key", value("x")); !errors.As(err, &validation) {
		t.Fatalf("a bad key is not a validation error: %v", err)
	}
	if _, err := WithStorage(nil, strings.Repeat("k", 65), nil); !errors.As(err, &validation) {
		t.Fatalf("a long key is not a validation error: %v", err)
	}
	if _, err := WithStorage(nil, "k", value(strings.Repeat("v", MaxStorageValueBytes+1))); !errors.Is(err, ErrStorageFull) {
		t.Fatalf("an oversized value: %v", err)
	}
	if _, err := WithStorage(nil, "k", value(strings.Repeat("v", MaxStorageValueBytes))); err != nil {
		t.Fatalf("a value at the limit: %v", err)
	}
	full := map[string]string{}
	for index := range MaxStorageKeys {
		full[fmt.Sprintf("k%d", index)] = "v"
	}
	if _, err := WithStorage(full, "one-more", value("v")); !errors.Is(err, ErrStorageFull) {
		t.Fatalf("a key past the count: %v", err)
	}
	if _, err := WithStorage(full, "k0", value("replaced")); err != nil {
		t.Fatalf("replacing a key in a full map: %v", err)
	}
	heavy := map[string]string{}
	for index := range 8 {
		heavy[fmt.Sprintf("k%d", index)] = strings.Repeat("v", MaxStorageValueBytes-2)
	}
	if err := CheckStorage(heavy); err != nil {
		t.Fatalf("storage just under the total: %v", err)
	}
	if _, err := WithStorage(heavy, "k8", value("v")); !errors.Is(err, ErrStorageFull) {
		t.Fatalf("storage past the total: %v", err)
	}
}

// Plugin.Validate decides which stored records the service keeps as broken
// entries; its error is the field and rule the owner is shown.
func TestValidateNamesWhatMakesAStoredPluginBroken(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	plugin := Plugin{Package: settingsPackage().Normalized(), Enabled: true, Granted: []string{PermissionTheme}, InstalledAt: now, UpdatedAt: now, Storage: map[string]string{}}
	if err := plugin.Validate(); err != nil {
		t.Fatal(err)
	}
	full := map[string]string{}
	for index := range MaxStorageKeys + 1 {
		full[fmt.Sprintf("k%d", index)] = "v"
	}
	for name, test := range map[string]struct {
		field  string
		mutate func(*Plugin)
	}{
		"unordered grant":  {"granted", func(p *Plugin) { p.Granted = []string{PermissionStorage, PermissionTheme} }},
		"undeclared grant": {"granted", func(p *Plugin) { p.Granted = []string{PermissionWrite} }},
		"no install time":  {"timestamps", func(p *Plugin) { p.InstalledAt = time.Time{} }},
		"no update time":   {"timestamps", func(p *Plugin) { p.UpdatedAt = time.Time{} }},
		"time reversed":    {"timestamps", func(p *Plugin) { p.UpdatedAt = now.Add(-time.Millisecond) }},
		"bad storage key":  {"key", func(p *Plugin) { p.Storage = map[string]string{"bad key": "v"} }},
		"storage overfull": {"", func(p *Plugin) { p.Storage = full }},
		"old schema":       {"schema", func(p *Plugin) { p.Package.Schema = 0 }},
		"retired setting":  {"settings[2].type", func(p *Plugin) { p.Package.Settings[2].Type = "switch" }},
		"bad homepage":     {"homepage", func(p *Plugin) { p.Package.Homepage = "http://example.test" }},
	} {
		copied := plugin
		copied.Package = plugin.Package.Normalized()
		test.mutate(&copied)
		err := copied.Validate()
		var validation ValidationError
		switch {
		case test.field == "" && !errors.Is(err, ErrStorageFull):
			t.Errorf("%s: want ErrStorageFull, got %v", name, err)
		case test.field != "" && (!errors.As(err, &validation) || validation.Field != test.field):
			t.Errorf("%s: want a %s error, got %v", name, test.field, err)
		}
	}
}
