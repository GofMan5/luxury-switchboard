package imports

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/codex/application"
)

// jwtForTest builds a three-segment string whose middle segment decodes
// to a JSON object — the shape the parser trusts as an access token.
// The claims stay opaque: shape, not content, decides classification.
func jwtForTest() string {
	return "h." + base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"u"}`)) + ".s"
}

func TestLineModeTypesEachTokenByItsShape(t *testing.T) {
	candidates, err := Parser{}.ParseCredentials("token-a\n" + jwtForTest() + "\ntoken-b")
	if err != nil {
		t.Fatalf("line mode refused a plain paste: %v", err)
	}
	if len(candidates) != 3 {
		t.Fatalf("paste yielded %d candidates, want 3: %+v", len(candidates), candidates)
	}
	if candidates[0].Kind != application.CredentialRefresh || candidates[0].RefreshToken != "token-a" {
		t.Fatalf("first line classified as %+v, want a refresh token", candidates[0])
	}
	if candidates[1].Kind != application.CredentialAccess || candidates[1].AccessToken != jwtForTest() {
		t.Fatalf("jwt line classified as %+v, want an access token", candidates[1])
	}
	if candidates[2].Kind != application.CredentialRefresh || candidates[2].RefreshToken != "token-b" {
		t.Fatalf("last line classified as %+v, want a refresh token", candidates[2])
	}
}

func TestLineModeRefusesAPasteThatHalfHolds(t *testing.T) {
	// Line 1 imports, line 3 holds a number: a number is not a
	// credential, and importing line 1 anyway would sign in half a
	// paste the user believes is whole. The refusal must also count
	// the blank line, so it names line 3, not line 2.
	_, err := Parser{}.ParseCredentials("token-a\n\n123")
	if err == nil {
		t.Fatal("a paste with a worthless line was accepted")
	}
	if err.Error() != "no codex credentials found on line 3" {
		t.Fatalf("error = %q, want the numbered refusal", err.Error())
	}
}

func TestALineHoldingAnArrayIsFlattenedIntoItsItems(t *testing.T) {
	// The paste must open with a plain line: a text that starts with
	// "[" is one JSON array document, not line mode. Inside a paste,
	// the array line still contributes its items, not itself.
	candidates, err := Parser{}.ParseCredentials("token-c\n" + `["token-a", "token-b"]`)
	if err != nil {
		t.Fatalf("array line refused: %v", err)
	}
	if len(candidates) != 3 {
		t.Fatalf("array line yielded %d candidates, want 3: %+v", len(candidates), candidates)
	}
	if candidates[0].RefreshToken != "token-c" {
		t.Fatalf("first line classified as %+v", candidates[0])
	}
	if candidates[1].RefreshToken != "token-a" || candidates[2].RefreshToken != "token-b" {
		t.Fatalf("array items classified as %+v", candidates)
	}
}

func TestALineHoldingAnArrayWithAWorthlessItemIsRefusedOnThatLine(t *testing.T) {
	_, err := Parser{}.ParseCredentials("token-c\n" + `["token-a", 7]`)
	if err == nil {
		t.Fatal("an array line holding a number was accepted")
	}
	if err.Error() != "no codex credentials found on line 2" {
		t.Fatalf("error = %q, want the numbered refusal", err.Error())
	}
}

func TestAnObjectDocumentYieldsTheStrongestCredentialItCarries(t *testing.T) {
	// One walker, three documents, one expectation each: a full pair
	// beats a lone refresh token beats a lone access token, whatever
	// layout the fields arrived in.
	tests := []struct {
		name string
		text string
		want application.CredentialCandidate
	}{
		{
			name: "flat pair with a refresh token",
			text: `{"id_token":"id","access_token":"` + jwtForTest() + `","refresh_token":"ref","account_id":"acc_1"}`,
			want: application.CredentialCandidate{
				Kind:          application.CredentialFull,
				IDToken:       "id",
				AccessToken:   jwtForTest(),
				RefreshToken:  "ref",
				AccountIDHint: "acc_1",
			},
		},
		{
			name: "auth.json layout nests the pair under tokens",
			text: `{"tokens":{"id_token":"id","access_token":"` + jwtForTest() + `","refresh_token":"ref"},"account_id":"acc_2"}`,
			want: application.CredentialCandidate{
				Kind:          application.CredentialFull,
				IDToken:       "id",
				AccessToken:   jwtForTest(),
				RefreshToken:  "ref",
				AccountIDHint: "acc_2",
			},
		},
		{
			name: "session_token spelling completes the pair",
			text: `{"id_token":"id","access_token":"` + jwtForTest() + `","session_token":"sess","account_id":"acc_3"}`,
			want: application.CredentialCandidate{
				Kind:          application.CredentialFull,
				IDToken:       "id",
				AccessToken:   jwtForTest(),
				RefreshToken:  "sess",
				AccountIDHint: "acc_3",
			},
		},
		{
			name: "camelCase spellings read the same",
			text: `{"idToken":"id","accessToken":"` + jwtForTest() + `","refreshToken":"ref"}`,
			want: application.CredentialCandidate{
				Kind:         application.CredentialFull,
				IDToken:      "id",
				AccessToken:  jwtForTest(),
				RefreshToken: "ref",
			},
		},
		{
			name: "lone refresh token at the top level",
			text: `{"refresh_token":"ref"}`,
			want: application.CredentialCandidate{
				Kind:         application.CredentialRefresh,
				RefreshToken: "ref",
			},
		},
		{
			name: "lone refresh token under tokens",
			text: `{"tokens":{"refresh_token":"ref"}}`,
			want: application.CredentialCandidate{
				Kind:         application.CredentialRefresh,
				RefreshToken: "ref",
			},
		},
		{
			name: "access token survives a missing id token",
			text: `{"tokens":{"access_token":"` + jwtForTest() + `","account_id":"acc_4"}}`,
			want: application.CredentialCandidate{
				Kind:          application.CredentialAccess,
				AccessToken:   jwtForTest(),
				AccountIDHint: "acc_4",
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidates, err := Parser{}.ParseCredentials(test.text)
			if err != nil {
				t.Fatalf("document refused: %v", err)
			}
			if len(candidates) != 1 {
				t.Fatalf("document yielded %d candidates, want 1: %+v", len(candidates), candidates)
			}
			if candidates[0] != test.want {
				t.Fatalf("candidate = %+v, want %+v", candidates[0], test.want)
			}
		})
	}
}

func TestAPIKeyDocumentsAreNotCodexLogins(t *testing.T) {
	// An API key proves a billing account, not an OAuth session; the
	// key must not ride back to the caller as a credential either.
	texts := []string{
		`{"OPENAI_API_KEY":"sk-test-secret"}`,
		`{"api_key":"sk-test-secret"}`,
	}
	for _, text := range texts {
		candidates, err := Parser{}.ParseCredentials(text)
		if err != nil {
			t.Fatalf("key document %q errored: %v", text, err)
		}
		if len(candidates) != 0 {
			t.Fatalf("key document %q imported %+v", text, candidates)
		}
	}
}

func TestASub2APIExportKeepsOnlyItsCodexLogins(t *testing.T) {
	// The export lists accounts for many platforms; only openai/oauth
	// rows with a readable access token become candidates. Skips are
	// silent — an export is a list to choose from, not a paste that
	// must hold.
	text := `{"accounts":[` +
		`{"platform":"OpenAI","type":"OAuth","credentials":{"access_token":"` + jwtForTest() + `"}},` +
		`{"platform":"openai","type":"api_key","credentials":{"api_key":"sk-test-secret"}},` +
		`{"platform":"anthropic","type":"oauth","credentials":{"access_token":"` + jwtForTest() + `"}},` +
		`{"platform":"openai","type":"oauth","credentials":{"refresh_token":"ref"}},` +
		`{"platform":"openai","type":"oauth"},` +
		`"not-an-object"` +
		`]}`
	candidates, err := Parser{}.ParseCredentials(text)
	if err != nil {
		t.Fatalf("export refused: %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("export yielded %d candidates, want the one codex login: %+v", len(candidates), candidates)
	}
	if candidates[0].Kind != application.CredentialAccess || candidates[0].AccessToken != jwtForTest() {
		t.Fatalf("exported login classified as %+v", candidates[0])
	}
}

func TestAJSONArrayKeepsTheItemsThatHoldCredentials(t *testing.T) {
	text := `[` +
		`{"OPENAI_API_KEY":"sk-test-secret"},` +
		`{"id_token":"id","access_token":"` + jwtForTest() + `"},` +
		`"token-a",` +
		`7` +
		`]`
	candidates, err := Parser{}.ParseCredentials(text)
	if err != nil {
		t.Fatalf("array refused: %v", err)
	}
	if len(candidates) != 2 {
		t.Fatalf("array yielded %d candidates, want 2: %+v", len(candidates), candidates)
	}
	if candidates[0].Kind != application.CredentialFull {
		t.Fatalf("first item classified as %+v, want the full pair", candidates[0])
	}
	if candidates[1].Kind != application.CredentialRefresh || candidates[1].RefreshToken != "token-a" {
		t.Fatalf("string item classified as %+v", candidates[1])
	}
}

func TestAQuotedJSONStringIsOneCredential(t *testing.T) {
	candidates, err := Parser{}.ParseCredentials(`"  token-a  "`)
	if err != nil {
		t.Fatalf("quoted string refused: %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("quoted string yielded %d candidates, want 1", len(candidates))
	}
	if candidates[0].Kind != application.CredentialRefresh || candidates[0].RefreshToken != "token-a" {
		t.Fatalf("quoted string classified as %+v", candidates[0])
	}
}

func TestUnparseableJSONTextFailsWithoutQuotingIt(t *testing.T) {
	// A failure that echoed the text would paste a secret into an
	// error destined for the UI; the refusal names nothing.
	_, err := Parser{}.ParseCredentials(`{"id_token":"secret-token-value"`)
	if err == nil {
		t.Fatal("truncated JSON was accepted")
	}
	if err.Error() != "codex import text could not be parsed" {
		t.Fatalf("error = %q, want the content-free refusal", err.Error())
	}
}

func TestBOMAndPaddingAroundAPasteAreNotPartOfIt(t *testing.T) {
	// A paste from a Windows editor can open with a byte order mark,
	// which TrimSpace does not eat; without stripping it, the first
	// line's token would carry an invisible prefix and never match.
	text := string(rune(0xFEFF)) + "  token-a  \n  token-b  "
	candidates, err := Parser{}.ParseCredentials(text)
	if err != nil {
		t.Fatalf("padded paste refused: %v", err)
	}
	if len(candidates) != 2 {
		t.Fatalf("padded paste yielded %d candidates, want 2: %+v", len(candidates), candidates)
	}
	if candidates[0].RefreshToken != "token-a" || candidates[1].RefreshToken != "token-b" {
		t.Fatalf("padded paste classified as %+v", candidates)
	}
}

func TestBlankTextYieldsNoCandidatesAndNoError(t *testing.T) {
	for _, text := range []string{"", "   \n  \t"} {
		candidates, err := Parser{}.ParseCredentials(text)
		if err != nil {
			t.Fatalf("blank text %q errored: %v", text, err)
		}
		if len(candidates) != 0 {
			t.Fatalf("blank text %q yielded %+v", text, candidates)
		}
	}
}

func TestATokenThatIsNotShapedLikeAJWTIsARefreshToken(t *testing.T) {
	// A JWT with an unreadable middle segment is not an access token
	// the parser can trust; it is an opaque string, and OpenAI issues
	// opaque refresh tokens exactly that shape.
	fake := "h." + base64.RawURLEncoding.EncodeToString([]byte{0xff, 0xfe}) + ".s"
	if strings.Contains(fake, "eyJ") {
		t.Fatal("fixture accidentally built a readable token")
	}
	candidates, err := Parser{}.ParseCredentials(fake)
	if err != nil {
		t.Fatalf("opaque token refused: %v", err)
	}
	if len(candidates) != 1 || candidates[0].Kind != application.CredentialRefresh || candidates[0].RefreshToken != fake {
		t.Fatalf("opaque token classified as %+v, want a refresh token", candidates)
	}
}
