package providers

// The provisioner keeps one catalog entry per codex account. These tests
// pin the lifecycle through the registry port: a fresh login mints (or
// claims) an entry bound to the account, a returning login relinks the
// same row, logout retires it while keeping the binding, and removal
// deletes exactly what the account owns.

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"

	codexapp "github.com/luxuryprivate/switchboard/backend/internal/slices/codex/application"
	codexdomain "github.com/luxuryprivate/switchboard/backend/internal/slices/codex/domain"
	providerapp "github.com/luxuryprivate/switchboard/backend/internal/slices/providers/application"
	providerdomain "github.com/luxuryprivate/switchboard/backend/internal/slices/providers/domain"
)

var errNotFound = errors.New("provider not found")

type callKind int

const (
	callList callKind = iota
	callAdd
	callUpdate
	callSetPreset
	callDelete
)

type registryCall struct {
	kind      callKind
	id        string
	params    providerdomain.Params
	preset    providerdomain.Preset
	accountID providerdomain.AccountID
}

// fakeRegistry stands in for the providers manager. It mirrors the real
// contract the provisioner relies on: List answers existence (disabled
// entries included), Update preserves the stored builtin flag, preset and
// account id, SetPreset is the only binding change, Add honors the
// requested id and reports collisions, and Delete refuses entries the
// manager cannot delete.
type fakeRegistry struct {
	t               *testing.T
	entries         map[string]providerdomain.Provider
	activeID        string
	mintIDs         bool
	dropPresetOnAdd bool
	updateErr       error
	deleteErr       error
	addRefuses      map[string]bool
	nextMint        uint32
	calls           []registryCall
}

func newRegistry(t *testing.T) *fakeRegistry {
	t.Helper()
	return &fakeRegistry{t: t, entries: map[string]providerdomain.Provider{}}
}

func (fake *fakeRegistry) record(call registryCall) {
	fake.calls = append(fake.calls, call)
}

// mark returns a reader for the calls made after this point.
func (fake *fakeRegistry) mark() func() []registryCall {
	base := len(fake.calls)
	return func() []registryCall { return fake.calls[base:] }
}

