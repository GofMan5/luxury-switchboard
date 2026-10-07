package domain

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// fixtureHeader is a structurally valid JWT header so fixtures look
// like real tokens. Its content is never parsed.
const fixtureHeader = "eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9"

// signedIDToken builds a three-segment JWT from arbitrary claims. The
// signature segment is decorative; parsing never verifies it.
func signedIDToken(t *testing.T, claims map[string]any) string {
	t.Helper()
	encoded, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("marshal claims: %v", err)
	}
	return fixtureHeader + "." + base64.RawURLEncoding.EncodeToString(encoded) + ".sig"
}

func TestParseIDToken(t *testing.T) {
	// Claim lookup is a precedence question, so the table pins every
	// order the spec names: email falls back from the top level to the
	// profile claim, account and organization keys are tried most
	// specific first, and the auth claim outranks the top level even
	// when its key name ranks lower.
	cases := []struct {
		name    string
		claims  map[string]any
		want    Identity
		wantErr string
	}{
		{
			name: "a token with the full auth claim",
			claims: map[string]any{
				"email": "user@example.com",
				"https://api.openai.com/auth": map[string]any{
					"chatgpt_user_id":    "user-789",
					"chatgpt_plan_type":  "pro",
					"chatgpt_account_id": "acct-123",
					"organization_id":    "org-456",
				},
			},
			want: Identity{
				Email:          "user@example.com",
				ChatGPTUserID:  "user-789",
				Plan:           "pro",
				AccountID:      "acct-123",
				OrganizationID: "org-456",
			},
		},
		{
			name: "an email that lives in the profile claim",
			claims: map[string]any{
				"https://api.openai.com/profile": map[string]any{"email": "profile@example.com"},
				"https://api.openai.com/auth":    map[string]any{"chatgpt_account_id": "acct-123"},
			},
			want: Identity{Email: "profile@example.com", AccountID: "acct-123"},
		},
		{
			name:    "a token without any email",
			claims:  map[string]any{"https://api.openai.com/auth": map[string]any{"chatgpt_user_id": "user-789"}},
			wantErr: "codex id token has no email claim",
		},
		{
			name: "an account id under the generic auth key",
			claims: map[string]any{
				"email":                       "user@example.com",
				"https://api.openai.com/auth": map[string]any{"account_id": "acct-fallback"},
			},
			want: Identity{Email: "user@example.com", AccountID: "acct-fallback"},
		},
		{
			name:   "an account id at the top level without an auth claim",
			claims: map[string]any{"email": "user@example.com", "account_id": "acct-top"},
			want:   Identity{Email: "user@example.com", AccountID: "acct-top"},
		},
		{
			name:   "no account id anywhere",
			claims: map[string]any{"email": "user@example.com"},
			want:   Identity{Email: "user@example.com"},
		},
		{
			name: "an organization id under the primary key",
			claims: map[string]any{
				"email": "user@example.com",
				"https://api.openai.com/auth": map[string]any{
					"organization_id":         "org-primary",
					"chatgpt_organization_id": "org-secondary",
				},
			},
			want: Identity{Email: "user@example.com", OrganizationID: "org-primary"},
		},
		{
			name: "an organization id under the uppercase POID key",
			claims: map[string]any{
				"email":                       "user@example.com",
				"https://api.openai.com/auth": map[string]any{"POID": "org-poid"},
			},
			want: Identity{Email: "user@example.com", OrganizationID: "org-poid"},
		},
		{
			name: "an organization id under the lowercase poid key",
			claims: map[string]any{
				"email":                       "user@example.com",
				"https://api.openai.com/auth": map[string]any{"poid": "org-lower"},
			},
			want: Identity{Email: "user@example.com", OrganizationID: "org-lower"},
		},
		{
			name:   "an organization id at the top level without an auth claim",
			claims: map[string]any{"email": "user@example.com", "organization_id": "org-top"},
			want:   Identity{Email: "user@example.com", OrganizationID: "org-top"},
		},
		{
			name: "an organization id from the organizations list",
			claims: map[string]any{
				"email": "user@example.com",
				"organizations": []any{
					map[string]any{"id": ""},
					map[string]any{"id": "org-from-list"},
				},
			},
			want: Identity{Email: "user@example.com", OrganizationID: "org-from-list"},
		},
		{
			name: "no organization id anywhere",
			claims: map[string]any{
				"email":                       "user@example.com",
				"https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "pro"},
			},
			want: Identity{Email: "user@example.com", Plan: "pro"},
		},
		{
			name: "the auth claim outranks the top level even across key names",
			claims: map[string]any{
				"email":              "user@example.com",
				"chatgpt_account_id": "acct-top-priority",
				"https://api.openai.com/auth": map[string]any{
					"account_id": "acct-auth-wins",
				},
			},
			want: Identity{Email: "user@example.com", AccountID: "acct-auth-wins"},
		},
		{
			name:    "a null payload reads as claimless",
			claims:  nil,
			wantErr: "codex id token has no email claim",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got, err := ParseIDToken(signedIDToken(t, testCase.claims))

			if testCase.wantErr != "" {
				if err == nil {
					t.Fatalf("ParseIDToken(...) = %+v, want error %q", got, testCase.wantErr)
				}
				if err.Error() != testCase.wantErr {
					t.Fatalf("ParseIDToken(...) error = %q, want %q", err.Error(), testCase.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseIDToken(...) = %v, want nil error", err)
			}
			if got != testCase.want {
				t.Fatalf("ParseIDToken(...) = %+v, want %+v", got, testCase.want)
			}
		})
	}
}

func TestParseIDTokenAcceptsAPayloadWithTrailingPadding(t *testing.T) {
	// Some OAuth servers emit the payload with standard base64 padding
	// instead of the raw variant; the decoder strips it. The "sub" claim
	// exists only to push the payload length off a multiple of three so
	// the fixture really is padded.
	claims, err := json.Marshal(map[string]any{"email": "padded@example.com", "sub": "u1"})
	if err != nil {
		t.Fatalf("marshal claims: %v", err)
	}
	paddedSegment := base64.URLEncoding.EncodeToString(claims)
	if !strings.HasSuffix(paddedSegment, "=") {
		t.Fatalf("fixture is not padded: %q", paddedSegment)
	}

	identity, err := ParseIDToken(fixtureHeader + "." + paddedSegment + ".sig")
	if err != nil {
		t.Fatalf("ParseIDToken(...) = %v, want nil error", err)
	}
	if identity.Email != "padded@example.com" {
		t.Fatalf("ParseIDToken(...) email = %q, want %q", identity.Email, "padded@example.com")
	}
}

func TestParseIDTokenAcceptsATwoSegmentToken(t *testing.T) {
	// A JWT with no signature segment is still structurally decodable:
	// two segments satisfy the header.payload minimum.
	payload, err := json.Marshal(map[string]any{"email": "two@example.com"})
	if err != nil {
		t.Fatalf("marshal claims: %v", err)
	}
	token := fixtureHeader + "." + base64.RawURLEncoding.EncodeToString(payload)

	identity, err := ParseIDToken(token)
	if err != nil {
		t.Fatalf("ParseIDToken(...) = %v, want nil error", err)
	}
	if identity.Email != "two@example.com" {
		t.Fatalf("ParseIDToken(...) email = %q, want %q", identity.Email, "two@example.com")
	}
}

func TestParseIDTokenRejectsMalformedTokens(t *testing.T) {
	// Only structural damage lands here: fewer than two segments, bytes
	// that are not base64url, or a payload that is not a JSON object.
	cases := []struct {
		name  string
		token string
	}{
		{"a single segment without a payload", "only-segment"},
		{"an empty token", ""},
		{"a payload that is not base64url", fixtureHeader + ".###.sig"},
		{"a payload that is not JSON", fixtureHeader + "." + base64.RawURLEncoding.EncodeToString([]byte("not json")) + ".sig"},
		{"a payload that is JSON but not an object", fixtureHeader + "." + base64.RawURLEncoding.EncodeToString([]byte("42")) + ".sig"},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := ParseIDToken(testCase.token)
			if err == nil || err.Error() != "codex id token is malformed" {
				t.Fatalf("ParseIDToken(%q) error = %v, want %q", testCase.token, err, "codex id token is malformed")
			}
		})
	}
}

