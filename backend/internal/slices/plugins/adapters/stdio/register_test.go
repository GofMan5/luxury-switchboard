package pluginsstdio

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"

	platform "github.com/luxuryprivate/switchboard/backend/internal/platform/stdio"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/plugins/application"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/plugins/domain"
)

type memory struct {
	plugins []domain.Plugin
	loadErr error
	fail    bool
}

func (store *memory) Load(context.Context) ([]domain.Plugin, error) {
	return store.plugins, store.loadErr
}

func (store *memory) Save(_ context.Context, plugins []domain.Plugin) error {
	if store.fail {
		return errors.New("disk is full")
	}
	store.plugins = plugins
	return nil
}

type frame struct {
	Type    string             `json:"type"`
	ID      string             `json:"id"`
	Topic   string             `json:"topic"`
	OK      bool               `json:"ok"`
	Payload json.RawMessage    `json:"payload"`
	Error   *platform.RPCError `json:"error"`
}

type harness struct {
	t       *testing.T
	service *application.Service
}

func newHarness(t *testing.T, store *memory) harness {
	t.Helper()
	service, err := application.NewService(store, platform.MaxPayloadBytes)
	if err != nil {
		t.Fatal(err)
	}
	_ = service.Load(context.Background())
	return harness{t: t, service: service}
}

// call sends one command through a real server and returns its result and
// every event the command caused.
func (h harness) call(method, payload string) (frame, []frame) {
	h.t.Helper()
	request := `{"v":1,"id":"test","type":"command","method":"` + method + `"`
	if payload != "" {
		request += `,"payload":` + payload
	}
	var output bytes.Buffer
	server := platform.NewServer(strings.NewReader(request+"}\n"), &output, 1)
	Register(server, h.service)
	if err := server.Serve(context.Background()); err != nil {
		h.t.Fatal(err)
	}
	var result frame
	var events []frame
	for _, line := range strings.Split(output.String(), "\n") {
		var parsed frame
		if json.Unmarshal([]byte(line), &parsed) != nil {
			continue
		}
		if parsed.Type == "event" {
			events = append(events, parsed)
		} else if parsed.ID == "test" {
			result = parsed
		}
	}
	if result.ID == "" {
		h.t.Fatalf("missing result: %s", output.String())
	}
	return result, events
}

func (h harness) ok(method, payload string) json.RawMessage {
	h.t.Helper()
	result, _ := h.call(method, payload)
	if !result.OK {
		h.t.Fatalf("%s failed: %+v", method, result.Error)
	}
	return result.Payload
}

func (h harness) refused(method, payload, code string) {
	h.t.Helper()
	result, events := h.call(method, payload)
	if result.OK || result.Error == nil || result.Error.Code != code {
		h.t.Fatalf("%s %s: want %s, got ok=%v %+v", method, payload, code, result.OK, result.Error)
	}
	if len(events) != 0 {
		h.t.Fatalf("%s: a refusal was announced", method)
	}
}

const themePackage = `{"schema":1,"id":"acme.dusk","name":"Dusk","version":"1.0.0","permissions":["ui:theme","ui:css","storage"],"theme":{"name":"Dusk","scheme":"dark","tokens":{"--bg":"#101010"}},"css":".x{}","script":"export default 1"}`

func TestTheWireContract(t *testing.T) {
	h := newHarness(t, &memory{})
	if list := h.ok("plugins.list", ""); string(list) != `{"plugins":[]}` {
		t.Fatalf("an empty list: %s", list)
	}

	result, events := h.call("plugins.install", `{"package":`+themePackage+`,"granted":["storage","ui:theme"]}`)
	if !result.OK {
		t.Fatalf("install failed: %+v", result.Error)
	}
	var installed struct {
		Plugin map[string]json.RawMessage `json:"plugin"`
	}
	if json.Unmarshal(result.Payload, &installed) != nil {
		t.Fatalf("unexpected install answer: %s", result.Payload)
	}
	fields := slices.Sorted(maps.Keys(installed.Plugin))
	want := []string{"author", "description", "enabled", "granted", "hasCss", "hasScript", "hasTheme", "id", "installedAt", "name", "permissions", "sizeBytes", "updatedAt", "version"}
	if !slices.Equal(fields, want) {
		t.Fatalf("summary fields %v, want %v", fields, want)
	}
	if string(installed.Plugin["granted"]) != `["ui:theme","storage"]` || string(installed.Plugin["author"]) != `""` || string(installed.Plugin["enabled"]) != "true" {
		t.Fatalf("unexpected summary: %s", result.Payload)
	}
	if len(events) != 1 || events[0].Topic != "plugins.changed" || !bytes.Contains(events[0].Payload, []byte(`"plugins":[{"id":"acme.dusk"`)) {
		t.Fatalf("unexpected events: %+v", events)
	}

	var detail struct {
		Plugin struct{ ID string } `json:"plugin"`
		Script string              `json:"script"`
		CSS    string              `json:"css"`
		Theme  *domain.Theme       `json:"theme"`
	}
	if json.Unmarshal(h.ok("plugins.get", `{"id":"acme.dusk"}`), &detail) != nil || detail.Plugin.ID != "acme.dusk" || detail.Script != "export default 1" || detail.CSS != ".x{}" || detail.Theme == nil || detail.Theme.Scheme != "dark" {
		t.Fatalf("unexpected detail: %+v", detail)
	}

	result, events = h.call("plugins.update", `{"id":"acme.dusk","enabled":false}`)
	if !result.OK || !bytes.Contains(result.Payload, []byte(`"enabled":false`)) || len(events) != 1 {
		t.Fatalf("unexpected update: %s %+v %d", result.Payload, result.Error, len(events))
	}
	if update := h.ok("plugins.update", `{"id":"acme.dusk","granted":[]}`); !bytes.Contains(update, []byte(`"granted":[]`)) {
		t.Fatalf("an emptied grant: %s", update)
	}

	result, events = h.call("plugins.storage.set", `{"id":"acme.dusk","key":"prefs:a","value":"1"}`)
	if !result.OK || string(result.Payload) != `{"values":{"prefs:a":"1"}}` || len(events) != 0 {
		t.Fatalf("unexpected storage write: %s %+v %d", result.Payload, result.Error, len(events))
	}
	if values := h.ok("plugins.storage.get", `{"id":"acme.dusk"}`); string(values) != `{"values":{"prefs:a":"1"}}` {
		t.Fatalf("unexpected storage read: %s", values)
	}
	if values := h.ok("plugins.storage.set", `{"id":"acme.dusk","key":"prefs:a","value":null}`); string(values) != `{"values":{}}` {
		t.Fatalf("unexpected storage delete: %s", values)
	}

	result, events = h.call("plugins.remove", `{"id":"acme.dusk"}`)
	if !result.OK || string(result.Payload) != `{"removed":true}` || len(events) != 1 || string(events[0].Payload) != `{"plugins":[]}` {
		t.Fatalf("unexpected removal: %s %+v", result.Payload, events)
	}
}

