// Package oauth implements the OpenAI OAuth client for the Codex preset
// on top of the application layer's Authorizer port: it composes the
// authorize URL the system browser opens, trades the loopback redirect's
// code for a session, refreshes access tokens silently, and runs the
// device-code flow for screens that cannot take a browser redirect.
//
// Every endpoint is a pinned constant rather than configuration: a client
// whose token endpoint could be swapped could be swapped for one that
// echoes the tokens back out. Error text carries OAuth error codes and
// HTTP status text only — never token material or response bodies —
// because it flows into login status, logs and crash reports.
package oauth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/codex/application"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/codex/domain"
)

// The adapter must satisfy the port as declared, not as remembered.
var _ application.Authorizer = (*Authorizer)(nil)

const (
	// clientID is the application identity OpenAI issued to this preset.
	// It is public by design: the flow's security rests on PKCE and the
	// loopback redirect, not on a secret embedded in the binary.
	clientID = "app_EMoamEEZ73f0CkXaXp7hrann"

	// productionAuthorizeURL and productionTokenURL are OpenAI's OAuth
	// endpoints; productionAccountsCheckURL is the ChatGPT backend's
	// account liveness check that gates a fresh login, and
	// productionUsageURL is the same backend's usage endpoint the quota
	// card probes. The token endpoint also serves the device flow: the
	// exchange that trades a granted device code for tokens is a form
	// POST to the same URL.
	productionAuthorizeURL     = "https://auth.openai.com/oauth/authorize"
	productionTokenURL         = "https://auth.openai.com/oauth/token"
	productionAccountsCheckURL = "https://chatgpt.com/backend-api/wham/accounts/check"
	productionUsageURL         = "https://chatgpt.com/backend-api/wham/usage"

	// productionRedirectURI is the loopback address registered with
	// OpenAI for this client. Its port must stay in lockstep with the
	// loopback adapter's pinned listener.
	productionRedirectURI = "http://localhost:1455/auth/callback"

	// The device flow's legs: the accounts endpoints that hand out a
	// user code and report whether the user approved it, the page the
	// user is told to open, and the redirect address the granted code
	// was issued against — a public callback, not the loopback port,
	// because this flow never touches the local listener.
	productionDeviceUserCodeURL      = "https://auth.openai.com/api/accounts/deviceauth/usercode"
	productionDeviceTokenURL         = "https://auth.openai.com/api/accounts/deviceauth/token"
	productionDeviceVerificationURL  = "https://auth.openai.com/codex/device"
	productionDeviceExchangeRedirect = "https://auth.openai.com/deviceauth/callback"

	// deviceErrorPrefix opens the error reported when the device flow
	// could not even start: the endpoint that hands out the user code
	// refused or could not be reached. It is deliberately not the
	// exchange prefix, so a login that never started is not read as one
	// that could not finish.
	deviceErrorPrefix = "codex oauth device flow failed"

	// deviceRequestTimeout bounds every single request of the device
	// flow. Polling outlives it: the loop keeps asking until the caller
	// cancels or the grant resolves.
	deviceRequestTimeout = 25 * time.Second

	// defaultDevicePollIntervalSeconds is the polling cadence the
	// device flow falls back to when the endpoint does not name one.
	defaultDevicePollIntervalSeconds = 5

	// scope asks for the OpenID identity claims, offline access so a
	// refresh token exists at all, and the connector API grants the relay
	// exercises on the account's behalf.
	scope = "openid profile email offline_access api.connectors.read api.connectors.invoke"

	// originator stamps the client in the requests OpenAI telemetry
	// correlates the flow by.
	originator = "Codex Desktop"

	// exchangeErrorPrefix and refreshErrorPrefix open every error this
	// adapter reports for the matching token operation, so the login flow
	// can attribute a failure to the right step.
	exchangeErrorPrefix = "codex oauth exchange failed"
	refreshErrorPrefix  = "codex oauth refresh failed"

	// usageErrorPrefix opens every error the usage probe reports. It is
	// deliberately not a token-operation prefix: a failed probe must not
	// read as a failed login or refresh, and the login flow's error
	// matching never sees probe errors.
	usageErrorPrefix = "codex oauth usage probe failed"

	// maxResponseBodyBytes bounds how much of an endpoint answer is read
	// into memory. Real token responses are a few kilobytes; the bound
	// keeps a broken endpoint from ballooning the process, and an
	// oversized answer fails to decode rather than being trusted.
	maxResponseBodyBytes = 256 * 1024
)

