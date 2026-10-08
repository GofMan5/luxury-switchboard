package oauth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/codex/application"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/codex/domain"
)

// testAppVersion is the application version the test authorizers carry;
// the refresh request's User-Agent embeds it.
const testAppVersion = "1.0.8"

// testRedirectURI is the redirect address the test authorizers register,
// equal to the production one so the form assertions pin the real shape.
const testRedirectURI = "http://localhost:1455/auth/callback"

// recordedRequest is what a test endpoint saw, delivered on a channel so
// assertions in the test goroutine synchronize on the request itself
// rather than trusting handler timing.
type recordedRequest struct {
	header http.Header
	body   []byte
}

// record captures the request, then hands the response to the responder.
// The capture happens before the response is written, so once the caller
// under test has read the response, the channel already holds the record.
func record(out chan recordedRequest, respond func(w http.ResponseWriter)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 64*1024))
		out <- recordedRequest{header: r.Header.Clone(), body: body}
		respond(w)
	}
}

// writeJSON answers with a JSON body and status.
func writeJSON(w http.ResponseWriter, statusCode int, payload any) {
	body, err := json.Marshal(payload)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_, _ = w.Write(body)
}

// writeRaw answers with a verbatim body and status, for error bodies the
// endpoint shapes by hand.
func writeRaw(w http.ResponseWriter, statusCode int, body string) {
	w.WriteHeader(statusCode)
	_, _ = io.WriteString(w, body)
}

// testIDToken builds a JWT-shaped id_token carrying claims. The domain
// parser is a claim reader, not a signature validator — tokens reach it
// over TLS from the OAuth server — so an unsigned three-segment token
// with the right claims is exactly what it accepts in production.
func testIDToken(claims map[string]any) string {
	header, _ := json.Marshal(map[string]string{"alg": "none", "typ": "JWT"})
	payload, _ := json.Marshal(claims)
	return base64.RawURLEncoding.EncodeToString(header) + "." +
		base64.RawURLEncoding.EncodeToString(payload) + ".unsigned"
}

// identityClaims is a domain-consistent claim set: the email and expiry
// at the top level, the ChatGPT fields under OpenAI's namespaced auth
// claim.
func identityClaims(email, accountID string, expiry time.Time) map[string]any {
	return map[string]any{
		"email": email,
		"exp":   expiry.Unix(),
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_user_id":    "user-1",
			"chatgpt_plan_type":  "plus",
			"chatgpt_account_id": accountID,
			"organization_id":    "org-1",
		},
	}
}

// newTestEndpoints wires an Authorizer with a nil client (exercising the
// default-client fallback) against local token and accounts check
// servers.
func newTestEndpoints(t *testing.T, tokenHandler, accountsHandler http.HandlerFunc) *Authorizer {
	t.Helper()
	tokenServer := httptest.NewServer(tokenHandler)
	accountsServer := httptest.NewServer(accountsHandler)
	t.Cleanup(func() {
		tokenServer.Close()
		accountsServer.Close()
	})
	return NewAuthorizerWithEndpoints(nil, testAppVersion, Endpoints{
		AuthorizeURL:              "https://authorize.test/oauth/authorize",
		TokenURL:                  tokenServer.URL,
		AccountsCheckURL:          accountsServer.URL,
		RedirectURI:               testRedirectURI,
		DeviceUserCodeURL:         "https://device-usercode.test",
		DeviceTokenURL:            "https://device-token.test",
		DeviceVerificationURL:     "https://device-verification.test",
		DeviceExchangeRedirectURI: "https://device-exchange.test",
	})
}

func TestAuthorizeURLCarriesEveryPinnedParameter(t *testing.T) {
	authorizer := NewAuthorizer(nil, testAppVersion)

	got := authorizer.AuthorizeURL("aabbccdd00112233", "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSNBCcU1Hk")
	want := "https://auth.openai.com/oauth/authorize" +
		"?client_id=app_EMoamEEZ73f0CkXaXp7hrann" +
		"&redirect_uri=http%3A%2F%2Flocalhost%3A1455%2Fauth%2Fcallback" +
		"&response_type=code" +
		"&scope=openid+profile+email+offline_access+api.connectors.read+api.connectors.invoke" +
		"&state=aabbccdd00112233" +
		"&code_challenge=E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSNBCcU1Hk" +
		"&code_challenge_method=S256" +
		"&id_token_add_organizations=true" +
		"&codex_cli_simplified_flow=true" +
		"&originator=Codex+Desktop"
	if got != want {
		t.Fatalf("AuthorizeURL() =\n%s\nwant\n%s", got, want)
	}
}

