package oauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/codex/domain"
)

func TestRefreshSendsThePinnedJSONBodyAndHeaders(t *testing.T) {
	requests := make(chan recordedRequest, 1)
	accountsRequests := make(chan recordedRequest, 1)
	authorizer := newTestEndpoints(t,
		record(requests, func(w http.ResponseWriter) {
			writeJSON(w, http.StatusOK, map[string]string{
				"access_token":  "at-2",
				"refresh_token": "rt-2",
			})
		}),
		func(w http.ResponseWriter, r *http.Request) {
			// A refresh never checks the account; reaching this handler
			// means the contract broke.
			accountsRequests <- recordedRequest{}
			writeRaw(w, http.StatusOK, "{}")
		},
	)

	session, err := authorizer.Refresh(context.Background(), "rt-1")
	if err != nil {
		t.Fatalf("Refresh() error = %v, want nil", err)
	}

	request := <-requests
	if contentType := request.header.Get("Content-Type"); contentType != "application/json" {
		t.Fatalf("refresh Content-Type = %q, want %q", contentType, "application/json")
	}
	if origin := request.header.Get("Originator"); origin != originator {
		t.Fatalf("refresh originator = %q, want %q", origin, originator)
	}
	wantAgent := fmt.Sprintf("Codex Desktop/%s (%s; %s)", testAppVersion, runtime.GOOS, runtime.GOARCH)
	if agent := request.header.Get("User-Agent"); agent != wantAgent {
		t.Fatalf("refresh User-Agent = %q, want %q", agent, wantAgent)
	}

	var body map[string]string
	if err := json.Unmarshal(request.body, &body); err != nil {
		t.Fatalf("refresh body is not JSON: %v", err)
	}
	if len(body) != 3 {
		t.Fatalf("refresh body has %d fields (%v), want 3", len(body), body)
	}
	if got := body["client_id"]; got != clientID {
		t.Fatalf("refresh client_id = %q, want %q", got, clientID)
	}
	if got := body["grant_type"]; got != "refresh_token" {
		t.Fatalf("refresh grant_type = %q, want %q", got, "refresh_token")
	}
	if got := body["refresh_token"]; got != "rt-1" {
		t.Fatalf("refresh refresh_token = %q, want %q", got, "rt-1")
	}
	if _, present := body["scope"]; present {
		t.Fatal("refresh body must not re-ask for the scope")
	}

	// A session without an id_token carries tokens only.
	if session.AccessToken != "at-2" || session.RefreshToken != "rt-2" {
		t.Fatalf("Refresh() session = %+v, want tokens at-2/rt-2", session)
	}
	if session.IDToken != "" {
		t.Fatalf("Refresh() id token = %q, want empty", session.IDToken)
	}
	if session.Identity != (domain.Identity{}) {
		t.Fatalf("Refresh() identity = %+v, want zero", session.Identity)
	}
	if !session.AccessExpiry.IsZero() {
		t.Fatalf("Refresh() expiry = %v, want zero", session.AccessExpiry)
	}
	if len(accountsRequests) != 0 {
		t.Fatal("the accounts check ran during a refresh")
	}
}

func TestRefreshWithAnIDTokenCarriesIdentityAndExpiry(t *testing.T) {
	expiry := time.Unix(1800000000, 0).UTC()
	idToken := testIDToken(identityClaims("dev@example.com", "acct-2", expiry))
	authorizer := newTestEndpoints(t,
		func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.Copy(io.Discard, r.Body)
			writeJSON(w, http.StatusOK, map[string]string{
				"access_token":  "at-2",
				"refresh_token": "rt-2",
				"id_token":      idToken,
			})
		},
		func(w http.ResponseWriter, r *http.Request) { writeRaw(w, http.StatusOK, "{}") },
	)

	session, err := authorizer.Refresh(context.Background(), "rt-1")
	if err != nil {
		t.Fatalf("Refresh() error = %v, want nil", err)
	}
	if session.IDToken != idToken {
		t.Fatalf("Refresh() id token = %q, want the refreshed one", session.IDToken)
	}
	if !session.AccessExpiry.Equal(expiry) {
		t.Fatalf("Refresh() expiry = %v, want %v", session.AccessExpiry, expiry)
	}
	wantIdentity := domain.Identity{
		Email:          "dev@example.com",
		ChatGPTUserID:  "user-1",
		Plan:           "plus",
		AccountID:      "acct-2",
		OrganizationID: "org-1",
	}
	if session.Identity != wantIdentity {
		t.Fatalf("Refresh() identity = %+v, want %+v", session.Identity, wantIdentity)
	}
}

