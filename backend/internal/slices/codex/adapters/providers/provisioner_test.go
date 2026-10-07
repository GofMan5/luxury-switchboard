package providers

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	codexapp "github.com/luxuryprivate/switchboard/backend/internal/slices/codex/application"
	codexdomain "github.com/luxuryprivate/switchboard/backend/internal/slices/codex/domain"
	providerapp "github.com/luxuryprivate/switchboard/backend/internal/slices/providers/application"
	providerdomain "github.com/luxuryprivate/switchboard/backend/internal/slices/providers/domain"
)

// The narrow registry is a real subset of the manager: the composition root
// passes the manager itself.
var _ ProviderRegistry = (*providerapp.Manager)(nil)

type callKind string

const (
	callAdd       callKind = "add"
	callGet       callKind = "get"
	callUpdate    callKind = "update"
	callSetPreset callKind = "setpreset"
	callDelete    callKind = "delete"
)

type registryCall struct {
	kind      callKind
	id        string
	params    providerdomain.Params
	preset    providerdomain.Preset
	accountID providerdomain.AccountID
}

// fakeRegistry mirrors the manager's semantics on an in-memory map: Get
// reports existence regardless of Enabled, Add refuses an id that is already
// taken, Update forces id, builtin, preset and account from the stored
// entry, SetPreset preserves everything else, Delete refuses builtin and
// active entries — and fires deleteErr for the refusals behind them, the
// keys and routes a real manager still holds. Every method records its call
// before honouring a cancelled context.
type fakeRegistry struct {
	entries         map[string]providerdomain.Provider
	activeID        string
	mintIDs         bool
	dropPresetOnAdd bool
	updateErr       error
	deleteErr       error
	nextMint        int
	calls           []registryCall
}

func newFakeRegistry() *fakeRegistry {
	return &fakeRegistry{entries: map[string]providerdomain.Provider{}}
}

func (fake *fakeRegistry) record(call registryCall) {
	fake.calls = append(fake.calls, call)
}

func (fake *fakeRegistry) Get(ctx context.Context, id string) (providerdomain.Provider, bool) {
	fake.record(registryCall{kind: callGet, id: id})
	if err := ctx.Err(); err != nil {
		return providerdomain.Provider{}, false
	}
	current, ok := fake.entries[id]
	return current, ok
}

func (fake *fakeRegistry) Add(ctx context.Context, params providerdomain.Params) (providerdomain.Provider, error) {
	fake.record(registryCall{kind: callAdd, params: params})
	if err := ctx.Err(); err != nil {
		return providerdomain.Provider{}, err
	}
	id := params.ID
	if fake.mintIDs {
		fake.nextMint++
		id = fmt.Sprintf("provider_%08x", fake.nextMint)
	}
	if _, exists := fake.entries[id]; exists {
		return providerdomain.Provider{}, providerapp.ErrProviderIDExists
	}
	params.ID = id
	params.Builtin = false
	if fake.dropPresetOnAdd {
		params.Preset = ""
		params.AccountID = ""
	}
	provider, err := providerdomain.New(params)
	if err != nil {
		return providerdomain.Provider{}, err
	}
	fake.entries[id] = provider
	return provider, nil
}

func (fake *fakeRegistry) Update(ctx context.Context, id string, params providerdomain.Params) (providerdomain.Provider, error) {
	fake.record(registryCall{kind: callUpdate, id: id, params: params})
	if err := ctx.Err(); err != nil {
		return providerdomain.Provider{}, err
	}
	if fake.updateErr != nil {
		return providerdomain.Provider{}, fake.updateErr
	}
	current, ok := fake.entries[id]
	if !ok {
		return providerdomain.Provider{}, providerapp.ErrProviderUnavailable
	}
	params.ID = id
	params.Builtin = current.Builtin
	params.Preset = current.Preset
	params.AccountID = current.AccountID
	provider, err := providerdomain.New(params)
	if err != nil {
		return providerdomain.Provider{}, err
	}
	if fake.activeID == id && !provider.Enabled {
		return providerdomain.Provider{}, providerapp.ErrActiveProvider
	}
	fake.entries[id] = provider
	return provider, nil
}

