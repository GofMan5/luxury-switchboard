package application

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/codex/domain"
)

// accountSession builds a second signed-in account: an account id, user
// id, organization and email distinct from validSession's account-1, so
// multi-account tests tell their rows apart by content, never by
// position.
func accountSession(accountID, email string) domain.Session {
	return domain.Session{
		AccessToken:  "access-" + email,
		RefreshToken: "refresh-" + email,
		IDToken:      "id-" + email,
		AccessExpiry: time.Now().Add(time.Hour),
		Identity: domain.Identity{
			Email:          email,
			ChatGPTUserID:  "user-" + accountID,
			Plan:           "plus",
			AccountID:      accountID,
			OrganizationID: "org-" + accountID,
		},
	}
}

// importableIDToken mints the id token of an import candidate: an
// unsigned JWT whose payload carries exactly the claims ParseIDToken
// reads — the email, the ChatGPT fields under the auth namespace, and an
// exp. The domain is a claim reader, not a signature validator, so the
// signature segment is present but arbitrary, which is also the shape
// real import text hands this code.
func importableIDToken(email, accountID string) string {
	claims := fmt.Sprintf(
		`{"email":%q,"exp":%d,"https://api.openai.com/auth":{"chatgpt_account_id":%q,"chatgpt_user_id":"user-%s","chatgpt_plan_type":"plus"}}`,
		email, time.Now().Add(time.Hour).Unix(), accountID, accountID)
	return "jwt." + base64.RawURLEncoding.EncodeToString([]byte(claims)) + ".unsigned"
}

// A login of a second account is an addition, not a replacement: the
// first account keeps its row and its tokens, the new account gets its
// own, and every account lands its own provider entry.
func TestALoginOfAnotherAccountAddsARow(t *testing.T) {
	env := newTestEnv(t)
	env.signIn(t)

	env.authorizer.setRefreshResult(accountSession("account-2", "second@example.com"), nil)
	env.signIn(t)

	status := env.service.Status()
	if len(status.Accounts) != 2 {
		t.Fatalf("Status().Accounts has %d rows, want 2: %+v", len(status.Accounts), status.Accounts)
	}
	// Rows are sorted by AccountID, so the UI and the diff both see a
	// stable order no matter which sign-in landed first.
	if status.Accounts[0].AccountID != "account-1" || status.Accounts[1].AccountID != "account-2" {
		t.Fatalf("Accounts rows are %v, want sorted account-1, account-2", status.Accounts)
	}
	first := accountStatus(t, status, "account-1")
	if first.State != StateSignedIn || first.Email != "user@example.com" {
		t.Fatalf("account-1 row = %+v, want signed in as user@example.com", first)
	}
	second := accountStatus(t, status, "account-2")
	if second.State != StateSignedIn || second.Email != "second@example.com" {
		t.Fatalf("account-2 row = %+v, want signed in as second@example.com", second)
	}
	if status.State != StateSignedIn {
		t.Fatalf("aggregate state = %q, want %q", status.State, StateSignedIn)
	}

	ensured := env.provisioner.ensuredProviderIDs()
	if len(ensured) != 2 || ensured[0] != CodexProviderID || ensured[1] != CodexProviderID+"2" {
		t.Fatalf("ensured provider ids = %v, want one per sign-in", ensured)
	}

	stored := env.store.storedSessions()
	if len(stored) != 2 || stored[0].Identity.AccountID != "account-1" || stored[1].Identity.AccountID != "account-2" {
		t.Fatalf("stored sessions = %v, want account-1 then account-2", stored)
	}

	if token, err := env.service.AcquireAccessToken(context.Background(), "account-2"); err != nil || token != "access-second@example.com" {
		t.Fatalf("AcquireAccessToken(account-2) = %q, %v; want the account-2 access token", token, err)
	}
}

