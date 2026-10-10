// Package providers adapts the codex login flow to the providers catalog.
// It owns the managed codex preset entries — exactly one per signed-in
// account, created on sign-in, disabled and renamed while the account is
// signed out with the binding kept so a returning login relinks the same
// row, and deleted when the account leaves the switchboard.
package providers

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"

	codexapp "github.com/luxuryprivate/switchboard/backend/internal/slices/codex/application"
	codexdomain "github.com/luxuryprivate/switchboard/backend/internal/slices/codex/domain"
	providerapp "github.com/luxuryprivate/switchboard/backend/internal/slices/providers/application"
	providerdomain "github.com/luxuryprivate/switchboard/backend/internal/slices/providers/domain"
)

// ErrProviderIdTaken reports that the "codex" catalog slot is occupied by a
// provider the preset does not own — a hand-configured one found on the
// literal id, or one that raced in after the read probe missed — so the
// managed preset entry cannot be created there. Derived ids (codex2,
// codex3…) are never reserved: a squatter there only moves the next entry
// one slot further.
var ErrProviderIdTaken = errors.New("codex provider id is already used by a custom provider")

const (
	// baseURL is the ChatGPT backend the codex OAuth session talks to.
	baseURL = "https://chatgpt.com/backend-api/codex"
	// modelsPath lists the models behind the codex backend.
	modelsPath = "/models"
	// namePrefix labels the managed entry with the signed-in account.
	namePrefix = "Codex — "
	// nameLimit mirrors the catalog's 80-rune name cap, so a long or hostile
	// email can never make the entry invalid.
	nameLimit = 80
	// retireName is the neutral label the retired entry keeps while its
	// account is signed out; the account email comes back on the next login.
	retireName = "Codex"
	// maxSlots bounds how far the provisioner walks the codex, codex2,
	// codex3… namespace before refusing: it keeps a runaway catalog from
	// being probed forever.
	maxSlots = 100
)

// ProviderRegistry is the narrow slice of the providers manager the
// provisioner needs. List answers existence, not admission: disabled
// entries are included, so a retired row is told apart from a missing one.
// Update preserves the stored builtin flag, preset and account id whatever
// the params carry — SetPreset is how the preset marker and the account
// binding change — and Add creates the entry under params.ID: the manager
// honors a requested id verbatim and reports ErrProviderIDExists when that
// id is taken. Delete removes the entry and refuses when the manager
// cannot delete it (builtin, active, or holding keys or routes).
type ProviderRegistry interface {
	List(ctx context.Context) []providerdomain.Provider
	Add(ctx context.Context, params providerdomain.Params) (providerdomain.Provider, error)
	Update(ctx context.Context, id string, params providerdomain.Params) (providerdomain.Provider, error)
	SetPreset(ctx context.Context, id string, preset providerdomain.Preset, accountID providerdomain.AccountID) (providerdomain.Provider, error)
	Delete(ctx context.Context, id string) error
}

// Provisioner keeps the catalog's codex entries in sync with the codex
// sessions. It implements the application port so login registers the
// provider the relay routes through, logout retires the account's entry
// while keeping the row, and the leftover-entry cleanup removes it. It is
// safe for concurrent use.
type Provisioner struct {
	mu       sync.Mutex
	registry ProviderRegistry
}

// NewProvisioner wires the provisioner to the providers manager.
func NewProvisioner(registry ProviderRegistry) *Provisioner {
	return &Provisioner{registry: registry}
}

var _ codexapp.ProviderProvisioner = (*Provisioner)(nil)

// EnsureCodexProvider makes the managed codex entry for identity's account
// exist and returns the provider id the entry actually lives under. The row
// bound to the account is relinked — re-enabled, renamed for the account,
// binding refreshed — so a returning login lands on the same row rather
// than creating a second one, even after the registry minted its own id.
// The first unbound preset row is claimed for a new account: that is the
// shape an earlier, single-account release left behind. Otherwise a fresh
// entry is minted in the codex, codex2, codex3… namespace.
func (provisioner *Provisioner) EnsureCodexProvider(ctx context.Context, identity codexdomain.Identity) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	provisioner.mu.Lock()
	defer provisioner.mu.Unlock()

	entries := provisioner.registry.List(ctx)
	accountID := providerdomain.AccountID(identity.AccountID)

	// relink the row this account already owns, retired or not
	for _, entry := range entries {
		if entry.Preset == providerdomain.PresetCodex && entry.AccountID == accountID {
			return provisioner.relink(ctx, entry.ID, identity.Email, accountID)
		}
	}
	// claim the first unbound preset row for the new account
	for _, entry := range entries {
		if entry.Preset == providerdomain.PresetCodex && entry.AccountID == "" {
			return provisioner.relink(ctx, entry.ID, identity.Email, accountID)
		}
	}

	// mint a fresh row; the occupied map tells which slots are free without
	// another registry round-trip per probe
	held := make(map[string]providerdomain.Provider, len(entries))
	for _, entry := range entries {
		held[entry.ID] = entry
	}
	params := canonicalParams(providerName(identity.Email))
	for slot := 1; slot <= maxSlots; slot++ {
		id := codexapp.CodexProviderID
		if slot > 1 {
			id += strconv.Itoa(slot)
		}
		if occupied, exists := held[id]; exists {
			if id == codexapp.CodexProviderID && occupied.Preset != providerdomain.PresetCodex {
				// the literal id is reserved for the managed entry: a
				// stranger squatting on it blocks the account outright.
				// A preset holder on it is bound to another account — the
				// unbound ones were claimed above — so the probe moves on.
				return "", ErrProviderIdTaken
			}
			continue
		}
		params.ID = id
		params.Preset = providerdomain.PresetCodex
		params.AccountID = accountID
		added, err := provisioner.registry.Add(ctx, params)
		if err != nil {
			if errors.Is(err, providerapp.ErrProviderIDExists) {
				if id == codexapp.CodexProviderID {
					// the slot was free when List read it and is not now;
					// the literal id is occupied exactly like a stranger
					// on it occupies it
					return "", ErrProviderIdTaken
				}
				continue
			}
			return "", err
		}
		if added.Preset != providerdomain.PresetCodex || added.AccountID != accountID {
			// Defensive: a registry that loses the preset on the way in
			// would leave the entry unrecognizable as codex-owned.
			// SetPreset restores it, keyed by the id the registry minted.
			if _, err := provisioner.registry.SetPreset(ctx, added.ID, providerdomain.PresetCodex, accountID); err != nil {
				return "", err
			}
		}
		return added.ID, nil
	}
	return "", fmt.Errorf("no free codex slot within %d probes: %w", maxSlots, ErrProviderIdTaken)
}

