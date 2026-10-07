// Package domain holds the pure Codex preset logic: decoding OpenAI
// id_token claims, describing an authenticated session, and recognizing
// the failures that end one.
//
// The package imports only the standard library and nothing from the
// transport, storage or UI layers; adapters build outward from here. One
// invariant runs through it: session tokens are secrets, they never leave
// this package as text, and RedactedSummary is the only sanctioned
// rendering of a session.
package domain

import (
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

const (
	// authClaim carries the ChatGPT-specific fields OpenAI namespaces
	// under a full URL instead of leaving them at the token's top level.
	authClaim = "https://api.openai.com/auth"

	// profileClaim carries the email when the token has no top-level one.
	profileClaim = "https://api.openai.com/profile"
)

var (
	// errMalformedIDToken marks structural failure: too few segments,
	// undecodable base64url, or a payload that is not a JSON object.
	errMalformedIDToken = errors.New("codex id token is malformed")

	// errNoEmailClaim marks a structurally valid token that still has no
	// email to build an identity around.
	errNoEmailClaim = errors.New("codex id token has no email claim")

	// accountIDKeys name the account id claim, most specific first.
	accountIDKeys = []string{"chatgpt_account_id", "account_id"}

	// organizationIDKeys name the organization id claim, most specific
	// first. POID is uppercase on purpose: claim keys are case-sensitive,
	// so both spellings are listed as distinct keys.
	organizationIDKeys = []string{
		"organization_id",
		"chatgpt_organization_id",
		"chatgpt_org_id",
		"org_id",
		"poid",
		"POID",
	}
)

// Identity is the part of a decoded id_token the Codex preset needs.
type Identity struct {
	Email          string
	ChatGPTUserID  string
	Plan           string
	AccountID      string
	OrganizationID string
}

// ParseIDToken decodes the claims of an OpenAI id_token into an Identity.
//
// The token reaches this code from the OAuth server over TLS, so the
// signature is neither checked nor checkable here: this is a claim
// reader, not a validator. Unknown claims are tolerated because the
// server adds fields without notice, and a claim of the wrong shape
// reads as absent rather than failing the whole decode.
func ParseIDToken(idToken string) (Identity, error) {
	claims, err := decodeIDTokenClaims(idToken)
	if err != nil {
		return Identity{}, err
	}

	email := claimString(claims, "email")
	if email == "" {
		email = claimString(nestedClaims(claims, profileClaim), "email")
	}
	if email == "" {
		return Identity{}, errNoEmailClaim
	}

	auth := nestedClaims(claims, authClaim)
	organizationID := firstClaimString(organizationIDKeys, auth, claims)
	if organizationID == "" {
		organizationID = organizationFromList(claims)
	}

	return Identity{
		Email:          email,
		ChatGPTUserID:  claimString(auth, "chatgpt_user_id"),
		Plan:           claimString(auth, "chatgpt_plan_type"),
		AccountID:      firstClaimString(accountIDKeys, auth, claims),
		OrganizationID: organizationID,
	}, nil
}

// ParseTokenExpiry reads the top-level exp claim as a Unix timestamp.
//
// A missing or non-numeric exp yields the zero time with a nil error:
// the caller owns the policy for a token without an expiry, and
// NeedsRefresh already reads a zero expiry as "refresh now". Only
// structural damage returns an error.
func ParseTokenExpiry(idToken string) (time.Time, error) {
	claims, err := decodeIDTokenClaims(idToken)
	if err != nil {
		return time.Time{}, err
	}

	raw, ok := claims["exp"]
	if !ok {
		return time.Time{}, nil
	}
	seconds, ok := jsonFloat(raw)
	if !ok {
		return time.Time{}, nil
	}
	return time.Unix(int64(seconds), 0).UTC(), nil
}

// decodeIDTokenClaims extracts the payload segment of a JWT as a claim
// map. Anything that breaks that structure is malformed: fewer than two
// segments, bytes that are not base64url, or a payload that is not a
// JSON object. A JSON null payload decodes to a nil map without error —
// structurally valid, just claimless.
func decodeIDTokenClaims(idToken string) (map[string]json.RawMessage, error) {
	segments := strings.Split(idToken, ".")
	if len(segments) < 2 {
		return nil, errMalformedIDToken
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(segments[1], "="))
	if err != nil {
		return nil, errMalformedIDToken
	}
	var claims map[string]json.RawMessage
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, errMalformedIDToken
	}
	return claims, nil
}