func TestAuthorizeURLEscapesItsInputsAndNeverCarriesLoginID(t *testing.T) {
	authorizer := NewAuthorizerWithEndpoints(nil, testAppVersion, Endpoints{
		AuthorizeURL:              "https://authorize.test/oauth/authorize",
		TokenURL:                  "https://token.test/oauth/token",
		AccountsCheckURL:          "https://accounts.test/check",
		RedirectURI:               "http://localhost:9/auth/callback",
		DeviceUserCodeURL:         "https://device-usercode.test",
		DeviceTokenURL:            "https://device-token.test",
		DeviceVerificationURL:     "https://device-verification.test",
		DeviceExchangeRedirectURI: "https://device-exchange.test",
	})

	raw := authorizer.AuthorizeURL("st ate&=?", "challenge value")
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("AuthorizeURL() produced %q, which does not parse: %v", raw, err)
	}
	if parsed.Scheme != "https" || parsed.Host != "authorize.test" || parsed.Path != "/oauth/authorize" {
		t.Fatalf("AuthorizeURL() landed on %s, want https://authorize.test/oauth/authorize", raw)
	}

	query := parsed.Query()
	want := map[string]string{
		"client_id":                  clientID,
		"redirect_uri":               "http://localhost:9/auth/callback",
		"response_type":              "code",
		"scope":                      scope,
		"state":                      "st ate&=?",
		"code_challenge":             "challenge value",
		"code_challenge_method":      "S256",
		"id_token_add_organizations": "true",
		"codex_cli_simplified_flow":  "true",
		"originator":                 originator,
	}
	if len(query) != len(want) {
		t.Fatalf("authorize URL carries %d parameters (%v), want %d", len(query), query, len(want))
	}
	for key, value := range want {
		if got := query.Get(key); got != value {
			t.Fatalf("authorize URL parameter %s = %q, want %q", key, got, value)
		}
	}
	if _, present := query["login_id"]; present {
		t.Fatal("authorize URL must not carry a login_id parameter")
	}
}

