package pluginsstdio

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	platform "github.com/luxuryprivate/switchboard/backend/internal/platform/stdio"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/plugins/application"
)

type noNetwork struct{ calls int }

func (network *noNetwork) Fetch(context.Context, application.OutboundRequest, int) (application.InboundResponse, error) {
	network.calls++
	return application.InboundResponse{Status: 204}, nil
}

type collectionFiles map[string]application.CollectionData

func (files collectionFiles) Load(_ context.Context, id string) (application.CollectionData, error) {
	return files[id], nil
}

func (files collectionFiles) Save(_ context.Context, id string, data application.CollectionData) error {
	files[id] = data
	return nil
}

func (files collectionFiles) Delete(_ context.Context, id string) error {
	delete(files, id)
	return nil
}

// folderSource packs a folder as {"plugin.json" content} only.
type folderSource struct{}

func (folderSource) Pack(path string) (json.RawMessage, []string) {
	raw, err := os.ReadFile(filepath.Join(path, "plugin.json"))
	if err != nil {
		return nil, []string{"The folder has no plugin.json."}
	}
	return raw, nil
}

const fetchPackage = `{"schema":1,"id":"acme.fetch","name":"Fetch","version":"1.0.0","permissions":["net:fetch","storage"],"network":{"hosts":["api.example.com"]},"settings":[{"key":"token","label":"Token","type":"secret"}],"script":"export default 1"}`

func extendedHarness(t *testing.T) (harness, *noNetwork, collectionFiles) {
	t.Helper()
	h := newHarness(t, &memory{})
	network := &noNetwork{}
	files := collectionFiles{}
	fetcher, err := application.NewFetcher(h.service, network)
	if err != nil {
		t.Fatal(err)
	}
	collections, err := application.NewCollections(h.service, files)
	if err != nil {
		t.Fatal(err)
	}
	links, err := application.NewDevLinks(h.service, folderSource{})
	if err != nil {
		t.Fatal(err)
	}
	h.extensions = func(server *platform.Server) {
		RegisterFetch(server, fetcher)
		RegisterCollections(server, collections)
		RegisterDevLinks(server, links)
	}
	return h, network, files
}

func TestSecretsOnTheWire(t *testing.T) {
	h, _, _ := extendedHarness(t)
	h.ok("plugins.install", `{"package":`+fetchPackage+`,"granted":["net:fetch"]}`)
	result, events := h.call("plugins.secret.set", `{"id":"acme.fetch","key":"token","value":"fixture-secret"}`)
	if !result.OK || string(result.Payload) != `{"secrets":["token"]}` || len(events) != 1 || events[0].Topic != "plugins.changed" {
		t.Fatalf("set: %+v %s", result, result.Payload)
	}
	for method, payload := range map[string]string{"plugins.list": "", "plugins.get": `{"id":"acme.fetch"}`} {
		answer := h.ok(method, payload)
		if strings.Contains(string(answer), "fixture-secret") || !strings.Contains(string(answer), `"secrets":["token"]`) {
			t.Fatalf("%s: %s", method, answer)
		}
	}
	h.refused("plugins.secret.set", `{"id":"acme.fetch","key":"token"}`, "invalid_plugin")
	h.refused("plugins.secret.set", `{"id":"acme.fetch","key":"token","value":5}`, "invalid_plugin")
	h.refused("plugins.secret.set", `{"id":"acme.fetch","key":"nope","value":"x"}`, "invalid_plugin")
	h.refused("plugins.secret.set", `{"id":"acme.none","key":"token","value":"x"}`, "plugin_not_found")
	if answer := h.ok("plugins.secret.set", `{"id":"acme.fetch","key":"token","value":null}`); string(answer) != `{"secrets":[]}` {
		t.Fatalf("clear: %s", answer)
	}
}

func TestFetchOnTheWire(t *testing.T) {
	h, network, _ := extendedHarness(t)
	h.ok("plugins.install", `{"package":`+fetchPackage+`,"granted":[]}`)
	h.refused("plugins.fetch", `{"id":"acme.fetch","url":"https://api.example.com/"}`, "not_allowed")
	h.refused("plugins.fetch", `{"id":"acme.fetch","url":"https://api.example.com/","extra":1}`, "invalid_plugin")
	h.ok("plugins.update", `{"id":"acme.fetch","granted":["net:fetch"]}`)
	h.refused("plugins.fetch", `{"id":"acme.fetch","url":"https://other.example.com/"}`, "not_allowed")
	h.refused("plugins.fetch", `{"id":"acme.fetch","url":"https://api.example.com/","headers":{"A":"{{secret:token}}"}}`, "missing_secret")
	if network.calls != 0 {
		t.Fatal("a refused fetch reached the network")
	}
	answer := h.ok("plugins.fetch", `{"id":"acme.fetch","url":"https://api.example.com/","method":"GET","timeoutMs":5000}`)
	if string(answer) != `{"status":204,"headers":{},"body":"","redirected":false}` {
		t.Fatalf("fetch answered %s", answer)
	}
}