// A re-login of the same account replaces that one row in place: one
// account is one row, so the new tokens overwrite the old and the row
// count stays at one.
func TestALoginOfTheSameAccountReplacesTheRow(t *testing.T) {
	env := newTestEnv(t)
	env.signIn(t)

	env.authorizer.setRefreshResult(accountSession("account-1", "replaced@example.com"), nil)
	env.signIn(t)

	status := env.service.Status()
	if len(status.Accounts) != 1 {
		t.Fatalf("Status().Accounts has %d rows, want 1: %+v", len(status.Accounts), status.Accounts)
	}
	row := accountStatus(t, status, "account-1")
	if row.State != StateSignedIn || row.Email != "replaced@example.com" {
		t.Fatalf("account-1 row = %+v, want signed in as replaced@example.com", row)
	}

	stored := env.store.storedSessions()
	if len(stored) != 1 {
		t.Fatalf("store holds %d sessions, want 1: %+v", len(stored), stored)
	}
	if stored[0].AccessToken != "access-replaced@example.com" {
		t.Fatalf("stored access token = %q, want the replaced login's", stored[0].AccessToken)
	}

	if token, err := env.service.AcquireAccessToken(context.Background(), "account-1"); err != nil || token != "access-replaced@example.com" {
		t.Fatalf("AcquireAccessToken(account-1) = %q, %v; want the replaced login's token", token, err)
	}
}

// Logout aimed at one account disconnects exactly that account: its row
// disappears, its provider entry is retired, its record is cleared from
// the store — and the other account keeps serving tokens as if nothing
// happened.
func TestPerAccountLogoutDisconnectsOnlyThatAccount(t *testing.T) {
	env := newTestEnv(t)
	env.signIn(t)
	env.authorizer.setRefreshResult(accountSession("account-2", "second@example.com"), nil)
	env.signIn(t)

	if err := env.service.Logout(context.Background(), "account-1", false); err != nil {
		t.Fatalf("Logout(account-1) error = %v, want nil", err)
	}

	status := env.service.Status()
	if len(status.Accounts) != 1 {
		t.Fatalf("Status().Accounts has %d rows, want 1: %+v", len(status.Accounts), status.Accounts)
	}
	if accountStatus(t, status, "account-2").State != StateSignedIn {
		t.Fatalf("account-2 row = %+v, want still signed in", status.Accounts[0])
	}

	if retired := env.provisioner.retiredAccountIDs(); len(retired) != 1 || retired[0] != "account-1" {
		t.Fatalf("retired account ids = %v, want only account-1", retired)
	}
	if removed := env.provisioner.removedAccountIDs(); len(removed) != 0 {
		t.Fatalf("removed account ids = %v, want none", removed)
	}
	if count := env.store.clearCount(); count != 1 {
		t.Fatalf("store clears = %d, want 1", count)
	}

	stored := env.store.storedSessions()
	if len(stored) != 1 || stored[0].Identity.AccountID != "account-2" {
		t.Fatalf("stored sessions = %v, want only account-2", stored)
	}

	if token, err := env.service.AcquireAccessToken(context.Background(), "account-2"); err != nil || token != "access-second@example.com" {
		t.Fatalf("AcquireAccessToken(account-2) = %q, %v; want account-2's token untouched", token, err)
	}
}

// Logout without an account id is the all-accounts disconnect: every
// live account is retired, the whole store is cleared, and the preset
// returns to the signed-out aggregate with no rows left.
func TestLogoutWithoutAnAccountIDRetiresEveryAccount(t *testing.T) {
	env := newTestEnv(t)
	env.signIn(t)
	env.authorizer.setRefreshResult(accountSession("account-2", "second@example.com"), nil)
	env.signIn(t)

	if err := env.service.Logout(context.Background(), "", false); err != nil {
		t.Fatalf("Logout(\"\") error = %v, want nil", err)
	}

	status := env.service.Status()
	if len(status.Accounts) != 0 {
		t.Fatalf("Status().Accounts = %+v, want empty", status.Accounts)
	}
	if status.State != StateSignedOut {
		t.Fatalf("aggregate state = %q, want %q", status.State, StateSignedOut)
	}

	retired := env.provisioner.retiredAccountIDs()
	if len(retired) != 2 || retired[0] != "account-1" || retired[1] != "account-2" {
		t.Fatalf("retired account ids = %v, want both accounts", retired)
	}
	if count := env.store.clearCount(); count != 1 {
		t.Fatalf("store clears = %d, want 1 ClearAll", count)
	}
	if sessions := env.store.storedSessions(); len(sessions) != 0 {
		t.Fatalf("store still holds %d sessions, want 0", len(sessions))
	}
}

