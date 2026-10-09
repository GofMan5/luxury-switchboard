package providerstdio_test

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	platform "github.com/luxuryprivate/switchboard/backend/internal/platform/stdio"
	providerstdio "github.com/luxuryprivate/switchboard/backend/internal/slices/providers/adapters/stdio"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/providers/application"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/providers/domain"
)

// memoryStore is the smallest Repository the protocol needs: boot answers the
// saved state and writes are recorded, so a test can prove a refused command
// never reached the write path. Handlers run on server workers while the test
// reads the store, so the state is mutex-guarded.
type memoryStore struct {
	mu    sync.Mutex
	state application.SavedState
	saves int
}

func (store *memoryStore) Load(context.Context) (application.SavedState, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	providers := make([]domain.Provider, len(store.state.Providers))
	copy(providers, store.state.Providers)
	return application.SavedState{Providers: providers, ActiveID: store.state.ActiveID}, nil
}

func (store *memoryStore) Save(_ context.Context, state application.SavedState) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.saves++
	providers := make([]domain.Provider, len(state.Providers))
	copy(providers, state.Providers)
	store.state = application.SavedState{Providers: providers, ActiveID: state.ActiveID}
	return nil
}

func (store *memoryStore) saveCount() int {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.saves
}

// memoryKeyPool accepts every budget the manager proposes and remembers the
// removals: a delete that was refused at the boundary must never release the
// provider's request budget behind the manager's back.
type memoryKeyPool struct {
	mu      sync.Mutex
	removed []string
}

func (pool *memoryKeyPool) EnsureProvider(string, int, time.Duration) error { return nil }

func (pool *memoryKeyPool) RemoveProvider(providerID string) error {
	pool.mu.Lock()
	pool.removed = append(pool.removed, providerID)
	pool.mu.Unlock()
	return nil
}

func (pool *memoryKeyPool) DropProvider(_ context.Context, providerID string) error {
	pool.mu.Lock()
	pool.removed = append(pool.removed, providerID)
	pool.mu.Unlock()
	return nil
}

func (pool *memoryKeyPool) removals() []string {
	pool.mu.Lock()
	defer pool.mu.Unlock()
	return append([]string(nil), pool.removed...)
}

// noKeys reports an empty pool: the delete contract under test does not care
// how many keys a provider holds, only who owns the row.
type noKeys struct{}

func (noKeys) Count(string) int { return 0 }

// harness wires the adapter against a real catalog and manager, so the frames
// the test reads are the bytes the desktop shell would read.
type harness struct {
	store   *memoryStore
	pool    *memoryKeyPool
	catalog *application.Catalog
	manager *application.Manager
}

func newHarness(t *testing.T, providers []domain.Provider, activeID string) *harness {
	t.Helper()
	store := &memoryStore{state: application.SavedState{Providers: providers, ActiveID: activeID}}
	pool := &memoryKeyPool{}
	catalog, err := application.NewCatalog(providers, activeID)
	if err != nil {
		t.Fatalf("catalog: %v", err)
	}
	manager, err := application.NewManager(catalog, store, pool)
	if err != nil {
		t.Fatalf("manager: %v", err)
	}
	if err := manager.Load(context.Background()); err != nil {
		t.Fatalf("load: %v", err)
	}
	return &harness{store: store, pool: pool, catalog: catalog, manager: manager}
}

// exchange drives commands as real protocol frames through a real server, so
// the assertions see the exact bytes the desktop shell would read. One worker
// keeps the answers in input order; every line of the transcript must parse,
// because a corrupted frame must fail here rather than in an assertion that
// never ran. The whole transcript comes back with the results so a test can
// hold the protocol to what it did not emit as tightly as to what it did.
func exchange(t *testing.T, wired *harness, commands ...string) ([]map[string]any, string) {
	t.Helper()
	var input strings.Builder
	for index, command := range commands {
		input.WriteString(`{"v":1,"id":"req` + strconv.Itoa(index) + `","type":"command",` + command + "}\n")
	}
	var output strings.Builder
	server := platform.NewServer(strings.NewReader(input.String()), &output, 1)
	providerstdio.Register(server, wired.catalog, wired.manager, noKeys{}, nil)
	if err := server.Serve(context.Background()); err != nil {
		t.Fatalf("serve: %v", err)
	}
	var results []map[string]any
	for _, line := range linesOf(t, output.String()) {
		frame := parseFrame(t, line)
		if frame["type"] == "result" {
			results = append(results, frame)
		}
	}
	if len(results) != len(commands) {
		t.Fatalf("answered %d of %d commands, transcript: %s", len(results), len(commands), output.String())
	}
	return results, output.String()
}