// errTokenResponseUndecodable marks a 2xx answer whose body is not the
// JSON object the token endpoint is contracted to send. The fixed text
// deliberately echoes nothing from the body.
var errTokenResponseUndecodable = errors.New("token response could not be decoded")

// errDeviceUserCodeIncomplete marks a user code endpoint answer that is
// missing the device_auth_id or the user code itself. The fixed text
// deliberately echoes nothing from the body.
var errDeviceUserCodeIncomplete = errors.New("device user code response was incomplete")

// errDeviceGrantIncomplete marks a poll answer without all three of
// the authorization code, its verifier and its challenge: without the
// verifier the exchange cannot prove itself, so the grant is terminal
// rather than retried. The fixed text deliberately echoes nothing from
// the body.
var errDeviceGrantIncomplete = errors.New("device grant response was incomplete")

// errUsageResponseUndecodable marks a 2xx usage answer whose body is not
// the JSON object the usage endpoint is contracted to send. The fixed
// text deliberately echoes nothing from the body — the usage answer
// speaks about the account, and probe errors carry status text only.
var errUsageResponseUndecodable = errors.New("usage response could not be decoded")

// Endpoints addresses every leg of both login flows. Production takes
// productionEndpoints; tests point the legs at local servers. The legs
// travel together because they are registered together with OpenAI: a
// client whose token endpoint could be swapped could be swapped for
// one that echoes the tokens back out.
type Endpoints struct {
	AuthorizeURL              string
	TokenURL                  string
	AccountsCheckURL          string
	UsageURL                  string
	RedirectURI               string
	DeviceUserCodeURL         string
	DeviceTokenURL            string
	DeviceVerificationURL     string
	DeviceExchangeRedirectURI string
}

// productionEndpoints pins every address the preset talks to.
func productionEndpoints() Endpoints {
	return Endpoints{
		AuthorizeURL:              productionAuthorizeURL,
		TokenURL:                  productionTokenURL,
		AccountsCheckURL:          productionAccountsCheckURL,
		UsageURL:                  productionUsageURL,
		RedirectURI:               productionRedirectURI,
		DeviceUserCodeURL:         productionDeviceUserCodeURL,
		DeviceTokenURL:            productionDeviceTokenURL,
		DeviceVerificationURL:     productionDeviceVerificationURL,
		DeviceExchangeRedirectURI: productionDeviceExchangeRedirect,
	}
}

// Authorizer talks to OpenAI's OAuth endpoints. It implements the
// application layer's Authorizer port; the zero value is not usable, take
// one from NewAuthorizer.
type Authorizer struct {
	client     *http.Client
	appVersion string
	endpoints  Endpoints
}

// NewAuthorizer builds the production Authorizer: OpenAI's pinned
// endpoints and the loopback redirect address registered for this
// client. client may be nil, which selects a default client — request
// deadlines come from the contexts the application layer already bounds
// every call with. appVersion is the application's version string as the
// refresh request's User-Agent carries it.
func NewAuthorizer(client *http.Client, appVersion string) *Authorizer {
	return NewAuthorizerWithEndpoints(client, appVersion, productionEndpoints())
}

// NewAuthorizerWithEndpoints builds an Authorizer against explicit
// endpoints. It exists for tests, which point the token and account
// check requests at local servers; production takes NewAuthorizer.
func NewAuthorizerWithEndpoints(client *http.Client, appVersion string, endpoints Endpoints) *Authorizer {
	if client == nil {
		client = &http.Client{}
	}
	return &Authorizer{
		client:     client,
		appVersion: appVersion,
		endpoints:  endpoints,
	}
}

// AuthorizeURL builds the URL the system browser opens. The parameters
// are the flow's fixed identity: the client, the loopback redirect, the
// PKCE S256 challenge derived from the verifier the login minted, the
// connector scopes, and the stamps that select OpenAI's simplified CLI
// login page and add the user's organizations into the id_token. There is
// no login_id: account choice belongs to the authorize page, not to this
// client. The function is pure — no network, no state — so calling it
// twice with the same inputs yields the same URL.
func (a *Authorizer) AuthorizeURL(state, codeChallenge string) string {
	var query strings.Builder
	appendQueryParam(&query, "client_id", clientID)
	appendQueryParam(&query, "redirect_uri", a.endpoints.RedirectURI)
	appendQueryParam(&query, "response_type", "code")
	appendQueryParam(&query, "scope", scope)
	appendQueryParam(&query, "state", state)
	appendQueryParam(&query, "code_challenge", codeChallenge)
	appendQueryParam(&query, "code_challenge_method", "S256")
	appendQueryParam(&query, "id_token_add_organizations", "true")
	appendQueryParam(&query, "codex_cli_simplified_flow", "true")
	appendQueryParam(&query, "originator", originator)
	return a.endpoints.AuthorizeURL + "?" + query.String()
}