func (fake *fakeRegistry) List(ctx context.Context) []providerdomain.Provider {
	fake.record(registryCall{kind: callList})
	if err := ctx.Err(); err != nil {
		return nil
	}
	ids := make([]string, 0, len(fake.entries))
	for id := range fake.entries {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	entries := make([]providerdomain.Provider, 0, len(ids))
	for _, id := range ids {
		entries = append(entries, fake.entries[id])
	}
	return entries
}

func (fake *fakeRegistry) Add(ctx context.Context, params providerdomain.Params) (providerdomain.Provider, error) {
	fake.record(registryCall{kind: callAdd, params: params})
	if err := ctx.Err(); err != nil {
		return providerdomain.Provider{}, err
	}
	if fake.addRefuses[params.ID] {
		return providerdomain.Provider{}, providerapp.ErrProviderIDExists
	}
	id := params.ID
	if fake.mintIDs {
		fake.nextMint++
		id = fmt.Sprintf("provider_%08x", fake.nextMint)
	}
	params.ID = id
	params.Builtin = false
	if fake.dropPresetOnAdd {
		params.Preset = ""
		params.AccountID = ""
	}
	if _, exists := fake.entries[id]; exists {
		return providerdomain.Provider{}, providerapp.ErrProviderIDExists
	}
	provider, err := providerdomain.New(params)
	if err != nil {
		fake.t.Fatalf("invalid add params: %v", err)
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
		return providerdomain.Provider{}, errNotFound
	}
	params.ID = id
	params.Builtin = current.Builtin
	params.Preset = current.Preset
	params.AccountID = current.AccountID
	provider, err := providerdomain.New(params)
	if err != nil {
		fake.t.Fatalf("invalid update params: %v", err)
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
		return providerdomain.Provider{}, errNotFound
	}
	params := paramsOf(current)
	params.Preset = preset
	params.AccountID = accountID
	provider, err := providerdomain.New(params)
	if err != nil {
		fake.t.Fatalf("invalid set-preset params: %v", err)
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
		return errNotFound
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

// paramsOf returns the entry's own params without the preset and account
// id, which SetPreset owns.
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

// customParams seeds a provider the preset does not own.
func customParams(id string) providerdomain.Params {
	return providerdomain.Params{
		ID:       id,
		Name:     "Custom " + id,
		BaseURL:  "https://example.invalid/v1",
		AuthMode: providerdomain.AuthBearer,
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

// collidingRegistry reports every Add as a lost race, whatever the id.
type collidingRegistry struct {
	*fakeRegistry
}

func (registry *collidingRegistry) Add(ctx context.Context, params providerdomain.Params) (providerdomain.Provider, error) {
	registry.record(registryCall{kind: callAdd, params: params})
	if err := ctx.Err(); err != nil {
		return providerdomain.Provider{}, err
	}
	return providerdomain.Provider{}, providerapp.ErrProviderIDExists
}

// seedPreset plants a codex preset entry directly in the catalog, the
// shape an earlier release or a hand-edited file can leave behind.
func seedPreset(t *testing.T, registry *fakeRegistry, id, accountID string, enabled bool) providerdomain.Provider {
	t.Helper()
	params := providerdomain.Params{
		ID:         id,
		Name:       retireName,
		BaseURL:    baseURL,
		AuthMode:   providerdomain.AuthBearer,
		Dialect:    providerdomain.DialectAuto,
		ModelsPath: modelsPath,
		Format:     providerdomain.FormatResponses,
		Enabled:    enabled,
		Preset:     providerdomain.PresetCodex,
		AccountID:  providerdomain.AccountID(accountID),
	}
	provider, err := providerdomain.New(params)
	if err != nil {
		t.Fatalf("seed preset entry %s: %v", id, err)
	}
	registry.entries[id] = provider
	return provider
}

// A fresh login provisions one canonical preset entry bound to the
// account.
func TestFreshLoginCreatesTheCanonicalCodexEntry(t *testing.T) {
	registry := newRegistry(t)
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
		t.Errorf("BaseURL = %v", entry.BaseURL)
	}
	if entry.AuthMode != providerdomain.AuthBearer {
		t.Errorf("AuthMode = %q", entry.AuthMode)
	}
	if entry.AuthHeader != "" {
		t.Errorf("AuthHeader = %q, want the bearer default", entry.AuthHeader)
	}
	if entry.Dialect != providerdomain.DialectAuto {
		t.Errorf("Dialect = %q", entry.Dialect)
	}
	if entry.ModelsPath != modelsPath {
		t.Errorf("ModelsPath = %q", entry.ModelsPath)
	}
	if entry.Format != providerdomain.FormatResponses {
		t.Errorf("Format = %q", entry.Format)
	}
	if entry.ChatPath != "/v1/chat/completions" {
		t.Errorf("ChatPath = %q", entry.ChatPath)
	}
	if !entry.Enabled {
		t.Error("entry is disabled")
	}
	if entry.Builtin {
		t.Error("entry is builtin")
	}
	if entry.Preset != providerdomain.PresetCodex {
		t.Errorf("Preset = %q, want codex", entry.Preset)
	}
	if entry.AccountID != "acct-1234567890abcdef" {
		t.Errorf("AccountID = %q", entry.AccountID)
	}
}

// Re-login of the SAME account relinks its own entry: the email may have
// changed on the account, the provider row is refreshed in place.
func TestReLoginRefreshesTheExistingEntry(t *testing.T) {
	registry := newRegistry(t)
	provisioner := NewProvisioner(registry)
	const accountID = "acct-1234567890abcdef"

	if _, err := provisioner.EnsureCodexProvider(context.Background(), testIdentity("first@example.com", accountID)); err != nil {
		t.Fatalf("first EnsureCodexProvider: %v", err)
	}
	since := registry.mark()
	providerID, err := provisioner.EnsureCodexProvider(context.Background(), testIdentity("second@example.com", accountID))
	if err != nil {
		t.Fatalf("second EnsureCodexProvider: %v", err)
	}
	if providerID != codexapp.CodexProviderID {
		t.Errorf("providerID = %q, want %q", providerID, codexapp.CodexProviderID)
	}
	if len(registry.entries) != 1 {
		t.Fatalf("catalog holds %d entries, want 1", len(registry.entries))
	}
	entry := registry.entries[codexapp.CodexProviderID]
	if entry.Name != "Codex — second@example.com" {
		t.Errorf("Name = %q", entry.Name)
	}
	if entry.AccountID != accountID {
		t.Errorf("AccountID = %q", entry.AccountID)
	}
	if !entry.Enabled {
		t.Error("entry is disabled")
	}
	if countCalls(registry, callAdd) != 1 {
		t.Errorf("Add called %d times, want the single first-login add", countCalls(registry, callAdd))
	}
	calls := since()
	if len(calls) != 3 ||
		calls[0].kind != callList ||
		calls[1].kind != callUpdate ||
		calls[2].kind != callSetPreset {
		t.Fatalf("re-login calls = %+v, want [list update setpreset]", calls)
	}
}

// A returning login re-enables a retired row.
func TestReLoginReEnablesADisabledEntry(t *testing.T) {
	registry := newRegistry(t)
	provisioner := NewProvisioner(registry)
	const accountID = "acct-1234567890abcdef"

	if _, err := provisioner.EnsureCodexProvider(context.Background(), testIdentity("user@example.com", accountID)); err != nil {
		t.Fatalf("first EnsureCodexProvider: %v", err)
	}
	entry, ok := registry.entries[codexapp.CodexProviderID]
	if !ok {
		t.Fatalf("no entry under %q", codexapp.CodexProviderID)
	}
	entry.Enabled = false
	registry.entries[codexapp.CodexProviderID] = entry

	if _, err := provisioner.EnsureCodexProvider(context.Background(), testIdentity("user@example.com", accountID)); err != nil {
		t.Fatalf("second EnsureCodexProvider: %v", err)
	}
	if !registry.entries[codexapp.CodexProviderID].Enabled {
		t.Error("entry stayed disabled")
	}
	if countCalls(registry, callAdd) != 1 {
		t.Errorf("Add called %d times, want 1", countCalls(registry, callAdd))
	}
}

// A second ACCOUNT gets its own entry: multi-account is the point.
func TestALoginWithADifferentAccountGetsItsOwnEntry(t *testing.T) {
	registry := newRegistry(t)
	provisioner := NewProvisioner(registry)

	first, err := provisioner.EnsureCodexProvider(context.Background(), testIdentity("first@example.com", "acct-first000000001"))
	if err != nil {
		t.Fatalf("first EnsureCodexProvider: %v", err)
	}
	second, err := provisioner.EnsureCodexProvider(context.Background(), testIdentity("second@example.com", "acct-second000000001"))
	if err != nil {
		t.Fatalf("second EnsureCodexProvider: %v", err)
	}
	if first != codexapp.CodexProviderID {
		t.Errorf("first providerID = %q, want %q", first, codexapp.CodexProviderID)
	}
	if second != codexapp.CodexProviderID+"2" {
		t.Errorf("second providerID = %q, want %q", second, codexapp.CodexProviderID+"2")
	}
	if len(registry.entries) != 2 {
		t.Fatalf("catalog holds %d entries, want 2", len(registry.entries))
	}
	firstEntry := registry.entries[codexapp.CodexProviderID]
	secondEntry := registry.entries[codexapp.CodexProviderID+"2"]
	if firstEntry.AccountID != "acct-first000000001" || secondEntry.AccountID != "acct-second000000001" {
		t.Errorf("accounts = %q / %q", firstEntry.AccountID, secondEntry.AccountID)
	}
	if !firstEntry.Enabled || !secondEntry.Enabled {
		t.Error("an entry is disabled")
	}
}

// A hand-configured provider squatting on the literal codex id is
// refused, not overwritten.
func TestACustomProviderOnTheCodexIDIsRefused(t *testing.T) {
	registry := newRegistry(t)
	if _, err := registry.Add(context.Background(), customParams(codexapp.CodexProviderID)); err != nil {
		t.Fatalf("seed custom provider: %v", err)
	}
	provisioner := NewProvisioner(registry)

	_, err := provisioner.EnsureCodexProvider(context.Background(), testIdentity("user@example.com", "acct-1234567890abcdef"))
	if !errors.Is(err, ErrProviderIdTaken) {
		t.Fatalf("EnsureCodexProvider error = %v, want ErrProviderIdTaken", err)
	}
	if len(registry.entries) != 1 {
		t.Fatalf("catalog holds %d entries, want the seed only", len(registry.entries))
	}
	entry := registry.entries[codexapp.CodexProviderID]
	if entry.Preset != "" || entry.AccountID != "" {
		t.Errorf("stranger entry gained preset=%q account=%q", entry.Preset, entry.AccountID)
	}
	if countCalls(registry, callAdd) != 1 {
		t.Errorf("Add called %d times, want the single seed", countCalls(registry, callAdd))
	}
}

// An Add that loses a race on the literal id is refused.
func TestAnAddThatRacesIntoATakenIDIsRefused(t *testing.T) {
	registry := newRegistry(t)
	provisioner := NewProvisioner(&collidingRegistry{fakeRegistry: registry})

	since := registry.mark()
	_, err := provisioner.EnsureCodexProvider(context.Background(), testIdentity("user@example.com", "acct-1234567890abcdef"))
	if !errors.Is(err, ErrProviderIdTaken) {
		t.Fatalf("EnsureCodexProvider error = %v, want ErrProviderIdTaken", err)
	}
	calls := since()
	if len(calls) != 2 || calls[0].kind != callList || calls[1].kind != callAdd {
		t.Fatalf("calls = %+v, want [list add]", calls)
	}
}

// The first UNBOUND preset entry is claimed for a new account — the
// shape a pre-multi-account release left behind.
func TestTheFirstUnboundPresetEntryIsClaimedForANewAccount(t *testing.T) {
	registry := newRegistry(t)
	provisioner := NewProvisioner(registry)
	if _, err := provisioner.EnsureCodexProvider(context.Background(), testIdentity("first@example.com", "acct-first000000001")); err != nil {
		t.Fatalf("first EnsureCodexProvider: %v", err)
	}
	seedPreset(t, registry, codexapp.CodexProviderID+"2", "", false)
	since := registry.mark()

	providerID, err := provisioner.EnsureCodexProvider(context.Background(), testIdentity("second@example.com", "acct-second000000001"))
	if err != nil {
		t.Fatalf("second EnsureCodexProvider: %v", err)
	}
	if providerID != codexapp.CodexProviderID+"2" {
		t.Fatalf("providerID = %q, want the seeded slot %q", providerID, codexapp.CodexProviderID+"2")
	}
	entry := registry.entries[codexapp.CodexProviderID+"2"]
	if !entry.Enabled {
		t.Error("claimed entry is disabled")
	}
	if entry.Name != "Codex — second@example.com" {
		t.Errorf("Name = %q", entry.Name)
	}
	if entry.AccountID != "acct-second000000001" {
		t.Errorf("AccountID = %q", entry.AccountID)
	}
	if len(registry.entries) != 2 {
		t.Fatalf("catalog holds %d entries, want 2", len(registry.entries))
	}
	calls := since()
	if countCalls(registry, callAdd) != 1 {
		t.Errorf("Add called %d times, want the first login only", countCalls(registry, callAdd))
	}
	if len(calls) != 3 || calls[0].kind != callList || calls[1].kind != callUpdate || calls[2].kind != callSetPreset {
		t.Fatalf("claim calls = %+v, want [list update setpreset]", calls)
	}
}

// A stranger on a derived id is skipped, not refused: only the literal id
// is reserved.
func TestAStrangerOnACodexNIDIsSkipped(t *testing.T) {
	registry := newRegistry(t)
	provisioner := NewProvisioner(registry)
	if _, err := provisioner.EnsureCodexProvider(context.Background(), testIdentity("first@example.com", "acct-first000000001")); err != nil {
		t.Fatalf("first EnsureCodexProvider: %v", err)
	}
	stranger, err := registry.Add(context.Background(), customParams(codexapp.CodexProviderID+"2"))
	if err != nil {
		t.Fatalf("seed stranger: %v", err)
	}

	providerID, err := provisioner.EnsureCodexProvider(context.Background(), testIdentity("second@example.com", "acct-second000000001"))
	if err != nil {
		t.Fatalf("second EnsureCodexProvider: %v", err)
	}
	if providerID != codexapp.CodexProviderID+"3" {
		t.Fatalf("providerID = %q, want %q", providerID, codexapp.CodexProviderID+"3")
	}
	if got := registry.entries[codexapp.CodexProviderID+"2"]; got != stranger {
		t.Error("the stranger was written")
	}
}

// An Add that loses a race on a derived id moves on to the next slot.
func TestAnAddRacingIntoACodexNIDMovesOn(t *testing.T) {
	registry := newRegistry(t)
	registry.addRefuses = map[string]bool{codexapp.CodexProviderID + "2": true}
	provisioner := NewProvisioner(registry)
	if _, err := provisioner.EnsureCodexProvider(context.Background(), testIdentity("first@example.com", "acct-first000000001")); err != nil {
		t.Fatalf("first EnsureCodexProvider: %v", err)
	}

	providerID, err := provisioner.EnsureCodexProvider(context.Background(), testIdentity("second@example.com", "acct-second000000001"))
	if err != nil {
		t.Fatalf("second EnsureCodexProvider: %v", err)
	}
	if providerID != codexapp.CodexProviderID+"3" {
		t.Fatalf("providerID = %q, want %q", providerID, codexapp.CodexProviderID+"3")
	}
	if len(registry.entries) != 2 {
		t.Fatalf("catalog holds %d entries, want 2", len(registry.entries))
	}
}

// Retire disables the account's entry and neutralizes its name, but
// keeps the binding so the row survives the sign-out.
func TestRetireDisablesTheEntryAndKeepsTheBinding(t *testing.T) {
	registry := newRegistry(t)
	provisioner := NewProvisioner(registry)
	const accountID = "acct-1234567890abcdef"
	if _, err := provisioner.EnsureCodexProvider(context.Background(), testIdentity("user@example.com", accountID)); err != nil {
		t.Fatalf("EnsureCodexProvider: %v", err)
	}

	if err := provisioner.RetireCodexProvider(context.Background(), accountID); err != nil {
		t.Fatalf("RetireCodexProvider: %v", err)
	}
	entry, ok := registry.entries[codexapp.CodexProviderID]
	if !ok {
		t.Fatal("the entry was removed")
	}
	if entry.Enabled {
		t.Error("entry is still enabled")
	}
	if entry.Name != retireName {
		t.Errorf("Name = %q, want %q", entry.Name, retireName)
	}
	if entry.Preset != providerdomain.PresetCodex {
		t.Errorf("Preset = %q, want codex", entry.Preset)
	}
	if entry.AccountID != accountID {
		t.Errorf("AccountID = %q, want the binding kept", entry.AccountID)
	}
}

func TestRetireWithoutAnEntryIsANoOp(t *testing.T) {
	registry := newRegistry(t)
	provisioner := NewProvisioner(registry)

	if err := provisioner.RetireCodexProvider(context.Background(), "acct-1234567890abcdef"); err != nil {
		t.Fatalf("RetireCodexProvider: %v", err)
	}
	if len(registry.calls) != 1 || registry.calls[0].kind != callList {
		t.Fatalf("calls = %+v, want a single list", registry.calls)
	}
}

func TestRetireLeavesACustomProviderAlone(t *testing.T) {
	registry := newRegistry(t)
	stranger, err := registry.Add(context.Background(), customParams(codexapp.CodexProviderID))
	if err != nil {
		t.Fatalf("seed stranger: %v", err)
	}
	provisioner := NewProvisioner(registry)

	if err := provisioner.RetireCodexProvider(context.Background(), "acct-1234567890abcdef"); err != nil {
		t.Fatalf("RetireCodexProvider: %v", err)
	}
	if entry := registry.entries[codexapp.CodexProviderID]; entry != stranger {
		t.Error("the stranger was written")
	}
	for _, kind := range []callKind{callUpdate, callSetPreset, callDelete} {
		if count := countCalls(registry, kind); count != 0 {
			t.Errorf("call kind %d happened %d times, want 0: a stranger is never written", kind, count)
		}
	}
}

func TestRetireRefusesWhileTheCodexEntryIsActive(t *testing.T) {
	registry := newRegistry(t)
	provisioner := NewProvisioner(registry)
	const accountID = "acct-1234567890abcdef"
	if _, err := provisioner.EnsureCodexProvider(context.Background(), testIdentity("user@example.com", accountID)); err != nil {
		t.Fatalf("EnsureCodexProvider: %v", err)
	}
	before := registry.entries[codexapp.CodexProviderID]
	registry.activeID = codexapp.CodexProviderID

	err := provisioner.RetireCodexProvider(context.Background(), accountID)
	if !errors.Is(err, providerapp.ErrActiveProvider) {
		t.Fatalf("RetireCodexProvider error = %v, want ErrActiveProvider", err)
	}
	if entry := registry.entries[codexapp.CodexProviderID]; entry != before {
		t.Error("the refused retire changed the entry")
	}
}

func TestRetireIsIdempotent(t *testing.T) {
	registry := newRegistry(t)
	provisioner := NewProvisioner(registry)
	const accountID = "acct-1234567890abcdef"
	if _, err := provisioner.EnsureCodexProvider(context.Background(), testIdentity("user@example.com", accountID)); err != nil {
		t.Fatalf("EnsureCodexProvider: %v", err)
	}
	for round := 0; round < 2; round++ {
		if err := provisioner.RetireCodexProvider(context.Background(), accountID); err != nil {
			t.Fatalf("round %d RetireCodexProvider: %v", round, err)
		}
	}
	entry, ok := registry.entries[codexapp.CodexProviderID]
	if !ok {
		t.Fatal("the entry was removed")
	}
	if entry.Enabled {
		t.Error("entry is still enabled")
	}
	if entry.Preset != providerdomain.PresetCodex || entry.AccountID != accountID {
		t.Errorf("Preset = %q AccountID = %q, want the binding kept", entry.Preset, entry.AccountID)
	}
}

// The account's own next login relinks its retired entry.
func TestARetiredEntryIsRelinkedByItsOwnAccount(t *testing.T) {
	registry := newRegistry(t)
	provisioner := NewProvisioner(registry)
	const accountID = "acct-1234567890abcdef"
	if _, err := provisioner.EnsureCodexProvider(context.Background(), testIdentity("user@example.com", accountID)); err != nil {
		t.Fatalf("first EnsureCodexProvider: %v", err)
	}
	if err := provisioner.RetireCodexProvider(context.Background(), accountID); err != nil {
		t.Fatalf("RetireCodexProvider: %v", err)
	}

	providerID, err := provisioner.EnsureCodexProvider(context.Background(), testIdentity("returning@example.com", accountID))
	if err != nil {
		t.Fatalf("second EnsureCodexProvider: %v", err)
	}
	if providerID != codexapp.CodexProviderID {
		t.Errorf("providerID = %q, want the retired row relinked", providerID)
	}
	if countCalls(registry, callAdd) != 1 {
		t.Errorf("Add called %d times, want 1", countCalls(registry, callAdd))
	}
	entry := registry.entries[codexapp.CodexProviderID]
	if !entry.Enabled {
		t.Error("entry is disabled")
	}
	if entry.Name != "Codex — returning@example.com" {
		t.Errorf("Name = %q", entry.Name)
	}
}

// A retired row is bound to its account: another account cannot claim
// it.
func TestARetiredEntryIsNotClaimedByAnotherAccount(t *testing.T) {
	registry := newRegistry(t)
	provisioner := NewProvisioner(registry)
	if _, err := provisioner.EnsureCodexProvider(context.Background(), testIdentity("first@example.com", "acct-first000000001")); err != nil {
		t.Fatalf("first EnsureCodexProvider: %v", err)
	}
	if err := provisioner.RetireCodexProvider(context.Background(), "acct-first000000001"); err != nil {
		t.Fatalf("RetireCodexProvider: %v", err)
	}

	providerID, err := provisioner.EnsureCodexProvider(context.Background(), testIdentity("second@example.com", "acct-second000000001"))
	if err != nil {
		t.Fatalf("second EnsureCodexProvider: %v", err)
	}
	if providerID != codexapp.CodexProviderID+"2" {
		t.Fatalf("providerID = %q, want %q", providerID, codexapp.CodexProviderID+"2")
	}
	entry, ok := registry.entries[codexapp.CodexProviderID]
	if !ok {
		t.Fatal("the retired row was removed")
	}
	if entry.Enabled || entry.Name != retireName || entry.AccountID != "acct-first000000001" {
		t.Errorf("retired row = enabled:%v name:%q account:%q", entry.Enabled, entry.Name, entry.AccountID)
	}
}

func TestRemoveDeletesTheProvisionedEntry(t *testing.T) {
	registry := newRegistry(t)
	provisioner := NewProvisioner(registry)
	const accountID = "acct-1234567890abcdef"
	if _, err := provisioner.EnsureCodexProvider(context.Background(), testIdentity("user@example.com", accountID)); err != nil {
		t.Fatalf("EnsureCodexProvider: %v", err)
	}

	if err := provisioner.RemoveCodexProvider(context.Background(), accountID); err != nil {
		t.Fatalf("RemoveCodexProvider: %v", err)
	}
	if len(registry.entries) != 0 {
		t.Fatalf("catalog holds %d entries, want 0", len(registry.entries))
	}
}

func TestRemoveDeletesOnlyTheBoundEntry(t *testing.T) {
	registry := newRegistry(t)
	provisioner := NewProvisioner(registry)
	if _, err := provisioner.EnsureCodexProvider(context.Background(), testIdentity("first@example.com", "acct-first000000001")); err != nil {
		t.Fatalf("first EnsureCodexProvider: %v", err)
	}
	if _, err := provisioner.EnsureCodexProvider(context.Background(), testIdentity("second@example.com", "acct-second000000001")); err != nil {
		t.Fatalf("second EnsureCodexProvider: %v", err)
	}

	if err := provisioner.RemoveCodexProvider(context.Background(), "acct-first000000001"); err != nil {
		t.Fatalf("RemoveCodexProvider: %v", err)
	}
	if len(registry.entries) != 1 {
		t.Fatalf("catalog holds %d entries, want 1", len(registry.entries))
	}
	entry := registry.entries[codexapp.CodexProviderID+"2"]
	if entry.AccountID != "acct-second000000001" {
		t.Errorf("survivor AccountID = %q", entry.AccountID)
	}
}

// An empty account id tears down every preset entry — the shape a
// pre-multi-account release could leave behind with no live account.
func TestRemoveWithoutAnAccountDeletesEveryPresetEntry(t *testing.T) {
	registry := newRegistry(t)
	provisioner := NewProvisioner(registry)
	if _, err := provisioner.EnsureCodexProvider(context.Background(), testIdentity("first@example.com", "acct-first000000001")); err != nil {
		t.Fatalf("first EnsureCodexProvider: %v", err)
	}
	if _, err := provisioner.EnsureCodexProvider(context.Background(), testIdentity("second@example.com", "acct-second000000001")); err != nil {
		t.Fatalf("second EnsureCodexProvider: %v", err)
	}
	stranger, err := registry.Add(context.Background(), customParams("mine"))
	if err != nil {
		t.Fatalf("seed stranger: %v", err)
	}

	if err := provisioner.RemoveCodexProvider(context.Background(), ""); err != nil {
		t.Fatalf("RemoveCodexProvider: %v", err)
	}
	if len(registry.entries) != 1 {
		t.Fatalf("catalog holds %d entries, want the stranger only", len(registry.entries))
	}
	if entry := registry.entries["mine"]; entry != stranger {
		t.Error("the stranger was written")
	}
}

func TestRemoveWithoutAnEntryIsANoOp(t *testing.T) {
	registry := newRegistry(t)
	provisioner := NewProvisioner(registry)

	if err := provisioner.RemoveCodexProvider(context.Background(), "acct-1234567890abcdef"); err != nil {
		t.Fatalf("RemoveCodexProvider: %v", err)
	}
	if len(registry.calls) != 1 || registry.calls[0].kind != callList {
		t.Fatalf("calls = %+v, want a single list", registry.calls)
	}
}

func TestRemoveRefusesWhileTheCodexEntryIsActive(t *testing.T) {
	registry := newRegistry(t)
	provisioner := NewProvisioner(registry)
	const accountID = "acct-1234567890abcdef"
	if _, err := provisioner.EnsureCodexProvider(context.Background(), testIdentity("user@example.com", accountID)); err != nil {
		t.Fatalf("EnsureCodexProvider: %v", err)
	}
	before := registry.entries[codexapp.CodexProviderID]
	registry.activeID = codexapp.CodexProviderID

	err := provisioner.RemoveCodexProvider(context.Background(), accountID)
	if !errors.Is(err, providerapp.ErrActiveProvider) {
		t.Fatalf("RemoveCodexProvider error = %v, want ErrActiveProvider", err)
	}
	if entry := registry.entries[codexapp.CodexProviderID]; entry != before {
		t.Error("the refused remove deleted the entry")
	}
}

func TestRemoveRefusesOnABuiltinCodexEntry(t *testing.T) {
	registry := newRegistry(t)
	entry := seedPreset(t, registry, codexapp.CodexProviderID, "", true)
	entry.Builtin = true
	registry.entries[codexapp.CodexProviderID] = entry
	provisioner := NewProvisioner(registry)

	err := provisioner.RemoveCodexProvider(context.Background(), "")
	if !errors.Is(err, providerapp.ErrBuiltinProvider) {
		t.Fatalf("RemoveCodexProvider error = %v, want ErrBuiltinProvider", err)
	}
	if _, ok := registry.entries[codexapp.CodexProviderID]; !ok {
		t.Fatal("the builtin entry was deleted")
	}
}

func TestRemoveCarriesARegistryFailureThroughAndKeepsTheEntry(t *testing.T) {
	registry := newRegistry(t)
	provisioner := NewProvisioner(registry)
	const accountID = "acct-1234567890abcdef"
	if _, err := provisioner.EnsureCodexProvider(context.Background(), testIdentity("user@example.com", accountID)); err != nil {
		t.Fatalf("EnsureCodexProvider: %v", err)
	}
	registry.deleteErr = errors.New("boom")

	err := provisioner.RemoveCodexProvider(context.Background(), accountID)
	if err != registry.deleteErr {
		t.Fatalf("RemoveCodexProvider error = %v, want the registry failure verbatim", err)
	}
	if _, ok := registry.entries[codexapp.CodexProviderID]; !ok {
		t.Fatal("the entry was deleted despite the failure")
	}
}

// A cancelled context refuses before the catalog is even read.
func TestACancelledContextPropagates(t *testing.T) {
	registry := newRegistry(t)
	provisioner := NewProvisioner(registry)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := provisioner.EnsureCodexProvider(ctx, testIdentity("user@example.com", "acct-1234567890abcdef")); !errors.Is(err, context.Canceled) {
		t.Errorf("EnsureCodexProvider error = %v, want context.Canceled", err)
	}
	if err := provisioner.RetireCodexProvider(ctx, "acct-1234567890abcdef"); !errors.Is(err, context.Canceled) {
		t.Errorf("RetireCodexProvider error = %v, want context.Canceled", err)
	}
	if err := provisioner.RemoveCodexProvider(ctx, "acct-1234567890abcdef"); !errors.Is(err, context.Canceled) {
		t.Errorf("RemoveCodexProvider error = %v, want context.Canceled", err)
	}
	if len(registry.calls) != 0 {
		t.Fatalf("registry calls = %+v, want none: the context is checked before the catalog is read", registry.calls)
	}
	if len(registry.entries) != 0 {
		t.Fatalf("catalog holds %d entries, want 0", len(registry.entries))
	}
}

func TestRegistryErrorsPropagateVerbatim(t *testing.T) {
	registry := newRegistry(t)
	provisioner := NewProvisioner(registry)
	const accountID = "acct-1234567890abcdef"
	if _, err := provisioner.EnsureCodexProvider(context.Background(), testIdentity("user@example.com", accountID)); err != nil {
		t.Fatalf("first EnsureCodexProvider: %v", err)
	}
	registry.updateErr = errors.New("boom")

	_, err := provisioner.EnsureCodexProvider(context.Background(), testIdentity("user@example.com", accountID))
	if err != registry.updateErr {
		t.Fatalf("EnsureCodexProvider error = %v, want the registry failure verbatim", err)
	}
}

// A registry that mints its own ids still ends up with one bound entry,
// and the minted id is remembered.
func TestAMintingRegistryGetsItsEntryRemembered(t *testing.T) {
	registry := newRegistry(t)
	registry.mintIDs = true
	provisioner := NewProvisioner(registry)

	first, err := provisioner.EnsureCodexProvider(context.Background(), testIdentity("user@example.com", "acct-1234567890abcdef"))
	if err != nil {
		t.Fatalf("first EnsureCodexProvider: %v", err)
	}
	if first != "provider_00000001" {
		t.Fatalf("providerID = %q, want the minted id", first)
	}
	since := registry.mark()
	second, err := provisioner.EnsureCodexProvider(context.Background(), testIdentity("user@example.com", "acct-1234567890abcdef"))
	if err != nil {
		t.Fatalf("second EnsureCodexProvider: %v", err)
	}
	if second != first {
		t.Fatalf("providerID = %q, want %q", second, first)
	}
	if countCalls(registry, callAdd) != 1 {
		t.Errorf("Add called %d times, want 1", countCalls(registry, callAdd))
	}
	calls := since()
	if len(calls) != 3 || calls[0].kind != callList || calls[1].kind != callUpdate || calls[2].kind != callSetPreset {
		t.Fatalf("re-login calls = %+v, want [list update setpreset]", calls)
	}
}

// Login/logout cycles on a minting registry stay clean: no duplicate
// entries accumulate.
func TestLoginLogoutLoginOnAMintingRegistryStaysClean(t *testing.T) {
	registry := newRegistry(t)
	registry.mintIDs = true
	provisioner := NewProvisioner(registry)
	const accountID = "acct-1234567890abcdef"

	for round := 0; round < 2; round++ {
		if _, err := provisioner.EnsureCodexProvider(context.Background(), testIdentity("user@example.com", accountID)); err != nil {
			t.Fatalf("round %d EnsureCodexProvider: %v", round, err)
		}
		if err := provisioner.RemoveCodexProvider(context.Background(), accountID); err != nil {
			t.Fatalf("round %d RemoveCodexProvider: %v", round, err)
		}
	}
	if countCalls(registry, callAdd) != 2 {
		t.Errorf("Add called %d times, want 2", countCalls(registry, callAdd))
	}
	if countCalls(registry, callDelete) != 2 {
		t.Errorf("Delete called %d times, want 2", countCalls(registry, callDelete))
	}
	if len(registry.entries) != 0 {
		t.Fatalf("catalog holds %d entries, want 0", len(registry.entries))
	}
}

// A registry that drops the preset binding on Add gets corrected.
func TestARegistryThatLosesThePresetOnAddIsCorrected(t *testing.T) {
	registry := newRegistry(t)
	registry.dropPresetOnAdd = true
	provisioner := NewProvisioner(registry)

	if _, err := provisioner.EnsureCodexProvider(context.Background(), testIdentity("user@example.com", "acct-1234567890abcdef")); err != nil {
		t.Fatalf("EnsureCodexProvider: %v", err)
	}
	if countCalls(registry, callSetPreset) != 1 {
		t.Fatalf("SetPreset called %d times, want the single correction", countCalls(registry, callSetPreset))
	}
	entry, ok := registry.entries[codexapp.CodexProviderID]
	if !ok {
		t.Fatalf("no entry under %q", codexapp.CodexProviderID)
	}
	if entry.Preset != providerdomain.PresetCodex || entry.AccountID != "acct-1234567890abcdef" {
		t.Errorf("Preset = %q AccountID = %q, want both restored", entry.Preset, entry.AccountID)
	}
}

// A very long email still yields a valid entry.
func TestAVeryLongEmailKeepsTheEntryValid(t *testing.T) {
	registry := newRegistry(t)
	provisioner := NewProvisioner(registry)
	email := strings.Repeat("a", 200) + "@example.com"

	if _, err := provisioner.EnsureCodexProvider(context.Background(), testIdentity(email, "acct-1234567890abcdef")); err != nil {
		t.Fatalf("EnsureCodexProvider: %v", err)
	}
	entry, ok := registry.entries[codexapp.CodexProviderID]
	if !ok {
		t.Fatalf("no entry under %q", codexapp.CodexProviderID)
	}
	if got := len([]rune(entry.Name)); got > nameLimit {
		t.Errorf("Name is %d runes, want at most %d", got, nameLimit)
	}
}

func TestProviderNameCapsAndFilters(t *testing.T) {
	if got := providerName("user@example.com"); got != "Codex — user@example.com" {
		t.Errorf("providerName = %q", got)
	}
	if got := providerName(strings.Repeat("a", 200) + "@example.com"); got != "Codex — "+strings.Repeat("a", 72) {
		t.Errorf("providerName = %q, want the prefix plus 72 runes", got)
	}
	if got := providerName("bad\x01user\x7f@example.com"); got != "Codex — baduser@example.com" {
		t.Errorf("providerName = %q, want control characters dropped", got)
	}
}