func TestRefusalsUseTheContractCodes(t *testing.T) {
	h := newHarness(t, &memory{})
	h.ok("plugins.install", `{"package":`+themePackage+`,"granted":[]}`)
	for _, test := range []struct{ method, payload, code string }{
		{"plugins.list", `{"extra":1}`, "invalid_plugin"},
		{"plugins.get", `{"id":"Bad Id"}`, "invalid_plugin"},
		{"plugins.get", `{}`, "invalid_plugin"},
		{"plugins.get", `{"id":"acme.missing"}`, "plugin_not_found"},
		{"plugins.install", `{"package":` + strings.Replace(themePackage, `"scheme":"dark"`, `"scheme":"dark","extra":1`, 1) + `}`, "invalid_plugin"},
		{"plugins.install", `{"package":` + strings.Replace(themePackage, `"schema":1`, `"schema":2`, 1) + `}`, "invalid_plugin"},
		{"plugins.install", `{"package":` + themePackage + `,"granted":["control:write"]}`, "invalid_plugin"},
		{"plugins.install", `{"package":` + themePackage + `,"granted":["storage","storage"]}`, "invalid_plugin"},
		{"plugins.update", `{"id":"acme.missing","enabled":true}`, "plugin_not_found"},
		{"plugins.update", `{"id":"acme.dusk","enabled":"yes"}`, "invalid_plugin"},
		{"plugins.remove", `{"id":"acme.missing"}`, "plugin_not_found"},
		{"plugins.storage.get", `{"id":"acme.missing"}`, "plugin_not_found"},
		{"plugins.storage.set", `{"id":"acme.dusk","key":"bad key","value":"1"}`, "invalid_plugin"},
		{"plugins.storage.set", `{"id":"acme.dusk","key":"k"}`, "invalid_plugin"},
		{"plugins.storage.set", `{"id":"acme.dusk","key":"k","value":1}`, "invalid_plugin"},
		{"plugins.storage.set", `{"id":"acme.dusk","key":"k","value":"` + strings.Repeat("v", domain.MaxStorageValueBytes+1) + `"}`, "plugin_storage_full"},
	} {
		h.refused(test.method, test.payload, test.code)
	}
}

func TestLimitAndSizeRefusals(t *testing.T) {
	h := newHarness(t, &memory{})
	for index := range domain.MaxPlugins {
		h.ok("plugins.install", fmt.Sprintf(`{"package":{"schema":1,"id":"plugin-%02d","name":"P","version":"1","permissions":[]}}`, index))
	}
	h.refused("plugins.install", `{"package":{"schema":1,"id":"plugin-extra","name":"P","version":"1","permissions":[]}}`, "plugin_limit")

	h = newHarness(t, &memory{})
	// Within every field limit and one request frame, but its answer would not
	// fit one payload: U+2028 arrives raw in three bytes and is always
	// answered escaped in six.
	script := strings.Repeat("\u2028", domain.MaxScriptBytes/3)
	h.refused("plugins.install", `{"package":{"schema":1,"id":"acme.big","name":"Big","version":"1","permissions":["ui:pages"],"script":"`+script+`"}}`, "plugin_too_large")
	if list := h.ok("plugins.list", ""); string(list) != `{"plugins":[]}` {
		t.Fatalf("a refused plugin was kept: %s", list)
	}
}

func TestStorageFailuresUseTheContractCodes(t *testing.T) {
	h := newHarness(t, &memory{loadErr: errors.New("locked")})
	h.refused("plugins.list", "", "secure_storage_unavailable")
	h.refused("plugins.install", `{"package":`+themePackage+`}`, "secure_storage_unavailable")

	store := &memory{}
	h = newHarness(t, store)
	h.ok("plugins.install", `{"package":`+themePackage+`}`)
	store.fail = true
	h.refused("plugins.remove", `{"id":"acme.dusk"}`, "plugin_save_failed")
	h.refused("plugins.storage.set", `{"id":"acme.dusk","key":"k","value":"v"}`, "plugin_save_failed")
}