func (fake *fakeRegistry) SetPreset(ctx context.Context, id string, preset providerdomain.Preset, accountID providerdomain.AccountID) (providerdomain.Provider, error) {
	fake.record(registryCall{kind: callSetPreset, id: id, preset: preset, accountID: accountID})
	if err := ctx.Err(); err != nil {
		return providerdomain.Provider{}, err
	}
	current, ok := fake.entries[id]
	if !ok {
		return providerdomain.Provider{}, providerapp.ErrProviderUnavailable
	}
	params := paramsOf(current)
	params.Preset = preset
	params.AccountID = accountID
	provider, err := providerdomain.New(params)
	if err != nil {
		return providerdomain.Provider{}, err
	}
	fake.entries[id] = provider
	return provider, nil
}

func (fake *fakeRegistry) Delete(ctx context.Context, id string) error {
	fake.record(registryCall{kind: callDelete, id: id})
	if err := ctx.Err(); err != nil {
		return err
	}
	current, ok := fake.entries[id]
	if !ok {
		return providerapp.ErrProviderUnavailable
	}
	if current.Builtin {
		return providerapp.ErrBuiltinProvider
	}
	if fake.activeID == id {
		return providerapp.ErrActiveProvider
	}
	if fake.deleteErr != nil {
		return fake.deleteErr
	}
	delete(fake.entries, id)
	return nil
}

// paramsOf mirrors the manager's private paramsOf: every field of the stored
// entry except the preset and account id, which SetPreset owns.
func paramsOf(provider providerdomain.Provider) providerdomain.Params {
	return providerdomain.Params{
		ID:          provider.ID,
		Name:        provider.Name,
		BaseURL:     provider.BaseURL.String(),
		AuthMode:    provider.AuthMode,
		AuthHeader:  provider.AuthHeader,
		Dialect:     provider.Dialect,
		ModelsPath:  provider.ModelsPath,
		Format:      provider.Format,
		ChatPath:    provider.ChatPath,
		ImageCompat: provider.ImageCompat,
		RPM:         provider.RPM,
		RateUnit:    provider.RateUnit,
		CacheTTL:    provider.CacheTTL,
		Enabled:     provider.Enabled,
		Builtin:     provider.Builtin,
	}
}

func testIdentity(email, accountID string) codexdomain.Identity {
	return codexdomain.Identity{Email: email, AccountID: accountID}
}

// customParams is a valid hand-configured provider without a preset.
func customParams(id string) providerdomain.Params {
	return providerdomain.Params{
		ID:       id,
		Name:     "Mine",
		BaseURL:  "https://api.example.com/v1",
		AuthMode: providerdomain.AuthAPIKey,
		Enabled:  true,
	}
}

func countCalls(fake *fakeRegistry, kind callKind) int {
	count := 0
	for _, call := range fake.calls {
		if call.kind == kind {
			count++
		}
	}
	return count
}

// collidingRegistry answers every probe with "missing" but refuses Add with
// the sentinel a real manager reports for a taken id. It is how the
// provisioner's translation of a raced add is exercised without real
// concurrency: the entry lands between the probe and the add.
type collidingRegistry struct {
	*fakeRegistry
}

func (fake *collidingRegistry) Add(ctx context.Context, params providerdomain.Params) (providerdomain.Provider, error) {
	fake.record(registryCall{kind: callAdd, params: params})
	if err := ctx.Err(); err != nil {
		return providerdomain.Provider{}, err
	}
	return providerdomain.Provider{}, providerapp.ErrProviderIDExists
}