func TestExchangeCodeSendsThePinnedFormAndBuildsTheSession(t *testing.T) {
	expiry := time.Unix(1800000000, 0).UTC()
	idToken := testIDToken(identityClaims("dev@example.com", "acct-1", expiry))
	tokenRequests := make(chan recordedRequest, 1)
	accountsRequests := make(chan recordedRequest, 1)

	authorizer := newTestEndpoints(t,
		record(tokenRequests, func(w http.ResponseWriter) {
			writeJSON(w, http.StatusOK, map[string]string{
				"access_token":  "at-1",
				"refresh_token": "rt-1",
				"id_token":      idToken,
			})
		}),
		func(w http.ResponseWriter, r *http.Request) {
			accountsRequests <- recordedRequest{header: r.Header.Clone()}
			writeRaw(w, http.StatusOK, `{"accounts":[]}`)
		},
	)

	session, err := authorizer.ExchangeCode(context.Background(), "the-code", "the-verifier")
	if err != nil {
		t.Fatalf("ExchangeCode() error = %v, want nil", err)
	}

	// The exchange request is bare: exactly the five form fields, the
	// form content type, and no client identity of its own.
	request := <-tokenRequests
	if contentType := request.header.Get("Content-Type"); contentType != "application/x-www-form-urlencoded" {
		t.Fatalf("exchange Content-Type = %q, want %q", contentType, "application/x-www-form-urlencoded")
	}
	if userAgent := request.header.Get("User-Agent"); strings.Contains(userAgent, "Codex") {
		t.Fatalf("exchange User-Agent = %q, want no client identity", userAgent)
	}
	if origin := request.header.Get("Originator"); origin != "" {
		t.Fatalf("exchange carried originator %q, want none", origin)
	}
	if auth := request.header.Get("Authorization"); auth != "" {
		t.Fatalf("exchange carried Authorization %q, want none", auth)
	}
	form, err := url.ParseQuery(string(request.body))
	if err != nil {
		t.Fatalf("exchange body is not a form: %v", err)
	}
	wantForm := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {"the-code"},
		"redirect_uri":  {testRedirectURI},
		"client_id":     {clientID},
		"code_verifier": {"the-verifier"},
	}
	if len(form) != len(wantForm) {
		t.Fatalf("exchange form has %d fields (%v), want %d", len(form), form, len(wantForm))
	}
	for key, values := range wantForm {
		if got := form.Get(key); got != values[0] {
			t.Fatalf("exchange form field %s = %q, want %q", key, got, values[0])
		}
	}

	// The session carries the tokens verbatim and the identity decoded
	// from the id_token.
	if session.AccessToken != "at-1" {
		t.Fatalf("session access token = %q, want %q", session.AccessToken, "at-1")
	}
	if session.RefreshToken != "rt-1" {
		t.Fatalf("session refresh token = %q, want %q", session.RefreshToken, "rt-1")
	}
	if session.IDToken != idToken {
		t.Fatalf("session id token = %q, want the exchanged one", session.IDToken)
	}
	if !session.AccessExpiry.Equal(expiry) {
		t.Fatalf("session access expiry = %v, want %v", session.AccessExpiry, expiry)
	}
	wantIdentity := domain.Identity{
		Email:          "dev@example.com",
		ChatGPTUserID:  "user-1",
		Plan:           "plus",
		AccountID:      "acct-1",
		OrganizationID: "org-1",
	}
	if session.Identity != wantIdentity {
		t.Fatalf("session identity = %+v, want %+v", session.Identity, wantIdentity)
	}

	// The accounts check authenticated as the fresh token on the
	// identity's account.
	check := <-accountsRequests
	if auth := check.header.Get("Authorization"); auth != "Bearer at-1" {
		t.Fatalf("accounts check Authorization = %q, want %q", auth, "Bearer at-1")
	}
	if accept := check.header.Get("Accept"); accept != "application/json" {
		t.Fatalf("accounts check Accept = %q, want %q", accept, "application/json")
	}
	if account := check.header.Get("account-id"); account != "acct-1" {
		t.Fatalf("accounts check account-id = %q, want %q", account, "acct-1")
	}
}

func TestExchangeCodeFailsWithTheOAuthErrorCodeVerbatim(t *testing.T) {
	cases := []struct {
		name     string
		body     string
		status   int
		wantCode string
		reauth   bool
	}{
		{"flat error string", `{"error":"invalid_grant"}`, http.StatusBadRequest, "invalid_grant", true},
		{"nested error object", `{"error":{"code":"token_expired"}}`, http.StatusBadRequest, "token_expired", false},
		{"bare code", `{"code":"server_error"}`, http.StatusForbidden, "server_error", false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			accountsRequests := make(chan recordedRequest, 1)
			authorizer := newTestEndpoints(t,
				func(w http.ResponseWriter, r *http.Request) {
					_, _ = io.Copy(io.Discard, r.Body)
					writeRaw(w, testCase.status, testCase.body)
				},
				func(w http.ResponseWriter, r *http.Request) {
					accountsRequests <- recordedRequest{}
					writeRaw(w, http.StatusOK, "{}")
				},
			)

			session, err := authorizer.ExchangeCode(context.Background(), "code", "verifier")
			if err == nil {
				t.Fatalf("ExchangeCode() = (%+v, nil), want an error", session)
			}
			want := fmt.Sprintf("codex oauth exchange failed: http %d %s", testCase.status, testCase.wantCode)
			if err.Error() != want {
				t.Fatalf("ExchangeCode() error = %q, want %q", err.Error(), want)
			}
			// The code must survive verbatim: IsReauthError classifies
			// exactly this text.
			if got := domain.IsReauthError(err.Error()); got != testCase.reauth {
				t.Fatalf("IsReauthError(%q) = %v, want %v", err.Error(), got, testCase.reauth)
			}
			// A failed exchange never reaches the accounts check.
			if len(accountsRequests) != 0 {
				t.Fatal("the accounts check ran although the exchange failed")
			}
		})
	}
}

