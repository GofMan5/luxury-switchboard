// Package providers adapts the codex login flow to the providers catalog.
// It owns exactly one managed entry: the "codex" preset provider the relay
// routes OAuth traffic through — created on sign-in, disabled and unbound
// while the account is signed out, and removed when the entry is left over
// with no live account behind it.
package providers

import (
	"context"
	"errors"
	"sync"

	codexapp "github.com/luxuryprivate/switchboard/backend/internal/slices/codex/application"
	codexdomain "github.com/luxuryprivate/switchboard/backend/internal/slices/codex/domain"
	providerapp "github.com/luxuryprivate/switchboard/backend/internal/slices/providers/application"
	providerdomain "github.com/luxuryprivate/switchboard/backend/internal/slices/providers/domain"
)

// ErrProviderIdTaken reports that the "codex" catalog slot is occupied by a
// provider the preset does not own — a hand-configured one found on the
// literal id, or any id that raced in after the read probe missed — so the
// managed preset entry cannot be created there.
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
)

// ProviderRegistry is the narrow slice of the providers manager the
// provisioner needs. Get answers existence, not admission: a disabled entry
// is still found, so the probe can tell a retired (disabled) entry from a
// missing one. Update preserves the stored builtin flag, preset and account
// id whatever the params carry — SetPreset is how the preset marker and the
// account binding change — and Add creates the entry under params.ID: the
// manager honors a requested id verbatim and reports ErrProviderIDExists
// when that id is taken. Delete removes the entry and refuses when the
// manager cannot delete it (builtin, active, or holding keys or routes).
type ProviderRegistry interface {
	Get(ctx context.Context, id string) (providerdomain.Provider, bool)
	Add(ctx context.Context, params providerdomain.Params) (providerdomain.Provider, error)
	Update(ctx context.Context, id string, params providerdomain.Params) (providerdomain.Provider, error)
	SetPreset(ctx context.Context, id string, preset providerdomain.Preset, accountID providerdomain.AccountID) (providerdomain.Provider, error)
	Delete(ctx context.Context, id string) error
}

// Provisioner keeps the catalog's codex entry in sync with the codex session.
// It implements the application port so login registers the provider the
// relay routes through, logout retires the entry while keeping the row, and
// the leftover-entry cleanup removes it. It is safe for concurrent use.
type Provisioner struct {
	mu       sync.Mutex
	registry ProviderRegistry

	// lastID is the id the codex entry actually lives under. Registries may
	// mint their own ids on Add, so the entry is not always found under
	// "codex"; the id is remembered so later logins land on the same entry.
	lastID string
}

// NewProvisioner wires the provisioner to the providers manager.
func NewProvisioner(registry ProviderRegistry) *Provisioner {
	return &Provisioner{registry: registry}
}

var _ codexapp.ProviderProvisioner = (*Provisioner)(nil)

// EnsureCodexProvider makes the managed codex entry exist for identity and
// returns the provider id the entry actually lives under. A missing entry is
// created, a retired (disabled) one is re-enabled, and the account binding is
// refreshed, so a login that switched accounts repoints the entry. The probe
// reads before it writes: only an entry the preset already owns is rewritten.
func (provisioner *Provisioner) EnsureCodexProvider(ctx context.Context, identity codexdomain.Identity) (string, error) {
	provisioner.mu.Lock()
	defer provisioner.mu.Unlock()

	accountID := providerdomain.AccountID(identity.AccountID)
	params := canonicalParams(providerName(identity.Email))
	for _, id := range provisioner.targets() {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		current, ok := provisioner.registry.Get(ctx, id)
		if !ok {
			continue
		}
		if current.Preset != providerdomain.PresetCodex {
			// an unmarked entry is a stranger: one squatting on the literal
			// id blocks the managed entry outright; on any other id it is
			// simply not ours to touch, and the probe moves on
			if id == codexapp.CodexProviderID {
				return "", ErrProviderIdTaken
			}
			continue
		}
		if _, err := provisioner.registry.Update(ctx, id, params); err != nil {
			return "", err
		}
		if _, err := provisioner.registry.SetPreset(ctx, id, providerdomain.PresetCodex, accountID); err != nil {
			return "", err
		}
		provisioner.lastID = id
		return id, nil
	}

	params.Preset = providerdomain.PresetCodex
	params.AccountID = accountID
	added, err := provisioner.registry.Add(ctx, params)
	if errors.Is(err, providerapp.ErrProviderIDExists) {
		// the id was free when the probe read it and is not now; the slot is
		// occupied exactly like a stranger on the literal id occupies it
		return "", ErrProviderIdTaken
	}
	if err != nil {
		return "", err
	}
	provisioner.lastID = added.ID
	if added.Preset != providerdomain.PresetCodex || added.AccountID != accountID {
		// Defensive: a registry that loses the preset on the way in would
		// leave the entry unrecognizable as codex-owned. SetPreset restores it.
		if _, err := provisioner.registry.SetPreset(ctx, added.ID, providerdomain.PresetCodex, accountID); err != nil {
			return "", err
		}
	}
	return added.ID, nil
}