func TestRefreshWithAMalformedIDTokenStaysLenient(t *testing.T) {
	authorizer := newTestEndpoints(t,
		func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.Copy(io.Discard, r.Body)
			writeJSON(w, http.StatusOK, map[string]string{
				"access_token":  "at-2",
				"refresh_token": "rt-2",
				"id_token":      "not-a-jwt",
			})
		},
		func(w http.ResponseWriter, r *http.Request) { writeRaw(w, http.StatusOK, "{}") },
	)

	session, err := authorizer.Refresh(context.Background(), "rt-1")
	if err != nil {
		t.Fatalf("Refresh() error = %v, want nil: the tokens stand even when the identity does not", err)
	}
	if session.AccessToken != "at-2" || session.RefreshToken != "rt-2" {
		t.Fatalf("Refresh() session = %+v, want tokens at-2/rt-2", session)
	}
	if session.IDToken != "" {
		t.Fatalf("Refresh() id token = %q, want dropped", session.IDToken)
	}
	if session.Identity != (domain.Identity{}) {
		t.Fatalf("Refresh() identity = %+v, want zero", session.Identity)
	}
}

func TestRefreshDropsAnAlreadyExpiredIDToken(t *testing.T) {
	// The lapsed id_token is the token endpoint's stale answer about who
	// the user is: the access/refresh pair it delivered is live, but
	// identity from an expired claim is a fact about the past. Dropping
	// it here leaves the merge to keep the stored identity untouched
	// instead of stamping the session with a lapse the UI would count
	// down to.
	expired := time.Now().Add(-time.Hour).UTC()
	idToken := testIDToken(identityClaims("dev@example.com", "acct-2", expired))
	authorizer := newTestEndpoints(t,
		func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.Copy(io.Discard, r.Body)
			writeJSON(w, http.StatusOK, map[string]string{
				"access_token":  "at-2",
				"refresh_token": "rt-2",
				"id_token":      idToken,
			})
		},
		func(w http.ResponseWriter, r *http.Request) { writeRaw(w, http.StatusOK, "{}") },
	)

	session, err := authorizer.Refresh(context.Background(), "rt-1")
	if err != nil {
		t.Fatalf("Refresh() error = %v, want nil: a lapsed id_token is not a failed refresh", err)
	}
	if session.AccessToken != "at-2" || session.RefreshToken != "rt-2" {
		t.Fatalf("Refresh() session = %+v, want tokens at-2/rt-2", session)
	}
	if session.IDToken != "" {
		t.Fatalf("Refresh() id token = %q, want dropped", session.IDToken)
	}
	if session.Identity != (domain.Identity{}) {
		t.Fatalf("Refresh() identity = %+v, want zero", session.Identity)
	}
	if !session.AccessExpiry.IsZero() {
		t.Fatalf("Refresh() expiry = %v, want zero: no usable id_token, no expiry", session.AccessExpiry)
	}
}

func TestRefreshKeepsAnUndatedIDTokenLenient(t *testing.T) {
	// An id_token without an exp claim says nothing about time at all,
	// which is not the same as saying the past: the old lenient path
	// (deliver identity, leave expiry zero) is exactly right for it, and
	// only a claim that positively lapsed is dropped.
	claims := identityClaims("dev@example.com", "acct-2", time.Time{})
	delete(claims, "exp")
	idToken := testIDToken(claims)
	authorizer := newTestEndpoints(t,
		func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.Copy(io.Discard, r.Body)
			writeJSON(w, http.StatusOK, map[string]string{
				"access_token":  "at-2",
				"refresh_token": "rt-2",
				"id_token":      idToken,
			})
		},
		func(w http.ResponseWriter, r *http.Request) { writeRaw(w, http.StatusOK, "{}") },
	)

	session, err := authorizer.Refresh(context.Background(), "rt-1")
	if err != nil {
		t.Fatalf("Refresh() error = %v, want nil", err)
	}
	if session.IDToken != idToken {
		t.Fatalf("Refresh() id token = %q, want the refreshed one", session.IDToken)
	}
	if session.Identity.Email != "dev@example.com" || session.Identity.AccountID != "acct-2" {
		t.Fatalf("Refresh() identity = %+v, want the undated identity kept", session.Identity)
	}
	if !session.AccessExpiry.IsZero() {
		t.Fatalf("Refresh() expiry = %v, want zero", session.AccessExpiry)
	}
}