func TestExchangeCodeWithAnUnparseableErrorBodyFallsBackToStatusText(t *testing.T) {
	authorizer := newTestEndpoints(t,
		func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.Copy(io.Discard, r.Body)
			writeRaw(w, http.StatusInternalServerError, "upstream exploded")
		},
		func(w http.ResponseWriter, r *http.Request) { writeRaw(w, http.StatusOK, "{}") },
	)

	_, err := authorizer.ExchangeCode(context.Background(), "code", "verifier")
	if err == nil {
		t.Fatal("ExchangeCode() with a 500 answer must fail")
	}
	want := "codex oauth exchange failed: http 500 Internal Server Error"
	if err.Error() != want {
		t.Fatalf("ExchangeCode() error = %q, want %q", err.Error(), want)
	}
}

func TestAMalformedIDTokenFailsTheExchange(t *testing.T) {
	cases := []struct {
		name    string
		idToken string
		want    string
	}{
		{"structurally broken", "not-a-jwt", "codex oauth exchange failed: codex id token is malformed"},
		{"no email claim", testIDToken(map[string]any{"exp": 123}), "codex oauth exchange failed: codex id token has no email claim"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			accountsRequests := make(chan recordedRequest, 1)
			authorizer := newTestEndpoints(t,
				func(w http.ResponseWriter, r *http.Request) {
					_, _ = io.Copy(io.Discard, r.Body)
					writeJSON(w, http.StatusOK, map[string]string{
						"access_token": "at-1",
						"id_token":     testCase.idToken,
					})
				},
				func(w http.ResponseWriter, r *http.Request) {
					accountsRequests <- recordedRequest{}
					writeRaw(w, http.StatusOK, "{}")
				},
			)

			_, err := authorizer.ExchangeCode(context.Background(), "code", "verifier")
			if err == nil {
				t.Fatalf("exchange with id token %q must fail", testCase.idToken)
			}
			if err.Error() != testCase.want {
				t.Fatalf("ExchangeCode() error = %q, want %q", err.Error(), testCase.want)
			}
			if len(accountsRequests) != 0 {
				t.Fatal("the accounts check ran although the id token failed to decode")
			}
		})
	}
}

func TestTheAccountsCheckFailsTheLoginOnlyOnAnExplicitRejection(t *testing.T) {
	expiry := time.Unix(1800000000, 0).UTC()
	idToken := testIDToken(identityClaims("dev@example.com", "acct-1", expiry))
	for _, statusCode := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		t.Run(fmt.Sprintf("http%d", statusCode), func(t *testing.T) {
			authorizer := newTestEndpoints(t,
				func(w http.ResponseWriter, r *http.Request) {
					_, _ = io.Copy(io.Discard, r.Body)
					writeJSON(w, http.StatusOK, map[string]string{
						"access_token":  "at-1",
						"refresh_token": "rt-1",
						"id_token":      idToken,
					})
				},
				func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(statusCode) },
			)

			session, err := authorizer.ExchangeCode(context.Background(), "code", "verifier")
			if err == nil {
				t.Fatalf("ExchangeCode() = (%+v, nil), want the login refused", session)
			}
			want := fmt.Sprintf("codex account check failed: http %d", statusCode)
			if err.Error() != want {
				t.Fatalf("ExchangeCode() error = %q, want %q", err.Error(), want)
			}
		})
	}
}