// A logout aimed at an account nobody signed in under is a no-op: no
// provider is retired or removed, no record is cleared, and the signed-in
// accounts are untouched. A specific unknown id must not trigger the
// leftover-entry teardown either.
func TestALogoutWithAnUnknownAccountIDChangesNothing(t *testing.T) {
	env := newTestEnv(t)
	env.signIn(t)
	env.authorizer.setRefreshResult(accountSession("account-2", "second@example.com"), nil)
	env.signIn(t)

	if err := env.service.Logout(context.Background(), "ghost", false); err != nil {
		t.Fatalf("Logout(ghost) error = %v, want nil", err)
	}

	if count := env.provisioner.retireCount(); count != 0 {
		t.Fatalf("retires = %d, want 0", count)
	}
	if count := env.provisioner.removeCount(); count != 0 {
		t.Fatalf("removes = %d, want 0", count)
	}
	if count := env.store.clearCount(); count != 0 {
		t.Fatalf("store clears = %d, want 0", count)
	}

	status := env.service.Status()
	if len(status.Accounts) != 2 {
		t.Fatalf("Status().Accounts has %d rows, want 2", len(status.Accounts))
	}
	if accountStatus(t, status, "account-1").State != StateSignedIn || accountStatus(t, status, "account-2").State != StateSignedIn {
		t.Fatalf("accounts = %+v, want both still signed in", status.Accounts)
	}
}

// Multi-account pasted text imports every account it holds: both
// candidates land, both rows appear sorted, and each account keeps its
// own tokens and provider entry.
func TestMultiAccountImportLandsEveryCandidate(t *testing.T) {
	env := newTestEnv(t)
	env.parser.candidates = []CredentialCandidate{
		{
			Kind:        CredentialFull,
			IDToken:     importableIDToken("first@example.com", "account-1"),
			AccessToken: "access-first@example.com",
		},
		{
			Kind:        CredentialFull,
			IDToken:     importableIDToken("second@example.com", "account-2"),
			AccessToken: "access-second@example.com",
		},
	}

	result, err := env.service.ImportJSON(context.Background(), "two accounts")
	if err != nil {
		t.Fatalf("ImportJSON() error = %v, want nil", err)
	}
	if result.ImportedFrom != "" {
		t.Fatalf("ImportedFrom = %q, want empty for pasted text", result.ImportedFrom)
	}

	status := result.Status
	if len(status.Accounts) != 2 {
		t.Fatalf("imported %d rows, want 2: %+v", len(status.Accounts), status.Accounts)
	}
	if status.Accounts[0].AccountID != "account-1" || status.Accounts[1].AccountID != "account-2" {
		t.Fatalf("imported rows = %v, want sorted account-1, account-2", status.Accounts)
	}
	first := accountStatus(t, status, "account-1")
	if first.State != StateSignedIn || first.Email != "first@example.com" {
		t.Fatalf("account-1 row = %+v, want signed in as first@example.com", first)
	}
	second := accountStatus(t, status, "account-2")
	if second.State != StateSignedIn || second.Email != "second@example.com" {
		t.Fatalf("account-2 row = %+v, want signed in as second@example.com", second)
	}

	ensured := env.provisioner.ensuredProviderIDs()
	if len(ensured) != 2 {
		t.Fatalf("ensured provider ids = %v, want one per imported account", ensured)
	}

	stored := env.store.storedSessions()
	if len(stored) != 2 {
		t.Fatalf("store holds %d sessions, want 2", len(stored))
	}

	if token, err := env.service.AcquireAccessToken(context.Background(), "account-2"); err != nil || token != "access-second@example.com" {
		t.Fatalf("AcquireAccessToken(account-2) = %q, %v; want the imported token", token, err)
	}
}

// importableAccessToken mints the access token of an import candidate:
// an unsigned JWT whose payload carries the exp the freshness rule
// reads. Real access tokens are JWTs of exactly this shape; the expiry
// claim, not the signature, is what an offline import decision is
// allowed to trust.
func importableAccessToken(expiresIn time.Duration) string {
	claims := fmt.Sprintf(`{"exp":%d}`, time.Now().Add(expiresIn).Unix())
	return "jwt." + base64.RawURLEncoding.EncodeToString([]byte(claims)) + ".unsigned"
}

