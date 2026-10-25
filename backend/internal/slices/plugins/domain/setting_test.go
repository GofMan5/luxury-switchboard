package domain

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"
)

func number(value float64) *float64 { return &value }

// settingsPackage declares one setting of every type.
func settingsPackage() Package {
	pkg := validPackage()
	pkg.Settings = []Setting{
		{Key: "greeting", Label: "Greeting", Description: "Shown first.", Type: SettingText, Default: json.RawMessage(`"hi"`), Placeholder: "Say something", MaxLength: 20},
		{Key: "limit", Label: "Limit", Type: SettingNumber, Default: json.RawMessage(`5`), Min: number(1), Max: number(10)},
		{Key: "compact", Label: "Compact", Type: SettingToggle, Default: json.RawMessage(`false`)},
		{Key: "mode", Label: "Mode", Type: SettingSelect, Default: json.RawMessage(`"b"`), Options: []SettingOption{{Value: "a", Label: "A"}, {Value: "b", Label: "B"}}},
	}
	return pkg
}

func TestValidSettingsAreAccepted(t *testing.T) {
	if err := settingsPackage().Normalized().Validate(); err != nil {
		t.Fatal(err)
	}
	minimal := settingsPackage()
	minimal.Settings = []Setting{
		{Key: "a", Label: "A", Type: SettingText, Default: json.RawMessage(`""`)},
		{Key: "b", Label: "B", Type: SettingNumber, Default: json.RawMessage(`-1.5e3`)},
		{Key: "c", Label: "C", Type: SettingNumber, Default: json.RawMessage(`0`), Min: number(0)},
		{Key: "d", Label: "D", Type: SettingNumber, Default: json.RawMessage(`0`), Max: number(0)},
		{Key: "e", Label: "E", Type: SettingToggle, Default: json.RawMessage(`true`)},
		{Key: "f", Label: "F", Type: SettingSelect, Default: json.RawMessage(`"only"`), Options: []SettingOption{{Value: "only", Label: "Only"}}},
		{Key: "g", Label: "G", Type: SettingText, Default: json.RawMessage(`"` + strings.Repeat("é", MaxSettingTextRunes) + `"`)},
		{Key: "h", Label: strings.Repeat("é", MaxSettingLabelRunes), Description: strings.Repeat("é", MaxSettingDescriptionRunes), Type: SettingText, Default: json.RawMessage(`"ééé"`), MaxLength: 3},
		{Key: "a" + strings.Repeat("Z9_", 10) + "x", Label: "Longest key", Type: SettingToggle, Default: json.RawMessage(`false`)},
	}
	if err := minimal.Validate(); err != nil {
		t.Fatalf("settings at their limits were refused: %v", err)
	}
	full := settingsPackage()
	full.Settings = nil
	for index := range MaxSettings {
		full.Settings = append(full.Settings, Setting{Key: fmt.Sprintf("k%d", index), Label: "K", Type: SettingToggle, Default: json.RawMessage(`true`)})
	}
	if err := full.Validate(); err != nil {
		t.Fatalf("%d settings were refused: %v", MaxSettings, err)
	}
}