func TestRefreshWithoutARotatedRefreshTokenStillDeliversTheAccessToken(t *testing.T) {
	authorizer := newTestEndpoints(t,
		func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.Copy(io.Discard, r.Body)
			writeJSON(w, http.StatusOK, map[string]any{
				"access_token": "at-2",
				"expires_in":   3600,
			})
		},
		func(w http.ResponseWriter, r *http.Request) { writeRaw(w, http.StatusOK, "{}") },
	)

	session, err := authorizer.Refresh(context.Background(), "rt-1")
	if err != nil {
		t.Fatalf("Refresh() error = %v, want nil", err)
	}
	// Only the access token rotated, and it must still be delivered: the
	// application layer's merge falls back to the stored refresh token,
	// but an access token dropped here would leave the relay sending an
	// empty Bearer with no signal to re-login.
	if session.AccessToken != "at-2" {
		t.Fatalf("Refresh() access token = %q, want %q", session.AccessToken, "at-2")
	}
	if session.RefreshToken != "" {
		t.Fatalf("Refresh() refresh token = %q, want empty: the fallback to the stored one is the caller's", session.RefreshToken)
	}
	if session.IDToken != "" {
		t.Fatalf("Refresh() id token = %q, want empty", session.IDToken)
	}
	if session.Identity != (domain.Identity{}) {
		t.Fatalf("Refresh() identity = %+v, want zero", session.Identity)
	}
	// The session's expiry comes from the id_token's claims alone, so a
	// response without one leaves it zero despite the expires_in field.
	if !session.AccessExpiry.IsZero() {
		t.Fatalf("Refresh() expiry = %v, want zero", session.AccessExpiry)
	}
}

func TestRefreshFailsWithTheOAuthErrorCodeVerbatim(t *testing.T) {
	cases := []struct {
		name     string
		body     string
		status   int
		wantCode string
		reauth   bool
	}{
		{"flat error string", `{"error":"invalid_grant"}`, http.StatusBadRequest, "invalid_grant", true},
		// token_invalidated is one of the domain's reauth markers: a
		// revoked refresh token means the session is dead for good.
		{"nested error object", `{"error":{"code":"token_invalidated"}}`, http.StatusBadRequest, "token_invalidated", true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			authorizer := newTestEndpoints(t,
				func(w http.ResponseWriter, r *http.Request) {
					_, _ = io.Copy(io.Discard, r.Body)
					writeRaw(w, testCase.status, testCase.body)
				},
				func(w http.ResponseWriter, r *http.Request) { writeRaw(w, http.StatusOK, "{}") },
			)

			session, err := authorizer.Refresh(context.Background(), "rt-1")
			if err == nil {
				t.Fatalf("Refresh() = (%+v, nil), want an error", session)
			}
			want := fmt.Sprintf("codex oauth refresh failed: http %d %s", testCase.status, testCase.wantCode)
			if err.Error() != want {
				t.Fatalf("Refresh() error = %q, want %q", err.Error(), want)
			}
			if got := domain.IsReauthError(err.Error()); got != testCase.reauth {
				t.Fatalf("IsReauthError(%q) = %v, want %v", err.Error(), got, testCase.reauth)
			}
		})
	}
}

func TestTokenRequestsHonorContextCancellation(t *testing.T) {
	// The endpoints never answer: a cancelled context must fail the
	// request before any response could arrive.
	hang := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	})
	tokenServer := httptest.NewServer(hang)
	t.Cleanup(tokenServer.Close)
	accountsServer := httptest.NewServer(hang)
	t.Cleanup(accountsServer.Close)
	authorizer := NewAuthorizerWithEndpoints(nil, testAppVersion, Endpoints{
		AuthorizeURL:              "https://authorize.test/oauth/authorize",
		TokenURL:                  tokenServer.URL,
		AccountsCheckURL:          accountsServer.URL,
		RedirectURI:               testRedirectURI,
		DeviceUserCodeURL:         "https://device-usercode.test",
		DeviceTokenURL:            "https://device-token.test",
		DeviceVerificationURL:     "https://device-verification.test",
		DeviceExchangeRedirectURI: "https://device-exchange.test",
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := authorizer.ExchangeCode(ctx, "code", "verifier"); err == nil {
		t.Fatal("ExchangeCode() with a cancelled context must fail")
	} else if !errors.Is(err, context.Canceled) {
		t.Fatalf("ExchangeCode() error = %v, want context.Canceled inside", err)
	} else if !strings.HasPrefix(err.Error(), "codex oauth exchange failed: ") {
		t.Fatalf("ExchangeCode() error = %q, want the exchange prefix", err.Error())
	}

	if _, err := authorizer.Refresh(ctx, "rt-1"); err == nil {
		t.Fatal("Refresh() with a cancelled context must fail")
	} else if !errors.Is(err, context.Canceled) {
		t.Fatalf("Refresh() error = %v, want context.Canceled inside", err)
	} else if !strings.HasPrefix(err.Error(), "codex oauth refresh failed: ") {
		t.Fatalf("Refresh() error = %q, want the refresh prefix", err.Error())
	}
}