// linesOf splits a transcript into frame lines, dropping nothing but the
// trailing newline.
func linesOf(t *testing.T, transcript string) []string {
	t.Helper()
	var lines []string
	for _, line := range strings.Split(strings.TrimSpace(transcript), "\n") {
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

// parseFrame reads one frame as a JSON object, keeping numbers exact.
func parseFrame(t *testing.T, line string) map[string]any {
	t.Helper()
	frame := make(map[string]any)
	decoder := json.NewDecoder(strings.NewReader(line))
	decoder.UseNumber()
	if decoder.Decode(&frame) != nil {
		t.Fatalf("unreadable frame: %s", line)
	}
	return frame
}

// payloadOf asserts the protocol's object-result rule: every successful
// command answers with a JSON object.
func payloadOf(t *testing.T, frame map[string]any) map[string]any {
	t.Helper()
	if frame["ok"] != true {
		t.Fatalf("command failed: %+v", frame)
	}
	payload, ok := frame["payload"].(map[string]any)
	if !ok {
		t.Fatalf("command did not answer with an object: %+v", frame)
	}
	return payload
}

// failureOf asserts the frame failed and returns its error object.
func failureOf(t *testing.T, frame map[string]any) map[string]any {
	t.Helper()
	if frame["ok"] != false {
		t.Fatalf("command unexpectedly succeeded: %+v", frame)
	}
	failure, ok := frame["error"].(map[string]any)
	if !ok {
		t.Fatalf("command failed without an error object: %+v", frame)
	}
	return failure
}

// TestTheGenericDeleteRefusesAPresetProviderAndLeavesItInPlace pins the
// boundary the codex slice owns: a preset-marked entry is created, relinked
// and removed by the account sign-in, so the generic delete must refuse it
// with an actionable message instead of stranding the account link — while
// the codex slice's own disconnect keeps deleting through the manager. The
// entry in the fixture is enabled but not active and holds no keys, so every
// other refusal in the manager is silent and the preset is the only reason
// left for the refusal.
func TestTheGenericDeleteRefusesAPresetProviderAndLeavesItInPlace(t *testing.T) {
	codex, err := domain.New(domain.Params{
		ID:         "codex",
		Name:       "Codex — ada@example.com",
		BaseURL:    "https://chatgpt.com/backend-api/codex",
		AuthMode:   domain.AuthBearer,
		ModelsPath: "/models",
		Format:     domain.FormatResponses,
		Enabled:    true,
		Preset:     domain.PresetCodex,
		AccountID:  "acct-4f2",
	})
	if err != nil {
		t.Fatalf("codex provider: %v", err)
	}
	custom, err := domain.New(domain.Params{
		ID:       "lab",
		Name:     "Lab relay",
		BaseURL:  "http://127.0.0.1:8080/v1",
		AuthMode: domain.AuthAPIKey,
		Enabled:  true,
	})
	if err != nil {
		t.Fatalf("custom provider: %v", err)
	}
	wired := newHarness(t, []domain.Provider{codex, custom}, "lab")

	results, transcript := exchange(t, wired,
		`"method":"providers.delete","payload":{"id":"codex"}`,
		`"method":"providers.list","payload":{}`,
	)

	failure := failureOf(t, results[0])
	if failure["code"] != "provider_managed" {
		t.Fatalf("error code = %#v, want provider_managed", failure["code"])
	}
	if failure["message"] != "Managed provider cannot be deleted. Disconnect its account sign-in to remove it." {
		t.Fatalf("error message = %#v, want the managed-refusal text", failure["message"])
	}

	// The entry survives with its marker intact: the row still belongs to the
	// account sign-in, exactly as the codex slice expects to find it.
	payload := payloadOf(t, results[1])
	rows, ok := payload["providers"].([]any)
	if !ok {
		t.Fatalf("provider list did not answer with rows: %+v", payload)
	}
	var survivor map[string]any
	for _, row := range rows {
		if entry, ok := row.(map[string]any); ok && entry["id"] == "codex" {
			survivor = entry
		}
	}
	if survivor == nil {
		t.Fatalf("the preset provider is gone from the catalog: %s", transcript)
	}
	if survivor["preset"] != "codex" {
		t.Fatalf("the surviving row lost its preset marker: %+v", survivor)
	}

	// A refused delete is not a change: no event frame may announce one, or
	// every open surface would rebuild its catalog for nothing.
	for _, line := range linesOf(t, transcript) {
		frame := parseFrame(t, line)
		if frame["type"] == "event" && frame["topic"] == "providers.changed" {
			t.Fatalf("the refused delete still announced a change: %s", line)
		}
	}

	// The refusal happened before the write path: nothing was saved and the
	// provider's request budget was never released.
	if saves := wired.store.saveCount(); saves != 0 {
		t.Fatalf("the refused delete still saved state, saves = %d", saves)
	}
	if removals := wired.pool.removals(); len(removals) != 0 {
		t.Fatalf("the refused delete still released the key pool: %v", removals)
	}
}