func TestANonVerdictAccountsCheckDoesNotCostTheLogin(t *testing.T) {
	expiry := time.Unix(1800000000, 0).UTC()
	idToken := testIDToken(identityClaims("dev@example.com", "acct-1", expiry))
	respondWithTokens := func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		writeJSON(w, http.StatusOK, map[string]string{
			"access_token":  "at-1",
			"refresh_token": "rt-1",
			"id_token":      idToken,
		})
	}

	t.Run("server error", func(t *testing.T) {
		authorizer := newTestEndpoints(t, respondWithTokens,
			func(w http.ResponseWriter, r *http.Request) { writeRaw(w, http.StatusInternalServerError, "{}") })

		session, err := authorizer.ExchangeCode(context.Background(), "code", "verifier")
		if err != nil {
			t.Fatalf("ExchangeCode() error = %v, want nil: a 5xx check is not a verdict", err)
		}
		if session.AccessToken != "at-1" || session.RefreshToken != "rt-1" || session.IDToken != idToken {
			t.Fatalf("ExchangeCode() session = %+v, want the exchanged tokens", session)
		}
	})

	t.Run("network failure", func(t *testing.T) {
		// A closed server stands in for an unreachable endpoint: the
		// port answers with a refusal instead of a verdict.
		dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		deadURL := dead.URL
		dead.Close()

		tokenServer := httptest.NewServer(http.HandlerFunc(respondWithTokens))
		t.Cleanup(tokenServer.Close)
		authorizer := NewAuthorizerWithEndpoints(nil, testAppVersion, Endpoints{
			AuthorizeURL:              "https://authorize.test/oauth/authorize",
			TokenURL:                  tokenServer.URL,
			AccountsCheckURL:          deadURL,
			RedirectURI:               testRedirectURI,
			DeviceUserCodeURL:         "https://device-usercode.test",
			DeviceTokenURL:            "https://device-token.test",
			DeviceVerificationURL:     "https://device-verification.test",
			DeviceExchangeRedirectURI: "https://device-exchange.test",
		})

		session, err := authorizer.ExchangeCode(context.Background(), "code", "verifier")
		if err != nil {
			t.Fatalf("ExchangeCode() error = %v, want nil: an unreachable check is not a verdict", err)
		}
		if session.AccessToken != "at-1" || session.RefreshToken != "rt-1" || session.IDToken != idToken {
			t.Fatalf("ExchangeCode() session = %+v, want the exchanged tokens", session)
		}
	})
}

func TestATwoHundredAnswerThatIsNotTheContractedJSONFails(t *testing.T) {
	authorizer := newTestEndpoints(t,
		func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.Copy(io.Discard, r.Body)
			writeRaw(w, http.StatusOK, "welcome to nginx")
		},
		func(w http.ResponseWriter, r *http.Request) { writeRaw(w, http.StatusOK, "{}") },
	)

	_, err := authorizer.ExchangeCode(context.Background(), "code", "verifier")
	if err == nil {
		t.Fatal("ExchangeCode() with an unparseable 2xx body must fail")
	}
	want := "codex oauth exchange failed: token response could not be decoded"
	if err.Error() != want {
		t.Fatalf("ExchangeCode() error = %q, want %q", err.Error(), want)
	}
}

// newUsageAuthorizer wires an Authorizer against a local usage endpoint.
// The token and accounts legs point at inert placeholders: the usage
// probe must never touch them.
func newUsageAuthorizer(t *testing.T, usageHandler http.HandlerFunc) *Authorizer {
	t.Helper()
	usageServer := httptest.NewServer(usageHandler)
	t.Cleanup(usageServer.Close)
	return NewAuthorizerWithEndpoints(nil, testAppVersion, Endpoints{
		AuthorizeURL:              "https://authorize.test/oauth/authorize",
		TokenURL:                  "https://token.test/oauth/token",
		AccountsCheckURL:          "https://accounts.test/check",
		UsageURL:                  usageServer.URL,
		RedirectURI:               testRedirectURI,
		DeviceUserCodeURL:         "https://device-usercode.test",
		DeviceTokenURL:            "https://device-token.test",
		DeviceVerificationURL:     "https://device-verification.test",
		DeviceExchangeRedirectURI: "https://device-exchange.test",
	})
}

