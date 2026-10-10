// Package providers adapts the codex login flow to the providers catalog.
// It owns the managed codex preset row — exactly one for the whole fleet,
// created on the first sign-in, parked disabled while the fleet is empty,
// and deleted when the last account leaves the switchboard. Every account
// answers through that one row: the relay's token source picks the account
// per request, so the catalog never grows with the fleet.
package providers

import (
	"context"
	"errors"
	"fmt"
	"sync"

	codexapp "github.com/luxuryprivate/switchboard/backend/internal/slices/codex/application"
	providerapp "github.com/luxuryprivate/switchboard/backend/internal/slices/providers/application"
	providerdomain "github.com/luxuryprivate/switchboard/backend/internal/slices/providers/domain"
)

// ErrProviderIdTaken reports that the "codex" catalog slot is occupied by a
// provider the preset does not own — a hand-configured one found on the
// literal id, or one that raced in after the read probe missed — so the
// managed fleet row cannot live there. There is exactly one managed id and
// nothing is derived from it anymore: a squatter on it blocks the fleet.
var ErrProviderIdTaken = errors.New("codex provider id is already used by a custom provider")

const (
	// baseURL is the ChatGPT backend the codex OAuth sessions talk to.
	baseURL = "https://chatgpt.com/backend-api/codex"
	// modelsPath lists the models behind the codex backend.
	modelsPath = "/models"
	// fleetName labels the single managed row. The row serves every
	// account, so it carries no per-account label; the codex inspector is
	// the surface that shows which accounts are behind it.
	fleetName = "Codex"
)

// ProviderRegistry is the narrow slice of the providers manager the
// provisioner needs. List answers existence, not admission: disabled
// entries are included, so a parked row is told apart from a missing one.
// Update preserves the stored builtin flag, preset and account id whatever
// the params carry — SetPreset is how the preset marker and the account
// binding change — and Add creates the entry under params.ID: the manager
// honors a requested id verbatim and reports ErrProviderIDExists when that
// id is taken. Fold is the migration call: it retargets every route that
// names the folded entry at the survivor and removes the entry, so an
// operator's routes live through the fold instead of dying with the row.
// Delete removes the entry and refuses when the manager cannot delete it
// (builtin, active, or holding keys or routes).
type ProviderRegistry interface {
	List(ctx context.Context) []providerdomain.Provider
	Add(ctx context.Context, params providerdomain.Params) (providerdomain.Provider, error)
	Update(ctx context.Context, id string, params providerdomain.Params) (providerdomain.Provider, error)
	SetPreset(ctx context.Context, id string, preset providerdomain.Preset, accountID providerdomain.AccountID) (providerdomain.Provider, error)
	Fold(ctx context.Context, fromID, toID string) error
	Delete(ctx context.Context, id string) error
}

// Provisioner keeps the catalog's codex row in sync with the codex fleet.
// It implements the application port so login registers the provider the
// relay routes through, logout parks the fleet row while keeping it, and
// the leftover-entry cleanup removes it. It is safe for concurrent use.
type Provisioner struct {
	mu       sync.Mutex
	registry ProviderRegistry
}

// NewProvisioner wires the provisioner to the providers manager.
func NewProvisioner(registry ProviderRegistry) *Provisioner {
	return &Provisioner{registry: registry}
}

var _ codexapp.ProviderProvisioner = (*Provisioner)(nil)