func TestEachSettingRuleIsEnforced(t *testing.T) {
	text := func(s *Setting) {
		*s = Setting{Key: s.Key, Label: s.Label, Type: SettingText, Default: json.RawMessage(`"x"`)}
	}
	for name, test := range map[string]struct {
		field  string
		mutate func(*Package)
	}{
		"too many": {"settings", func(p *Package) {
			p.Settings = nil
			for index := range MaxSettings + 1 {
				p.Settings = append(p.Settings, Setting{Key: fmt.Sprintf("k%d", index), Label: "K", Type: SettingToggle, Default: json.RawMessage(`true`)})
			}
		}},
		"without a script": {"settings", func(p *Package) {
			p.Script = ""
			p.Permissions = []string{PermissionTheme, PermissionCSS}
		}},
		"empty key":                {"settings[0].key", func(p *Package) { p.Settings[0].Key = "" }},
		"key starts upper":         {"settings[0].key", func(p *Package) { p.Settings[0].Key = "Greeting" }},
		"key starts with digit":    {"settings[0].key", func(p *Package) { p.Settings[0].Key = "1a" }},
		"key with colon":           {"settings[0].key", func(p *Package) { p.Settings[0].Key = "a:b" }},
		"key with dash":            {"settings[0].key", func(p *Package) { p.Settings[0].Key = "a-b" }},
		"key too long":             {"settings[0].key", func(p *Package) { p.Settings[0].Key = "a" + strings.Repeat("b", MaxSettingKeyBytes) }},
		"repeated key":             {"settings[2].key", func(p *Package) { p.Settings[2].Key = "greeting" }},
		"blank label":              {"settings[1].label", func(p *Package) { p.Settings[1].Label = " \u00a0 " }},
		"empty label":              {"settings[1].label", func(p *Package) { p.Settings[1].Label = "" }},
		"long label":               {"settings[1].label", func(p *Package) { p.Settings[1].Label = strings.Repeat("a", MaxSettingLabelRunes+1) }},
		"control in label":         {"settings[1].label", func(p *Package) { p.Settings[1].Label = "a\u0085" }},
		"newline in description":   {"settings[0].description", func(p *Package) { p.Settings[0].Description = "a\nb" }},
		"long description":         {"settings[0].description", func(p *Package) { p.Settings[0].Description = strings.Repeat("a", MaxSettingDescriptionRunes+1) }},
		"unknown type":             {"settings[0].type", func(p *Package) { p.Settings[0].Type = "color" }},
		"upper-case type":          {"settings[0].type", func(p *Package) { p.Settings[0].Type = "Text" }},
		"text with options":        {"settings[0].options", func(p *Package) { p.Settings[0].Options = []SettingOption{{Value: "a", Label: "A"}} }},
		"text with min":            {"settings[0].min", func(p *Package) { p.Settings[0].Min = number(0) }},
		"number with maxLength":    {"settings[1].maxLength", func(p *Package) { p.Settings[1].MaxLength = 4 }},
		"toggle with placeholder":  {"settings[2].placeholder", func(p *Package) { p.Settings[2].Placeholder = "x" }},
		"select with max":          {"settings[3].max", func(p *Package) { p.Settings[3].Max = number(1) }},
		"select with placeholder":  {"settings[3].placeholder", func(p *Package) { p.Settings[3].Placeholder = "x" }},
		"maxLength too large":      {"settings[0].maxLength", func(p *Package) { p.Settings[0].MaxLength = MaxSettingTextRunes + 1 }},
		"maxLength negative":       {"settings[0].maxLength", func(p *Package) { p.Settings[0].MaxLength = -1 }},
		"long placeholder":         {"settings[0].placeholder", func(p *Package) { p.Settings[0].Placeholder = strings.Repeat("a", MaxSettingPlaceholderRunes+1) }},
		"control in placeholder":   {"settings[1].placeholder", func(p *Package) { p.Settings[1].Placeholder = "a\tb" }},
		"text default absent":      {"settings[0].default", func(p *Package) { p.Settings[0].Default = nil }},
		"text default null":        {"settings[0].default", func(p *Package) { p.Settings[0].Default = json.RawMessage(`null`) }},
		"text default number":      {"settings[0].default", func(p *Package) { p.Settings[0].Default = json.RawMessage(`1`) }},
		"text default past length": {"settings[0].default", func(p *Package) { p.Settings[0].Default = json.RawMessage(`"` + strings.Repeat("é", 21) + `"`) }},
		"text default past cap": {"settings[0].default", func(p *Package) {
			text(&p.Settings[0])
			p.Settings[0].Default = json.RawMessage(`"` + strings.Repeat("a", MaxSettingTextRunes+1) + `"`)
		}},
		"text default control":     {"settings[0].default", func(p *Package) { p.Settings[0].Default = json.RawMessage(`"a\u0000"`) }},
		"text default newline":     {"settings[0].default", func(p *Package) { p.Settings[0].Default = json.RawMessage(`"a\nb"`) }},
		"text default two values":  {"settings[0].default", func(p *Package) { p.Settings[0].Default = json.RawMessage(`"a" "b"`) }},
		"text default malformed":   {"settings[0].default", func(p *Package) { p.Settings[0].Default = json.RawMessage(`"a`) }},
		"number min not finite":    {"settings[1].min", func(p *Package) { p.Settings[1].Min = number(math.Inf(-1)) }},
		"number max NaN":           {"settings[1].max", func(p *Package) { p.Settings[1].Max = number(math.NaN()) }},
		"number min above max":     {"settings[1].max", func(p *Package) { p.Settings[1].Min = number(11) }},
		"number default absent":    {"settings[1].default", func(p *Package) { p.Settings[1].Default = nil }},
		"number default null":      {"settings[1].default", func(p *Package) { p.Settings[1].Default = json.RawMessage(`null`) }},
		"number default string":    {"settings[1].default", func(p *Package) { p.Settings[1].Default = json.RawMessage(`"5"`) }},
		"number default below min": {"settings[1].default", func(p *Package) { p.Settings[1].Default = json.RawMessage(`0.5`) }},
		"number default above max": {"settings[1].default", func(p *Package) { p.Settings[1].Default = json.RawMessage(`10.0001`) }},
		"number default overflows": {"settings[1].default", func(p *Package) {
			p.Settings[1].Min, p.Settings[1].Max = nil, nil
			p.Settings[1].Default = json.RawMessage(`1e999`)
		}},
		"toggle default absent":  {"settings[2].default", func(p *Package) { p.Settings[2].Default = nil }},
		"toggle default null":    {"settings[2].default", func(p *Package) { p.Settings[2].Default = json.RawMessage(`null`) }},
		"toggle default string":  {"settings[2].default", func(p *Package) { p.Settings[2].Default = json.RawMessage(`"false"`) }},
		"toggle default number":  {"settings[2].default", func(p *Package) { p.Settings[2].Default = json.RawMessage(`0`) }},
		"select without options": {"settings[3].options", func(p *Package) { p.Settings[3].Options = nil }},
		"select too many options": {"settings[3].options", func(p *Package) {
			p.Settings[3].Options = nil
			for index := range MaxSettingOptions + 1 {
				p.Settings[3].Options = append(p.Settings[3].Options, SettingOption{Value: fmt.Sprint(index), Label: "O"})
			}
			p.Settings[3].Default = json.RawMessage(`"0"`)
		}},
		"option value empty":     {"settings[3].options[1].value", func(p *Package) { p.Settings[3].Options[1].Value = "" }},
		"option value blank":     {"settings[3].options[1].value", func(p *Package) { p.Settings[3].Options[1].Value = "  " }},
		"option value long":      {"settings[3].options[0].value", func(p *Package) { p.Settings[3].Options[0].Value = strings.Repeat("a", MaxSettingOptionRunes+1) }},
		"option value repeated":  {"settings[3].options[1].value", func(p *Package) { p.Settings[3].Options[1].Value = "a" }},
		"option label empty":     {"settings[3].options[0].label", func(p *Package) { p.Settings[3].Options[0].Label = "" }},
		"option label control":   {"settings[3].options[0].label", func(p *Package) { p.Settings[3].Options[0].Label = "A\u007f" }},
		"select default absent":  {"settings[3].default", func(p *Package) { p.Settings[3].Default = nil }},
		"select default null":    {"settings[3].default", func(p *Package) { p.Settings[3].Default = json.RawMessage(`null`) }},
		"select default unknown": {"settings[3].default", func(p *Package) { p.Settings[3].Default = json.RawMessage(`"c"`) }},
		"select default label":   {"settings[3].default", func(p *Package) { p.Settings[3].Default = json.RawMessage(`"B"`) }},
		"select default number":  {"settings[3].default", func(p *Package) { p.Settings[3].Default = json.RawMessage(`1`) }},
	} {
		pkg := settingsPackage().Normalized()
		test.mutate(&pkg)
		var validation ValidationError
		if err := pkg.Validate(); !errors.As(err, &validation) || validation.Field != test.field {
			t.Errorf("%s: want a %s error, got %v", name, test.field, err)
		}
	}
}