func TestParseTokenExpiry(t *testing.T) {
	// A structurally valid token without a usable exp reads as the zero
	// time with no error: NeedsRefresh already treats a zero expiry as
	// "refresh now", so the missing-expiry policy stays with the caller.
	cases := []struct {
		name   string
		claims map[string]any
		want   time.Time
	}{
		{"a numeric exp", map[string]any{"exp": 1750000000}, time.Unix(1750000000, 0).UTC()},
		{"a missing exp", map[string]any{"email": "user@example.com"}, time.Time{}},
		{"a string exp", map[string]any{"exp": "soon"}, time.Time{}},
		{"a null exp", map[string]any{"exp": nil}, time.Time{}},
		{"a boolean exp", map[string]any{"exp": true}, time.Time{}},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got, err := ParseTokenExpiry(signedIDToken(t, testCase.claims))
			if err != nil {
				t.Fatalf("ParseTokenExpiry(...) = %v, want nil error", err)
			}
			if !got.Equal(testCase.want) {
				t.Fatalf("ParseTokenExpiry(...) = %s, want %s", got.Format(time.RFC3339), testCase.want.Format(time.RFC3339))
			}
		})
	}

	if _, err := ParseTokenExpiry("garbage"); err == nil || err.Error() != "codex id token is malformed" {
		t.Fatalf("ParseTokenExpiry(%q) error = %v, want %q", "garbage", err, "codex id token is malformed")
	}
}
