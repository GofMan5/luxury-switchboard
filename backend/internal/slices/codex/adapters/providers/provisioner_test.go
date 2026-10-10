package providers

// The provisioner keeps exactly one catalog entry for the whole codex
// fleet. These tests pin the lifecycle through the registry port: the
// first sign-in creates the fleet row under the literal id with the
// canonical shape, later sign-ins refresh that same row in place,
// legacy per-account rows a pre-1.0.56 release minted are folded into
// it so the operator's routes survive the upgrade, logout parks the
// row while keeping the preset marker, and removal deletes every
// preset entry the codex slice owns — never a stranger's.

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"

	codexapp "github.com/luxuryprivate/switchboard/backend/internal/slices/codex/application"
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
	callFold
	callDelete
)

type registryCall struct {
	kind      callKind
	id        string
	toID      string
	params    providerdomain.Params
	preset    providerdomain.Preset
	accountID providerdomain.AccountID
}

// fakeRegistry stands in for the providers manager. It mirrors the real
// contract the provisioner relies on: List answers existence (disabled
// entries included), Update preserves the stored builtin flag, preset
// and account id, SetPreset is the only binding change, Add honors the
// requested id and reports collisions, Fold removes the folded entry
// (the route retargeting it stands for is the manager's business), and
// Delete refuses entries the manager cannot delete.
type fakeRegistry struct {
	t               *testing.T
	entries         map[string]providerdomain.Provider
	activeID        string
	mintIDs         bool
	dropPresetOnAdd bool
	updateErr       error
	deleteErr       error
	foldErr         error
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

func (fake *fakeRegistry) Fold(ctx context.Context, fromID, toID string) error {
	fake.record(registryCall{kind: callFold, id: fromID, toID: toID})
	if err := ctx.Err(); err != nil {
		return err
	}
	if fake.foldErr != nil {
		return fake.foldErr
	}
	if _, ok := fake.entries[fromID]; !ok {
		return errNotFound
	}
	delete(fake.entries, fromID)
	return nil
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
	return len(filterKinds(fake.calls, kind))
}

// filterKinds narrows a call list to one verb, so a test can count calls
// made after its seeding mark without the seeding itself.
func filterKinds(calls []registryCall, kind callKind) []registryCall {
	var matched []registryCall
	for _, call := range calls {
		if call.kind == kind {
			matched = append(matched, call)
		}
	}
	return matched
}

// callNames render a call sequence readably: the choreography — which
// registry verb answers which lifecycle step — is part of what these
// tests pin, so failures have to name it.
var callNames = map[callKind]string{
	callList:      "list",
	callAdd:       "add",
	callUpdate:    "update",
	callSetPreset: "setpreset",
	callFold:      "fold",
	callDelete:    "delete",
}

func kindsString(kinds []callKind) string {
	parts := make([]string, 0, len(kinds))
	for _, kind := range kinds {
		name := callNames[kind]
		if name == "" {
			name = fmt.Sprintf("kind%d", kind)
		}
		parts = append(parts, name)
	}
	return strings.Join(parts, " ")
}

func callKinds(calls []registryCall) []callKind {
	kinds := make([]callKind, len(calls))
	for i, call := range calls {
		kinds[i] = call.kind
	}
	return kinds
}

// wantKinds fatals unless the recorded calls happened in exactly the
// given order.
func wantKinds(t *testing.T, calls []registryCall, kinds ...callKind) {
	t.Helper()
	got := callKinds(calls)
	if len(got) != len(kinds) {
		t.Fatalf("call kinds = %q, want %q", kindsString(got), kindsString(kinds))
	}
	for i, kind := range got {
		if kind != kinds[i] {
			t.Fatalf("call kinds = %q, want %q", kindsString(got), kindsString(kinds))
		}
	}
}

// foldIDs lists every fold's from→to pair in call order.
func foldIDs(fake *fakeRegistry) [][2]string {
	var pairs [][2]string
	for _, call := range fake.calls {
		if call.kind == callFold {
			pairs = append(pairs, [2]string{call.id, call.toID})
		}
	}
	return pairs
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
// shape an earlier release or a hand-edited file can leave behind: the
// name and account binding of that release's per-account rows.
func seedPreset(t *testing.T, registry *fakeRegistry, id, name, accountID string, enabled bool) providerdomain.Provider {
	t.Helper()
	params := providerdomain.Params{
		ID:         id,
		Name:       name,
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

// The first sign-in provisions the one canonical fleet row: the literal
// id, the fixed fleet name, the bearer shape the relay expects, and —
// the point of the fleet — no account binding at all.
func TestAFreshCatalogGetsTheCanonicalFleetRow(t *testing.T) {
	registry := newRegistry(t)
	provisioner := NewProvisioner(registry)

	providerID, err := provisioner.EnsureCodexProvider(context.Background())
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
	if entry.Name != fleetName {
		t.Errorf("Name = %q, want %q", entry.Name, fleetName)
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
	if entry.AccountID != "" {
		t.Errorf("AccountID = %q, want the fleet-wide empty binding", entry.AccountID)
	}
}

// Every later sign-in answers with the same row: the catalog never
// grows with the fleet. The choreography is a read and an in-place
// refresh — no Add, no binding change, no fold.
func TestALaterSignInRefreshesTheSameRow(t *testing.T) {
	registry := newRegistry(t)
	provisioner := NewProvisioner(registry)
	if _, err := provisioner.EnsureCodexProvider(context.Background()); err != nil {
		t.Fatalf("first EnsureCodexProvider: %v", err)
	}
	since := registry.mark()

	providerID, err := provisioner.EnsureCodexProvider(context.Background())
	if err != nil {
		t.Fatalf("second EnsureCodexProvider: %v", err)
	}
	if providerID != codexapp.CodexProviderID {
		t.Errorf("providerID = %q, want %q", providerID, codexapp.CodexProviderID)
	}
	if len(registry.entries) != 1 {
		t.Fatalf("catalog holds %d entries, want 1", len(registry.entries))
	}
	if countCalls(registry, callAdd) != 1 {
		t.Errorf("Add called %d times, want the first sign-in only", countCalls(registry, callAdd))
	}
	wantKinds(t, since(), callList, callUpdate)
}

// A stranger on the literal id blocks the fleet outright: the id is the
// routing anchor the relay's token source matches, so nothing may be
// derived around it anymore. The stranger is never written.
func TestAStrangerOnTheLiteralIDRefusesTheFleet(t *testing.T) {
	registry := newRegistry(t)
	stranger, err := registry.Add(context.Background(), customParams(codexapp.CodexProviderID))
	if err != nil {
		t.Fatalf("seed stranger: %v", err)
	}
	since := registry.mark()
	provisioner := NewProvisioner(registry)

	_, err = provisioner.EnsureCodexProvider(context.Background())
	if !errors.Is(err, ErrProviderIdTaken) {
		t.Fatalf("EnsureCodexProvider error = %v, want ErrProviderIdTaken", err)
	}
	if entry := registry.entries[codexapp.CodexProviderID]; entry != stranger {
		t.Error("the stranger was written")
	}
	for _, kind := range []callKind{callAdd, callUpdate, callSetPreset, callFold, callDelete} {
		if count := len(filterKinds(since(), kind)); count != 0 {
			t.Errorf("call %q happened %d times, want 0: a stranger is never written", callNames[kind], count)
		}
	}
}

// An Add that loses a race on the literal id is the same refusal: the
// slot was free when List read it and is not now.
func TestAnAddRacingIntoTheLiteralIDRefusesTheFleet(t *testing.T) {
	registry := &collidingRegistry{newRegistry(t)}
	provisioner := NewProvisioner(registry)

	_, err := provisioner.EnsureCodexProvider(context.Background())
	if !errors.Is(err, ErrProviderIdTaken) {
		t.Fatalf("EnsureCodexProvider error = %v, want ErrProviderIdTaken", err)
	}
	wantKinds(t, registry.calls, callList, callAdd)
}

// A registry that mints its own id moves the routing anchor out from
// under the relay; the provisioner refuses rather than chase it.
func TestAMintingRegistryIsRefused(t *testing.T) {
	registry := newRegistry(t)
	registry.mintIDs = true
	provisioner := NewProvisioner(registry)

	_, err := provisioner.EnsureCodexProvider(context.Background())
	if err == nil || err.Error() != `codex row was minted under "provider_00000001"` {
		t.Fatalf("EnsureCodexProvider error = %v, want the minted-id refusal", err)
	}
}

// A registry that loses the preset on the way in is corrected by
// SetPreset, keyed by the id the registry reported.
func TestARegistryThatLosesThePresetOnAddIsCorrected(t *testing.T) {
	registry := newRegistry(t)
	registry.dropPresetOnAdd = true
	provisioner := NewProvisioner(registry)

	if _, err := provisioner.EnsureCodexProvider(context.Background()); err != nil {
		t.Fatalf("EnsureCodexProvider: %v", err)
	}
	if countCalls(registry, callSetPreset) != 1 {
		t.Fatalf("SetPreset called %d times, want the single correction", countCalls(registry, callSetPreset))
	}
	entry, ok := registry.entries[codexapp.CodexProviderID]
	if !ok {
		t.Fatalf("no entry under %q", codexapp.CodexProviderID)
	}
	if entry.Preset != providerdomain.PresetCodex || entry.AccountID != "" {
		t.Errorf("Preset = %q AccountID = %q, want the marker restored and no binding", entry.Preset, entry.AccountID)
	}
}

// Legacy per-account rows a pre-1.0.56 release minted are folded into
// the fleet row: the fleet row is ensured first, then each legacy row
// is folded away so the operator's routes survive the upgrade pointed
// at the row that now serves every account.
func TestLegacyPresetRowsAreFoldedIntoTheFleetRow(t *testing.T) {
	registry := newRegistry(t)
	seedPreset(t, registry, codexapp.CodexProviderID+"2", "Codex — first@example.com", "acct-first000000001", true)
	seedPreset(t, registry, codexapp.CodexProviderID+"3", "Codex — second@example.com", "acct-second000000001", false)
	provisioner := NewProvisioner(registry)

	providerID, err := provisioner.EnsureCodexProvider(context.Background())
	if err != nil {
		t.Fatalf("EnsureCodexProvider: %v", err)
	}
	if providerID != codexapp.CodexProviderID {
		t.Errorf("providerID = %q, want %q", providerID, codexapp.CodexProviderID)
	}
	if len(registry.entries) != 1 {
		t.Fatalf("catalog holds %d entries, want the fleet row only", len(registry.entries))
	}
	entry := registry.entries[codexapp.CodexProviderID]
	if entry.Preset != providerdomain.PresetCodex || entry.AccountID != "" || !entry.Enabled {
		t.Errorf("fleet row = preset:%q account:%q enabled:%v, want codex, unbound, enabled", entry.Preset, entry.AccountID, entry.Enabled)
	}
	folds := foldIDs(registry)
	if len(folds) != 2 ||
		folds[0] != [2]string{codexapp.CodexProviderID + "2", codexapp.CodexProviderID} ||
		folds[1] != [2]string{codexapp.CodexProviderID + "3", codexapp.CodexProviderID} {
		t.Fatalf("folds = %v, want each legacy row folded into the fleet row in list order", folds)
	}
	wantKinds(t, registry.calls, callList, callAdd, callFold, callFold)
}

// A fold that fails aborts the ensure with the remainder in place, and
// the next attempt finishes the migration: List only ever names rows
// that still exist, so the retry folds whatever is left and never
// re-adds the fleet row.
func TestAFailingFoldAbortsTheEnsureAndTheRetryFinishes(t *testing.T) {
	registry := newRegistry(t)
	seedPreset(t, registry, codexapp.CodexProviderID+"2", "Codex — first@example.com", "acct-first000000001", true)
	seedPreset(t, registry, codexapp.CodexProviderID+"3", "Codex — second@example.com", "acct-second000000001", true)
	registry.foldErr = errors.New("boom")
	provisioner := NewProvisioner(registry)

	if _, err := provisioner.EnsureCodexProvider(context.Background()); err != registry.foldErr {
		t.Fatalf("first EnsureCodexProvider error = %v, want the fold failure verbatim", err)
	}
	if _, ok := registry.entries[codexapp.CodexProviderID]; !ok {
		t.Fatal("the fleet row was not created before the folds")
	}
	if _, ok := registry.entries[codexapp.CodexProviderID+"2"]; !ok {
		t.Fatal("the row whose fold failed was removed anyway")
	}
	if _, ok := registry.entries[codexapp.CodexProviderID+"3"]; !ok {
		t.Fatal("the row after the failed fold was removed anyway")
	}

	registry.foldErr = nil
	since := registry.mark()
	providerID, err := provisioner.EnsureCodexProvider(context.Background())
	if err != nil {
		t.Fatalf("retry EnsureCodexProvider: %v", err)
	}
	if providerID != codexapp.CodexProviderID {
		t.Errorf("providerID = %q, want %q", providerID, codexapp.CodexProviderID)
	}
	if len(registry.entries) != 1 {
		t.Fatalf("catalog holds %d entries, want the fleet row only", len(registry.entries))
	}
	if count := countCalls(registry, callAdd); count != 1 {
		t.Errorf("Add called %d times, want the first attempt only: the retry claims the existing row", count)
	}
	wantKinds(t, since(), callList, callUpdate, callFold, callFold)
}

// A pre-1.0.56 release bound the literal row to one account. The fleet
// row carries no binding, so the ensure clears it in place — the row
// keeps its id and its routes, the account stops owning it.
func TestAPreviousReleaseBindingOnTheLiteralRowIsCleared(t *testing.T) {
	registry := newRegistry(t)
	seedPreset(t, registry, codexapp.CodexProviderID, "Codex — user@example.com", "acct-1234567890abcdef", true)
	provisioner := NewProvisioner(registry)

	providerID, err := provisioner.EnsureCodexProvider(context.Background())
	if err != nil {
		t.Fatalf("EnsureCodexProvider: %v", err)
	}
	if providerID != codexapp.CodexProviderID {
		t.Errorf("providerID = %q, want %q", providerID, codexapp.CodexProviderID)
	}
	entry := registry.entries[codexapp.CodexProviderID]
	if entry.AccountID != "" {
		t.Errorf("AccountID = %q, want the binding cleared", entry.AccountID)
	}
	if entry.Name != fleetName {
		t.Errorf("Name = %q, want %q", entry.Name, fleetName)
	}
	if !entry.Enabled || entry.Preset != providerdomain.PresetCodex {
		t.Errorf("row = enabled:%v preset:%q, want enabled and codex", entry.Enabled, entry.Preset)
	}
	if count := countCalls(registry, callFold); count != 0 {
		t.Errorf("Fold called %d times, want 0: the row keeps its id", count)
	}
	wantKinds(t, registry.calls, callList, callUpdate, callSetPreset)
}

// Only the literal id is reserved. A stranger on any other id is not
// the fleet's business: nothing reads it, nothing writes it, nothing
// folds it.
func TestAStrangerOnAnotherIDIsNeverTouched(t *testing.T) {
	registry := newRegistry(t)
	provisioner := NewProvisioner(registry)
	if _, err := provisioner.EnsureCodexProvider(context.Background()); err != nil {
		t.Fatalf("first EnsureCodexProvider: %v", err)
	}
	stranger, err := registry.Add(context.Background(), customParams(codexapp.CodexProviderID+"-mine"))
	if err != nil {
		t.Fatalf("seed stranger: %v", err)
	}

	providerID, err := provisioner.EnsureCodexProvider(context.Background())
	if err != nil {
		t.Fatalf("second EnsureCodexProvider: %v", err)
	}
	if providerID != codexapp.CodexProviderID {
		t.Errorf("providerID = %q, want %q", providerID, codexapp.CodexProviderID)
	}
	if got := registry.entries[codexapp.CodexProviderID+"-mine"]; got != stranger {
		t.Error("the stranger was written")
	}
	if count := countCalls(registry, callFold); count != 0 {
		t.Errorf("Fold called %d times, want 0: a stranger is never folded", count)
	}
}

// Retire parks the fleet row when its last live account signs out: the
// preset marker survives — SetPreset is never called — so a returning
// login relinks the same row, but nothing routes traffic to credentials
// the accounts slice no longer holds.
func TestRetireParksTheFleetRow(t *testing.T) {
	registry := newRegistry(t)
	provisioner := NewProvisioner(registry)
	if _, err := provisioner.EnsureCodexProvider(context.Background()); err != nil {
		t.Fatalf("EnsureCodexProvider: %v", err)
	}

	if err := provisioner.RetireCodexProvider(context.Background()); err != nil {
		t.Fatalf("RetireCodexProvider: %v", err)
	}
	entry, ok := registry.entries[codexapp.CodexProviderID]
	if !ok {
		t.Fatal("the entry was removed")
	}
	if entry.Enabled {
		t.Error("entry is still enabled")
	}
	if entry.Name != fleetName {
		t.Errorf("Name = %q, want %q", entry.Name, fleetName)
	}
	if entry.Preset != providerdomain.PresetCodex {
		t.Errorf("Preset = %q, want the marker kept", entry.Preset)
	}
	if count := countCalls(registry, callSetPreset); count != 0 {
		t.Errorf("SetPreset called %d times, want 0: retire never touches the marker", count)
	}
}

// A failed migration can leave legacy preset rows behind; retire parks
// them with the fleet row — their accounts are signed out too, or the
// caller would not be retiring.
func TestRetireParksLegacyRowsToo(t *testing.T) {
	registry := newRegistry(t)
	provisioner := NewProvisioner(registry)
	if _, err := provisioner.EnsureCodexProvider(context.Background()); err != nil {
		t.Fatalf("EnsureCodexProvider: %v", err)
	}
	seedPreset(t, registry, codexapp.CodexProviderID+"2", "Codex — first@example.com", "acct-first000000001", true)

	if err := provisioner.RetireCodexProvider(context.Background()); err != nil {
		t.Fatalf("RetireCodexProvider: %v", err)
	}
	if len(registry.entries) != 2 {
		t.Fatalf("catalog holds %d entries, want both parked", len(registry.entries))
	}
	for id, entry := range registry.entries {
		if entry.Enabled {
			t.Errorf("entry %s is still enabled", id)
		}
		if entry.Preset != providerdomain.PresetCodex {
			t.Errorf("entry %s preset = %q, want codex kept", id, entry.Preset)
		}
	}
	wantKinds(t, registry.calls, callList, callAdd, callList, callUpdate, callUpdate)
}

// Retire with nothing to park is a single read, not an error.
func TestRetireWithoutAnEntryIsANoOp(t *testing.T) {
	registry := newRegistry(t)
	provisioner := NewProvisioner(registry)

	if err := provisioner.RetireCodexProvider(context.Background()); err != nil {
		t.Fatalf("RetireCodexProvider: %v", err)
	}
	wantKinds(t, registry.calls, callList)
}

// A stranger squatting even on the literal id is not the preset's
// business: retire parks preset rows only and leaves the stranger
// byte-identical.
func TestRetireLeavesACustomProviderAlone(t *testing.T) {
	registry := newRegistry(t)
	stranger, err := registry.Add(context.Background(), customParams(codexapp.CodexProviderID))
	if err != nil {
		t.Fatalf("seed stranger: %v", err)
	}
	since := registry.mark()
	provisioner := NewProvisioner(registry)

	if err := provisioner.RetireCodexProvider(context.Background()); err != nil {
		t.Fatalf("RetireCodexProvider: %v", err)
	}
	if got := registry.entries[codexapp.CodexProviderID]; got != stranger {
		t.Error("the stranger was written")
	}
	wantKinds(t, since(), callList)
}

// An entry the registry cannot switch off while it is active refuses
// the retire: the row stays byte-identical and the refusal travels
// verbatim.
func TestRetireRefusesWhileTheFleetRowIsActive(t *testing.T) {
	registry := newRegistry(t)
	provisioner := NewProvisioner(registry)
	if _, err := provisioner.EnsureCodexProvider(context.Background()); err != nil {
		t.Fatalf("EnsureCodexProvider: %v", err)
	}
	registry.activeID = codexapp.CodexProviderID
	before := registry.entries[codexapp.CodexProviderID]

	err := provisioner.RetireCodexProvider(context.Background())
	if !errors.Is(err, providerapp.ErrActiveProvider) {
		t.Fatalf("RetireCodexProvider error = %v, want ErrActiveProvider", err)
	}
	if got := registry.entries[codexapp.CodexProviderID]; got != before {
		t.Error("the refused retire still wrote the row")
	}
}

// Retiring twice leaves the same parked row — the second round is
// another update with the identical shape.
func TestRetireIsIdempotent(t *testing.T) {
	registry := newRegistry(t)
	provisioner := NewProvisioner(registry)
	if _, err := provisioner.EnsureCodexProvider(context.Background()); err != nil {
		t.Fatalf("EnsureCodexProvider: %v", err)
	}
	if err := provisioner.RetireCodexProvider(context.Background()); err != nil {
		t.Fatalf("first RetireCodexProvider: %v", err)
	}

	if err := provisioner.RetireCodexProvider(context.Background()); err != nil {
		t.Fatalf("second RetireCodexProvider: %v", err)
	}
	if len(registry.entries) != 1 {
		t.Fatalf("catalog holds %d entries, want the parked row", len(registry.entries))
	}
	entry := registry.entries[codexapp.CodexProviderID]
	if entry.Enabled {
		t.Error("entry is still enabled")
	}
	if entry.Preset != providerdomain.PresetCodex {
		t.Errorf("Preset = %q, want the marker kept", entry.Preset)
	}
}

// A sign-in after a retire relinks the same row: the preset marker
// survived the park, so the ensure refreshes the row in place instead
// of adding a second one.
func TestARetiredRowIsRelinkedByTheNextLogin(t *testing.T) {
	registry := newRegistry(t)
	provisioner := NewProvisioner(registry)
	if _, err := provisioner.EnsureCodexProvider(context.Background()); err != nil {
		t.Fatalf("first EnsureCodexProvider: %v", err)
	}
	if err := provisioner.RetireCodexProvider(context.Background()); err != nil {
		t.Fatalf("RetireCodexProvider: %v", err)
	}
	since := registry.mark()

	providerID, err := provisioner.EnsureCodexProvider(context.Background())
	if err != nil {
		t.Fatalf("relink EnsureCodexProvider: %v", err)
	}
	if providerID != codexapp.CodexProviderID {
		t.Errorf("providerID = %q, want %q", providerID, codexapp.CodexProviderID)
	}
	if count := countCalls(registry, callAdd); count != 1 {
		t.Errorf("Add called %d times, want the original sign-in only: the row is relinked, not reminted", count)
	}
	entry := registry.entries[codexapp.CodexProviderID]
	if !entry.Enabled {
		t.Error("entry is still parked")
	}
	if entry.Name != fleetName {
		t.Errorf("Name = %q, want %q", entry.Name, fleetName)
	}
	wantKinds(t, since(), callList, callUpdate)
}

// Removal is the full teardown: the fleet row and any legacy preset row
// a failed migration left behind are deleted, and a stranger on any id
// survives untouched.
func TestRemoveDeletesEveryPresetEntry(t *testing.T) {
	registry := newRegistry(t)
	provisioner := NewProvisioner(registry)
	if _, err := provisioner.EnsureCodexProvider(context.Background()); err != nil {
		t.Fatalf("EnsureCodexProvider: %v", err)
	}
	seedPreset(t, registry, codexapp.CodexProviderID+"2", "Codex — first@example.com", "acct-first000000001", true)
	stranger, err := registry.Add(context.Background(), customParams("mine"))
	if err != nil {
		t.Fatalf("seed stranger: %v", err)
	}

	if err := provisioner.RemoveCodexProvider(context.Background()); err != nil {
		t.Fatalf("RemoveCodexProvider: %v", err)
	}
	if len(registry.entries) != 1 {
		t.Fatalf("catalog holds %d entries, want the stranger only", len(registry.entries))
	}
	if got := registry.entries["mine"]; got != stranger {
		t.Error("the stranger was written")
	}
	if count := countCalls(registry, callDelete); count != 2 {
		t.Errorf("Delete called %d times, want both preset rows", count)
	}
	if count := countCalls(registry, callFold); count != 0 {
		t.Errorf("Fold called %d times, want 0: removal deletes, it does not fold", count)
	}
}

// Removal with nothing to delete is a single read, not an error.
func TestRemoveWithoutAnEntryIsANoOp(t *testing.T) {
	registry := newRegistry(t)
	provisioner := NewProvisioner(registry)

	if err := provisioner.RemoveCodexProvider(context.Background()); err != nil {
		t.Fatalf("RemoveCodexProvider: %v", err)
	}
	wantKinds(t, registry.calls, callList)
}

// An entry the registry cannot delete while it is active refuses the
// removal: the row stays and the refusal travels verbatim.
func TestRemoveRefusesWhileTheFleetRowIsActive(t *testing.T) {
	registry := newRegistry(t)
	provisioner := NewProvisioner(registry)
	if _, err := provisioner.EnsureCodexProvider(context.Background()); err != nil {
		t.Fatalf("EnsureCodexProvider: %v", err)
	}
	registry.activeID = codexapp.CodexProviderID

	err := provisioner.RemoveCodexProvider(context.Background())
	if !errors.Is(err, providerapp.ErrActiveProvider) {
		t.Fatalf("RemoveCodexProvider error = %v, want ErrActiveProvider", err)
	}
	if _, ok := registry.entries[codexapp.CodexProviderID]; !ok {
		t.Error("the refused removal still deleted the row")
	}
}

// A codex preset entry the registry reports as builtin refuses the
// removal rather than deleting what the catalog owns.
func TestRemoveRefusesOnABuiltinCodexEntry(t *testing.T) {
	registry := newRegistry(t)
	seedPreset(t, registry, codexapp.CodexProviderID, fleetName, "", true)
	builtin := registry.entries[codexapp.CodexProviderID]
	// The registry reports the codex entry as builtin: a seeded row the
	// manager owns. The provisioner must refuse the delete, not strip the
	// flag to reach it.
	builtin.Builtin = true
	registry.entries[codexapp.CodexProviderID] = builtin
	provisioner := NewProvisioner(registry)

	err := provisioner.RemoveCodexProvider(context.Background())
	if !errors.Is(err, providerapp.ErrBuiltinProvider) {
		t.Fatalf("RemoveCodexProvider error = %v, want ErrBuiltinProvider", err)
	}
	if _, ok := registry.entries[codexapp.CodexProviderID]; !ok {
		t.Error("the refused removal still deleted the row")
	}
}

// A registry failure during removal travels verbatim and leaves the
// entries it did not reach in place.
func TestRemoveCarriesARegistryFailureThroughAndKeepsTheEntry(t *testing.T) {
	registry := newRegistry(t)
	provisioner := NewProvisioner(registry)
	if _, err := provisioner.EnsureCodexProvider(context.Background()); err != nil {
		t.Fatalf("EnsureCodexProvider: %v", err)
	}
	registry.deleteErr = errors.New("boom")

	err := provisioner.RemoveCodexProvider(context.Background())
	if err != registry.deleteErr {
		t.Fatalf("RemoveCodexProvider error = %v, want the registry failure verbatim", err)
	}
	if _, ok := registry.entries[codexapp.CodexProviderID]; !ok {
		t.Error("the failed removal still deleted the row")
	}
}

// A cancelled context refuses before the registry is touched: all three
// lifecycle steps check it first.
func TestACancelledContextPropagates(t *testing.T) {
	for _, step := range []struct {
		name string
		call func(provisioner *Provisioner, ctx context.Context) error
	}{
		{"ensure", func(provisioner *Provisioner, ctx context.Context) error {
			_, err := provisioner.EnsureCodexProvider(ctx)
			return err
		}},
		{"retire", func(provisioner *Provisioner, ctx context.Context) error {
			return provisioner.RetireCodexProvider(ctx)
		}},
		{"remove", func(provisioner *Provisioner, ctx context.Context) error {
			return provisioner.RemoveCodexProvider(ctx)
		}},
	} {
		t.Run(step.name, func(t *testing.T) {
			registry := newRegistry(t)
			provisioner := NewProvisioner(registry)
			ctx, cancel := context.WithCancel(context.Background())
			cancel()

			if err := step.call(provisioner, ctx); err != context.Canceled {
				t.Fatalf("error = %v, want context.Canceled", err)
			}
			if len(registry.calls) != 0 {
				t.Fatalf("registry calls = %q, want none: a cancelled context refuses before the catalog is read", kindsString(callKinds(registry.calls)))
			}
			if len(registry.entries) != 0 {
				t.Fatalf("catalog holds %d entries, want none", len(registry.entries))
			}
		})
	}
}

// Registry failures propagate verbatim from every step that performs a
// write — an update refusal on a later sign-in is not swallowed into a
// success.
func TestRegistryErrorsPropagateVerbatim(t *testing.T) {
	registry := newRegistry(t)
	provisioner := NewProvisioner(registry)
	if _, err := provisioner.EnsureCodexProvider(context.Background()); err != nil {
		t.Fatalf("first EnsureCodexProvider: %v", err)
	}
	registry.updateErr = errors.New("boom")

	if _, err := provisioner.EnsureCodexProvider(context.Background()); err != registry.updateErr {
		t.Fatalf("second EnsureCodexProvider error = %v, want the update failure verbatim", err)
	}
}