func TestSettingChecksRunInOrder(t *testing.T) {
	for name, test := range map[string]struct {
		setting Setting
		want    ValidationError
	}{
		"key before label":        {Setting{Key: "Bad", Label: ""}, ValidationError{"settings[0].key", "must be a lowercase letter then at most 31 letters, digits or underscores"}},
		"label before type":       {Setting{Key: "a", Label: "", Type: "color"}, ValidationError{"settings[0].label", "must be 1-64 characters without control characters"}},
		"type before attributes":  {Setting{Key: "a", Label: "A", Type: "color", Options: []SettingOption{{}}}, ValidationError{"settings[0].type", "must be text, number, toggle, select or secret"}},
		"unused before own rules": {Setting{Key: "a", Label: "A", Type: SettingText, MaxLength: -5, Min: number(0)}, ValidationError{"settings[0].min", "is not used by a text setting"}},
		"own rules before default": {Setting{Key: "a", Label: "A", Type: SettingSelect},
			ValidationError{"settings[0].options", "must list 1-32 options"}},
		"absent default": {Setting{Key: "a", Label: "A", Type: SettingToggle}, ValidationError{"settings[0].default", "is required"}},
		"null default":   {Setting{Key: "a", Label: "A", Type: SettingToggle, Default: json.RawMessage(`null`)}, ValidationError{"settings[0].default", "must be true or false"}},
	} {
		pkg := settingsPackage()
		pkg.Settings = []Setting{test.setting}
		if err := pkg.Validate(); err != test.want {
			t.Errorf("%s: want %q, got %v", name, test.want, err)
		}
	}
}