// ExchangeCode trades the authorization code the browser delivered to
// the loopback redirect for a session. The request is deliberately bare:
// exactly the five form fields the endpoint expects and the form content
// type — no User-Agent, no originator, none of the identity the refresh
// request carries.
//
// The exchange is strict about identity: an id_token that does not decode
// into an Identity fails the login, because a session without identity
// can be neither provisioned nor rendered. After the tokens decode, the
// account check asks the ChatGPT backend whether the account may still
// use them; only an explicit 401/403 fails the login — the account is
// gone — while any other check failure lets the login through, since the
// tokens themselves were already issued and the browser round-trip is
// already paid for. Both HTTP requests honor ctx.
func (a *Authorizer) ExchangeCode(ctx context.Context, code, codeVerifier string) (domain.Session, error) {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {a.endpoints.RedirectURI},
		"client_id":     {clientID},
		"code_verifier": {codeVerifier},
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, a.endpoints.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return domain.Session{}, fmt.Errorf("%s: %w", exchangeErrorPrefix, err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	payload, err := a.postToken(request, exchangeErrorPrefix)
	if err != nil {
		return domain.Session{}, err
	}
	return a.sessionFromExchange(ctx, payload)
}

// sessionFromExchange is the strict post-exchange pipeline both grant
// types share: the id_token must decode into an Identity — a session
// without identity can be neither provisioned nor rendered — the expiry
// comes off the same token, and the account check asks the ChatGPT
// backend whether the account may still use the fresh access token.
func (a *Authorizer) sessionFromExchange(ctx context.Context, payload tokenResponse) (domain.Session, error) {
	identity, err := domain.ParseIDToken(payload.IDToken)
	if err != nil {
		return domain.Session{}, fmt.Errorf("%s: %w", exchangeErrorPrefix, err)
	}
	expiry, err := domain.ParseTokenExpiry(payload.IDToken)
	if err != nil {
		// Unreachable after a successful ParseIDToken — both decode the
		// same segments — but a strict path surfaces rather than guesses.
		return domain.Session{}, fmt.Errorf("%s: %w", exchangeErrorPrefix, err)
	}

	session := domain.Session{
		AccessToken:  payload.AccessToken,
		RefreshToken: payload.RefreshToken,
		IDToken:      payload.IDToken,
		AccessExpiry: expiry,
		Identity:     identity,
	}

	if err := a.checkAccounts(ctx, session.AccessToken, identity.AccountID); err != nil {
		return domain.Session{}, err
	}
	return session, nil
}

// RequestDeviceUserCode starts the device flow: it asks for a code the
// user can approve on a page they open themselves. The request carries
// nothing but the client identity — the code arrives by the user typing
// it into OpenAI's page, not by any callback to this process, so this
// flow never touches the loopback listener. The answer is trimmed
// before it is trusted, the polling cadence the endpoint names is
// honored as seconds (a missing, malformed or non-positive one falls
// back to the default), and the verification URL is the pinned page
// rather than anything the body might carry. Each call is bounded by
// deviceRequestTimeout on top of ctx.
func (a *Authorizer) RequestDeviceUserCode(ctx context.Context) (application.DeviceUserCode, error) {
	body, err := json.Marshal(deviceUserCodeRequest{ClientID: clientID})
	if err != nil {
		// Marshaling one string field cannot fail; if it ever does, the
		// failure is local and carries no secret.
		return application.DeviceUserCode{}, fmt.Errorf("%s: %w", deviceErrorPrefix, err)
	}
	requestCtx, cancel := context.WithTimeout(ctx, deviceRequestTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, a.endpoints.DeviceUserCodeURL, bytes.NewReader(body))
	if err != nil {
		return application.DeviceUserCode{}, fmt.Errorf("%s: %w", deviceErrorPrefix, err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")

	response, err := a.client.Do(request)
	if err != nil {
		return application.DeviceUserCode{}, fmt.Errorf("%s: %w", deviceErrorPrefix, err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBodyBytes))
	if err != nil {
		return application.DeviceUserCode{}, fmt.Errorf("%s: %w", deviceErrorPrefix, err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return application.DeviceUserCode{}, tokenEndpointError(deviceErrorPrefix, response, raw)
	}
	var payload deviceUserCodeResponse
	if err := json.Unmarshal(raw, &payload); err != nil {
		return application.DeviceUserCode{}, fmt.Errorf("%s: %w", deviceErrorPrefix, errDeviceUserCodeIncomplete)
	}
	view := application.DeviceUserCode{
		DeviceAuthID:    strings.TrimSpace(payload.DeviceAuthID),
		UserCode:        strings.TrimSpace(payload.UserCode),
		VerificationURL: a.endpoints.DeviceVerificationURL,
		PollInterval:    devicePollIntervalSeconds(payload.Interval),
	}
	if view.UserCode == "" {
		view.UserCode = strings.TrimSpace(payload.UserCodeAlt)
	}
	if view.DeviceAuthID == "" || view.UserCode == "" {
		return application.DeviceUserCode{}, fmt.Errorf("%s: %w", deviceErrorPrefix, errDeviceUserCodeIncomplete)
	}
	return view, nil
}

// AwaitDeviceAuthorization polls the device token endpoint until the
// user approves (or refuses) the code. The endpoint's contract: 403/404
// means still waiting, any other non-2xx status is a verdict the flow
// cannot recover from, and a transport failure says nothing about the
// user's decision — it is waited out and retried. Polling happens
// before the first sleep, so an already-granted code returns without a
// wasted wait. A granted code is returned whole: the authorization
// code, its verifier and its challenge are all required, because the
// exchange cannot prove itself without the verifier. ctx bounds the
// wait; when it expires the error is context.DeadlineExceeded, which
// the application layer renders as the device login timing out, and
// cancellation surfaces as ctx.Err().
func (a *Authorizer) AwaitDeviceAuthorization(ctx context.Context, start application.DeviceUserCode) (application.DeviceAuthorization, error) {
	interval := start.PollInterval
	if interval < 1 {
		interval = defaultDevicePollIntervalSeconds
	}
	for {
		authorization, pending, err := a.pollDeviceToken(ctx, start)
		if !pending {
			return authorization, err
		}
		timer := time.NewTimer(time.Duration(interval) * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return application.DeviceAuthorization{}, ctx.Err()
		case <-timer.C:
		}
	}
}

// pollDeviceToken performs one poll of the device token endpoint. The
// boolean says whether the flow should keep waiting; an error alongside
// a true boolean is a transient transport failure the caller retries,
// never a verdict on the code.
func (a *Authorizer) pollDeviceToken(ctx context.Context, start application.DeviceUserCode) (application.DeviceAuthorization, bool, error) {
	body, err := json.Marshal(deviceTokenRequest{
		DeviceAuthID: start.DeviceAuthID,
		UserCode:     start.UserCode,
	})
	if err != nil {
		// Marshaling two string fields cannot fail; if it ever does, the
		// failure is local and carries no secret.
		return application.DeviceAuthorization{}, false, fmt.Errorf("%s: %w", exchangeErrorPrefix, err)
	}
	requestCtx, cancel := context.WithTimeout(ctx, deviceRequestTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, a.endpoints.DeviceTokenURL, bytes.NewReader(body))
	if err != nil {
		return application.DeviceAuthorization{}, false, fmt.Errorf("%s: %w", exchangeErrorPrefix, err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")

	response, err := a.client.Do(request)
	if err != nil {
		// The user has not refused anything: the wire is flaky or the
		// single request timed out. Keep waiting, bounded by ctx.
		return application.DeviceAuthorization{}, true, fmt.Errorf("%s: %w", exchangeErrorPrefix, err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBodyBytes))
	if err != nil {
		return application.DeviceAuthorization{}, true, fmt.Errorf("%s: %w", exchangeErrorPrefix, err)
	}
	if response.StatusCode == http.StatusForbidden || response.StatusCode == http.StatusNotFound {
		// The endpoint's "still waiting" statuses. The body was drained
		// above, so the connection is reused by the next poll.
		return application.DeviceAuthorization{}, true, nil
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return application.DeviceAuthorization{}, false, tokenEndpointError(exchangeErrorPrefix, response, raw)
	}
	var payload deviceGrantResponse
	if err := json.Unmarshal(raw, &payload); err != nil {
		return application.DeviceAuthorization{}, false, fmt.Errorf("%s: %w", exchangeErrorPrefix, errDeviceGrantIncomplete)
	}
	authorization := application.DeviceAuthorization{
		AuthorizationCode: strings.TrimSpace(payload.AuthorizationCode),
		CodeVerifier:      strings.TrimSpace(payload.CodeVerifier),
		CodeChallenge:     strings.TrimSpace(payload.CodeChallenge),
	}
	if authorization.AuthorizationCode == "" || authorization.CodeVerifier == "" || authorization.CodeChallenge == "" {
		return application.DeviceAuthorization{}, false, fmt.Errorf("%s: %w", exchangeErrorPrefix, errDeviceGrantIncomplete)
	}
	return authorization, false, nil
}

// ExchangeDeviceCode trades the granted device authorization for a
// session. The request is the same bare form the browser flow's
// exchange sends — the grant, the verifier, the client, and the
// redirect the code was issued against, which for the device flow is
// OpenAI's public callback, not the loopback port — against the same
// token endpoint, and it runs the same strict post-exchange pipeline:
// identity, expiry, account check. No User-Agent or originator, like
// the browser exchange.
func (a *Authorizer) ExchangeDeviceCode(ctx context.Context, authorization application.DeviceAuthorization) (domain.Session, error) {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {authorization.AuthorizationCode},
		"redirect_uri":  {a.endpoints.DeviceExchangeRedirectURI},
		"client_id":     {clientID},
		"code_verifier": {authorization.CodeVerifier},
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, a.endpoints.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return domain.Session{}, fmt.Errorf("%s: %w", exchangeErrorPrefix, err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	payload, err := a.postToken(request, exchangeErrorPrefix)
	if err != nil {
		return domain.Session{}, err
	}
	return a.sessionFromExchange(ctx, payload)
}

// Refresh renews an access token with the stored refresh token. The
// request is JSON with exactly the three fields the endpoint needs — the
// scope is deliberately absent: it was granted at authorize time, and
// re-asking it can come back downgraded. The refresh identifies itself
// as the desktop client with a full User-Agent and the originator stamp.
//
// The refresh is lenient where the exchange is strict, because the caller
// holds a session it can keep falling back on: a missing or malformed
// id_token yields a session with a zero Identity and no error, and so does
// one whose exp has already lapsed — an expired claim cannot speak for the
// new access token's lifetime, and the identity it carries is as stale as
// its date (in both cases the application layer's merge keeps the previous
// id_token, identity and expiry). A response without a rotated refresh
// token returns the new access token with an empty RefreshToken — the
// application layer's merge falls back to the stored refresh token. The
// session carries exactly the fields the response delivered; nothing is
// zeroed wholesale, because a fresh access token dropped here would leave
// the relay sending an empty Bearer with no signal to re-login. No account
// check runs on refresh — the account was checked at login, and a refresh
// must not sign the user out.
func (a *Authorizer) Refresh(ctx context.Context, refreshToken string) (domain.Session, error) {
	body, err := json.Marshal(refreshRequest{
		ClientID:     clientID,
		GrantType:    "refresh_token",
		RefreshToken: refreshToken,
	})
	if err != nil {
		// Marshaling three string fields cannot fail; if it ever does,
		// the failure is local and carries no token material.
		return domain.Session{}, fmt.Errorf("%s: %w", refreshErrorPrefix, err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, a.endpoints.TokenURL, bytes.NewReader(body))
	if err != nil {
		return domain.Session{}, fmt.Errorf("%s: %w", refreshErrorPrefix, err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", a.userAgent())
	request.Header.Set("originator", originator)

	payload, err := a.postToken(request, refreshErrorPrefix)
	if err != nil {
		return domain.Session{}, err
	}

	// The refresh token stays empty when the endpoint rotated nothing:
	// the application layer's merge falls back to the stored one.
	session := domain.Session{
		AccessToken:  payload.AccessToken,
		RefreshToken: payload.RefreshToken,
	}
	if payload.IDToken == "" {
		return session, nil
	}
	identity, idErr := domain.ParseIDToken(payload.IDToken)
	if idErr != nil {
		// Lenient by contract: the tokens stand, the identity does not.
		return session, nil
	}
	if expiry, expErr := domain.ParseTokenExpiry(payload.IDToken); expErr == nil && !expiry.IsZero() && !expiry.After(time.Now()) {
		// An id_token that is already lapsed is treated as not
		// delivered: its expiry must not stand in for the new access
		// token's unknown one, and neither must its identity overwrite
		// a live one. The merge keeps the previous id_token, identity
		// and expiry instead.
		return session, nil
	}
	session.IDToken = payload.IDToken
	session.Identity = identity
	if expiry, expErr := domain.ParseTokenExpiry(payload.IDToken); expErr == nil {
		session.AccessExpiry = expiry
	}
	return session, nil
}

// checkAccounts asks the ChatGPT backend whether the account behind the
// fresh access token may still use it. A 401 or 403 is the backend's
// explicit verdict that the account is gone or barred, and it fails the
// login; every other failure — unreachable network, 5xx, timeout — is
// non-fatal, because the tokens themselves were already issued and a
// struggling check endpoint must not cost the user a browser round-trip
// that already succeeded. The 2xx body is read (bounded) and discarded:
// draining lets the connection be reused and caps what a chatty endpoint
// can push. ctx bounds the request.
func (a *Authorizer) checkAccounts(ctx context.Context, accessToken, accountID string) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, a.endpoints.AccountsCheckURL, nil)
	if err != nil {
		// The check request could not even be built: a local failure, not
		// a check verdict, and it carries no secret.
		return fmt.Errorf("%s: %w", exchangeErrorPrefix, err)
	}
	request.Header.Set("Authorization", "Bearer "+accessToken)
	request.Header.Set("Accept", "application/json")
	request.Header.Set("account-id", accountID)

	response, err := a.client.Do(request)
	if err != nil {
		return nil
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		return fmt.Errorf("codex account check failed: http %d", response.StatusCode)
	}
	// Write errors on a discard mean the endpoint hung up mid-body;
	// nothing about the verdict changes.
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxResponseBodyBytes))
	return nil
}

// FetchUsage asks the ChatGPT backend's usage endpoint for the account's
// two quota windows and the plan's name. A 401 wraps
// application.ErrUsageUnauthorized so the caller can rotate the access
// token and retry once; every other failure — transport, timeout,
// non-2xx, a 2xx body that is not the contracted JSON — reports the
// status text only. The body is read bounded, as everywhere: a probe
// answer speaks about the account, never about the request, and none of
// it is echoed. An empty account-id is sent as an empty header — the
// account check does the same, and the endpoint's own 4xx becomes the
// reported error rather than a guess here.
func (a *Authorizer) FetchUsage(ctx context.Context, accessToken, accountID string) (domain.Usage, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, a.endpoints.UsageURL, nil)
	if err != nil {
		// The probe request could not even be built: a local failure
		// that carries no secret.
		return domain.Usage{}, fmt.Errorf("%s: %w", usageErrorPrefix, err)
	}
	request.Header.Set("Authorization", "Bearer "+accessToken)
	request.Header.Set("Accept", "application/json")
	request.Header.Set("account-id", accountID)

	response, err := a.client.Do(request)
	if err != nil {
		return domain.Usage{}, fmt.Errorf("%s: %w", usageErrorPrefix, err)
	}
	defer response.Body.Close()

	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBodyBytes))
	if err != nil {
		return domain.Usage{}, fmt.Errorf("%s: %w", usageErrorPrefix, err)
	}
	if response.StatusCode == http.StatusUnauthorized {
		// The backend's explicit verdict that the access token is
		// rejected: the caller rotates the token and probes once more.
		// 403 is not folded in — the account check treats it as a
		// login-ending verdict, but a probe has no login to fail, and
		// a barred account reports as an ordinary failed probe.
		return domain.Usage{}, fmt.Errorf("%s: http %d: %w", usageErrorPrefix, response.StatusCode, application.ErrUsageUnauthorized)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return domain.Usage{}, fmt.Errorf("%s: http %d", usageErrorPrefix, response.StatusCode)
	}

	var payload usageResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return domain.Usage{}, fmt.Errorf("%s: %w", usageErrorPrefix, errUsageResponseUndecodable)
	}
	now := time.Now()
	return domain.Usage{
		PlanType:  payload.PlanType,
		Primary:   domain.NormalizeQuotaWindow(payload.rateLimit().Primary.fields(), now),
		Secondary: domain.NormalizeQuotaWindow(payload.rateLimit().Secondary.fields(), now),
	}, nil
}

// postToken performs one token endpoint POST and decodes its 2xx body.
// Transport failures wrap with prefix; a non-2xx answer becomes an error
// carrying the endpoint's OAuth error code verbatim — domain.IsReauthError
// classifies exactly that text, so translating or summarizing it would
// break reauth detection; a 2xx body that is not the contracted JSON
// object fails with a fixed message that echoes nothing from it.
func (a *Authorizer) postToken(request *http.Request, prefix string) (tokenResponse, error) {
	response, err := a.client.Do(request)
	if err != nil {
		return tokenResponse{}, fmt.Errorf("%s: %w", prefix, err)
	}
	defer response.Body.Close()

	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBodyBytes))
	if err != nil {
		return tokenResponse{}, fmt.Errorf("%s: %w", prefix, err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return tokenResponse{}, tokenEndpointError(prefix, response, body)
	}
	var payload tokenResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return tokenResponse{}, fmt.Errorf("%s: %w", prefix, errTokenResponseUndecodable)
	}
	return payload, nil
}