func TestCollectionsOnTheWire(t *testing.T) {
	h, _, files := extendedHarness(t)
	h.ok("plugins.install", `{"package":`+themePackage+`,"granted":[]}`)
	for _, step := range []struct{ method, payload, want string }{
		{"get", `{"id":"acme.dusk","collection":"notes","docId":"a"}`, `{"doc":null}`},
		{"put", `{"id":"acme.dusk","collection":"notes","docId":"a","doc":{"n":1}}`, `{"ok":true}`},
		{"get", `{"id":"acme.dusk","collection":"notes","docId":"a"}`, `{"doc":{"n":1}}`},
		{"list", `{"id":"acme.dusk","collection":"notes","limit":1}`, `{"items":[{"id":"a","doc":{"n":1}}]}`},
		{"stats", `{"id":"acme.dusk"}`, `{"collections":[{"name":"notes","count":1,"bytes":16}],"bytes":16,"limitBytes":2097152}`},
		{"delete", `{"id":"acme.dusk","collection":"notes","docId":"a"}`, `{"deleted":true}`},
		{"clear", `{"id":"acme.dusk"}`, `{"cleared":0}`},
	} {
		if answer := h.ok("plugins.collection."+step.method, step.payload); string(answer) != step.want {
			t.Fatalf("%s: %s, want %s", step.method, answer, step.want)
		}
	}
	h.refused("plugins.collection.put", `{"id":"acme.dusk","collection":"notes","docId":"a"}`, "invalid_plugin")
	h.refused("plugins.collection.get", `{"id":"acme.dusk","collection":"notes","docId":"a","doc":1}`, "invalid_plugin")
	h.refused("plugins.collection.put", `{"id":"acme.dusk","collection":"Notes","docId":"a","doc":1}`, "invalid_plugin")
	h.refused("plugins.collection.list", `{"id":"acme.dusk","collection":"notes","limit":500}`, "invalid_plugin")
	h.refused("plugins.collection.get", `{"id":"acme.none","collection":"notes","docId":"a"}`, "plugin_not_found")

	h.ok("plugins.collection.put", `{"id":"acme.dusk","collection":"notes","docId":"b","doc":2}`)
	h.ok("plugins.remove", `{"id":"acme.dusk"}`)
	if len(files) != 0 {
		t.Fatalf("collections outlived the plugin: %v", files)
	}
}

func TestDevLinksOnTheWire(t *testing.T) {
	h, _, _ := extendedHarness(t)
	h.refused("plugins.dev.link", `{"path":"relative"}`, "invalid_plugin")
	h.refused("plugins.dev.link", `{"path":`+quote(t.TempDir())+`}`, "plugin_dev_problem")
	folder := t.TempDir()
	if err := os.WriteFile(filepath.Join(folder, "plugin.json"), []byte(themePackage), 0o600); err != nil {
		t.Fatal(err)
	}
	result, events := h.call("plugins.dev.link", `{"path":`+quote(folder)+`}`)
	var summary struct {
		ID      string   `json:"id"`
		Granted []string `json:"granted"`
		Dev     struct {
			Path     string `json:"path"`
			LoadedAt string `json:"loadedAt"`
		} `json:"dev"`
	}
	if !result.OK || json.Unmarshal(result.Payload, &summary) != nil || summary.ID != "acme.dusk" || summary.Dev.Path != folder || summary.Dev.LoadedAt == "" || len(summary.Granted) != 0 || len(events) != 1 {
		t.Fatalf("link: %+v %s", result.Error, result.Payload)
	}
	if answer := h.ok("plugins.dev.unlink", `{"id":"acme.dusk"}`); strings.Contains(string(answer), `"dev"`) {
		t.Fatalf("unlink: %s", answer)
	}
	h.refused("plugins.dev.unlink", `{"id":"acme.dusk"}`, "plugin_not_linked")
}

func quote(text string) string {
	encoded, _ := json.Marshal(text)
	return string(encoded)
}