func TestFreshLoginCreatesTheCanonicalCodexEntry(t *testing.T) {
	registry := newFakeRegistry()
	provisioner := NewProvisioner(registry)

	providerID, err := provisioner.EnsureCodexProvider(context.Background(), testIdentity("user@example.com", "acct-1234567890abcdef"))
	if err != nil {
		t.Fatalf("EnsureCodexProvider: %v", err)
	}
	if providerID != codexapp.CodexProviderID {
		t.Errorf("providerID = %q, want %q", providerID, codexapp.CodexProviderID)
	}
	if len(registry.entries) != 1 {
		t.Fatalf("catalog holds %d entries, want 1", len(registry.entries))
	}
	entry, ok := registry.entries[codexapp.CodexProviderID]
	if !ok {
		t.Fatalf("no entry under %q", codexapp.CodexProviderID)
	}
	if entry.Name != "Codex — user@example.com" {
		t.Errorf("Name = %q", entry.Name)
	}
	if entry.BaseURL == nil || entry.BaseURL.String() != baseURL {
		t.Errorf("BaseURL = %v, want %q", entry.BaseURL, baseURL)
	}
	if entry.AuthMode != providerdomain.AuthBearer {
		t.Errorf("AuthMode = %q, want %q", entry.AuthMode, providerdomain.AuthBearer)
	}
	if entry.AuthHeader != "" {
		t.Errorf("AuthHeader = %q, want empty", entry.AuthHeader)
	}
	if entry.Dialect != providerdomain.DialectAuto {
		t.Errorf("Dialect = %q, want %q", entry.Dialect, providerdomain.DialectAuto)
	}
	if entry.ModelsPath != modelsPath {
		t.Errorf("ModelsPath = %q, want %q", entry.ModelsPath, modelsPath)
	}
	if entry.Format != providerdomain.FormatResponses {
		t.Errorf("Format = %q, want %q", entry.Format, providerdomain.FormatResponses)
	}
	if entry.ChatPath != "/v1/chat/completions" {
		t.Errorf("ChatPath = %q, want the domain default", entry.ChatPath)
	}
	if entry.ImageCompat {
		t.Error("ImageCompat = true, want false")
	}
	if entry.RPM != 0 {
		t.Errorf("RPM = %d, want 0 (codex traffic never uses the key pool queues)", entry.RPM)
	}
	if entry.RateUnit != providerdomain.RatePerMinute {
		t.Errorf("RateUnit = %q, want the domain default", entry.RateUnit)
	}
	if entry.CacheTTL != 0 {
		t.Errorf("CacheTTL = %v, want 0", entry.CacheTTL)
	}
	if !entry.Enabled {
		t.Error("Enabled = false, want true")
	}
	if entry.Builtin {
		t.Error("Builtin = true, want false")
	}
	if entry.Preset != providerdomain.PresetCodex {
		t.Errorf("Preset = %q, want %q", entry.Preset, providerdomain.PresetCodex)
	}
	if entry.AccountID != providerdomain.AccountID("acct-1234567890abcdef") {
		t.Errorf("AccountID = %q", entry.AccountID)
	}
}

func TestReLoginRefreshesTheExistingEntry(t *testing.T) {
	registry := newFakeRegistry()
	provisioner := NewProvisioner(registry)
	if _, err := provisioner.EnsureCodexProvider(context.Background(), testIdentity("first@example.com", "acct-first000000001")); err != nil {
		t.Fatalf("first login: %v", err)
	}

	callsBefore := len(registry.calls)
	providerID, err := provisioner.EnsureCodexProvider(context.Background(), testIdentity("second@example.com", "acct-second000000001"))
	if err != nil {
		t.Fatalf("second login: %v", err)
	}
	if providerID != codexapp.CodexProviderID {
		t.Errorf("providerID = %q, want %q", providerID, codexapp.CodexProviderID)
	}
	newCalls := registry.calls[callsBefore:]
	if len(newCalls) != 3 || newCalls[0].kind != callGet || newCalls[1].kind != callUpdate || newCalls[2].kind != callSetPreset {
		t.Fatalf("second login calls = %v, want exactly get + update + setpreset", newCalls)
	}
	if len(registry.entries) != 1 {
		t.Fatalf("catalog holds %d entries, want 1", len(registry.entries))
	}
	entry := registry.entries[codexapp.CodexProviderID]
	if entry.Name != "Codex — second@example.com" {
		t.Errorf("Name = %q, want the refreshed email", entry.Name)
	}
	if entry.AccountID != providerdomain.AccountID("acct-second000000001") {
		t.Errorf("AccountID = %q, want the refreshed account", entry.AccountID)
	}
	if !entry.Enabled {
		t.Error("Enabled = false, want true")
	}
}

