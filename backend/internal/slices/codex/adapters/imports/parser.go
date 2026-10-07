// Package imports turns pasted text and picked auth files into import
// candidates for the codex application layer, implementing the
// CredentialParser port.
//
// Classification is deliberately shape-driven, mirroring the upstream
// codex tooling: a document that carries an id token and an access
// token is a full login, a bare non-JWT string is a refresh token, and
// a JWT-shaped string is an access token. Where a value was found —
// a pasted line, an auth.json document, or a sub2api-style export —
// never changes what it proves. Errors from this package carry
// positions only, never token material: a parse failure names the
// line, not the text.
package imports

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/codex/application"
)

// Parser extracts import candidates from credential text. It is
// stateless and safe for concurrent use; one instance serves the
// service for the lifetime of the process.
type Parser struct{}

// NewParser returns the parser the composition root hands the service.
func NewParser() Parser { return Parser{} }

// ParseCredentials implements application.CredentialParser. Text that
// is not JSON at all is line mode — one credential per line — because
// that is how single tokens are pasted. Text that starts like JSON is
// parsed as exactly one document: an object, an array of documents, or
// a bare JSON string.
func (Parser) ParseCredentials(text string) ([]application.CredentialCandidate, error) {
	// A paste from a Windows editor can carry a UTF-8 byte order mark,
	// which is not whitespace to strings.TrimSpace.
	trimmed := strings.TrimPrefix(strings.TrimSpace(text), string(rune(0xFEFF)))
	if trimmed == "" {
		return nil, nil
	}
	if !strings.HasPrefix(trimmed, "{") && !strings.HasPrefix(trimmed, "[") {
		return parseCredentialLines(trimmed)
	}
	var value any
	if err := json.Unmarshal([]byte(trimmed), &value); err != nil {
		return nil, errors.New("codex import text could not be parsed")
	}
	switch typed := value.(type) {
	case map[string]any:
		return candidatesFromObject(typed), nil
	case []any:
		return candidatesFromItems(typed), nil
	case string:
		if candidate, ok := candidateFromString(typed); ok {
			return []application.CredentialCandidate{candidate}, nil
		}
		return nil, nil
	default:
		return nil, nil
	}
}

// parseCredentialLines imports line mode. Every non-empty line must
// yield a candidate: a paste of tokens is typed by hand or copied in
// full, so a line that holds nothing means the paste is not what the
// user thinks it is, and importing the rest silently would sign in
// half an input. A line that parses as JSON but is an array is
// flattened — the line still contributes its items, not itself.
func parseCredentialLines(text string) ([]application.CredentialCandidate, error) {
	var candidates []application.CredentialCandidate
	for index, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		var value any
		if err := json.Unmarshal([]byte(line), &value); err != nil {
			value = line
		}
		if array, ok := value.([]any); ok {
			for _, item := range array {
				candidate, ok := candidateFromValue(item)
				if !ok {
					return nil, fmt.Errorf("no codex credentials found on line %d", index+1)
				}
				candidates = append(candidates, candidate)
			}
			continue
		}
		candidate, ok := candidateFromValue(value)
		if !ok {
			return nil, fmt.Errorf("no codex credentials found on line %d", index+1)
		}
		candidates = append(candidates, candidate)
	}
	return candidates, nil
}

// candidatesFromObject handles one JSON object. A sub2api-style export
// carries an accounts array and is expanded to one candidate per
// matching account; any other object is a single document worth at
// most one candidate.
func candidatesFromObject(object map[string]any) []application.CredentialCandidate {
	if accounts, ok := object["accounts"].([]any); ok {
		return candidatesFromExportedAccounts(accounts)
	}
	if candidate, ok := candidateFromObject(object); ok {
		return []application.CredentialCandidate{candidate}
	}
	return nil
}

// candidatesFromExportedAccounts filters a sub2api export down to its
// codex logins: accounts marked platform openai and type oauth. The
// rest of the export — other platforms, API-key accounts — is skipped
// without an error, because an export is a list to choose from, not a
// paste that must hold.
func candidatesFromExportedAccounts(accounts []any) []application.CredentialCandidate {
	var candidates []application.CredentialCandidate
	for _, item := range accounts {
		object, ok := item.(map[string]any)
		if !ok {
			continue
		}
		platform, _ := object["platform"].(string)
		accountType, _ := object["type"].(string)
		if !strings.EqualFold(platform, "openai") || !strings.EqualFold(accountType, "oauth") {
			continue
		}
		if candidate, ok := candidateFromObject(object); ok {
			candidates = append(candidates, candidate)
		}
	}
	return candidates
}

// candidatesFromItems imports a JSON array: every item that holds a
// candidate is collected and items that hold none are skipped. An
// array is a structured export, where filtering is expected; the
// strictness of line mode does not apply.
func candidatesFromItems(items []any) []application.CredentialCandidate {
	var candidates []application.CredentialCandidate
	for _, item := range items {
		if candidate, ok := candidateFromValue(item); ok {
			candidates = append(candidates, candidate)
		}
	}
	return candidates
}

// candidateFromValue classifies one JSON value by its shape. Strings
// are tokens; objects are credential documents; anything else holds
// nothing importable.
func candidateFromValue(value any) (application.CredentialCandidate, bool) {
	switch typed := value.(type) {
	case string:
		return candidateFromString(typed)
	case map[string]any:
		return candidateFromObject(typed)
	default:
		return application.CredentialCandidate{}, false
	}
}