func TestSettingsSurviveNormalizationAndJSON(t *testing.T) {
	original := settingsPackage()
	original.Settings[0].Default = json.RawMessage(" \"<b>&\\u00e9\" ")
	original.Settings[1].Default = json.RawMessage(`5.50`)
	normalized := original.Normalized()
	if string(normalized.Settings[0].Default) != `"<b>&é"` || string(normalized.Settings[1].Default) != `5.50` {
		t.Fatalf("defaults are not canonical: %s %s", normalized.Settings[0].Default, normalized.Settings[1].Default)
	}
	if string(original.Settings[0].Default) != " \"<b>&\\u00e9\" " {
		t.Fatal("Normalized changed its receiver")
	}
	normalized.Settings[1].Default[0] = '9'
	*normalized.Settings[1].Min = 7
	normalized.Settings[3].Options[0].Label = "changed"
	if string(original.Settings[1].Default) != `5.50` || *original.Settings[1].Min != 1 || original.Settings[3].Options[0].Label != "A" {
		t.Fatal("Normalized shares settings with its receiver")
	}
	if normalized := original.Normalized(); !reflect.DeepEqual(normalized, normalized.Normalized()) {
		t.Fatal("Normalized is not idempotent")
	}

	pkg := settingsPackage().Normalized()
	before := fmt.Sprintf("%+v", pkg)
	if err := pkg.Validate(); err != nil || fmt.Sprintf("%+v", pkg) != before {
		t.Fatalf("Validate failed or changed the package: %v", err)
	}
	encoded, err := json.Marshal(pkg)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Package
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded.Normalized(), pkg) {
		t.Fatalf("settings did not survive JSON:\n%+v\n%+v", decoded.Normalized(), pkg)
	}
	if again, _ := json.Marshal(decoded.Normalized()); string(again) != string(encoded) {
		t.Fatalf("a stored package does not read back byte for byte:\n%s\n%s", encoded, again)
	}
	if !strings.Contains(string(encoded), `{"key":"compact","label":"Compact","type":"toggle","default":false}`) {
		t.Fatalf("a toggle encodes unexpected attributes: %s", encoded)
	}
	bare := Package{Schema: 1, ID: "abc", Name: "A", Version: "1"}.Normalized()
	if encoded, _ := json.Marshal(bare); strings.Contains(string(encoded), "settings") || strings.Contains(string(encoded), "homepage") || strings.Contains(string(encoded), "minAppVersion") {
		t.Fatalf("absent optional fields are encoded: %s", encoded)
	}
}

func TestTheShellsNormalizedSettingsAreAccepted(t *testing.T) {
	var pkg Package
	payload := `{"schema":1,"id":"acme.tool","name":"Tool","version":"1","permissions":["storage"],"script":"x","settings":[` +
		`{"key":"a","label":"A","description":"","type":"text","default":"","placeholder":"","maxLength":256},` +
		`{"key":"b","label":"B","description":"","type":"number","default":0,"placeholder":""},` +
		`{"key":"c","label":"C","description":"","type":"number","default":2,"placeholder":"","min":1,"max":3},` +
		`{"key":"d","label":"D","description":"","type":"toggle","default":true},` +
		`{"key":"e","label":"E","description":"","type":"select","default":"x","options":[{"value":"x","label":"X"}]}]}`
	if err := json.Unmarshal([]byte(payload), &pkg); err != nil {
		t.Fatal(err)
	}
	if err := pkg.Normalized().Validate(); err != nil {
		t.Fatalf("the shell's normalized form was refused: %v", err)
	}
}