func TestReLoginReEnablesADisabledEntry(t *testing.T) {
	registry := newFakeRegistry()
	provisioner := NewProvisioner(registry)
	if _, err := provisioner.EnsureCodexProvider(context.Background(), testIdentity("user@example.com", "acct-1234567890abcdef")); err != nil {
		t.Fatalf("first login: %v", err)
	}
	entry := registry.entries[codexapp.CodexProviderID]
	entry.Enabled = false
	registry.entries[codexapp.CodexProviderID] = entry

	if _, err := provisioner.EnsureCodexProvider(context.Background(), testIdentity("user@example.com", "acct-1234567890abcdef")); err != nil {
		t.Fatalf("second login over a disabled entry: %v", err)
	}
	if !registry.entries[codexapp.CodexProviderID].Enabled {
		t.Error("the disabled entry was not re-enabled")
	}
	if countCalls(registry, callAdd) != 1 {
		t.Errorf("add calls = %d, want 1 (no duplicate entry)", countCalls(registry, callAdd))
	}
}

func TestACustomProviderOnTheCodexIDIsRefused(t *testing.T) {
	registry := newFakeRegistry()
	if _, err := registry.Add(context.Background(), customParams(codexapp.CodexProviderID)); err != nil {
		t.Fatalf("seed custom entry: %v", err)
	}
	provisioner := NewProvisioner(registry)

	_, err := provisioner.EnsureCodexProvider(context.Background(), testIdentity("user@example.com", "acct-1234567890abcdef"))
	if !errors.Is(err, ErrProviderIdTaken) {
		t.Fatalf("err = %v, want ErrProviderIdTaken", err)
	}
	entry, ok := registry.entries[codexapp.CodexProviderID]
	if !ok {
		t.Fatal("the custom entry vanished")
	}
	if entry.Preset != "" {
		t.Errorf("custom entry gained preset %q", entry.Preset)
	}
	if countCalls(registry, callAdd) != 1 {
		t.Errorf("add calls = %d, want 1 (only the seed)", countCalls(registry, callAdd))
	}
}

func TestAnAddThatRacesIntoATakenIDIsRefused(t *testing.T) {
	registry := newFakeRegistry()
	provisioner := NewProvisioner(&collidingRegistry{registry})

	_, err := provisioner.EnsureCodexProvider(context.Background(), testIdentity("user@example.com", "acct-1234567890abcdef"))
	if !errors.Is(err, ErrProviderIdTaken) {
		t.Fatalf("err = %v, want ErrProviderIdTaken", err)
	}
	if len(registry.calls) != 2 || registry.calls[0].kind != callGet || registry.calls[1].kind != callAdd {
		t.Fatalf("calls = %v, want a probe of the literal id then a refused add", registry.calls)
	}
	if len(registry.entries) != 0 {
		t.Fatalf("catalog holds %d entries, want 0 (the collision deleted nothing)", len(registry.entries))
	}
}

func TestRemoveDeletesTheProvisionedEntry(t *testing.T) {
	registry := newFakeRegistry()
	provisioner := NewProvisioner(registry)
	if _, err := provisioner.EnsureCodexProvider(context.Background(), testIdentity("user@example.com", "acct-1234567890abcdef")); err != nil {
		t.Fatalf("login: %v", err)
	}

	if err := provisioner.RemoveCodexProvider(context.Background()); err != nil {
		t.Fatalf("RemoveCodexProvider: %v", err)
	}
	if len(registry.entries) != 0 {
		t.Fatalf("catalog still holds %d entries, want 0", len(registry.entries))
	}
	if countCalls(registry, callDelete) != 1 {
		t.Errorf("delete calls = %d, want 1", countCalls(registry, callDelete))
	}
}

func TestRemoveLeavesACustomProviderAlone(t *testing.T) {
	registry := newFakeRegistry()
	if _, err := registry.Add(context.Background(), customParams(codexapp.CodexProviderID)); err != nil {
		t.Fatalf("seed custom entry: %v", err)
	}
	provisioner := NewProvisioner(registry)

	if err := provisioner.RemoveCodexProvider(context.Background()); err != nil {
		t.Fatalf("RemoveCodexProvider: %v", err)
	}
	if _, ok := registry.entries[codexapp.CodexProviderID]; !ok {
		t.Fatal("the custom entry was deleted")
	}
	if countCalls(registry, callDelete) != 0 {
		t.Errorf("delete calls = %d, want 0", countCalls(registry, callDelete))
	}
}

