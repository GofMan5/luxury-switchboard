package pluginsstdio

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/plugins/domain"
)

// settingsPackage carries one setting of every type in the shape the shell
// sends: normalized, with empty descriptions and placeholders spelled out.
const settingsPackage = `{"schema":1,"id":"acme.tuned","name":"Tuned","version":"1.0.0",` +
	`"homepage":"https://example.test/tuned","minAppVersion":"1.2.3","permissions":["storage"],"script":"export default 1","settings":[` +
	`{"key":"greeting","label":"Greeting","description":"","type":"text","default":"<hi>","placeholder":"","maxLength":20},` +
	`{"key":"limit","label":"Limit","description":"How many.","type":"number","default":5,"placeholder":"","min":1,"max":10},` +
	`{"key":"compact","label":"Compact","description":"","type":"toggle","default":false},` +
	`{"key":"mode","label":"Mode","description":"","type":"select","default":"b","options":[{"value":"a","label":"A"},{"value":"b","label":"B"}]}]}`

// installedSettings is how plugins.get answers those settings: empty
// attributes are left out and the default keeps its JSON type.
const installedSettings = `[` +
	`{"key":"greeting","label":"Greeting","type":"text","default":"<hi>","maxLength":20},` +
	`{"key":"limit","label":"Limit","description":"How many.","type":"number","default":5,"min":1,"max":10},` +
	`{"key":"compact","label":"Compact","type":"toggle","default":false},` +
	`{"key":"mode","label":"Mode","type":"select","default":"b","options":[{"value":"a","label":"A"},{"value":"b","label":"B"}]}]`

func TestSettingsRoundTripThroughTheWire(t *testing.T) {
	store := &memory{}
	h := newHarness(t, store)
	var installed struct {
		Plugin struct {
			Homepage      string `json:"homepage"`
			MinAppVersion string `json:"minAppVersion"`
			HasSettings   bool   `json:"hasSettings"`
			Problem       string `json:"problem"`
			UpdatedAt     string `json:"updatedAt"`
		} `json:"plugin"`
	}
	answer := h.ok("plugins.install", `{"package":`+settingsPackage+`,"granted":["storage"]}`)
	if json.Unmarshal(answer, &installed) != nil || installed.Plugin.Homepage != "https://example.test/tuned" ||
		installed.Plugin.MinAppVersion != "1.2.3" || !installed.Plugin.HasSettings || installed.Plugin.Problem != "" {
		t.Fatalf("unexpected install answer: %s", answer)
	}
	if updated, err := time.Parse(time.RFC3339Nano, installed.Plugin.UpdatedAt); err != nil || updated.Location() != time.UTC {
		t.Fatalf("updatedAt %q is not an RFC 3339 UTC time: %v", installed.Plugin.UpdatedAt, err)
	}
	var detail struct {
		Settings json.RawMessage `json:"settings"`
	}
	answer = h.ok("plugins.get", `{"id":"acme.tuned"}`)
	if json.Unmarshal(answer, &detail) != nil || string(detail.Settings) != installedSettings {
		t.Fatalf("unexpected settings:\n%s\nwant\n%s", detail.Settings, installedSettings)
	}

	restarted := newHarness(t, store)
	if again := restarted.ok("plugins.get", `{"id":"acme.tuned"}`); !bytes.Equal(again, answer) {
		t.Fatalf("a restart changed the detail:\n%s\n%s", again, answer)
	}
}

func TestMalformedSettingsAndNewFieldsAreRefused(t *testing.T) {
	h := newHarness(t, &memory{})
	for name, pkg := range map[string]string{
		"unknown setting field":   strings.Replace(settingsPackage, `"key":"compact",`, `"key":"compact","hint":"x",`, 1),
		"unknown option field":    strings.Replace(settingsPackage, `{"value":"a","label":"A"}`, `{"value":"a","label":"A","icon":"x"}`, 1),
		"null default":            strings.Replace(settingsPackage, `"default":false`, `"default":null`, 1),
		"missing default":         strings.Replace(settingsPackage, `"type":"toggle","default":false`, `"type":"toggle"`, 1),
		"string for a number":     strings.Replace(settingsPackage, `"default":5`, `"default":"5"`, 1),
		"attribute of other type": strings.Replace(settingsPackage, `"type":"toggle","default":false`, `"type":"toggle","default":false,"maxLength":3`, 1),
		"settings without script": strings.Replace(strings.Replace(settingsPackage, `"script":"export default 1",`, ``, 1), `"permissions":["storage"]`, `"permissions":[]`, 1),
		"plain http homepage":     strings.Replace(settingsPackage, `"https://example.test/tuned"`, `"http://example.test/tuned"`, 1),
		"homepage credentials":    strings.Replace(settingsPackage, `"https://example.test/tuned"`, `"https://user@example.test/tuned"`, 1),
		"short app version":       strings.Replace(settingsPackage, `"minAppVersion":"1.2.3"`, `"minAppVersion":"1.2"`, 1),
		"newer app version":       strings.Replace(settingsPackage, `"minAppVersion":"1.2.3"`, `"minAppVersion":"1.2.4"`, 1),
		"too many settings":       manySettings(domain.MaxSettings + 1),
	} {
		result, events := h.call("plugins.install", `{"package":`+pkg+`}`)
		if result.OK || result.Error == nil || result.Error.Code != "invalid_plugin" || len(events) != 0 {
			t.Errorf("%s: want invalid_plugin, got ok=%v %+v", name, result.OK, result.Error)
		}
	}
	result, _ := h.call("plugins.install", `{"package":`+strings.Replace(settingsPackage, `"minAppVersion":"1.2.3"`, `"minAppVersion":"1.2.4"`, 1)+`}`)
	if result.Error == nil || result.Error.Message != "Plugin minAppVersion needs Luxury Switchboard 1.2.4 or later" {
		t.Fatalf("unexpected newer-app refusal: %+v", result.Error)
	}
	h.ok("plugins.install", `{"package":`+manySettings(domain.MaxSettings)+`}`)
}