// RetireCodexProvider signs a live account out while keeping the entry: the
// preset marker survives so the providers row keeps its identity, the
// account binding is cleared and the entry is disabled so nothing routes
// traffic to a dead credential. A later login relinks the same entry rather
// than creating a second one.
func (provisioner *Provisioner) RetireCodexProvider(ctx context.Context) error {
	provisioner.mu.Lock()
	defer provisioner.mu.Unlock()

	params := canonicalParams(retireName)
	params.Enabled = false
	for _, id := range provisioner.targets() {
		if err := ctx.Err(); err != nil {
			return err
		}
		current, ok := provisioner.registry.Get(ctx, id)
		if !ok || current.Preset != providerdomain.PresetCodex {
			continue
		}
		// the disabled shape lands first, so a refusal — the entry is active
		// and cannot be switched off — leaves the entry byte-identical: the
		// account binding is only cleared once the row is already disabled.
		if _, err := provisioner.registry.Update(ctx, id, params); err != nil {
			return err
		}
		if _, err := provisioner.registry.SetPreset(ctx, id, providerdomain.PresetCodex, ""); err != nil {
			return err
		}
	}
	provisioner.lastID = ""
	return nil
}

// RemoveCodexProvider deletes the leftover preset entries when there is no
// live account. It must refuse an entry that is builtin or still holds keys
// or routes — the manager's refusals travel verbatim — and must never touch
// an entry the preset did not provision: a hand-configured provider
// squatting on a candidate id survives untouched.
func (provisioner *Provisioner) RemoveCodexProvider(ctx context.Context) error {
	provisioner.mu.Lock()
	defer provisioner.mu.Unlock()

	for _, id := range provisioner.targets() {
		if err := ctx.Err(); err != nil {
			return err
		}
		current, ok := provisioner.registry.Get(ctx, id)
		if !ok || current.Preset != providerdomain.PresetCodex {
			continue
		}
		if err := provisioner.registry.Delete(ctx, id); err != nil {
			return err
		}
	}
	provisioner.lastID = ""
	return nil
}

// targets lists the catalog ids the codex entry may live under, most recent
// first. The remembered id comes first because a registry that mints ids on
// Add will not have the entry under "codex"; the literal id comes last
// because that is where a registry honoring params.ID puts it.
func (provisioner *Provisioner) targets() []string {
	if provisioner.lastID != "" && provisioner.lastID != codexapp.CodexProviderID {
		return []string{provisioner.lastID, codexapp.CodexProviderID}
	}
	return []string{codexapp.CodexProviderID}
}

// canonicalParams is the full shape of the managed codex entry: a bearer
// OAuth provider on the ChatGPT backend. The ID names the entry the slice
// routes by — the registry honors a requested id verbatim and refuses a
// taken one, so this is the id the entry lands under. RPM stays zero
// because codex traffic is dispatched through the relay's token source, not
// the key pool's per-key queues; ChatPath, the rate unit and the cache TTL
// keep their domain defaults.
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