func TestRemoveWithoutAnEntryIsANoOp(t *testing.T) {
	registry := newFakeRegistry()
	provisioner := NewProvisioner(registry)

	if err := provisioner.RemoveCodexProvider(context.Background()); err != nil {
		t.Fatalf("RemoveCodexProvider: %v", err)
	}
	if len(registry.calls) != 1 || registry.calls[0].kind != callGet {
		t.Fatalf("calls = %v, want a single probe", registry.calls)
	}
	if len(registry.entries) != 0 {
		t.Fatalf("catalog holds %d entries, want 0", len(registry.entries))
	}
}

func TestRemoveRefusesWhileTheCodexEntryIsActive(t *testing.T) {
	registry := newFakeRegistry()
	provisioner := NewProvisioner(registry)
	if _, err := provisioner.EnsureCodexProvider(context.Background(), testIdentity("user@example.com", "acct-1234567890abcdef")); err != nil {
		t.Fatalf("login: %v", err)
	}
	registry.activeID = codexapp.CodexProviderID

	err := provisioner.RemoveCodexProvider(context.Background())
	if !errors.Is(err, providerapp.ErrActiveProvider) {
		t.Fatalf("err = %v, want ErrActiveProvider", err)
	}
	if _, ok := registry.entries[codexapp.CodexProviderID]; !ok {
		t.Fatal("the active entry was deleted anyway")
	}
}

func TestRemoveRefusesOnABuiltinCodexEntry(t *testing.T) {
	registry := newFakeRegistry()
	entry, err := providerdomain.New(providerdomain.Params{
		ID:         codexapp.CodexProviderID,
		Name:       "Codex",
		BaseURL:    baseURL,
		AuthMode:   providerdomain.AuthBearer,
		ModelsPath: modelsPath,
		Format:     providerdomain.FormatResponses,
		Enabled:    true,
		Builtin:    true,
		Preset:     providerdomain.PresetCodex,
	})
	if err != nil {
		t.Fatalf("seed builtin entry: %v", err)
	}
	registry.entries[codexapp.CodexProviderID] = entry
	provisioner := NewProvisioner(registry)

	err = provisioner.RemoveCodexProvider(context.Background())
	if !errors.Is(err, providerapp.ErrBuiltinProvider) {
		t.Fatalf("err = %v, want ErrBuiltinProvider", err)
	}
	if _, ok := registry.entries[codexapp.CodexProviderID]; !ok {
		t.Fatal("the builtin entry was deleted anyway")
	}
}

func TestRemoveRefusesWhenTheEntryStillHoldsKeys(t *testing.T) {
	registry := newFakeRegistry()
	provisioner := NewProvisioner(registry)
	if _, err := provisioner.EnsureCodexProvider(context.Background(), testIdentity("user@example.com", "acct-1234567890abcdef")); err != nil {
		t.Fatalf("login: %v", err)
	}
	registry.deleteErr = providerapp.ErrProviderHasKeys

	err := provisioner.RemoveCodexProvider(context.Background())
	if !errors.Is(err, providerapp.ErrProviderHasKeys) {
		t.Fatalf("err = %v, want ErrProviderHasKeys", err)
	}
	if _, ok := registry.entries[codexapp.CodexProviderID]; !ok {
		t.Fatal("the key-carrying entry was deleted anyway")
	}
}

func TestRetireDisablesTheEntryAndClearsTheAccount(t *testing.T) {
	registry := newFakeRegistry()
	provisioner := NewProvisioner(registry)
	if _, err := provisioner.EnsureCodexProvider(context.Background(), testIdentity("user@example.com", "acct-1234567890abcdef")); err != nil {
		t.Fatalf("login: %v", err)
	}

	if err := provisioner.RetireCodexProvider(context.Background()); err != nil {
		t.Fatalf("RetireCodexProvider: %v", err)
	}
	entry, ok := registry.entries[codexapp.CodexProviderID]
	if !ok {
		t.Fatal("the retired entry was deleted")
	}
	if entry.Enabled {
		t.Error("Enabled = true, want false (nothing may route to a signed-out account)")
	}
	if entry.Preset != providerdomain.PresetCodex {
		t.Errorf("Preset = %q, want the marker to survive so the entry keeps its identity", entry.Preset)
	}
	if entry.AccountID != "" {
		t.Errorf("AccountID = %q, want the account binding cleared", entry.AccountID)
	}
	if entry.Name != "Codex" {
		t.Errorf("Name = %q, want the neutral label of a signed-out entry", entry.Name)
	}
	if entry.BaseURL == nil || entry.BaseURL.String() != baseURL {
		t.Errorf("BaseURL = %v, want the canonical %q", entry.BaseURL, baseURL)
	}
}