// A candidate whose pair still stands imports without touching the
// network. The access token answers for itself until its own expiry, so
// a refresh exchange that cannot run — no route to the auth server, or
// a refresh token already rotated by another machine — has no right to
// refuse a still-valid import. The refresh token lands with the session
// so the runtime's refresh-on-expiry loop renews it later, and the
// acquired token is the candidate's own.
func TestAFreshPairImportsWithoutTouchingTheNetwork(t *testing.T) {
	env := newTestEnv(t)
	candidateAccess := importableAccessToken(time.Hour)
	env.parser.candidates = []CredentialCandidate{
		{
			Kind:         CredentialFull,
			IDToken:      importableIDToken("fresh@example.com", "account-1"),
			AccessToken:  candidateAccess,
			RefreshToken: "refresh-fresh",
		},
	}
	env.authorizer.setRefreshResult(domain.Session{}, errors.New("codex oauth refresh failed: no route to host"))

	result, err := env.service.ImportJSON(context.Background(), "fresh pair")
	if err != nil {
		t.Fatalf("ImportJSON() error = %v, want nil", err)
	}
	if refreshes := env.authorizer.refreshes(); len(refreshes) != 0 {
		t.Fatalf("refresh calls = %v, want none for a pair whose access token has not expired", refreshes)
	}
	if len(result.Status.Accounts) != 1 || accountStatus(t, result.Status, "account-1").State != StateSignedIn {
		t.Fatalf("imported rows = %+v, want one signed-in row for account-1", result.Status.Accounts)
	}

	stored := env.store.storedSessions()
	if len(stored) != 1 {
		t.Fatalf("store holds %d sessions, want 1", len(stored))
	}
	if stored[0].AccessToken != candidateAccess {
		t.Fatalf("stored access token = %q, want the candidate's own", stored[0].AccessToken)
	}
	if stored[0].RefreshToken != "refresh-fresh" {
		t.Fatalf("stored refresh token = %q, want the candidate's refresh token kept", stored[0].RefreshToken)
	}
	if token, err := env.service.AcquireAccessToken(context.Background(), "account-1"); err != nil || token == "" {
		t.Fatalf("AcquireAccessToken(account-1) = %q, %v; want the imported token served", token, err)
	}
}

// A stale pair — access token already expired — still pays for the live
// exchange, because an expired access token proves nothing and the
// refresh token is the only live proof left.
func TestAStalePairStillExchangesItsRefreshToken(t *testing.T) {
	env := newTestEnv(t)
	env.parser.candidates = []CredentialCandidate{
		{
			Kind:         CredentialFull,
			IDToken:      importableIDToken("stale@example.com", "account-1"),
			AccessToken:  importableAccessToken(-time.Hour),
			RefreshToken: "refresh-stale",
		},
	}
	env.authorizer.setRefreshResult(accountSession("account-1", "stale@example.com"), nil)

	if _, err := env.service.ImportJSON(context.Background(), "stale pair"); err != nil {
		t.Fatalf("ImportJSON() error = %v, want nil", err)
	}
	if refreshes := env.authorizer.refreshes(); len(refreshes) != 1 || refreshes[0] != "refresh-stale" {
		t.Fatalf("refresh calls = %v, want one exchange of the candidate's refresh token", refreshes)
	}
	stored := env.store.storedSessions()
	if len(stored) != 1 || stored[0].AccessToken != "access-stale@example.com" {
		t.Fatalf("stored session = %+v, want the refreshed session's access token", stored)
	}
}

// A refresh-only candidate has no pair to stand on, so a failed
// exchange is still the import's failure: importing it would sign the
// user into nothing.
func TestARefreshOnlyCandidateThatCannotExchangeStillFails(t *testing.T) {
	env := newTestEnv(t)
	env.parser.candidates = []CredentialCandidate{
		{Kind: CredentialRefresh, RefreshToken: "refresh-only"},
	}
	env.authorizer.setRefreshResult(domain.Session{}, errors.New("codex oauth refresh failed: refresh_token_reused"))

	_, err := env.service.ImportJSON(context.Background(), "refresh only")
	if err == nil {
		t.Fatalf("ImportJSON() error = nil, want the failed exchange to fail the import")
	}
	if refreshes := env.authorizer.refreshes(); len(refreshes) != 1 || refreshes[0] != "refresh-only" {
		t.Fatalf("refresh calls = %v, want one exchange attempt", refreshes)
	}
}