// EnsureCodexProvider makes the single managed codex row exist and returns
// its id. The row is fleet-wide: no account binding, no per-account name —
// every signed-in account answers through it and the relay's token source
// picks which one per request. Releases before 1.0.56 minted one row per
// account (codex, codex2, …, each bound to one account); each of those
// legacy rows is folded into the fleet row here, so the operator's routes
// survive the upgrade pointed at the row that now serves every account. A
// fold that fails aborts the ensure and leaves the remainder in place:
// List only ever names rows that still exist, so the next attempt folds
// whatever is left.
func (provisioner *Provisioner) EnsureCodexProvider(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	provisioner.mu.Lock()
	defer provisioner.mu.Unlock()

	var row providerdomain.Provider
	held := false
	legacy := make([]string, 0, 4)
	for _, entry := range provisioner.registry.List(ctx) {
		if entry.ID != codexapp.CodexProviderID {
			if entry.Preset == providerdomain.PresetCodex {
				legacy = append(legacy, entry.ID)
			}
			continue
		}
		if entry.Preset != providerdomain.PresetCodex {
			// The literal id is reserved for the managed row: a stranger
			// squatting on it blocks the fleet outright.
			return "", ErrProviderIdTaken
		}
		row, held = entry, true
	}

	if held {
		if _, err := provisioner.registry.Update(ctx, row.ID, canonicalParams()); err != nil {
			return "", err
		}
		if row.AccountID != "" {
			// A pre-1.0.56 release bound the literal row to one account;
			// the fleet row carries no binding.
			if _, err := provisioner.registry.SetPreset(ctx, row.ID, providerdomain.PresetCodex, ""); err != nil {
				return "", err
			}
		}
	} else {
		added, err := provisioner.registry.Add(ctx, canonicalParams())
		if err != nil {
			if errors.Is(err, providerapp.ErrProviderIDExists) {
				// The slot was free when List read it and is not now: the
				// literal id is occupied exactly like a stranger on it.
				return "", ErrProviderIdTaken
			}
			return "", err
		}
		if added.ID != codexapp.CodexProviderID {
			// Defensive: the literal id is the routing anchor the relay's
			// token source matches, and a registry that mints its own id
			// would move it out from under the relay. Refuse rather than
			// chase it.
			return "", fmt.Errorf("codex row was minted under %q", added.ID)
		}
		if added.Preset != providerdomain.PresetCodex || added.AccountID != "" {
			// Defensive: a registry that loses the preset on the way in
			// would leave the row unrecognizable as codex-owned. SetPreset
			// restores it, keyed by the id the registry minted.
			if _, err := provisioner.registry.SetPreset(ctx, added.ID, providerdomain.PresetCodex, ""); err != nil {
				return "", err
			}
		}
	}

	for _, legacyID := range legacy {
		if err := provisioner.registry.Fold(ctx, legacyID, codexapp.CodexProviderID); err != nil {
			return "", err
		}
	}
	return codexapp.CodexProviderID, nil
}

// RetireCodexProvider parks the fleet row while its last live account is
// signed out: the preset marker survives — SetPreset is never called here —
// so a returning login relinks the same row, but the row is disabled so
// nothing routes traffic to credentials the accounts slice no longer
// holds. The caller only retires when no live account remains, so the
// fleet behind the row is empty by construction, and any legacy preset row
// a failed migration left behind is parked with it — its account is signed
// out too, or the caller would not be retiring. The disabled shape lands
// via Update, so a refusal — the entry is active and cannot be switched
// off — leaves the row byte-identical and the refusal travels verbatim.
func (provisioner *Provisioner) RetireCodexProvider(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	provisioner.mu.Lock()
	defer provisioner.mu.Unlock()

	for _, entry := range provisioner.registry.List(ctx) {
		if entry.Preset != providerdomain.PresetCodex {
			continue
		}
		if _, err := provisioner.registry.Update(ctx, entry.ID, retireParams()); err != nil {
			return err
		}
	}
	return nil
}

// RemoveCodexProvider deletes the preset entries the codex slice leaves
// behind: the fleet row and any legacy per-account rows a pre-1.0.56
// release or a failed migration left in the catalog — the full teardown
// the leftover cleanup performs when nothing live is left at all. The
// delete is a deliberate cascade — the manager deletes the entry's keys
// and the routes that use it in the same save, and only a builtin target
// or a store failure refuses — and the refusals travel verbatim. It never
// touches an entry the preset did not provision: a hand-configured
// provider squatting on any id survives untouched.
func (provisioner *Provisioner) RemoveCodexProvider(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	provisioner.mu.Lock()
	defer provisioner.mu.Unlock()

	for _, entry := range provisioner.registry.List(ctx) {
		if entry.Preset != providerdomain.PresetCodex {
			continue
		}
		if err := provisioner.registry.Delete(ctx, entry.ID); err != nil {
			return err
		}
	}
	return nil
}

// retireParams is the parked shape an empty fleet keeps: disabled, until
// an account signs back in.
func retireParams() providerdomain.Params {
	params := canonicalParams()
	params.Enabled = false
	return params
}

// canonicalParams is the full shape of the managed codex row: a bearer
// OAuth provider on the ChatGPT backend serving every account in the
// fleet. ID is the routing anchor the relay's token source matches and it
// never moves: derived ids would strand both the routes that name this row
// and the relay that answers for it. RPM stays zero because codex traffic
// is dispatched through the relay's token source, not the key pool's
// per-key queues; ChatPath, the rate unit and the cache TTL keep their
// domain defaults. The empty account id is what makes the row fleet-wide —
// SetPreset keeps the binding, so it stays empty until it is deliberately
// cleared.
func canonicalParams() providerdomain.Params {
	return providerdomain.Params{
		ID:         codexapp.CodexProviderID,
		Name:       fleetName,
		BaseURL:    baseURL,
		AuthMode:   providerdomain.AuthBearer,
		Dialect:    providerdomain.DialectAuto,
		ModelsPath: modelsPath,
		Format:     providerdomain.FormatResponses,
		Preset:     providerdomain.PresetCodex,
		Enabled:    true,
	}
}