func manySettings(count int) string {
	settings := make([]string, count)
	for index := range settings {
		settings[index] = fmt.Sprintf(`{"key":"s%d","label":"S","type":"toggle","default":true}`, index)
	}
	return `{"schema":1,"id":"acme.many","name":"Many","version":"1","permissions":[],"script":"export default 1","settings":[` + strings.Join(settings, ",") + `]}`
}

func TestStorageUpdateOnTheWire(t *testing.T) {
	h := newHarness(t, &memory{})
	h.ok("plugins.install", `{"package":`+themePackage+`}`)
	h.ok("plugins.storage.set", `{"id":"acme.dusk","key":"old","value":"1"}`)

	result, events := h.call("plugins.storage.update", `{"id":"acme.dusk","values":{"setting:mode":"b","setting:limit":"5","old":null}}`)
	if !result.OK || string(result.Payload) != `{"values":{"setting:limit":"5","setting:mode":"b"}}` || len(events) != 0 {
		t.Fatalf("unexpected batch: %s %+v %d", result.Payload, result.Error, len(events))
	}
	if values := h.ok("plugins.storage.update", `{"id":"acme.dusk","values":{"fresh":"1"},"clear":true}`); string(values) != `{"values":{"fresh":"1"}}` {
		t.Fatalf("unexpected clear: %s", values)
	}
	if values := h.ok("plugins.storage.update", `{"id":"acme.dusk","values":{},"clear":true}`); string(values) != `{"values":{}}` {
		t.Fatalf("unexpected clear alone: %s", values)
	}

	entries := make([]string, domain.MaxStorageKeys+1)
	for index := range entries {
		entries[index] = fmt.Sprintf(`"k%02d":"v"`, index)
	}
	h.refused("plugins.storage.update", `{"id":"acme.dusk","values":{`+strings.Join(entries, ",")+`}}`, "invalid_plugin")
	h.refused("plugins.storage.update", `{"id":"acme.dusk","values":{"a":"1","bad key":"1"}}`, "invalid_plugin")
	h.refused("plugins.storage.update", `{"id":"acme.dusk","values":{"a":"1","b":"`+strings.Repeat("v", domain.MaxStorageValueBytes+1)+`"}}`, "plugin_storage_full")
	if values := h.ok("plugins.storage.get", `{"id":"acme.dusk"}`); string(values) != `{"values":{}}` {
		t.Fatalf("a refused batch changed storage: %s", values)
	}
	if values := h.ok("plugins.storage.update", `{"id":"acme.dusk","values":{`+strings.Join(entries[1:], ",")+`}}`); strings.Count(string(values), `":"v"`) != domain.MaxStorageKeys {
		t.Fatalf("a full batch: %s", values)
	}
}

func TestABrokenPluginIsListedAndRefused(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	stale := domain.Plugin{
		Package: domain.Package{Schema: 1, ID: "acme.stale", Name: "Stale", Version: "1", Permissions: []string{}, MinAppVersion: "9.0.0"},
		Granted: []string{}, Enabled: true, InstalledAt: now, UpdatedAt: now, Storage: map[string]string{},
	}
	h := newHarness(t, &memory{plugins: []domain.Plugin{stale}})
	list := h.ok("plugins.list", "")
	if !bytes.Contains(list, []byte(`"enabled":false`)) ||
		!bytes.Contains(list, []byte(`"problem":"Needs Luxury Switchboard 9.0.0 or later; this is 1.2.3. Update the app or install an older version of the plugin."`)) {
		t.Fatalf("unexpected list: %s", list)
	}
	for method, payload := range map[string]string{
		"plugins.get":            `{"id":"acme.stale"}`,
		"plugins.update":         `{"id":"acme.stale","enabled":true}`,
		"plugins.storage.get":    `{"id":"acme.stale"}`,
		"plugins.storage.set":    `{"id":"acme.stale","key":"k","value":"v"}`,
		"plugins.storage.update": `{"id":"acme.stale","values":{"k":"v"}}`,
	} {
		h.refused(method, payload, "invalid_plugin")
	}
	result, _ := h.call("plugins.get", `{"id":"acme.stale"}`)
	if result.Error.Message != "Plugin cannot be used by this version; reinstall it or remove it" {
		t.Fatalf("unexpected message: %q", result.Error.Message)
	}
	if answer := h.ok("plugins.install", `{"package":{"schema":1,"id":"acme.stale","name":"Stale","version":"2","permissions":[]}}`); !bytes.Contains(answer, []byte(`"problem":""`)) {
		t.Fatalf("a reinstall did not repair the plugin: %s", answer)
	}
	if removed := h.ok("plugins.remove", `{"id":"acme.stale"}`); string(removed) != `{"removed":true}` {
		t.Fatalf("unexpected removal: %s", removed)
	}
}