func TestRetireThenRelinkOnTheNextLogin(t *testing.T) {
	registry := newFakeRegistry()
	provisioner := NewProvisioner(registry)
	if _, err := provisioner.EnsureCodexProvider(context.Background(), testIdentity("first@example.com", "acct-first000000001")); err != nil {
		t.Fatalf("first login: %v", err)
	}
	if err := provisioner.RetireCodexProvider(context.Background()); err != nil {
		t.Fatalf("retire: %v", err)
	}

	if _, err := provisioner.EnsureCodexProvider(context.Background(), testIdentity("second@example.com", "acct-second000000001")); err != nil {
		t.Fatalf("re-login after retire: %v", err)
	}
	if countCalls(registry, callAdd) != 1 {
		t.Errorf("add calls = %d, want 1 (the retired entry is relinked, not duplicated)", countCalls(registry, callAdd))
	}
	if len(registry.entries) != 1 {
		t.Fatalf("catalog holds %d entries, want 1", len(registry.entries))
	}
	entry := registry.entries[codexapp.CodexProviderID]
	if !entry.Enabled {
		t.Error("the retired entry was not re-enabled")
	}
	if entry.AccountID != providerdomain.AccountID("acct-second000000001") {
		t.Errorf("AccountID = %q, want the refreshed account", entry.AccountID)
	}
	if entry.Name != "Codex — second@example.com" {
		t.Errorf("Name = %q, want the relabeled account", entry.Name)
	}
}

func TestRetireWithoutAnEntryIsANoOp(t *testing.T) {
	registry := newFakeRegistry()
	provisioner := NewProvisioner(registry)

	if err := provisioner.RetireCodexProvider(context.Background()); err != nil {
		t.Fatalf("RetireCodexProvider: %v", err)
	}
	if len(registry.calls) != 1 || registry.calls[0].kind != callGet {
		t.Fatalf("calls = %v, want a single probe", registry.calls)
	}
	if len(registry.entries) != 0 {
		t.Fatalf("catalog holds %d entries, want 0", len(registry.entries))
	}
}

func TestRetireLeavesACustomProviderAlone(t *testing.T) {
	registry := newFakeRegistry()
	stranger, err := providerdomain.New(customParams(codexapp.CodexProviderID))
	if err != nil {
		t.Fatalf("seed custom entry: %v", err)
	}
	registry.entries[codexapp.CodexProviderID] = stranger
	provisioner := NewProvisioner(registry)

	if err := provisioner.RetireCodexProvider(context.Background()); err != nil {
		t.Fatalf("RetireCodexProvider: %v", err)
	}
	if entry, ok := registry.entries[codexapp.CodexProviderID]; !ok || entry != stranger {
		t.Fatalf("the custom entry was rewritten: %+v", entry)
	}
	for _, kind := range []callKind{callUpdate, callSetPreset, callDelete} {
		if count := countCalls(registry, kind); count != 0 {
			t.Errorf("%s calls = %d, want 0 (a stranger is never written)", kind, count)
		}
	}
}

func TestRetireRefusesWhileTheCodexEntryIsActive(t *testing.T) {
	registry := newFakeRegistry()
	provisioner := NewProvisioner(registry)
	if _, err := provisioner.EnsureCodexProvider(context.Background(), testIdentity("user@example.com", "acct-1234567890abcdef")); err != nil {
		t.Fatalf("login: %v", err)
	}
	registry.activeID = codexapp.CodexProviderID
	before := registry.entries[codexapp.CodexProviderID]

	err := provisioner.RetireCodexProvider(context.Background())
	if !errors.Is(err, providerapp.ErrActiveProvider) {
		t.Fatalf("err = %v, want ErrActiveProvider", err)
	}
	if entry, ok := registry.entries[codexapp.CodexProviderID]; !ok || entry != before {
		t.Fatalf("the refused retire mutated the entry: %+v", entry)
	}
}