// candidateFromString classifies a bare string: a JWT-shaped string
// carries an access token, anything else is treated as a refresh
// token — the opaque shape OpenAI issues for them.
func candidateFromString(token string) (application.CredentialCandidate, bool) {
	token = strings.TrimSpace(token)
	if token == "" {
		return application.CredentialCandidate{}, false
	}
	if tokenPayloadReadable(token) {
		return application.CredentialCandidate{
			Kind:        application.CredentialAccess,
			AccessToken: token,
		}, true
	}
	return application.CredentialCandidate{
		Kind:         application.CredentialRefresh,
		RefreshToken: token,
	}, true
}

// candidateFromObject classifies one credential document. The order
// matters: a full pair beats a lone refresh token beats a lone access
// token, so whatever the document proves most strongly is what gets
// imported.
func candidateFromObject(object map[string]any) (application.CredentialCandidate, bool) {
	if candidate, ok := fullCandidate(object); ok {
		return candidate, true
	}
	if candidate, ok := refreshCandidate(object); ok {
		return candidate, true
	}
	return accessCandidate(object)
}

// fullCandidate recognizes the two layouts that carry a full login.
// Format 1 is a flat document with id_token and access_token at the
// top level. Format 2 is the auth.json layout, the same fields nested
// under "tokens". A session-token spelling ("session_token") counts as
// the pair's refresh token; an API-key document has neither pair and
// is not a codex OAuth login.
func fullCandidate(object map[string]any) (application.CredentialCandidate, bool) {
	id := firstString(object, "id_token", "idToken")
	access := firstString(object, "access_token", "accessToken")
	if id != "" && access != "" {
		return application.CredentialCandidate{
			Kind:          application.CredentialFull,
			IDToken:       id,
			AccessToken:   access,
			RefreshToken:  firstString(object, "refresh_token", "refreshToken", "session_token", "sessionToken"),
			AccountIDHint: firstString(object, "account_id", "accountId"),
		}, true
	}
	tokens, _ := object["tokens"].(map[string]any)
	if tokens == nil {
		return application.CredentialCandidate{}, false
	}
	id = firstString(tokens, "id_token", "idToken")
	access = firstString(tokens, "access_token", "accessToken")
	if id == "" || access == "" {
		return application.CredentialCandidate{}, false
	}
	refresh := firstString(tokens, "refresh_token", "refreshToken", "session_token", "sessionToken")
	if refresh == "" {
		refresh = firstString(object, "session_token", "sessionToken")
	}
	hint := firstString(tokens, "account_id")
	if hint == "" {
		hint = firstString(object, "account_id", "accountId")
	}
	return application.CredentialCandidate{
		Kind:          application.CredentialFull,
		IDToken:       id,
		AccessToken:   access,
		RefreshToken:  refresh,
		AccountIDHint: hint,
	}, true
}

// refreshCandidate looks for a lone refresh token once no full pair
// was found, in the documents that carry one at the top level or
// under "tokens". Session-token spellings do not apply here: those
// belong to the full-pair layouts above, and a lone "session_token"
// is not a refresh path.
func refreshCandidate(object map[string]any) (application.CredentialCandidate, bool) {
	refresh := firstString(object, "refresh_token", "refreshToken")
	if refresh == "" {
		if tokens, _ := object["tokens"].(map[string]any); tokens != nil {
			refresh = firstString(tokens, "refresh_token", "refreshToken")
		}
	}
	if refresh == "" {
		return application.CredentialCandidate{}, false
	}
	return application.CredentialCandidate{
		Kind:         application.CredentialRefresh,
		RefreshToken: refresh,
	}, true
}

// accessCandidate is the last resort: no pair and no refresh token,
// but a JWT access token somewhere. An auth.json whose id token went
// missing still carries the account id hint, which survives the
// access-only import; a sub2api export nests the token under
// "credentials".
func accessCandidate(object map[string]any) (application.CredentialCandidate, bool) {
	if tokens, _ := object["tokens"].(map[string]any); tokens != nil {
		if access := firstJWT(tokens, "access_token", "accessToken"); access != "" {
			hint := firstString(tokens, "account_id")
			if hint == "" {
				hint = firstString(object, "account_id", "accountId")
			}
			return application.CredentialCandidate{
				Kind:          application.CredentialAccess,
				AccessToken:   access,
				AccountIDHint: hint,
			}, true
		}
	}
	if credentials, _ := object["credentials"].(map[string]any); credentials != nil {
		if access := firstJWT(credentials, "access_token", "accessToken"); access != "" {
			return application.CredentialCandidate{
				Kind:        application.CredentialAccess,
				AccessToken: access,
			}, true
		}
	}
	if access := firstJWT(object, "access_token", "accessToken", "token"); access != "" {
		return application.CredentialCandidate{
			Kind:        application.CredentialAccess,
			AccessToken: access,
		}, true
	}
	return application.CredentialCandidate{}, false
}

// firstString returns the first non-empty string value under the keys.
func firstString(object map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := object[key].(string); ok && value != "" {
			return value
		}
	}
	return ""
}

// firstJWT returns the first value under the keys that reads as a JWT
// access token. Tokens that do not decode are skipped, not trusted:
// an arbitrary string under "access_token" proves nothing.
func firstJWT(object map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := object[key].(string); ok && tokenPayloadReadable(value) {
			return value
		}
	}
	return ""
}

// tokenPayloadReadable reports whether the string is shaped like a
// JWT: three dot-separated segments whose middle decodes as base64url
// into a JSON object. Claims are not inspected — shape decides here,
// and the payload stays a black box.
func tokenPayloadReadable(token string) bool {
	segments := strings.Split(token, ".")
	if len(segments) != 3 || segments[0] == "" || segments[1] == "" {
		return false
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(segments[1], "="))
	if err != nil {
		return false
	}
	trimmed := bytes.TrimSpace(payload)
	return len(trimmed) > 0 && trimmed[0] == '{' && json.Valid(trimmed)
}