// tokenEndpointError renders a non-2xx token endpoint answer. The
// machine-readable OAuth error code is echoed verbatim when the endpoint
// sent one, and the HTTP status text stands in when it did not. The body
// is never echoed: it can carry request material, and this text ends up
// in login status, logs and crash reports.
func tokenEndpointError(prefix string, response *http.Response, body []byte) error {
	if code := oauthErrorCode(body); code != "" {
		return fmt.Errorf("%s: http %d %s", prefix, response.StatusCode, code)
	}
	if text := httpStatusText(response); text != "" {
		return fmt.Errorf("%s: http %d %s", prefix, response.StatusCode, text)
	}
	return fmt.Errorf("%s: http %d", prefix, response.StatusCode)
}

// oauthErrorCode pulls the machine-readable error code out of a token
// endpoint failure body. The endpoint speaks three shapes — a flat
// {"error":"..."}, a nested {"error":{"code":"..."}} and a bare
// {"code":"..."} — and anything else (an HTML error page, an empty body)
// reads as "no code", deferring to the HTTP status text.
func oauthErrorCode(body []byte) string {
	var payload struct {
		Error json.RawMessage `json:"error"`
		Code  string          `json:"code"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return ""
	}
	var code string
	if err := json.Unmarshal(payload.Error, &code); err == nil && code != "" {
		return code
	}
	var nested struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(payload.Error, &nested); err == nil && nested.Code != "" {
		return nested.Code
	}
	return payload.Code
}

// httpStatusText renders the status text of a response: the server's own
// reason phrase when it sent one, the canonical Go phrase when it did
// not, and empty for a status nobody has a phrase for, so the message
// never carries a dangling space.
func httpStatusText(response *http.Response) string {
	if text := strings.TrimSpace(strings.TrimPrefix(response.Status, strconv.Itoa(response.StatusCode))); text != "" {
		return text
	}
	return http.StatusText(response.StatusCode)
}

// appendQueryParam appends one escaped key=value pair to a query builder,
// separating pairs with '&'. Pairs are written in the flow's fixed order
// rather than url.Values' sorted order, so the URL is byte-stable for a
// given input pair.
func appendQueryParam(query *strings.Builder, key, value string) {
	if query.Len() > 0 {
		query.WriteByte('&')
	}
	query.WriteString(url.QueryEscape(key))
	query.WriteByte('=')
	query.WriteString(url.QueryEscape(value))
}

// userAgent stamps the refresh request the way OpenAI telemetry expects
// the desktop client: name, version, then the platform pair.
func (a *Authorizer) userAgent() string {
	return fmt.Sprintf("%s/%s (%s; %s)", originator, a.appVersion, runtime.GOOS, runtime.GOARCH)
}

// refreshRequest is the JSON body of a refresh: exactly the fields the
// endpoint requires. The scope stays out on purpose.
type refreshRequest struct {
	ClientID     string `json:"client_id"`
	GrantType    string `json:"grant_type"`
	RefreshToken string `json:"refresh_token"`
}

// tokenResponse is the subset of the token endpoint's answer this preset
// uses. Fields the endpoint omits decode as empty strings, and each
// caller applies its own policy to them: the exchange is strict about
// the id_token, the refresh is lenient.
type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
}

// deviceUserCodeRequest is the JSON body that starts the device flow:
// the client identity alone.
type deviceUserCodeRequest struct {
	ClientID string `json:"client_id"`
}

// deviceUserCodeResponse is the user code endpoint's answer. The user
// code arrives under either of two keys the endpoint has been observed
// to use, and the interval arrives as a number or as a string.
type deviceUserCodeResponse struct {
	DeviceAuthID string          `json:"device_auth_id"`
	UserCode     string          `json:"user_code"`
	UserCodeAlt  string          `json:"usercode"`
	Interval     json.RawMessage `json:"interval"`
}

// deviceTokenRequest is the JSON body of one poll: the pair the endpoint
// needs to find the grant the user is deciding on.
type deviceTokenRequest struct {
	DeviceAuthID string `json:"device_auth_id"`
	UserCode     string `json:"user_code"`
}

// deviceGrantResponse is the poll endpoint's answer once the user
// approves: the code to exchange, plus the PKCE pair it was issued with.
// All three are required — the exchange cannot prove itself without the
// verifier.
type deviceGrantResponse struct {
	AuthorizationCode string `json:"authorization_code"`
	CodeVerifier      string `json:"code_verifier"`
	CodeChallenge     string `json:"code_challenge"`
}

// devicePollIntervalSeconds reads the polling cadence the endpoint
// named, as a positive whole number of seconds. A missing, malformed,
// non-positive or absurdly large value falls back to the default — the
// loop must sleep a real interval, never zero.
func devicePollIntervalSeconds(raw json.RawMessage) int {
	var number uint64
	if err := json.Unmarshal(raw, &number); err == nil {
		if number > 0 && number <= 1<<30 {
			return int(number)
		}
		return defaultDevicePollIntervalSeconds
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		if parsed, err := strconv.ParseUint(strings.TrimSpace(text), 10, 32); err == nil && parsed > 0 {
			return int(parsed)
		}
	}
	return defaultDevicePollIntervalSeconds
}

// usageResponse is the subset of the usage endpoint's answer this preset
// reads: the plan's name and the two rate-limit windows the quota card
// meters. The endpoint also speaks a code-review window; it is
// deliberately not decoded — the card draws two meters, and a third
// window would invent a scenario the preset does not run.
type usageResponse struct {
	PlanType  string          `json:"plan_type"`
	RateLimit *usageRateLimit `json:"rate_limit"`
}

// usageRateLimit holds the two windows the quota card meters: the
// primary window requests are counted against and the secondary window
// the plan spends alongside it. A missing rate_limit block leaves both
// windows absent, which renders empty meters rather than a failed probe
// — the plan name is still worth showing, and the endpoint did answer.
type usageRateLimit struct {
	Primary   *usageWindow `json:"primary_window"`
	Secondary *usageWindow `json:"secondary_window"`
}

// rateLimit returns the response's limits, or the zero pair when the
// endpoint sent none. The indirection keeps FetchUsage's result
// construction flat and single-branched.
func (payload *usageResponse) rateLimit() usageRateLimit {
	if payload.RateLimit == nil {
		return usageRateLimit{}
	}
	return *payload.RateLimit
}

// usageWindow is one quota window as the usage endpoint reports it.
// Every number is optional by pointer: a window may be present without
// its used share, its span or its reset, and the normalization decides
// what each omission means.
type usageWindow struct {
	UsedPercent        *int64 `json:"used_percent"`
	LimitWindowSeconds *int64 `json:"limit_window_seconds"`
	ResetAt            *int64 `json:"reset_at"`
	ResetAfterSeconds  *int64 `json:"reset_after_seconds"`
}

// fields projects the window onto the domain's normalization input. A
// nil window projects as unreported; a present one carries its numbers
// across as-is, absent numbers staying nil.
func (window *usageWindow) fields() domain.QuotaWindowFields {
	if window == nil {
		return domain.QuotaWindowFields{}
	}
	return domain.QuotaWindowFields{
		Reported:           true,
		UsedPercent:        window.UsedPercent,
		LimitWindowSeconds: window.LimitWindowSeconds,
		ResetAt:            window.ResetAt,
		ResetAfterSeconds:  window.ResetAfterSeconds,
	}
}