// The quota card asks per account: a probe for account-2 presents
// account-2's access token and account-2's id, never account-1's.
func TestAQuotaProbeTargetsTheNamedAccount(t *testing.T) {
	env := newTestEnv(t)
	env.signIn(t)
	env.authorizer.setRefreshResult(accountSession("account-2", "second@example.com"), nil)
	env.signIn(t)

	snapshot := env.service.RefreshQuota(context.Background(), "account-2")
	if snapshot.Err != "" {
		t.Fatalf("RefreshQuota(account-2) error = %q, want none", snapshot.Err)
	}

	tokens, ids := env.authorizer.usageSeen()
	if len(tokens) != 1 || tokens[0] != "access-second@example.com" {
		t.Fatalf("usage tokens = %v, want account-2's access token", tokens)
	}
	if len(ids) != 1 || ids[0] != "account-2" {
		t.Fatalf("usage account ids = %v, want account-2", ids)
	}
}

// Acquire without an account id is a refusal even while accounts are
// signed in: the relay resolves a provider to an account and names it,
// and an empty id has no row to serve.
func TestAcquireWithoutAnAccountIDRefusesEvenWithAccountsSignedIn(t *testing.T) {
	env := newTestEnv(t)
	env.signIn(t)
	env.authorizer.setRefreshResult(accountSession("account-2", "second@example.com"), nil)
	env.signIn(t)

	if _, err := env.service.AcquireAccessToken(context.Background(), ""); !errors.Is(err, errNotSignedIn) {
		t.Fatalf("AcquireAccessToken(\"\") error = %v, want errNotSignedIn", err)
	}
}

// A rejected access token marks exactly the account it belongs to:
// account-1 goes reauth-needed while account-2 keeps serving tokens.
func TestRejectingOneAccessTokenMarksOnlyThatAccount(t *testing.T) {
	env := newTestEnv(t)
	env.signIn(t)
	env.authorizer.setRefreshResult(accountSession("account-2", "second@example.com"), nil)
	env.signIn(t)

	env.service.RejectAccessToken("access-user@example.com")

	status := env.service.Status()
	if row := accountStatus(t, status, "account-1"); row.State != StateReauthNeeded {
		t.Fatalf("account-1 row = %+v, want reauth needed", row)
	}
	if row := accountStatus(t, status, "account-2"); row.State != StateSignedIn {
		t.Fatalf("account-2 row = %+v, want still signed in", row)
	}

	if token, err := env.service.AcquireAccessToken(context.Background(), "account-2"); err != nil || token != "access-second@example.com" {
		t.Fatalf("AcquireAccessToken(account-2) = %q, %v; want account-2 unaffected", token, err)
	}
}

// Restore signs in every stored account: each record gets its row, its
// provider entry and the signed-in state, in the store's order.
func TestRestoreSignsInEveryStoredAccount(t *testing.T) {
	env := newTestEnv(t)
	env.store.seed(validSession("user@example.com"))
	env.store.seed(accountSession("account-2", "second@example.com"))

	env.restore(t)

	status := env.service.Status()
	if len(status.Accounts) != 2 {
		t.Fatalf("Status().Accounts has %d rows, want 2: %+v", len(status.Accounts), status.Accounts)
	}
	first := accountStatus(t, status, "account-1")
	if first.State != StateSignedIn || first.Email != "user@example.com" || first.ProviderID != CodexProviderID {
		t.Fatalf("account-1 row = %+v, want signed in with the codex provider", first)
	}
	second := accountStatus(t, status, "account-2")
	if second.State != StateSignedIn || second.Email != "second@example.com" || second.ProviderID != CodexProviderID+"2" {
		t.Fatalf("account-2 row = %+v, want signed in with its own provider", second)
	}

	ensured := env.provisioner.ensuredIdentities()
	if len(ensured) != 2 {
		t.Fatalf("restored %d provider entries, want 2", len(ensured))
	}
}