// claimString reads a string claim from a map that may itself be nil.
// Absent, null or wrongly typed reads as empty, never as an error.
func claimString(claims map[string]json.RawMessage, key string) string {
	var value string
	if err := json.Unmarshal(claims[key], &value); err != nil {
		return ""
	}
	return value
}

// nestedClaims reads a claim expected to hold a nested object. Absent,
// null or non-object yields nil, which every helper reads as empty.
func nestedClaims(claims map[string]json.RawMessage, key string) map[string]json.RawMessage {
	var nested map[string]json.RawMessage
	if err := json.Unmarshal(claims[key], &nested); err != nil {
		return nil
	}
	return nested
}

// firstClaimString walks the sources in order and, within each source,
// the keys in order: the auth claim outranks the top level even when
// its key name ranks lower, because the namespaced claim is the server
// speaking about ChatGPT while the top-level one is legacy. Returns the
// first non-empty string claim found.
func firstClaimString(keys []string, sources ...map[string]json.RawMessage) string {
	for _, source := range sources {
		for _, key := range keys {
			if value := claimString(source, key); value != "" {
				return value
			}
		}
	}
	return ""
}

// organizationFromList falls back to the top-level organizations array
// when no claim key carried an organization id. The first entry with a
// non-empty id wins: an entry with no id is a miss, not a veto.
func organizationFromList(claims map[string]json.RawMessage) string {
	var organizations []map[string]json.RawMessage
	if err := json.Unmarshal(claims["organizations"], &organizations); err != nil {
		return ""
	}
	for _, organization := range organizations {
		if id := claimString(organization, "id"); id != "" {
			return id
		}
	}
	return ""
}

// jsonFloat reads a JSON number. Decoding into any and asserting
// float64 is the strict path: a string, bool or null is a miss, not a
// number that happens to hold a surprising value.
func jsonFloat(raw json.RawMessage) (float64, bool) {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return 0, false
	}
	seconds, ok := value.(float64)
	return seconds, ok
}

// TokenClaimsAreReadable reports whether a token carries a decodable JWT
// payload, regardless of what the claims say. Import classification uses
// it to tell an access token (always a JWT) from a raw refresh token
// (an opaque string): the shape, not the field names, decides.
func TokenClaimsAreReadable(token string) bool {
	if token == "" {
		return false
	}
	_, err := decodeIDTokenClaims(token)
	return err == nil
}

// IdentityFromAccessToken builds an identity from the access token's own
// claims when no id token exists — the shape every imported access-only
// credential arrives in. The email fallback chain mirrors the upstream
// codex tooling: a real email claim first, then stable stand-ins so two
// imports of the same account land on the same identity even when the
// token is silent about who owns it. ok is false only when the token
// cannot be read as a JWT at all.
func IdentityFromAccessToken(accessToken string) (Identity, bool) {
	claims, err := decodeIDTokenClaims(accessToken)
	if err != nil {
		return Identity{}, false
	}
	auth := nestedClaims(claims, authClaim)
	profile := nestedClaims(claims, profileClaim)
	accountID := firstClaimString(accountIDKeys, auth, claims)
	userID := firstClaimString([]string{"chatgpt_user_id", "user_id"}, auth, claims)
	if userID == "" {
		userID = claimString(claims, "sub")
	}
	organizationID := firstClaimString(organizationIDKeys, auth, claims)
	if organizationID == "" {
		organizationID = organizationFromList(claims)
	}
	email := claimString(claims, "email")
	if email == "" {
		email = claimString(profile, "email")
	}
	if email == "" {
		switch {
		case accountID != "":
			email = "codex-" + accountID
		case userID != "":
			email = "codex-" + userID
		default:
			email = "codex-access-" + tokenFingerprint(accessToken)
		}
	}
	return Identity{
		Email:          email,
		ChatGPTUserID:  userID,
		Plan:           claimString(auth, "chatgpt_plan_type"),
		AccountID:      accountID,
		OrganizationID: organizationID,
	}, true
}

// tokenFingerprint derives a stable stand-in from the token bytes: the
// first 12 hex characters of its digest. It is not treated as a secret —
// it never leaves the process and only ever feeds the synthetic fallback
// email, which is display text by design.
func tokenFingerprint(token string) string {
	digest := md5.Sum([]byte(token))
	return hex.EncodeToString(digest[:])[:12]
}