// RetireCodexProvider signs a live account out while keeping its row: the
// preset marker and the account binding survive — SetPreset is never
// called here — so the providers row keeps its identity and a later login
// of the same account relinks it, but the row is disabled so nothing
// routes traffic to a dead credential. The disabled shape lands via
// Update first, so a refusal — the entry is active and cannot be switched
// off — leaves the row byte-identical.
func (provisioner *Provisioner) RetireCodexProvider(ctx context.Context, accountID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	provisioner.mu.Lock()
	defer provisioner.mu.Unlock()

	params := retireParams()
	for _, entry := range provisioner.registry.List(ctx) {
		if entry.Preset != providerdomain.PresetCodex || entry.AccountID != providerdomain.AccountID(accountID) {
			continue
		}
		if _, err := provisioner.registry.Update(ctx, entry.ID, params); err != nil {
			return err
		}
	}
	return nil
}

// RemoveCodexProvider deletes the preset entries an account leaves behind:
// its own bound row when accountID names the account, and every preset row
// when accountID is empty — the full teardown the leftover cleanup performs
// when no live account is left at all. The delete is a deliberate cascade —
// the manager deletes the entry's keys and the routes that use it in the
// same save, and only a builtin target or a store failure refuses — and
// the refusals travel verbatim. It never touches an entry the preset did
// not provision: a hand-configured provider squatting on a candidate id
// survives untouched.
func (provisioner *Provisioner) RemoveCodexProvider(ctx context.Context, accountID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	provisioner.mu.Lock()
	defer provisioner.mu.Unlock()

	for _, entry := range provisioner.registry.List(ctx) {
		if entry.Preset != providerdomain.PresetCodex {
			continue
		}
		if accountID != "" && entry.AccountID != providerdomain.AccountID(accountID) {
			continue
		}
		if err := provisioner.registry.Delete(ctx, entry.ID); err != nil {
			return err
		}
	}
	return nil
}

// relink refreshes a preset row under the held lock: the canonical shape —
// enabled, named for the account — lands via Update, which preserves the
// stored builtin flag, and the binding is (re)written with SetPreset, the
// only writer of the preset marker and the account id.
func (provisioner *Provisioner) relink(ctx context.Context, id, email string, accountID providerdomain.AccountID) (string, error) {
	params := canonicalParams(providerName(email))
	if _, err := provisioner.registry.Update(ctx, id, params); err != nil {
		return "", err
	}
	if _, err := provisioner.registry.SetPreset(ctx, id, providerdomain.PresetCodex, accountID); err != nil {
		return "", err
	}
	return id, nil
}

// retireParams is the neutral shape a signed-out row keeps: disabled and
// named "Codex" until its account comes back.
func retireParams() providerdomain.Params {
	params := canonicalParams(retireName)
	params.Enabled = false
	return params
}

// canonicalParams is the full shape of a managed codex entry: a bearer
// OAuth provider on the ChatGPT backend. ID names the slot the entry is
// minted into — the registry honors a requested id verbatim and refuses a
// taken one, and one that mints its own id gets corrected after Add. RPM
// stays zero because codex traffic is dispatched through the relay's token
// source, not the key pool's per-key queues; ChatPath, the rate unit and
// the cache TTL keep their domain defaults.
func canonicalParams(name string) providerdomain.Params {
	return providerdomain.Params{
		ID:         codexapp.CodexProviderID,
		Name:       name,
		BaseURL:    baseURL,
		AuthMode:   providerdomain.AuthBearer,
		Dialect:    providerdomain.DialectAuto,
		ModelsPath: modelsPath,
		Format:     providerdomain.FormatResponses,
		Enabled:    true,
	}
}

// providerName labels the entry with the signed-in account. Control
// characters are dropped and the name is capped at 80 runes on a rune
// boundary, so the catalog's name validation can never reject the login.
func providerName(email string) string {
	runes := make([]rune, 0, nameLimit)
	for _, character := range namePrefix + email {
		if character < 32 || character == 127 {
			continue
		}
		if len(runes) == nameLimit {
			break
		}
		runes = append(runes, character)
	}
	return string(runes)
}
