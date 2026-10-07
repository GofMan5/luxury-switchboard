package domain

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

// Session is an authenticated Codex preset: the tokens from the OAuth
// exchange plus the identity decoded from the id_token.
//
// The token fields are secrets. The type deliberately has no String
// method, so no incidental fmt path can echo them; RedactedSummary is
// the only sanctioned rendering, and logs, crash reports and UI go
// through it.
type Session struct {
	AccessToken  string
	RefreshToken string
	IDToken      string
	AccessExpiry time.Time
	Identity     Identity
}

// Empty reports whether the session carries no tokens at all. An
// identity without tokens is not a usable session.
func (s Session) Empty() bool {
	return s.AccessToken == "" && s.RefreshToken == "" && s.IDToken == ""
}

// NeedsRefresh reports whether the access token should be replaced
// before serving a request. A zero expiry counts as needing refresh,
// because an unknown expiry is never trusted as a valid one. The skew
// moves the boundary earlier so the token is replaced before it lapses
// rather than after; the boundary itself is still usable.
func (s Session) NeedsRefresh(now time.Time, skew time.Duration) bool {
	if s.AccessExpiry.IsZero() {
		return true
	}
	return now.After(s.AccessExpiry.Add(-skew))
}

// RedactedSummary renders the session as one safe line: email, plan,
// account id and access expiry only. Token material and the
// organization id never appear.
func (s Session) RedactedSummary() string {
	expiry := "unknown"
	if !s.AccessExpiry.IsZero() {
		expiry = s.AccessExpiry.UTC().Format(time.RFC3339)
	}
	return fmt.Sprintf("codex session for %s (plan %s, account %s), access token expires %s",
		s.Identity.Email, s.Identity.Plan, s.Identity.AccountID, expiry)
}

// reauthMarkers are the lowercase fragments that mark an OAuth failure
// as terminal: the stored refresh token is dead and only a fresh login
// by the user fixes the session.
var reauthMarkers = []string{
	"refresh_token_reused",
	"token_invalidated",
	"invalid_grant",
	"invalid refresh token",
	"authentication token has been invalidated",
}

// IsReauthError reports whether an error message from the OAuth
// exchange means the stored session can never be refreshed again, only
// re-issued by the user. Matching is a case-insensitive substring scan
// because servers wrap the marker in vendor prefixes and mixed-case
// prose.
func IsReauthError(message string) bool {
	lowered := strings.ToLower(message)
	for _, marker := range reauthMarkers {
		if strings.Contains(lowered, marker) {
			return true
		}
	}
	return false
}

// revokedAccessTokenCodes are the codes an upstream names when it has
// killed the access token itself: the token is dead for good, no retry
// with the same string can revive it, and only a fresh login (or a
// successful refresh that lands a different access token) changes the
// verdict. These are upstream error-body codes, not OAuth messages:
// IsReauthError reads the token endpoint, this reads the relay path.
var revokedAccessTokenCodes = []string{
	"token_revoked",
	"token_invalidated",
}

// refreshTokenMessageMarkers mark an upstream error body that talks
// about the refresh token, not the access token. Those verdicts belong
// to the refresh path, and a revocation code riding such a message is
// the provider describing the session's renewal, not the token being
// served — the two must not be conflated into an access-token
// revocation.
var refreshTokenMessageMarkers = []string{
	"refresh_token",
	"refresh token",
	"刷新 token",
	"token 刷新",
}

// IsAccessTokenRevocation reports whether an upstream 401/403 body's
// error code and message name a revoked access token. The code is
// trimmed and lowercased before matching because providers emit it in
// mixed case with stray whitespace; the message only ever vetoed by
// refresh-token markers, never matched, so unrelated prose cannot forge
// a revocation that the code did not name. An empty code is never a
// revocation: a body without the provider's own code carries no verdict
// about the token family.
func IsAccessTokenRevocation(errorCode, errorMessage string) bool {
	code := strings.ToLower(strings.TrimSpace(errorCode))
	revokedCode := false
	for _, marker := range revokedAccessTokenCodes {
		if code == marker {
			revokedCode = true
			break
		}
	}
	if !revokedCode {
		return false
	}
	lowered := strings.ToLower(errorMessage)
	for _, marker := range refreshTokenMessageMarkers {
		if strings.Contains(lowered, marker) {
			return false
		}
	}
	return true
}

// StorageID derives the storage key for a Codex account. The digest is
// a stable identifier, not a security primitive: it exists so the
// on-disk name does not carry the email, not to resist a determined
// attacker.
func StorageID(email, accountID, organizationID string) string {
	digest := md5.Sum([]byte(email + "|" + accountID + "|" + organizationID))
	return "codex_" + hex.EncodeToString(digest[:])
}