func TestHomepageAndMinAppVersion(t *testing.T) {
	for _, homepage := range []string{
		"", "https://example.test", "https://example.test/", "https://example.test:8443/a?b=c#d", "https://[::1]/x",
		"https://example.test/" + strings.Repeat("a", MaxHomepageBytes-len("https://example.test/")),
	} {
		pkg := validPackage()
		pkg.Homepage = homepage
		if err := pkg.Validate(); err != nil {
			t.Errorf("%q was refused: %v", homepage, err)
		}
	}
	for _, homepage := range []string{
		"http://example.test", "HTTPS://example.test", "https:example.test", "https:/example.test", "https://",
		"https:///path", "https://user@example.test", "https://user:pass@example.test", "https://@example.test",
		"https://exa mple.test", "https://example.test/\t", "https://example.test/\u2028", "https://example.test/\x7f",
		"https://example.test/%zz", "javascript:alert(1)", " https://example.test", "https://example.test/\xff",
		"https://example.test/" + strings.Repeat("a", MaxHomepageBytes-len("https://example.test/")+1),
	} {
		pkg := validPackage()
		pkg.Homepage = homepage
		want := ValidationError{"homepage", "must be an https:// address of at most 200 characters"}
		if err := pkg.Validate(); err != want {
			t.Errorf("%q: want %v, got %v", homepage, want, err)
		}
	}
	for _, version := range []string{"", "0.0.0", "1.0.64", "9999.9999.9999"} {
		pkg := validPackage()
		pkg.MinAppVersion = version
		if err := pkg.Validate(); err != nil {
			t.Errorf("minAppVersion %q was refused: %v", version, err)
		}
	}
	for _, version := range []string{"1", "1.0", "1.0.0.0", "v1.0.0", "1.0.0-beta", "10000.0.0", "1..0", " 1.0.0", "1.0.0\n", "١.٠.٠"} {
		pkg := validPackage()
		pkg.MinAppVersion = version
		var validation ValidationError
		if err := pkg.Validate(); !errors.As(err, &validation) || validation.Field != "minAppVersion" {
			t.Errorf("minAppVersion %q: %v", version, err)
		}
	}
}

func TestNeedsNewerAppComparesNumbers(t *testing.T) {
	for _, test := range []struct {
		minimum, running string
		newer            bool
	}{
		{"", "1.0.64", false},
		{"1.0.64", "1.0.64", false},
		{"1.0.9", "1.0.64", false},
		{"1.0.65", "1.0.64", true},
		{"1.1.0", "1.0.64", true},
		{"0.9.999", "1.0.0", false},
		{"2.0.0", "1.9999.9999", true},
		{"01.0.064", "1.0.64", false},
	} {
		if got := (Package{MinAppVersion: test.minimum}).NeedsNewerApp(test.running); got != test.newer {
			t.Errorf("%q on %q: got %v", test.minimum, test.running, got)
		}
	}
}

func TestClipboardIsAScriptPermission(t *testing.T) {
	pkg := Package{Schema: 1, ID: "acme.clip", Name: "Clip", Version: "1", Permissions: []string{PermissionClipboard}, Script: "x"}
	if err := pkg.Validate(); err != nil {
		t.Fatalf("ui:clipboard was refused: %v", err)
	}
	if granted, err := pkg.Grant([]string{PermissionClipboard}); err != nil || len(granted) != 1 {
		t.Fatalf("ui:clipboard could not be granted: %v %v", granted, err)
	}
	pkg.Script = ""
	if err := pkg.Validate(); err != (ValidationError{"permissions", "declare script capabilities without a script"}) {
		t.Fatalf("ui:clipboard without a script: %v", err)
	}
}

