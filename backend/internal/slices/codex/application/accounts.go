package application

import (
	"context"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/codex/domain"
)

// account is one signed-in ChatGPT account: its session, its connection
// state and the working data that belongs to exactly this account — the
// provisioned provider entry, the invalidation flag and the quota probe
// state. The refresh and quota gates live here as value fields: entries
// are only ever inserted or deleted, never swapped, so the gates' identity
// is stable for the lifetime of an account even across map lookups made
// from a pointer taken before an edit.
type account struct {
	session     domain.Session
	connState   ConnState
	providerID  string
	invalidated bool

	quota       QuotaSnapshot
	quotaProbes uint64
	quotaGate   refreshGate
	refreshGate refreshGate
}

// AccountStatus is one account's row in the provider list: the account's
// key, its labels and the state the UI renders. Email, Plan and AccountID
// come from the id token's identity; the owner-only Providers screen may
// show them, which is fine — they are account labels, not credentials.
type AccountStatus struct {
	AccountID  string
	Email      string
	Plan       string
	State      ConnState
	ProviderID string
}

// AccountStore persists every signed-in Codex account, keyed by the
// account's identity. Plural accounts are the product: a sign-in is not
// a single slot but one more record, and the storage layer derives the
// key from the session itself, so the application never invents file
// names.
type AccountStore interface {
	// Load returns every stored account, in the store's deterministic
	// order. An empty answer means no account is signed in and is not
	// an error.
	Load(ctx context.Context) ([]domain.Session, error)
	// Save durably stores one account; saving an identity again
	// replaces that account's record.
	Save(ctx context.Context, session domain.Session) error
	// Clear removes one account's record; a missing record is a
	// success, because the outcome — that account is gone — is the
	// same.
	Clear(ctx context.Context, session domain.Session) error
	// ClearAll removes every stored account, including leftovers from
	// older single-session layouts.
	ClearAll(ctx context.Context) error
}