func TestRetireIsIdempotent(t *testing.T) {
	registry := newFakeRegistry()
	provisioner := NewProvisioner(registry)
	if _, err := provisioner.EnsureCodexProvider(context.Background(), testIdentity("user@example.com", "acct-1234567890abcdef")); err != nil {
		t.Fatalf("login: %v", err)
	}

	if err := provisioner.RetireCodexProvider(context.Background()); err != nil {
		t.Fatalf("first retire: %v", err)
	}
	if err := provisioner.RetireCodexProvider(context.Background()); err != nil {
		t.Fatalf("second retire: %v", err)
	}
	entry, ok := registry.entries[codexapp.CodexProviderID]
	if !ok {
		t.Fatal("the already-retired entry was deleted")
	}
	if entry.Enabled || entry.AccountID != "" {
		t.Fatalf("retired twice = %+v, want a disabled entry with no account", entry)
	}
}

func TestACancelledContextPropagates(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	registry := newFakeRegistry()
	provisioner := NewProvisioner(registry)
	if _, err := provisioner.EnsureCodexProvider(ctx, testIdentity("user@example.com", "acct-1234567890abcdef")); !errors.Is(err, context.Canceled) {
		t.Errorf("EnsureCodexProvider err = %v, want context.Canceled", err)
	}
	if err := provisioner.RetireCodexProvider(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("RetireCodexProvider err = %v, want context.Canceled", err)
	}
	if err := provisioner.RemoveCodexProvider(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("RemoveCodexProvider err = %v, want context.Canceled", err)
	}
	if len(registry.calls) != 0 {
		t.Errorf("calls = %v, want none (cancellation is checked before every probe)", registry.calls)
	}
	if len(registry.entries) != 0 {
		t.Errorf("catalog holds %d entries, want 0", len(registry.entries))
	}
}

func TestRegistryErrorsPropagateVerbatim(t *testing.T) {
	errBrokenRegistry := errors.New("provider settings could not be saved")
	registry := newFakeRegistry()
	provisioner := NewProvisioner(registry)
	if _, err := provisioner.EnsureCodexProvider(context.Background(), testIdentity("user@example.com", "acct-1234567890abcdef")); err != nil {
		t.Fatalf("first login: %v", err)
	}
	registry.updateErr = errBrokenRegistry

	_, err := provisioner.EnsureCodexProvider(context.Background(), testIdentity("user@example.com", "acct-1234567890abcdef"))
	if err != errBrokenRegistry {
		t.Errorf("err = %v, want the identical registry error", err)
	}
}

func TestAMintingRegistryGetsItsEntryRemembered(t *testing.T) {
	registry := newFakeRegistry()
	registry.mintIDs = true
	provisioner := NewProvisioner(registry)
	minted := "provider_00000001"

	if _, err := provisioner.EnsureCodexProvider(context.Background(), testIdentity("user@example.com", "acct-1234567890abcdef")); err != nil {
		t.Fatalf("first login: %v", err)
	}
	providerID, err := provisioner.EnsureCodexProvider(context.Background(), testIdentity("user@example.com", "acct-1234567890abcdef"))
	if err != nil {
		t.Fatalf("second login: %v", err)
	}
	if providerID != minted {
		t.Errorf("providerID = %q, want the minted %q, not the literal", providerID, minted)
	}

	calls := registry.calls
	if len(calls) != 5 {
		t.Fatalf("calls = %d, want 5", len(calls))
	}
	if calls[0].kind != callGet || calls[0].id != codexapp.CodexProviderID {
		t.Errorf("calls[0] = %v, want a probe of the literal id", calls[0])
	}
	if calls[1].kind != callAdd {
		t.Errorf("calls[1].kind = %q, want add", calls[1].kind)
	}
	if calls[2].kind != callGet || calls[2].id != minted {
		t.Errorf("calls[2] = %v, want a probe of the minted id", calls[2])
	}
	if calls[3].kind != callUpdate || calls[3].id != minted {
		t.Errorf("calls[3] = %v, want a relink of the minted id", calls[3])
	}
	if calls[4].kind != callSetPreset || calls[4].id != minted {
		t.Errorf("calls[4] = %v, want a refresh of the minted id", calls[4])
	}
	if len(registry.entries) != 1 {
		t.Fatalf("catalog holds %d entries, want 1", len(registry.entries))
	}
}