func TestListedBlanksWhatBreaksItsOwnRule(t *testing.T) {
	broken := Package{
		Schema: 9, ID: "acme.old", Name: "\x00", Version: "bad version", Author: strings.Repeat("a", MaxAuthorRunes+1),
		Description: "fine", Homepage: "http://x", MinAppVersion: "v1", Permissions: []string{"fs:write"}, Script: "x",
	}
	listed := broken.Listed()
	if listed.ID != "acme.old" || listed.Name != "acme.old" || listed.Version != "" || listed.Author != "" || listed.Description != "fine" ||
		listed.Homepage != "" || listed.MinAppVersion != "" || listed.Permissions == nil || len(listed.Permissions) != 0 || listed.Script != "x" {
		t.Fatalf("unexpected listing %+v", listed)
	}
	if broken.Name != "\x00" || broken.Permissions[0] != "fs:write" {
		t.Fatal("Listed changed its receiver")
	}
	healthy := settingsPackage().Normalized()
	if !reflect.DeepEqual(healthy.Listed(), healthy) {
		t.Fatal("Listed changed a valid package")
	}
}

func TestStorageChangesApplyTogether(t *testing.T) {
	value := func(text string) *string { return &text }
	current := map[string]string{"a": "1", "setting:mode": "b"}
	next, err := WithStorageChanges(current, map[string]*string{"a": nil, "b": value("2"), "missing": nil}, false)
	if err != nil || !reflect.DeepEqual(next, map[string]string{"b": "2", "setting:mode": "b"}) || current["a"] != "1" || len(current) != 2 {
		t.Fatalf("unexpected batch %v %v (input %v)", next, err, current)
	}
	cleared, err := WithStorageChanges(current, map[string]*string{"setting:mode": value("b")}, true)
	if err != nil || !reflect.DeepEqual(cleared, map[string]string{"setting:mode": "b"}) {
		t.Fatalf("clear then set: %v %v", cleared, err)
	}
	if empty, err := WithStorageChanges(current, map[string]*string{}, true); err != nil || empty == nil || len(empty) != 0 {
		t.Fatalf("clearing alone: %v %v", empty, err)
	}
	var validation ValidationError
	if _, err := WithStorageChanges(current, map[string]*string{}, false); !errors.As(err, &validation) || validation.Field != "values" {
		t.Fatalf("an empty batch: %v", err)
	}
	tooMany := map[string]*string{}
	for index := range MaxStorageKeys + 1 {
		tooMany[fmt.Sprintf("k%d", index)] = nil
	}
	if _, err := WithStorageChanges(nil, tooMany, true); !errors.As(err, &validation) || validation.Field != "values" {
		t.Fatalf("%d changes: %v", MaxStorageKeys+1, err)
	}
	delete(tooMany, "k0")
	if next, err := WithStorageChanges(nil, tooMany, false); err != nil || len(next) != 0 {
		t.Fatalf("%d removals: %v %v", MaxStorageKeys, next, err)
	}
	if _, err := WithStorageChanges(current, map[string]*string{"ok": value("1"), "bad key": value("1")}, false); !errors.As(err, &validation) || validation.Field != "key" {
		t.Fatalf("a bad key: %v", err)
	}
	if _, err := WithStorageChanges(current, map[string]*string{"bad key": nil}, false); !errors.As(err, &validation) {
		t.Fatalf("removing a bad key: %v", err)
	}
	heavy := map[string]*string{}
	for index := range 8 {
		heavy[fmt.Sprintf("k%d", index)] = value(strings.Repeat("v", MaxStorageValueBytes-2))
	}
	if _, err := WithStorageChanges(nil, heavy, false); err != nil {
		t.Fatalf("a batch just under the total: %v", err)
	}
	heavy["k8"] = value("v")
	if _, err := WithStorageChanges(nil, heavy, false); !errors.Is(err, ErrStorageFull) {
		t.Fatalf("a batch past the total: %v", err)
	}
	if _, err := WithStorageChanges(nil, map[string]*string{"k": value(strings.Repeat("v", MaxStorageValueBytes+1))}, false); !errors.Is(err, ErrStorageFull) {
		t.Fatalf("an oversized value: %v", err)
	}
	full := map[string]string{}
	for index := range MaxStorageKeys {
		full[fmt.Sprintf("k%d", index)] = "v"
	}
	if _, err := WithStorageChanges(full, map[string]*string{"extra": value("v")}, false); !errors.Is(err, ErrStorageFull) {
		t.Fatalf("a key past the count: %v", err)
	}
	if next, err := WithStorageChanges(full, map[string]*string{"extra": value("v"), "k0": nil}, false); err != nil || len(next) != MaxStorageKeys {
		t.Fatalf("a swap within the count: %v", err)
	}
}
