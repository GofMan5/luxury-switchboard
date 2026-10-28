package pluginsstdio

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	platform "github.com/luxuryprivate/switchboard/backend/internal/platform/stdio"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/plugins/application"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/plugins/domain"
)

type journalFile struct {
	entries []domain.JournalEntry
	loadErr error
}

func (file *journalFile) Load(context.Context) ([]domain.JournalEntry, error) {
	return slices.Clone(file.entries), file.loadErr
}

func (file *journalFile) Save(_ context.Context, entries []domain.JournalEntry) error {
	file.entries = slices.Clone(entries)
	return nil
}

func (file *journalFile) Delete(context.Context) error {
	file.entries, file.loadErr = nil, nil
	return nil
}

func journalHarness(t *testing.T, file *journalFile) harness {
	t.Helper()
	h := newHarness(t, &memory{})
	journal, err := application.NewJournal(h.service, file)
	if err != nil {
		t.Fatal(err)
	}
	h.extensions = func(server *platform.Server) { RegisterJournal(server, journal) }
	return h
}

func TestJournalOnTheWire(t *testing.T) {
	h := journalHarness(t, &journalFile{})
	h.ok("plugins.install", `{"package":`+fetchPackage+`,"granted":["net:fetch"]}`)
	h.ok("plugins.install", `{"package":`+themePackage+`,"granted":[]}`)
	h.ok("plugins.secret.set", `{"id":"acme.fetch","key":"token","value":"fixture-secret"}`)

	var page application.JournalPage
	answer := h.ok("plugins.journal.list", `{}`)
	if err := json.Unmarshal(answer, &page); err != nil || len(page.Entries) != 2 || page.Incomplete {
		t.Fatalf("journal %s", answer)
	}
	if strings.Contains(string(answer), "fixture-secret") || strings.Contains(string(answer), "api.example.com") {
		t.Fatalf("the journal leaked plugin data: %s", answer)
	}
	if first := page.Entries[1]; first.Kind != domain.JournalInstalled || first.PluginID != "acme.fetch" || !slices.Equal(first.Granted, []string{"net:fetch"}) {
		t.Fatalf("install entry %+v", first)
	}
	answer = h.ok("plugins.journal.list", `{"pluginId":"acme.fetch","limit":1}`)
	if err := json.Unmarshal(answer, &page); err != nil || len(page.Entries) != 1 || page.Entries[0].PluginID != "acme.fetch" || page.Next != 0 {
		t.Fatalf("filtered %s", answer)
	}
	for _, payload := range []string{`{"pluginId":"Not An Id"}`, `{"limit":201}`, `{"before":-1}`, `{"extra":1}`, `[]`} {
		h.refused("plugins.journal.list", payload, "invalid_plugin")
	}
	h.refused("plugins.journal.clear", `{"extra":1}`, "invalid_plugin")
	if answer := h.ok("plugins.journal.clear", `{}`); string(answer) != `{"ok":true}` {
		t.Fatalf("clear answered %s", answer)
	}
	if answer := h.ok("plugins.journal.list", `{}`); !strings.Contains(string(answer), `"entries":[]`) {
		t.Fatalf("after clear %s", answer)
	}
}

func TestAnUnreadableJournalIsRefusedOnTheWire(t *testing.T) {
	h := journalHarness(t, &journalFile{loadErr: errors.New("keyring is locked")})
	h.refused("plugins.journal.list", `{}`, "secure_storage_unavailable")
	h.ok("plugins.journal.clear", `{}`)
	h.ok("plugins.journal.list", `{}`)
}
