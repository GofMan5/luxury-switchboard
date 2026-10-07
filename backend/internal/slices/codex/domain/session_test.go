package domain

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestSessionNeedsRefresh(t *testing.T) {
	// The skew must shrink the usable window from the far side only:
	// at exactly expiry-skew the token is still usable, one nanosecond
	// later it is not, and a zero expiry is never trusted.
	now := time.Unix(1_750_000_000, 0).UTC()
	expiry := now.Add(10 * time.Minute)
	skew := time.Minute
	refreshAt := expiry.Add(-skew)

	cases := []struct {
		name    string
		session Session
		now     time.Time
		want    bool
	}{
		{"a zero expiry always needs refresh", Session{}, now, true},
		{"a token well before the refresh boundary is usable", Session{AccessExpiry: expiry}, now, false},
		{"the boundary instant itself is still usable", Session{AccessExpiry: expiry}, refreshAt, false},
		{"one nanosecond past the boundary needs refresh", Session{AccessExpiry: expiry}, refreshAt.Add(time.Nanosecond), true},
		{"the expiry instant needs refresh", Session{AccessExpiry: expiry}, expiry, true},
		{"long after the expiry needs refresh", Session{AccessExpiry: expiry}, expiry.Add(time.Hour), true},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := testCase.session.NeedsRefresh(testCase.now, skew); got != testCase.want {
				t.Fatalf("NeedsRefresh(%s, %s) = %t, want %t",
					testCase.now.Format(time.RFC3339Nano), skew, got, testCase.want)
			}
		})
	}
}

func TestSessionEmpty(t *testing.T) {
	// Empty is a statement about tokens, not identity: an email without
	// any token material cannot be presented to anyone.
	cases := []struct {
		name    string
		session Session
		want    bool
	}{
		{"no tokens at all", Session{}, true},
		{"an access token present", Session{AccessToken: "at"}, false},
		{"a refresh token present", Session{RefreshToken: "rt"}, false},
		{"an id token present", Session{IDToken: "idt"}, false},
		{"an identity without tokens", Session{Identity: Identity{Email: "user@example.com"}}, true},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := testCase.session.Empty(); got != testCase.want {
				t.Fatalf("Empty() = %t, want %t", got, testCase.want)
			}
		})
	}
}

func TestIsReauthError(t *testing.T) {
	// Reauth markers arrive wrapped in vendor prefixes and arbitrary
	// casing, so detection is a folded substring scan; anything else is
	// an ordinary transport failure, not a reason to drop the session.
	messages := []struct {
		message string
		want    bool
	}{
		{"oauth: refresh_token_reused during token exchange", true},
		{"ERROR: REFRESH_TOKEN_REUSED", true},
		{"Token_Invalidated by upstream", true},
		{"grant rejected: INVALID_GRANT", true},
		{"Invalid Refresh Token for session", true},
		{"Authentication Token Has Been Invalidated", true},
		{"connection reset by peer", false},
		{"dial tcp: i/o timeout", false},
		{"", false},
	}

	for _, testCase := range messages {
		if got := IsReauthError(testCase.message); got != testCase.want {
			t.Fatalf("IsReauthError(%q) = %t, want %t", testCase.message, got, testCase.want)
		}
	}
}

func TestStorageID(t *testing.T) {
	// StorageID is a stable fingerprint of the account triple: same
	// inputs, same id, any single input changed, different id.
	base := StorageID("user@example.com", "acct-123", "org-456")

	if again := StorageID("user@example.com", "acct-123", "org-456"); again != base {
		t.Fatalf("StorageID is not stable: %q vs %q", base, again)
	}
	if !strings.HasPrefix(base, "codex_") {
		t.Fatalf("StorageID = %q, want prefix %q", base, "codex_")
	}
	if digest := strings.TrimPrefix(base, "codex_"); len(digest) != 32 {
		t.Fatalf("StorageID digest = %q, want 32 hex characters", digest)
	}

	variants := []struct {
		name string
		id   string
	}{
		{"a different email", StorageID("other@example.com", "acct-123", "org-456")},
		{"a different account id", StorageID("user@example.com", "acct-999", "org-456")},
		{"a different organization id", StorageID("user@example.com", "acct-123", "org-999")},
	}
	for _, variant := range variants {
		if variant.id == base {
			t.Fatalf("StorageID ignores %s", variant.name)
		}
	}
}

func TestSessionRedactedSummaryOmitsTokenMaterial(t *testing.T) {
	// The session is the one place token secrets live, so the summary
	// is checked against sentinels rather than against exact wording:
	// whatever it says, the tokens, their prefix and the organization id
	// must not survive into it.
	session := Session{
		AccessToken:  "ACCESS-SENTINEL-do-not-print",
		RefreshToken: "REFRESH-SENTINEL-do-not-print",
		IDToken:      "ID-SENTINEL-do-not-print." + strings.Repeat("x", 32),
		AccessExpiry: time.Unix(1750000000, 0).UTC(),
		Identity: Identity{
			Email:          "user@example.com",
			Plan:           "pro",
			AccountID:      "acct-123",
			OrganizationID: "org-should-not-appear",
		},
	}

	summary := session.RedactedSummary()

	for _, want := range []string{"user@example.com", "pro", "acct-123", "2025-06-15T15:06:40Z"} {
		if !strings.Contains(summary, want) {
			t.Fatalf("RedactedSummary() = %q, want it to contain %q", summary, want)
		}
	}
	for _, forbidden := range []string{
		"ACCESS-SENTINEL-do-not-print",
		"REFRESH-SENTINEL-do-not-print",
		"ID-SENTINEL",
		"SENTINEL",
		"org-should-not-appear",
		"\n",
	} {
		if strings.Contains(summary, forbidden) {
			t.Fatalf("RedactedSummary() = %q, must not contain %q", summary, forbidden)
		}
	}

	// A String method is the standing temptation that leaks the tokens
	// through %v/%s on the next formatting site; both receivers are
	// checked because the value method set hides pointer-receiver ones.
	for _, receiver := range []reflect.Type{reflect.TypeOf(Session{}), reflect.TypeOf(&Session{})} {
		if method, found := receiver.MethodByName("String"); found {
			t.Fatalf("Session must not implement fmt.Stringer, found String method %v", method)
		}
	}
}