func TestLoginLogoutLoginOnAMintingRegistryStaysClean(t *testing.T) {
	registry := newFakeRegistry()
	registry.mintIDs = true
	provisioner := NewProvisioner(registry)

	for round := 1; round <= 2; round++ {
		if _, err := provisioner.EnsureCodexProvider(context.Background(), testIdentity("user@example.com", "acct-1234567890abcdef")); err != nil {
			t.Fatalf("login %d: %v", round, err)
		}
		if len(registry.entries) != 1 {
			t.Fatalf("after login %d the catalog holds %d entries, want 1", round, len(registry.entries))
		}
		if err := provisioner.RemoveCodexProvider(context.Background()); err != nil {
			t.Fatalf("logout %d: %v", round, err)
		}
		if len(registry.entries) != 0 {
			t.Fatalf("after logout %d the catalog holds %d entries, want 0", round, len(registry.entries))
		}
	}
	if countCalls(registry, callAdd) != 2 {
		t.Errorf("add calls = %d, want 2 (one fresh entry per cycle)", countCalls(registry, callAdd))
	}
	if countCalls(registry, callDelete) != 2 {
		t.Errorf("delete calls = %d, want 2 (one removal per cycle)", countCalls(registry, callDelete))
	}
}

func TestARegistryThatLosesThePresetOnAddIsCorrected(t *testing.T) {
	registry := newFakeRegistry()
	registry.dropPresetOnAdd = true
	provisioner := NewProvisioner(registry)

	if _, err := provisioner.EnsureCodexProvider(context.Background(), testIdentity("user@example.com", "acct-1234567890abcdef")); err != nil {
		t.Fatalf("login: %v", err)
	}
	entry := registry.entries[codexapp.CodexProviderID]
	if entry.Preset != providerdomain.PresetCodex {
		t.Errorf("Preset = %q, want %q", entry.Preset, providerdomain.PresetCodex)
	}
	if entry.AccountID != providerdomain.AccountID("acct-1234567890abcdef") {
		t.Errorf("AccountID = %q, want the login account", entry.AccountID)
	}
	if countCalls(registry, callSetPreset) != 1 {
		t.Errorf("setpreset calls = %d, want 1 (the defensive correction)", countCalls(registry, callSetPreset))
	}
}

func TestAVeryLongEmailKeepsTheEntryValid(t *testing.T) {
	registry := newFakeRegistry()
	provisioner := NewProvisioner(registry)

	email := strings.Repeat("a", 200) + "@example.com"
	if _, err := provisioner.EnsureCodexProvider(context.Background(), testIdentity(email, "acct-1234567890abcdef")); err != nil {
		t.Fatalf("login with a %d-rune email: %v", len(email), err)
	}
	name := registry.entries[codexapp.CodexProviderID].Name
	if utf8.RuneCountInString(name) > nameLimit {
		t.Errorf("name is %d runes, want at most %d", utf8.RuneCountInString(name), nameLimit)
	}
	if !strings.HasPrefix(name, namePrefix) {
		t.Errorf("name = %q, want the %q prefix", name, namePrefix)
	}
}

func TestProviderNameCapsAndFilters(t *testing.T) {
	longEmail := strings.Repeat("a", 200) + "@example.com"
	hostile := "bad\x01user\x7f@example.com"
	cases := []struct {
		email string
		want  string
	}{
		{"user@example.com", "Codex — user@example.com"},
		{longEmail, "Codex — " + strings.Repeat("a", 72)},
		{hostile, "Codex — baduser@example.com"},
	}
	for _, testCase := range cases {
		if got := providerName(testCase.email); got != testCase.want {
			t.Errorf("providerName(%q) = %q, want %q", testCase.email, got, testCase.want)
		}
	}
}