// TestTheUsageProbeCarriesTheAccountAndNormalizesBothWindows pins the
// happy path: a GET bearing the access token and the account id, and
// the answer projected onto the domain's two meters — the epoch reset
// wins on the primary window, the offset reset settles for "now plus"
// on the secondary, and remaining counts down from a hundred.
func TestTheUsageProbeCarriesTheAccountAndNormalizesBothWindows(t *testing.T) {
	usageRequests := make(chan recordedRequest, 1)
	epochReset := time.Now().Add(3 * time.Hour).Unix()
	authorizer := newUsageAuthorizer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("usage probe method = %s, want GET", r.Method)
		}
		usageRequests <- recordedRequest{header: r.Header.Clone()}
		writeJSON(w, http.StatusOK, map[string]any{
			"plan_type": "plus",
			"rate_limit": map[string]any{
				"primary_window": map[string]any{
					"used_percent":         62,
					"limit_window_seconds": 300,
					"reset_at":             epochReset,
				},
				"secondary_window": map[string]any{
					"used_percent":         5,
					"limit_window_seconds": 604_800,
					"reset_after_seconds":  3600,
				},
			},
		})
	})

	probeStart := time.Now()
	usage, err := authorizer.FetchUsage(context.Background(), "at-1", "acct-1")
	probeEnd := time.Now()
	if err != nil {
		t.Fatalf("FetchUsage() error = %v, want nil", err)
	}

	request := <-usageRequests
	if got := request.header.Get("Authorization"); got != "Bearer at-1" {
		t.Fatalf("usage probe Authorization = %q, want the bearer token", got)
	}
	if got := request.header.Get("Accept"); got != "application/json" {
		t.Fatalf("usage probe Accept = %q, want application/json", got)
	}
	if got := request.header.Get("account-id"); got != "acct-1" {
		t.Fatalf("usage probe account-id = %q, want the account id", got)
	}

	if usage.PlanType != "plus" {
		t.Fatalf("PlanType = %q, want plus", usage.PlanType)
	}
	if usage.Primary.Present != true {
		t.Fatalf("primary window Present = %v, want true", usage.Primary.Present)
	}
	if usage.Primary.RemainingPercent != 38 {
		t.Fatalf("primary RemainingPercent = %d, want 38", usage.Primary.RemainingPercent)
	}
	if usage.Primary.WindowMinutes != 5 {
		t.Fatalf("primary WindowMinutes = %d, want 5", usage.Primary.WindowMinutes)
	}
	if !usage.Primary.ResetAt.Equal(time.Unix(epochReset, 0).UTC()) {
		t.Fatalf("primary ResetAt = %v, want the reported epoch exactly", usage.Primary.ResetAt)
	}
	if usage.Secondary.Present != true {
		t.Fatalf("secondary window Present = %v, want true", usage.Secondary.Present)
	}
	if usage.Secondary.RemainingPercent != 95 {
		t.Fatalf("secondary RemainingPercent = %d, want 95", usage.Secondary.RemainingPercent)
	}
	if usage.Secondary.WindowMinutes != 10_080 {
		t.Fatalf("secondary WindowMinutes = %d, want 10080", usage.Secondary.WindowMinutes)
	}
	if reset := usage.Secondary.ResetAt; reset.Before(probeStart.Add(59*time.Minute)) || reset.After(probeEnd.Add(61*time.Minute)) {
		t.Fatalf("secondary ResetAt = %v, want now plus the reported hour", reset)
	}
}

// TestAUsageAnswerWithoutALimitBlockStillReportsThePlan pins the
// graceful shape: an endpoint that names the plan but reports no
// windows is a successful probe with absent meters — "no limit
// reported", not "all spent".
func TestAUsageAnswerWithoutALimitBlockStillReportsThePlan(t *testing.T) {
	authorizer := newUsageAuthorizer(t, func(w http.ResponseWriter, r *http.Request) {
		writeRaw(w, http.StatusOK, `{"plan_type":"team"}`)
	})

	usage, err := authorizer.FetchUsage(context.Background(), "at-1", "acct-1")
	if err != nil {
		t.Fatalf("FetchUsage() error = %v, want nil: the plan name is still worth showing", err)
	}
	if usage.PlanType != "team" {
		t.Fatalf("PlanType = %q, want team", usage.PlanType)
	}
	for name, window := range map[string]domain.QuotaWindow{"primary": usage.Primary, "secondary": usage.Secondary} {
		if window.Present {
			t.Fatalf("%s window Present = true, want false", name)
		}
		if window.RemainingPercent != 100 {
			t.Fatalf("%s RemainingPercent = %d, want 100: an unreported limit must not read as spent", name, window.RemainingPercent)
		}
		if window.WindowMinutes != 0 || !window.ResetAt.IsZero() {
			t.Fatalf("%s window = %+v, want no invented width or deadline", name, window)
		}
	}
}

