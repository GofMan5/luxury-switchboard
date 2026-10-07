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

// StorageID derives the storage key for a Codex account. The digest is
// a stable identifier, not a security primitive: it exists so the
// on-disk name does not carry the email, not to resist a determined
// attacker.
func StorageID(email, accountID, organizationID string) string {
	digest := md5.Sum([]byte(email + "|" + accountID + "|" + organizationID))
	return "codex_" + hex.EncodeToString(digest[:])
}