// TestARejectedUsageProbeReportsTheUnauthorizedSentinel pins the 401
// contract: the caller rotates the token and retries, and the body the
// endpoint offered is echoed nowhere.
func TestARejectedUsageProbeReportsTheUnauthorizedSentinel(t *testing.T) {
	authorizer := newUsageAuthorizer(t, func(w http.ResponseWriter, r *http.Request) {
		writeRaw(w, http.StatusUnauthorized, `{"error":"the token at-1 is revoked"}`)
	})

	_, err := authorizer.FetchUsage(context.Background(), "at-1", "acct-1")
	if !errors.Is(err, application.ErrUsageUnauthorized) {
		t.Fatalf("FetchUsage() error = %v, want the unauthorized sentinel", err)
	}
	if err.Error() != "codex oauth usage probe failed: http 401: codex usage probe was unauthorized" {
		t.Fatalf("FetchUsage() error = %q, want status text only", err.Error())
	}
	if strings.Contains(err.Error(), "revoked") {
		t.Fatalf("FetchUsage() error = %q echoes the endpoint's body, want none", err.Error())
	}
}

// A barred account is an ordinary failed probe, not a rotation signal:
// the account check treats 403 as a login-ending verdict, but a probe
// has no login to fail.
func TestABarredAccountIsAnOrdinaryFailedProbe(t *testing.T) {
	authorizer := newUsageAuthorizer(t, func(w http.ResponseWriter, r *http.Request) {
		writeRaw(w, http.StatusForbidden, "forbidden")
	})

	_, err := authorizer.FetchUsage(context.Background(), "at-1", "acct-1")
	if errors.Is(err, application.ErrUsageUnauthorized) {
		t.Fatalf("FetchUsage() error = %v, want 403 to stay an ordinary failure", err)
	}
	if err == nil || err.Error() != "codex oauth usage probe failed: http 403" {
		t.Fatalf("FetchUsage() error = %v, want status text only", err)
	}
}

// Non-2xx answers report the status alone for any code the backend
// picks: the probe's failure text stays short, fixed and body-free.
func TestAFailedUsageEndpointReportsTheStatusOnly(t *testing.T) {
	authorizer := newUsageAuthorizer(t, func(w http.ResponseWriter, r *http.Request) {
		writeRaw(w, http.StatusBadGateway, "upstream splat")
	})

	_, err := authorizer.FetchUsage(context.Background(), "at-1", "acct-1")
	if err == nil || err.Error() != "codex oauth usage probe failed: http 502" {
		t.Fatalf("FetchUsage() error = %v, want status text only", err)
	}
}

// TestAnUnparseableUsageAnswerIsTheUndecodableSentinel pins the 2xx
// failure: a body that is not the contracted JSON is the probe's own
// undecodable verdict, fixed text, nothing echoed.
func TestAnUnparseableUsageAnswerIsTheUndecodableSentinel(t *testing.T) {
	authorizer := newUsageAuthorizer(t, func(w http.ResponseWriter, r *http.Request) {
		writeRaw(w, http.StatusOK, "welcome to nginx")
	})

	_, err := authorizer.FetchUsage(context.Background(), "at-1", "acct-1")
	if !errors.Is(err, errUsageResponseUndecodable) {
		t.Fatalf("FetchUsage() error = %v, want the undecodable sentinel", err)
	}
	if err == nil || err.Error() != "codex oauth usage probe failed: usage response could not be decoded" {
		t.Fatalf("FetchUsage() error = %v, want the fixed undecodable text", err)
	}
}

// A usage endpoint that never answers reports the transport failure
// under the probe prefix, and is not a rotation signal.
func TestAnUnreachableUsageEndpointReportsATransportFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	usageURL := server.URL
	server.Close()
	authorizer := NewAuthorizerWithEndpoints(nil, testAppVersion, Endpoints{
		AuthorizeURL:              "https://authorize.test/oauth/authorize",
		TokenURL:                  "https://token.test/oauth/token",
		AccountsCheckURL:          "https://accounts.test/check",
		UsageURL:                  usageURL,
		RedirectURI:               testRedirectURI,
		DeviceUserCodeURL:         "https://device-usercode.test",
		DeviceTokenURL:            "https://device-token.test",
		DeviceVerificationURL:     "https://device-verification.test",
		DeviceExchangeRedirectURI: "https://device-exchange.test",
	})

	_, err := authorizer.FetchUsage(context.Background(), "at-1", "acct-1")
	if errors.Is(err, application.ErrUsageUnauthorized) {
		t.Fatalf("FetchUsage() error = %v, want a transport failure, not the rotation signal", err)
	}
	if err == nil || !strings.HasPrefix(err.Error(), "codex oauth usage probe failed:") {
		t.Fatalf("FetchUsage() error = %v, want the probe prefix", err)
	}
}
