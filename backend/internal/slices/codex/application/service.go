// Package application implements the Codex OAuth preset: browser sign-in
// with PKCE, session persistence, silent token refresh and provider
// auto-provisioning. The domain owns token semantics (expiry, reauth
// classification, identity parsing); this layer owns orchestration and the
// observable state the UI binds to.
package application

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/codex/domain"
	providerapp "github.com/luxuryprivate/switchboard/backend/internal/slices/providers/application"
)

// CodexProviderID is the id the codex preset is provisioned under. It is
// fixed here rather than minted by the provisioner: the relay routes by
// provider id, so routing must find the preset even while provisioning is
// being retried and Status.ProviderID is still empty.
const CodexProviderID = "codex"

// LoginPhase is where the interactive sign-in flow currently is.
type LoginPhase string

const (
	PhaseIdle       LoginPhase = "idle"
	PhaseWaiting    LoginPhase = "waiting"
	PhaseExchanging LoginPhase = "exchanging"
	PhaseSuccess    LoginPhase = "success"
	PhaseError      LoginPhase = "error"
)

// ConnState is whether the preset can serve requests right now.
type ConnState string

const (
	StateSignedOut    ConnState = "signed_out"
	StateSignedIn     ConnState = "signed_in"
	StateReauthNeeded ConnState = "reauth_needed"
)

// LoginStatus is the login flow as the sign-in dialog renders it.
type LoginStatus struct {
	Phase LoginPhase
	// Err is human-readable and safe for the UI: the domain declares
	// session tokens secrets, and this field only ever carries phase
	// explanations and adapter error text, never token material.
	Err string
	// DeviceUserCode and DeviceVerificationURL carry the device-code
	// flow's proof pair, and only while that flow is waiting or
	// exchanging: the user must type the code and open the page while the
	// poll runs, and neither field means anything outside that window or
	// for the browser flow, which keeps them empty.
	DeviceUserCode        string
	DeviceVerificationURL string
}

// Status is the connection as the provider list renders it: one aggregate
// state over every account, plus the per-account rows.
type Status struct {
	State ConnState
	// Accounts lists every stored account, sorted by AccountID. The
	// aggregate state is signed-in when at least one account is, and
	// reauth-needed when only broken accounts remain.
	Accounts []AccountStatus
}

// Snapshot is the payload OnChanged delivers: the whole observable state
// at once, so a listener never observes a login phase and a connection
// state that did not coexist.
type Snapshot struct {
	Login LoginStatus
	Conn  Status
}

// DeviceUserCode is what the device-authorization endpoint hands back
// before any user interaction: the pair the user types at the
// verification page plus the poll cadence the endpoint asked for.
type DeviceUserCode struct {
	DeviceAuthID    string
	UserCode        string
	VerificationURL string
	// PollInterval is the endpoint's requested seconds between polls;
	// the caller clamps it, it is never obeyed raw.
	PollInterval int
}

// DeviceAuthorization is the granted half of the device flow: the code
// trio the token endpoint needs to mint tokens without a redirect.
type DeviceAuthorization struct {
	AuthorizationCode string
	CodeVerifier      string
	CodeChallenge     string
}

// DeviceLoginView is the slice of DeviceUserCode the UI renders.
type DeviceLoginView struct {
	UserCode            string
	VerificationURL     string
	PollIntervalSeconds int
}

// ImportResult reports an import that landed: the live status plus the
// file the session came from, empty for pasted JSON.
type ImportResult struct {
	Status       Status
	ImportedFrom string
}

// CredentialKind classifies an imported credential by what it can prove.
type CredentialKind int

const (
	// CredentialFull carries an id token (identity) and an access token.
	CredentialFull CredentialKind = iota
	// CredentialRefresh carries only a refresh token.
	CredentialRefresh
	// CredentialAccess carries only a JWT access token.
	CredentialAccess
)

// CredentialCandidate is one importable credential extracted from pasted
// text or an auth file. Classification is shape-driven, mirroring the
// upstream codex tooling: what a value carries decides how it is
// imported, not where it was found.
type CredentialCandidate struct {
	Kind          CredentialKind
	IDToken       string
	AccessToken   string
	RefreshToken  string
	AccountIDHint string
}

// CredentialParser extracts import candidates from pasted text.
type CredentialParser interface {
	// ParseCredentials returns zero or more candidates; zero means the
	// text held nothing importable. Errors mean the text was shaped like
	// credentials but could not be honored, never that it was empty.
	ParseCredentials(text string) ([]CredentialCandidate, error)
}

// AuthFileReader reads one auth file for import.
type AuthFileReader interface {
	// ReadAuthFile returns the file's text. It must fail with
	// ErrAuthFileTooLarge when the file exceeds the read cap and must
	// never echo the file's path or contents in its errors.
	ReadAuthFile(path string) (string, error)
}

// ErrAuthFileTooLarge is the reader's refusal for oversized files. It
// lives here so the service can recognize it without importing the
// adapter; the UI wording is the service's, and names no path.
var ErrAuthFileTooLarge = errors.New("codex auth file is too large")

// Authorizer talks to OpenAI's OAuth endpoints: it builds the authorize
// URL the system browser opens and performs the token requests.
type Authorizer interface {
	// AuthorizeURL builds the browser URL carrying the state and the PKCE
	// S256 code challenge. It never touches the network.
	AuthorizeURL(state, codeChallenge string) string
	// ExchangeCode trades the loopback redirect's code plus the PKCE
	// verifier for a session.
	ExchangeCode(ctx context.Context, code, codeVerifier string) (domain.Session, error)
	// RequestDeviceUserCode starts a device-authorization flow: it asks
	// the endpoint for the pair the user types at the verification page.
	RequestDeviceUserCode(ctx context.Context) (DeviceUserCode, error)
	// AwaitDeviceAuthorization polls until the user approves the request
	// or the flow's own deadline ends. It must return promptly on
	// cancellation; an expired deadline surfaces as context.DeadlineExceeded.
	AwaitDeviceAuthorization(ctx context.Context, start DeviceUserCode) (DeviceAuthorization, error)
	// ExchangeDeviceCode trades the granted device authorization for a
	// session through the device endpoint's own redirect form.
	ExchangeDeviceCode(ctx context.Context, authorization DeviceAuthorization) (domain.Session, error)
	// Refresh renews an access token. The token endpoint may omit the
	// refresh token and id token on a response that only rotates the
	// access token; the caller keeps the previous values.
	Refresh(ctx context.Context, refreshToken string) (domain.Session, error)
	// FetchUsage asks the ChatGPT backend's usage endpoint for the
	// account's quota windows. accessToken is the bearer token;
	// accountID scopes the answer to the account that owns it. A 401
	// surfaces as ErrUsageUnauthorized so the caller can rotate the
	// token and retry; every other failure is an ordinary error whose
	// text names the status, never the body.
	FetchUsage(ctx context.Context, accessToken, accountID string) (domain.Usage, error)
}

// RedirectServer owns the loopback listener (port 1455) the browser
// redirects back to.
type RedirectServer interface {
	// Start binds the listener expecting exactly this state parameter.
	Start(expectedState string) error
	// AwaitCode blocks until the browser redirects back or ctx ends; a
	// cancelled flow must return promptly.
	AwaitCode(ctx context.Context) (code string, err error)
	// Stop releases the listener so the next login can rebind it; it must
	// be idempotent because both cancel and completion stop it.
	Stop()
}

// ProviderProvisioner owns the "codex" provider entry so the relay can
// route to it. There is exactly one entry for the whole fleet of accounts;
// the relay's token source decides which account answers each request.
type ProviderProvisioner interface {
	// EnsureCodexProvider makes the fleet entry exist and returns its id.
	// It is idempotent and tolerant of per-session failure: a caller whose
	// ensure failed leaves the session without a provider id and the next
	// login or restore heals it.
	EnsureCodexProvider(ctx context.Context) (providerID string, err error)
	// RetireCodexProvider parks the fleet entry while its last live
	// account is signed out: the preset marker survives so a returning
	// login relinks the same row, and the entry is disabled so nothing
	// routes traffic to credentials the accounts slice no longer holds.
	RetireCodexProvider(ctx context.Context) error
	// RemoveCodexProvider deletes the preset entries the codex slice
	// leaves behind — the fleet row plus any legacy per-account rows — the
	// full teardown performed when nothing live is left to serve. It must
	// refuse an entry that is builtin or still holds keys or routes, and
	// must never touch an entry the preset did not provision.
	RemoveCodexProvider(ctx context.Context) error
}

// Timeouts and cadences are OAuth protocol invariants, not user settings:
// they bound the browser round-trip and the token endpoint's own latency,
// and a UI misconfiguration must not be able to wedge the flow forever.
// The durations tests must shorten (login timeout, refresh cadence,
// backoffs) are overridable struct fields initialized from these
// defaults; the rest stay constants.
const (
	// defaultLoginTimeout bounds the whole interactive login: browser,
	// redirect, exchange, save and provisioning. The OpenAI authorize page
	// legitimately sits for minutes while the user picks an account or
	// types a password, so this is minutes, not seconds.
	defaultLoginTimeout = 5 * time.Minute
	// exchangeTimeout bounds the post-browser steps — token exchange, the
	// DPAPI save and the provider provisioning. These are machine-speed
	// calls on a fresh connection; 30s is already generous.
	exchangeTimeout = 30 * time.Second
	// refreshTimeout bounds a single token refresh: an access token worth
	// having is served well inside 30s, and the relay request waiting on
	// it needs the failure, not an unbounded hang.
	refreshTimeout = 30 * time.Second
	// refreshSkew is how early a refresh is attempted before the access
	// token actually expires, covering request latency on the way up.
	refreshSkew = 5 * time.Minute
	// refreshRetries caps the ticker's attempts within one cycle. The
	// acquire path deliberately refreshes once only — see AcquireAccessToken.
	refreshRetries = 3
	// defaultRefreshInterval is how often the ticker re-examines the
	// session; the expiry plus skew decide whether anything happens.
	defaultRefreshInterval = time.Minute
	// defaultDeviceLoginTimeout bounds the whole device-code flow: the
	// endpoint's own grant window is 15 minutes, so waiting longer only
	// keeps a poll running against a request the endpoint has already
	// forgotten.
	defaultDeviceLoginTimeout = 15 * time.Minute
	// defaultQuotaTimeout bounds one usage probe. The card is rendered
	// from the answer, so a hanging endpoint must not hold the caller
	// hostage for as long as its socket lives.
	defaultQuotaTimeout = 20 * time.Second
)

// The device poll's clamp bounds: the endpoint dictates the interval but
// a broken or hostile value must not become an unbounded sleep or a busy
// loop. The endpoint's default is 5s.
const (
	deviceLoginMinIntervalSeconds = 1
	deviceLoginMaxIntervalSeconds = 60
)

// defaultRefreshBackoffs separate the ticker's retry attempts: 1s absorbs
// a blip, 5s gives a struggling token endpoint real room without letting
// the ticker become a busy loop. Tests zero it to run instantly.
var defaultRefreshBackoffs = []time.Duration{time.Second, 5 * time.Second}

var (
	// errNotSignedIn is the acquire path's refusal when there is no
	// session to serve from.
	errNotSignedIn = errors.New("codex is not signed in")
	// errNeedsSignIn answers an unrefreshable session: there is a session,
	// but only a fresh login can renew it.
	errNeedsSignIn = errors.New("codex session needs sign-in")
	// errLoginAlreadyCancelled answers a LoginStart that landed inside the
	// cancellation sliver: the waiting flow it saw is already being torn
	// down, and handing back its URL would open a login that is dead.
	errLoginAlreadyCancelled = errors.New("codex login was already cancelled")
	// errLoginAlreadyInProgress answers a start or import that landed
	// while another sign-in flow is already running: exactly one
	// interactive sign-in runs at a time, and two flows racing for the
	// login fields would hand the UI two truths.
	errLoginAlreadyInProgress = errors.New("codex login is already in progress")
	// ErrUsageUnauthorized marks a usage probe the backend answered with
	// 401: the access token was rejected, and the answer is a rotation
	// plus one retry, not a new failure report. Public because the OAuth
	// adapter wraps it with %w.
	ErrUsageUnauthorized = errors.New("codex usage probe was unauthorized")
)

// loginFlowKind names which sign-in flow owns the current phase. The
// browser and device flows share the phase machine but not its
// bookkeeping: the browser flow has an authorize URL and a loopback
// listener to stop; the device flow has a user code and neither of those.
type loginFlowKind int

const (
	loginFlowNone loginFlowKind = iota
	loginFlowBrowser
	loginFlowDevice
)

// Service orchestrates the Codex preset: browser login, persistence,
// refresh and provider provisioning, for any number of signed-in
// accounts at once.
//
// mu guards every field it can reach — the login phase and its flow data,
// the accounts map and each entry's fields, the listener slice and the
// loop-started flag. Adapter calls (store, authorizer, redirects,
// provisioner), the login context's cancel func, and listener delivery
// never happen under mu: a slow adapter must not block status reads, and
// a listener may query the service right back.
//
// accounts is keyed by the account's identity AccountID. Entries are only
// inserted (a login or import that landed, a restore commit) or deleted
// (a logout); a same-account re-login or a token refresh mutates the
// entry's fields in place under mu. That stability is what makes the
// per-account gates sound: a gate acquired through an *account pointer
// stays meaningful even if the map itself is being edited, and every
// waiter re-looks the entry up by account id after acquiring to notice a
// logout that deleted it.
//
// Each account owns its own refreshGate and quotaGate as value fields on
// the entry. The old single-session serialization contract — one refresh
// token in flight at a time, because that is what the token endpoint
// expects — now holds per account instead of globally: two accounts may
// refresh concurrently, one account never refreshes twice at once. The
// lock order is unchanged: quotaGate is always the outer lock, then
// refreshGate, then mu only inside it, never the other way. Unlike
// mutexes, waiting on a gate is cancellable — an acquire whose caller
// gave up leaves the gate to the winner instead of blocking a goroutine
// nobody is waiting on — and the ticker's backoff sleeps run outside it,
// so a retry cycle does not hold a caller hostage between attempts.
//
// The quota snapshot is deliberately not part of Snapshot: it is
// transient on-demand state for the quota card, not login or connection
// state the rest of the UI reacts to.
//
// The login phase and flow data stay ONE global flow: exactly one
// interactive sign-in runs at a time, and whichever account completes it
// is committed by its own AccountID.
type Service struct {
	store       AccountStore
	authorizer  Authorizer
	redirects   RedirectServer
	provisioner ProviderProvisioner
	parser      CredentialParser
	files       AuthFileReader
	now         func() time.Time

	mu sync.Mutex

	// Shared for ordinary per-account mutations; exclusive only for fleet-wide
	// load/clear. Status never takes this I/O fence and unrelated saves run in
	// parallel. Gates remain stable across removal and a later sign-in.
	persistenceMu   sync.RWMutex
	diskGates       map[string]*accountLifecycle
	fleetGeneration uint64
	clearingAll     uint64

	phase    LoginPhase
	loginErr string

	authorizeURL    string
	verifier        string
	loginCancel     context.CancelFunc
	cancelRequested bool
	loginGen        uint64
	loginFlow       loginFlowKind

	deviceUserCode        string
	deviceVerificationURL string

	accounts map[string]*account

	listeners      []func(Snapshot)
	refreshRunning bool

	// loginTimeout, deviceLoginTimeout, refreshInterval and
	// refreshBackoffs are the production cadences as overridable fields:
	// tests shorten them to milliseconds, production takes the package
	// defaults.
	loginTimeout       time.Duration
	deviceLoginTimeout time.Duration
	refreshInterval    time.Duration
	refreshBackoffs    []time.Duration
	quotaTimeout       time.Duration
}

// NewService builds the service. now may be nil, which means time.Now.
func NewService(store AccountStore, authorizer Authorizer, redirects RedirectServer, provisioner ProviderProvisioner, parser CredentialParser, files AuthFileReader, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{
		store:              store,
		authorizer:         authorizer,
		redirects:          redirects,
		provisioner:        provisioner,
		parser:             parser,
		files:              files,
		now:                now,
		accounts:           map[string]*account{},
		diskGates:          map[string]*accountLifecycle{},
		loginTimeout:       defaultLoginTimeout,
		deviceLoginTimeout: defaultDeviceLoginTimeout,
		refreshInterval:    defaultRefreshInterval,
		refreshBackoffs:    defaultRefreshBackoffs,
		quotaTimeout:       defaultQuotaTimeout,
	}
}

// LoginStatus reports the interactive login flow for the sign-in dialog.
func (service *Service) LoginStatus() LoginStatus {
	service.mu.Lock()
	defer service.mu.Unlock()
	status := LoginStatus{Phase: service.phase, Err: service.loginErr}
	if service.loginFlow == loginFlowDevice && (service.phase == PhaseWaiting || service.phase == PhaseExchanging) {
		status.DeviceUserCode = service.deviceUserCode
		status.DeviceVerificationURL = service.deviceVerificationURL
	}
	return status
}

// Status reports the connection for the provider list. The aggregate
// state is signed-in whenever at least one account is; a map with only
// broken accounts reads reauth-needed; an empty map reads signed-out.
// Accounts are sorted by AccountID so the list is stable between reads.
func (service *Service) Status() Status {
	service.mu.Lock()
	defer service.mu.Unlock()
	return service.statusLocked()
}

// OnChanged subscribes to observable state transitions. Listeners are
// invoked without any lock held — they are UI wiring that may query
// Status and LoginStatus right back — and are never removed.
func (service *Service) OnChanged(listener func(Snapshot)) {
	if listener == nil {
		return
	}
	service.mu.Lock()
	service.listeners = append(service.listeners, listener)
	service.mu.Unlock()
}

// LoginStart begins the OAuth flow: it starts the loopback redirect
// server, builds the authorize URL for the system browser and moves the
// phase to waiting. Calling it again while a browser login is already
// waiting (or exchanging) returns the same URL: the user re-opening the
// sign-in dialog must not spawn a second flow — unless that flow is
// already being cancelled, in which case the call refuses rather than
// handing back a URL whose login is dead. While a device-code flow holds
// the window it refuses with errLoginAlreadyInProgress instead: the two
// flows have nothing in common to re-open. Starting over from success or
// error is allowed — a fresh login legitimately replaces an old session.
// The refusal is errLoginAlreadyCancelled; the settled flow returns to
// idle and the next call starts a fresh flow.
func (service *Service) LoginStart() (string, error) {
	service.mu.Lock()
	if service.phase == PhaseWaiting || service.phase == PhaseExchanging {
		if service.cancelRequested {
			// The waiting flow is being torn down right now: its URL is
			// about to be dead, so say so instead of opening it.
			service.mu.Unlock()
			return "", errLoginAlreadyCancelled
		}
		if service.loginFlow == loginFlowDevice {
			// A device-code flow owns the sign-in window and has no
			// authorize URL to hand back; a browser start here would
			// spawn a second concurrent flow.
			service.mu.Unlock()
			return "", errLoginAlreadyInProgress
		}
		authorizeURL := service.authorizeURL
		service.mu.Unlock()
		return authorizeURL, nil
	}
	state, stateErr := randomLoginState()
	if stateErr != nil {
		service.mu.Unlock()
		return "", stateErr
	}
	verifier, verifierErr := randomCodeVerifier()
	if verifierErr != nil {
		service.mu.Unlock()
		return "", verifierErr
	}
	loginTimeout := service.loginTimeout
	service.mu.Unlock()

	// Adapter calls happen outside mu, so a slow redirect server cannot
	// block status reads. On a Start failure nothing observable happened:
	// the phase stays idle and no notification fires. The error is
	// returned as-is by contract — the port conflict text belongs to the
	// listener adapter that produced it.
	if err := service.redirects.Start(state); err != nil {
		return "", err
	}
	challenge := codeChallenge(verifier)
	authorizeURL := service.authorizer.AuthorizeURL(state, challenge)

	loginCtx, cancel := context.WithTimeout(context.Background(), loginTimeout)
	service.mu.Lock()
	before := service.snapshotLocked()
	service.loginGen++
	service.loginCancel = cancel
	service.cancelRequested = false
	service.authorizeURL = authorizeURL
	service.verifier = verifier
	service.loginErr = ""
	service.loginFlow = loginFlowBrowser
	service.deviceUserCode = ""
	service.deviceVerificationURL = ""
	service.phase = PhaseWaiting
	generation := service.loginGen
	snapshot, listeners, changed := service.diffLocked(before)
	service.mu.Unlock()
	if changed {
		service.fire(listeners, snapshot)
	}

	go service.runLogin(loginCtx, cancel, verifier, generation)

	// A cancellation can land in the sliver between the state commit and
	// this return: the URL handed back would open a login whose flow is
	// already dying. loginHandbackStale is the witness that cannot be
	// fooled here — the goroutine's own deferred cancel kills the
	// context on every exit path, a successful login included, so a
	// settled flow must still hand back its URL.
	if service.loginHandbackStale(generation) {
		return "", errLoginAlreadyCancelled
	}
	return authorizeURL, nil
}

// LoginCancel aborts an in-flight login. It never errors: cancelling an
// already-idle login is a no-op. The phase settles when the login
// goroutine observes the cancellation — it lands on idle, not error,
// because the user chose to stop; nothing failed.
func (service *Service) LoginCancel() {
	service.cancelActiveLogin()
}

// DeviceLoginStart begins the device-code flow: it asks the endpoint for
// the pair the user types at the verification page, moves the phase to
// waiting and starts the grant poll in the background. Unlike LoginStart
// it is not idempotent — there is no URL to re-open, so a second request
// during an active flow is a refusal from either side: the browser flow
// holding the window, or this flow holding it. The user code is requested
// before the lock is taken: a refusal costs one wasted round trip, never
// a wrong state.
func (service *Service) DeviceLoginStart() (DeviceLoginView, error) {
	startCtx, cancelStart := context.WithTimeout(context.Background(), exchangeTimeout)
	start, startErr := service.authorizer.RequestDeviceUserCode(startCtx)
	cancelStart()
	if startErr != nil {
		return DeviceLoginView{}, startErr
	}

	loginCtx, cancel := context.WithTimeout(context.Background(), service.deviceLoginTimeout)
	service.mu.Lock()
	if service.phase == PhaseWaiting || service.phase == PhaseExchanging {
		stalled := service.cancelRequested
		service.mu.Unlock()
		// The new context is dead on arrival either way; releasing its
		// timer here keeps the refused flow from holding a live one.
		cancel()
		if stalled {
			return DeviceLoginView{}, errLoginAlreadyCancelled
		}
		return DeviceLoginView{}, errLoginAlreadyInProgress
	}
	before := service.snapshotLocked()
	service.loginGen++
	service.loginCancel = cancel
	service.cancelRequested = false
	service.authorizeURL = ""
	service.verifier = ""
	service.loginErr = ""
	service.loginFlow = loginFlowDevice
	service.deviceUserCode = start.UserCode
	service.deviceVerificationURL = start.VerificationURL
	service.phase = PhaseWaiting
	generation := service.loginGen
	snapshot, listeners, changed := service.diffLocked(before)
	service.mu.Unlock()
	if changed {
		service.fire(listeners, snapshot)
	}

	go service.runDeviceLogin(loginCtx, cancel, start)

	// The same sliver LoginStart closes: a cancellation between the
	// commit and this return means the flow handed back is already
	// dying. loginHandbackStale reads the flow, not the context — the
	// goroutine's own deferred cancel kills the context on every exit
	// path, a completed device flow included, so the context alone can
	// never say who cancelled it.
	if service.loginHandbackStale(generation) {
		return DeviceLoginView{}, errLoginAlreadyCancelled
	}
	return DeviceLoginView{
		UserCode:            start.UserCode,
		VerificationURL:     start.VerificationURL,
		PollIntervalSeconds: clampPollInterval(start.PollInterval),
	}, nil
}

// clampPollInterval bounds the cadence the endpoint dictated: it is a
// suggestion to a poller, not a sleep the endpoint gets to size, and
// clamping happens only in the value handed to the UI — the adapter
// still honors its own request.
func clampPollInterval(seconds int) int {
	if seconds < deviceLoginMinIntervalSeconds {
		return deviceLoginMinIntervalSeconds
	}
	if seconds > deviceLoginMaxIntervalSeconds {
		return deviceLoginMaxIntervalSeconds
	}
	return seconds
}

// runDeviceLogin is the device flow's goroutine. It is bounded by
// loginCtx and settles the phase before returning. There is no loopback
// listener to stop — the device flow never starts one — so the context
// is the whole stale guard: every step takes it, and any superseding or
// cancelling path cancels it.
func (service *Service) runDeviceLogin(loginCtx context.Context, cancel context.CancelFunc, start DeviceUserCode) {
	// Releases the timeout timer promptly; LoginCancel also holds this
	// cancel func, but the goroutine owns the normal (non-cancel) path.
	defer cancel()

	authorization, err := service.authorizer.AwaitDeviceAuthorization(loginCtx, start)
	if service.loginCancelled(loginCtx) {
		// The user (or a logout) stopped the flow: quiet idle, not error.
		service.landLogin(PhaseIdle, "")
		return
	}
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || loginCtx.Err() == context.DeadlineExceeded {
			service.landLogin(PhaseError, "codex device login timed out")
			return
		}
		// A terminal grant refusal is the endpoint's to explain: its
		// text passes through as-is.
		service.landLogin(PhaseError, err.Error())
		return
	}

	// The exchanging landing keeps the flow fields — the poll is settled
	// but the grant is still being traded for tokens.
	service.landLogin(PhaseExchanging, "")

	exchangeCtx, cancelExchange := context.WithTimeout(loginCtx, exchangeTimeout)
	session, exchangeErr := service.authorizer.ExchangeDeviceCode(exchangeCtx, authorization)
	cancelExchange()

	service.settleExchange(loginCtx, LoginMethodDevice, session, exchangeErr)
}

// ImportJSON signs accounts in from credential text the user pasted:
// an auth.json document, a sub2api-style export, or a bare token. The
// parser decides what the text holds; this method settles every
// candidate it yields. Multi-account text imports every account — a
// multi-account export is the common case — and a candidate that fails
// to produce a session never stops the rest: the error is reported per
// candidate, and the import succeeds whenever at least one landed.
func (service *Service) ImportJSON(ctx context.Context, text string) (ImportResult, error) {
	if strings.TrimSpace(text) == "" {
		return ImportResult{}, errors.New("no codex credentials found")
	}
	if err := service.importGuard(); err != nil {
		return ImportResult{}, err
	}
	versions := service.captureImportVersions()
	candidates, err := service.parser.ParseCredentials(text)
	if err != nil {
		return ImportResult{}, err
	}
	if len(candidates) == 0 {
		return ImportResult{}, errors.New("no codex credentials found")
	}
	if len(candidates) == 1 {
		session, err := service.importCandidate(ctx, candidates[0])
		if err != nil {
			return ImportResult{}, err
		}
		// Pasted text has no file to name: ImportedFrom stays empty and
		// the UI renders the account, not a source.
		result, _, err := service.commitImport(ctx, session, "", versions.forAccount(session.Identity.AccountID))
		return result, err
	}
	failures := make([]string, 0, len(candidates))
	landed := 0
	for _, candidate := range candidates {
		session, err := service.importCandidate(ctx, candidate)
		if err != nil {
			failures = append(failures, err.Error())
			continue
		}
		if _, ok, err := service.commitImport(ctx, session, "", versions.forAccount(session.Identity.AccountID)); err != nil && !ok {
			failures = append(failures, err.Error())
			continue
		}
		landed++
	}
	if landed == 0 {
		return ImportResult{}, fmt.Errorf("codex import failed: none of the %d credentials produced a codex session: %s",
			len(candidates), strings.Join(failures, "\n"))
	}
	service.mu.Lock()
	status := service.statusLocked()
	service.mu.Unlock()
	return ImportResult{Status: status}, nil
}

// ImportFiles signs accounts in from auth files the desktop picker
// returned. Files are read in order and every candidate they hold is
// imported — a picked folder of per-account auth files is the
// multi-account case. Failure detail names only base file names — full
// picker paths leak directory structure the error has no business
// carrying — and a file with no credentials is skipped, not fatal: the
// picker lets users grab a folder where most files are unrelated. Only
// when every file came up empty does the import fail, reporting each
// file's reason. A single candidate overall keeps its own result and
// provisioning error verbatim; with more, the status is whatever
// landed.
func (service *Service) ImportFiles(ctx context.Context, paths []string) (ImportResult, error) {
	if len(paths) == 0 {
		return ImportResult{}, errors.New("no codex credentials found")
	}
	if err := service.importGuard(); err != nil {
		return ImportResult{}, err
	}
	versions := service.captureImportVersions()
	failures := make([]string, 0, len(paths))
	landedFiles := 0
	importedFrom := ""
	totalCandidates := 0
	var singleResult ImportResult
	var singleErr error
	for _, path := range paths {
		name := filepath.Base(path)
		content, err := service.files.ReadAuthFile(path)
		if err != nil {
			if errors.Is(err, ErrAuthFileTooLarge) {
				failures = append(failures, fmt.Sprintf("auth file %s is too large", name))
			} else {
				failures = append(failures, fmt.Sprintf("auth file %s could not be read", name))
			}
			continue
		}
		candidates, err := service.parser.ParseCredentials(content)
		if err != nil || len(candidates) == 0 {
			failures = append(failures, fmt.Sprintf("auth file %s had no codex credentials", name))
			continue
		}
		fileLanded := false
		for _, candidate := range candidates {
			totalCandidates++
			session, err := service.importCandidate(ctx, candidate)
			if err != nil {
				failures = append(failures, fmt.Sprintf("auth file %s: %v", name, err))
				continue
			}
			result, landed, err := service.commitImport(ctx, session, name, versions.forAccount(session.Identity.AccountID))
			if err != nil && !landed {
				failures = append(failures, fmt.Sprintf("auth file %s: %v", name, err))
				continue
			}
			// A stored session whose provider entry could not be
			// provisioned still counts as landed: the tokens are durable
			// and the entry self-heals on the next status or restore, so
			// the file is not reported as a failure.
			fileLanded = true
			if totalCandidates == 1 {
				singleResult, singleErr = result, err
			}
		}
		if fileLanded {
			landedFiles++
			if importedFrom == "" {
				importedFrom = name
			}
		}
	}
	if landedFiles == 0 {
		return ImportResult{}, fmt.Errorf("codex import failed: none of the %d files produced a codex session: %s",
			len(paths), strings.Join(failures, "\n"))
	}
	if totalCandidates == 1 {
		return singleResult, singleErr
	}
	service.mu.Lock()
	status := service.statusLocked()
	service.mu.Unlock()
	return ImportResult{Status: status, ImportedFrom: importedFrom}, nil
}

// importGuard refuses an import while a sign-in flow is in flight: the
// import commits the session and settles the login fields itself, so a
// concurrent flow would land on state this import is about to replace.
// The frontend already refuses to start one; this is defense in depth
// for hand-written callers.
func (service *Service) importGuard() error {
	service.mu.Lock()
	defer service.mu.Unlock()
	if service.phase == PhaseWaiting || service.phase == PhaseExchanging {
		if service.cancelRequested {
			return errLoginAlreadyCancelled
		}
		return errLoginAlreadyInProgress
	}
	return nil
}

// importCandidate turns one parsed credential into a session. What the
// candidate carries decides the path, mirroring the upstream codex
// tooling. A refresh-capable pair whose access token has not expired
// imports offline, exactly like a refresh-less pair: the access token
// is valid proof until its own expiry, and the refresh token is kept
// with the session so the runtime's refresh-on-expiry loop renews it
// later — so no import needs the network when the credentials already
// answer for themselves, and a route that is down or a refresh token
// another machine already rotated cannot refuse a still-valid import.
// A pair that cannot serve — missing, expired, or without a readable
// expiry, the same rule NeedsRefresh applies to stored sessions — pays
// for the live exchange, and that failure is the import's failure,
// because importing a token that cannot be exchanged now would sign
// the user into nothing. A bare access token is stored exactly as
// handed over, network-free.
func (service *Service) importCandidate(ctx context.Context, candidate CredentialCandidate) (domain.Session, error) {
	if candidate.RefreshToken != "" {
		if session, err := importFullPair(candidate); err == nil && !session.NeedsRefresh(service.now(), refreshSkew) {
			session.RefreshToken = candidate.RefreshToken
			return session, nil
		}
		return service.importViaRefresh(ctx, candidate)
	}
	if candidate.IDToken != "" {
		return importFullPair(candidate)
	}
	return importAccessToken(candidate)
}

// importViaRefresh validates a refresh-capable candidate the only way
// that can be validated without a browser: it exchanges the refresh
// token for fresh tokens immediately. The exchange's session is the
// base; fields the endpoint did not re-issue keep the candidate's, and
// identity is read from wherever it survives best — the fresh tokens
// first, then the candidate's id token, then the access token's claims.
func (service *Service) importViaRefresh(ctx context.Context, candidate CredentialCandidate) (domain.Session, error) {
	refreshCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), refreshTimeout)
	refreshed, err := service.authorizer.Refresh(refreshCtx, candidate.RefreshToken)
	cancel()
	if err != nil {
		if domain.IsReauthError(err.Error()) {
			return domain.Session{}, fmt.Errorf("codex refresh token was rejected: %v", err)
		}
		return domain.Session{}, err
	}

	session := refreshed
	if session.RefreshToken == "" {
		// The endpoint may omit the refresh token on a response that
		// only rotates the access token.
		session.RefreshToken = candidate.RefreshToken
	}
	if session.IDToken == "" {
		session.IDToken = candidate.IDToken
	}

	identity := session.Identity
	if identity.Email == "" {
		if parsed, parseErr := domain.ParseIDToken(session.IDToken); parseErr == nil {
			identity = parsed
		}
	}
	if identity.Email == "" {
		accessToken := session.AccessToken
		if accessToken == "" {
			accessToken = candidate.AccessToken
		}
		if fromAccess, ok := domain.IdentityFromAccessToken(accessToken); ok {
			identity = fromAccess
		}
	}
	if identity.Email == "" {
		return domain.Session{}, errors.New("codex import could not identify the account")
	}
	if identity.AccountID == "" {
		identity.AccountID = candidate.AccountIDHint
	}
	session.Identity = identity
	return session, nil
}

// importFullPair stores a candidate carrying both an id token and an
// access token but no refresh token. Identity comes from the id token's
// claims and nothing is exchanged: the tokens are exactly what the user
// handed over, and they stand until the access token expires.
func importFullPair(candidate CredentialCandidate) (domain.Session, error) {
	identity, err := domain.ParseIDToken(candidate.IDToken)
	if err != nil {
		return domain.Session{}, errors.New("codex import could not identify the account")
	}
	if identity.AccountID == "" {
		identity.AccountID = candidate.AccountIDHint
	}
	expiry, expiryErr := domain.ParseTokenExpiry(candidate.AccessToken)
	if expiryErr != nil || expiry.IsZero() {
		// An access token that is not a JWT carries no exp; the id
		// token's expiry stands in as the session's rough boundary.
		expiry, expiryErr = domain.ParseTokenExpiry(candidate.IDToken)
		if expiryErr != nil {
			return domain.Session{}, expiryErr
		}
	}
	return domain.Session{
		AccessToken:  candidate.AccessToken,
		IDToken:      candidate.IDToken,
		AccessExpiry: expiry,
		Identity:     identity,
	}, nil
}

// importAccessToken stores a bare access token. ChatGPT access tokens
// are JWTs that carry the auth namespace, so identity decodes from the
// token itself; the session deliberately holds no refresh token and no
// id token, and the refresh loop treats an empty refresh token as a
// no-op — the login is valid exactly as long as the pasted token is.
func importAccessToken(candidate CredentialCandidate) (domain.Session, error) {
	identity, ok := domain.IdentityFromAccessToken(candidate.AccessToken)
	if !ok {
		return domain.Session{}, errors.New("codex import could not identify the account")
	}
	if identity.AccountID == "" {
		identity.AccountID = candidate.AccountIDHint
	}
	expiry, err := domain.ParseTokenExpiry(candidate.AccessToken)
	if err != nil {
		return domain.Session{}, err
	}
	return domain.Session{
		AccessToken:  candidate.AccessToken,
		AccessExpiry: expiry,
		Identity:     identity,
	}, nil
}

// commitImport lands an imported session: stored, provisioned and
// settled, exactly like a login's post-exchange tail minus the flow
// bookkeeping — an import never started a login, so there is no
// generation to check and nothing to cancel. The phase lands idle with
// no error: the import command carries its own outcome, and the UI owns
// the connecting/success/error narrative instead of subscribing to a
// login flow that never ran. A provisioning failure keeps the session —
// the tokens are already durable — and returns the error; the next
// status or restore re-attempts the provider entry. The bool reports
// whether the session landed — stored and signed in — which is true
// even on a provisioning failure, because the tokens are durable.
func (service *Service) commitImport(ctx context.Context, session domain.Session, importedFrom string, version lifecycleVersion) (ImportResult, bool, error) {
	id := session.Identity.AccountID
	release, err := service.lockAccountDisk(ctx, id)
	if err != nil {
		return ImportResult{}, false, err
	}
	defer release()
	service.mu.Lock()
	current := service.versionMatchesLocked(id, version)
	service.mu.Unlock()
	if !current {
		return ImportResult{}, false, errLoginAlreadyCancelled
	}
	saveCtx, cancelSave := context.WithTimeout(context.Background(), exchangeTimeout)
	saveErr := service.store.Save(saveCtx, service.storedAccountForSave(session, LoginMethodImport))
	cancelSave()
	if saveErr != nil {
		// Nothing is persisted and nothing is signed in; provisioning is
		// not attempted for a session the disk rejected.
		return ImportResult{}, false, errors.New("codex session could not be stored")
	}

	ensureCtx, cancelEnsure := context.WithTimeout(context.Background(), exchangeTimeout)
	providerID, ensureErr := service.provisioner.EnsureCodexProvider(ensureCtx)
	cancelEnsure()

	service.mu.Lock()
	if !service.versionMatchesLocked(id, version) {
		service.mu.Unlock()
		return ImportResult{}, false, errLoginAlreadyCancelled
	}
	before := service.snapshotLocked()
	// providerID is empty on a provisioning failure, and the account
	// lands with no provider to route through — signed in, tokens kept,
	// Status showing the gap.
	service.commitAccountLocked(session, providerID, LoginMethodImport)
	service.phase = PhaseIdle
	service.loginErr = ""
	service.clearLoginFlowLocked()
	snapshot, listeners, changed := service.diffLocked(before)
	service.mu.Unlock()
	release()
	if changed {
		service.fire(listeners, snapshot)
	}
	if ensureErr != nil {
		return ImportResult{}, true, fmt.Errorf("codex provider could not be provisioned: %v", ensureErr)
	}
	return ImportResult{Status: snapshot.Conn, ImportedFrom: importedFrom}, true, nil
}

// LogoutError is the typed refusal a caller can act on: the logout
// stopped because the Codex provider entry is still depended on, and the
// disconnect needs a user decision — switch the active route, or remove
// the leftover routes or keys — rather than a retry. Code names the
// dependency; Message is the instruction shown to the user. The session
// is untouched when this is returned: still signed in, tokens intact.
type LogoutError struct {
	Code    string
	Message string
}

// Error implements the error contract: the message is the human-readable
// refusal, matched programmatically by Code.
func (err *LogoutError) Error() string {
	return err.Message
}

// logoutReleaseRefusal translates a provider-manager refusal into the
// typed logout error. It matches sentinel identity on the raw adapter
// error — before any wrapping — because the wrapped text is for humans,
// not for matching. The disable path refuses while Codex is the active
// route; the delete path never does (deleting the active provider
// switches the route to a deterministic builtin instead), so the
// active-route refusal is the only one left. Any other error is not a
// refusal: nil.
func logoutReleaseRefusal(err error) *LogoutError {
	if errors.Is(err, providerapp.ErrActiveProvider) {
		return &LogoutError{
			Code:    "codex_active_route",
			Message: "Codex is the active provider. Switch the active route away from Codex before disconnecting.",
		}
	}
	return nil
}

// Logout signs accounts out: it aborts an in-flight login, parks or
// removes the fleet provider, clears the stored sessions and resets the
// state. An empty accountID means every account; a specific one means
// only that account, and an unknown id is a no-op. The provider entry is
// fleet-wide — one row for every account — so the release decision is a
// fleet decision: when the last live account (signed in, or signed in
// but needing a re-sign-in) is leaving, the row is parked — it stays,
// marked and disabled, so the providers row keeps its preset identity and
// a later sign-in relinks to it; while another live account remains, the
// row stays enabled because that account still serves traffic through
// it. An account with no live session behind it has nothing to release.
// removeEntry overrides that choice for the delete path: the provider
// entry is removed outright, live session or not, because the user asked
// for it to be gone from the Providers page rather than to be parked for
// a re-sign-in.
//
// A release refusal — the provider is the active route, or a leftover
// entry still holds routes or keys — aborts the logout before anything
// is cleared: the sessions stay signed in with their tokens intact, and
// a *LogoutError tells the caller which dependency to resolve first.
// Every other step failure is reported but none stops the rest: a
// provider error must not keep dead tokens on disk, and a clear error
// must not leave a released provider registered. errors.Join carries
// all failures to the caller. The login phase resets only when the
// last account is gone: the flow narrative belongs to the account map,
// not to whichever account disconnected first.
func (service *Service) Logout(ctx context.Context, accountID string, removeEntry bool) error {
	// The user asked to disconnect: any in-flight login is torn down
	// first, even when the logout below refuses — a flow the user is
	// abandoning must not land as a sign-in behind a refusal.
	service.cancelActiveLogin()

	// Targets are copied by value under the lock; entry fields are never
	// read outside mu. The same pass answers the fleet question — whether
	// any live account survives this logout — because that can only be
	// decided atomically with selecting the targets: an account skipped
	// by the filter is the account that keeps the fleet row enabled.
	type logoutTarget struct {
		id      string
		session domain.Session
		live    bool
		entry   *account
	}
	service.mu.Lock()
	targets := make([]logoutTarget, 0, len(service.accounts))
	remainingLive := false
	liveTargets := false
	for _, id := range accountIDsLocked(service.accounts) {
		entry := service.accounts[id]
		live := entry.connState == StateSignedIn || entry.connState == StateReauthNeeded
		if accountID != "" && id != accountID {
			if live {
				remainingLive = true
			}
			continue
		}
		targets = append(targets, logoutTarget{
			id:      id,
			session: entry.session,
			live:    live,
			entry:   entry,
		})
		if live {
			liveTargets = true
		}
	}
	service.mu.Unlock()

	var failures []error
	if removeEntry {
		if err := service.provisioner.RemoveCodexProvider(ctx); err != nil {
			if refusal := logoutReleaseRefusal(err); refusal != nil {
				return refusal
			}
			failures = append(failures, fmt.Errorf("codex provider could not be removed: %v", err))
		}
	} else {
		if liveTargets && !remainingLive {
			// The last live account is leaving, so nothing behind the fleet
			// row can answer a request: park it — disabled, preset marker
			// intact — while the row itself survives for the next sign-in.
			if err := service.provisioner.RetireCodexProvider(ctx); err != nil {
				if refusal := logoutReleaseRefusal(err); refusal != nil {
					// The entry is still depended on: abort before the store is
					// cleared, while the session is still signed in and its
					// tokens intact. The caller resolves the dependency and
					// disconnects again.
					return refusal
				}
				failures = append(failures, fmt.Errorf("codex provider could not be retired: %v", err))
			}
		}
		if accountID == "" && len(targets) == 0 {
			// No account is signed in, so the leftover preset entries are
			// the only thing to clean up — for every account at once. A
			// specific unknown id must not reach this teardown.
			if err := service.provisioner.RemoveCodexProvider(ctx); err != nil {
				if refusal := logoutReleaseRefusal(err); refusal != nil {
					// Same refusal on the leftover-entry path: the stored sessions
					// stay exactly as they are until the dependency is resolved.
					return refusal
				}
				failures = append(failures, fmt.Errorf("codex provider could not be removed: %v", err))
			}
		}
	}
	// Revoke serving before waiting for an already-started Save. Pending
	// identities remain addressable on failure, but never serve credentials.
	service.mu.Lock()
	before := service.snapshotLocked()
	if accountID == "" {
		service.fleetGeneration++
		service.clearingAll++
		// Include sign-ins that completed while the provider was released.
		targets = targets[:0]
		for _, id := range accountIDsLocked(service.accounts) {
			entry := service.accounts[id]
			targets = append(targets, logoutTarget{id: id, session: entry.session, entry: entry})
		}
	}
	for _, target := range targets {
		if service.accounts[target.id] != target.entry {
			continue
		}
		service.lifecycleLocked(target.id).generation++
		target.entry.pendingDeletion = true
		target.entry.connState = StateSignedOut
		target.entry.providerID = ""
	}
	if aggregateStateLocked(service.accounts) == StateSignedOut {
		service.phase = PhaseIdle
		service.loginErr = ""
		service.clearLoginFlowLocked()
	}
	snapshot, listeners, changed := service.diffLocked(before)
	service.mu.Unlock()
	if changed {
		service.fire(listeners, snapshot)
	}

	if accountID == "" {
		service.persistenceMu.Lock()
		clearErr := service.store.ClearAll(ctx)
		service.mu.Lock()
		before = service.snapshotLocked()
		if clearErr == nil {
			for _, target := range targets {
				if service.accounts[target.id] == target.entry {
					delete(service.accounts, target.id)
				}
			}
		}
		service.clearingAll--
		snapshot, listeners, changed = service.diffLocked(before)
		service.mu.Unlock()
		service.persistenceMu.Unlock()
		if clearErr != nil {
			failures = append(failures, fmt.Errorf("codex session could not be cleared: %v", clearErr))
		}
		if changed {
			service.fire(listeners, snapshot)
		}
	} else {
		for _, target := range targets {
			release, lockErr := service.lockAccountDisk(ctx, target.id)
			if lockErr != nil {
				failures = append(failures, lockErr)
				continue
			}
			service.mu.Lock()
			current := service.accounts[target.id]
			pending := current == target.entry && current.pendingDeletion
			if pending {
				target.session = current.session
			}
			service.mu.Unlock()
			if !pending {
				release()
				continue
			}
			clearErr := service.store.Clear(ctx, target.session)
			service.mu.Lock()
			before = service.snapshotLocked()
			if clearErr == nil && service.accounts[target.id] == target.entry {
				delete(service.accounts, target.id)
			}
			snapshot, listeners, changed = service.diffLocked(before)
			service.mu.Unlock()
			release()
			if clearErr != nil {
				failures = append(failures, fmt.Errorf("codex session could not be cleared: %v", clearErr))
			}
			if changed {
				service.fire(listeners, snapshot)
			}
		}
	}
	return errors.Join(failures...)
}

// Restore loads the persisted accounts at startup, re-provisions their
// providers and starts the refresh loop. It is idempotent: the loop
// starts once and outlives logins and logouts, ending only when ctx —
// the application lifetime — is cancelled.
func (service *Service) Restore(ctx context.Context) error {
	service.startRefreshLoop(ctx)

	versions := service.captureImportVersions()
	service.persistenceMu.Lock()
	sessions, err := service.store.Load(ctx)
	if err != nil {
		service.persistenceMu.Unlock()
		// A failed load is a failed restore: the state stays signed out
		// and the caller hears the store's own error.
		return err
	}
	if len(sessions) == 0 {
		service.persistenceMu.Unlock()
		// Nothing was ever stored: the zero state is already correct and
		// announcing it would be noise, so there is no notification.
		return nil
	}

	service.mu.Lock()
	before := service.snapshotLocked()
	restored := make(map[string]*account)
	for _, stored := range sessions {
		session := stored.Session
		id := session.Identity.AccountID
		// Restore never overwrites a newer live session or pending deletion.
		if service.accounts[id] != nil || !service.versionMatchesLocked(id, versions.forAccount(id)) {
			continue
		}
		entry := &account{session: session, connState: StateSignedIn}
		service.accounts[id] = entry
		service.lifecycleLocked(id).generation++
		restored[id] = entry
		// The account facts the store recorded come back with the
		// session: how this session was signed in and how many limit
		// cycles it has been through survive a restart.
		entry.loginMethod = stored.LoginMethod
		entry.resets = stored.Resets
	}
	snapshot, listeners, changed := service.diffLocked(before)
	service.mu.Unlock()
	service.persistenceMu.Unlock()
	if changed {
		service.fire(listeners, snapshot)
	}
	for id, restoredEntry := range restored {
		providerID, err := service.provisioner.EnsureCodexProvider(ctx)
		if err != nil {
			// A deleted or conflicting provider entry must not destroy the
			// tokens: the session stays signed in and usable, Status shows the
			// missing provider, and the next login or restart re-attempts
			// provisioning. The restore itself succeeded, hence the continue.
			continue
		}

		service.mu.Lock()
		before = service.snapshotLocked()
		entry := service.accounts[id]
		if entry == restoredEntry && !entry.pendingDeletion {
			// The account may have been logged out mid-restore; a missing
			// entry means the provider id lands nowhere, which is correct.
			entry.providerID = providerID
		}
		snapshot, listeners, changed = service.diffLocked(before)
		service.mu.Unlock()
		if changed {
			service.fire(listeners, snapshot)
		}
	}
	return nil
}

// AcquireAccessToken returns an access token the relay can send
// upstream for the named account. A usable token is served without any
// network call. An invalidated or expiring one is refreshed — once, not
// three times: the caller is latency-bound, and the ticker's retry
// budget is what covers blips.
func (service *Service) AcquireAccessToken(ctx context.Context, accountID string) (string, error) {
	if accountID == "" {
		return "", errNotSignedIn
	}
	report := service.readAccountForAcquire(accountID)
	if !report.signedIn {
		return "", errNotSignedIn
	}
	if !report.refreshWork {
		return report.session.AccessToken, nil
	}

	// One account means one refresh token in flight at a time: the ticker
	// and every concurrent acquire serialize on that account's refresh
	// gate, so a second caller waits for the winner's result instead of
	// racing a second refresh with a token the server may have just
	// rotated. The wait honours this caller's ctx: a request that already
	// ended stops waiting and leaves the gate to whoever is still live.
	if err := report.refresh.acquire(ctx); err != nil {
		return "", err
	}
	// The receiver is read at defer time: the release always matches this
	// acquire even though report is re-read below — and if the entry is
	// replaced mid-refresh, the new entry's gate starts free while this
	// release settles the old one.
	defer report.refresh.release()

	// The winner may have refreshed while we waited for the guard.
	report = service.readAccountForAcquire(accountID)
	if !report.signedIn {
		return "", errNotSignedIn
	}
	if !report.refreshWork {
		return report.session.AccessToken, nil
	}
	if report.session.RefreshToken == "" {
		// The session cannot be renewed; only a fresh login fixes it.
		service.markReauthNeeded(accountID, report.version, report.session.RefreshToken)
		return "", errNeedsSignIn
	}

	// The refresh does not inherit the caller's cancellation: the token
	// endpoint rotates the refresh token, so a refresh cut off after the
	// server consumed the old token but before its answer lands strands
	// the account until a fresh login. A departed caller waits at most
	// refreshTimeout for the rotation to be stored.
	refreshCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), refreshTimeout)
	refreshed, err := service.authorizer.Refresh(refreshCtx, report.session.RefreshToken)
	cancel()
	if err != nil {
		if domain.IsReauthError(err.Error()) {
			// The refresh token is dead: only a fresh login fixes it, and
			// the UI needs to hear that immediately — unless a newer login
			// already replaced the session this token belonged to.
			service.markReauthNeeded(accountID, report.version, report.session.RefreshToken)
		}
		// Any other failure leaves the state alone: the next acquire or
		// the next tick retries, so a blip does not sign the user out.
		return "", fmt.Errorf("codex access token could not be refreshed: %v", err)
	}
	committed, err := service.applyRefresh(accountID, report.version, report.session, refreshed)
	if err != nil {
		// The disk refused the rotation, but the token endpoint has
		// already rotated: the fresh session is live in memory and the
		// next acquire serves it without another refresh. The error
		// surfaces here — this caller pays for the store's failure —
		// while a retry would only see the saved-token risk at restart.
		return "", fmt.Errorf("codex session could not be stored: %v", err)
	}
	if committed {
		return refreshed.AccessToken, nil
	}
	// The account's session was replaced mid-refresh (a login or logout
	// landed). Serve whatever is current now.
	service.mu.Lock()
	entry := service.accounts[accountID]
	token := ""
	if entry != nil && entry.connState == StateSignedIn {
		token = entry.session.AccessToken
	}
	service.mu.Unlock()
	if token == "" {
		return "", errNotSignedIn
	}
	return token, nil
}

// InvalidateAccessToken flags the access token as rejected: the next
// acquire refreshes before serving it. Cheap and silent on purpose — it
// runs on relay request paths that saw an auth failure, where the answer
// is a token rotation, not a UI announcement.
func (service *Service) InvalidateAccessToken(accessToken string) {
	service.mu.Lock()
	for _, entry := range service.accounts {
		if entry.session.AccessToken == accessToken {
			entry.invalidated = true
		}
	}
	service.mu.Unlock()
}

// QuotaSnapshot is the account's usage windows as the quota card draws
// them: what the plan is, how full each window's meter is, and when they
// reset. The probe's failure lives in the result rather than the error
// return — a failed probe is a stale card with a reason attached, not a
// broken command, and the previous good usage stays so the owner still
// sees what the account had. FetchedAt is Unix seconds; zero means no
// probe has ever succeeded.
type QuotaSnapshot struct {
	FetchedAt int64
	Usage     domain.Usage
	Err       string
}

// RefreshQuota probes the account's usage windows and returns the last
// settled answer. It is on-demand — the card asks on mount and on the
// Refresh button — so there is no loop, no threshold and no background
// polling behind it.
//
// Probes coalesce on quotaGate: concurrent callers wait for the winner
// instead of racing duplicate requests against the same account. A
// caller whose ctx ends while waiting reports the last settled answer
// without taking the gate. The winner's outcome is recorded under mu
// before the gate opens, so a waiter waking up sees the fresh counter and
// shares the winner's snapshot rather than starting a probe of its own.
// A cancelled winner records nothing — its ctx died, not the probe's
// subject — and leaves the answer to the next caller still interested.
//
// The quota gate nests outside the refresh gate and only that way round:
// a probe holds quotaGate across AcquireAccessToken's refreshGate wait,
// and nothing on the refresh path ever asks for quotaGate.
func (service *Service) RefreshQuota(ctx context.Context, accountID string) QuotaSnapshot {
	service.mu.Lock()
	entry := service.accounts[accountID]
	if entry == nil || entry.pendingDeletion {
		service.mu.Unlock()
		return QuotaSnapshot{Err: errNotSignedIn.Error()}
	}
	probes := entry.quotaProbes
	snapshot := entry.quota
	gate := &entry.quotaGate
	service.mu.Unlock()

	if err := gate.acquire(ctx); err != nil {
		// The caller gave up waiting; the last settled answer is all
		// this ctx is owed. The gate was not taken and must not be
		// released.
		return snapshot
	}
	defer gate.release()

	service.mu.Lock()
	entry = service.accounts[accountID]
	if entry == nil || entry.pendingDeletion {
		service.mu.Unlock()
		return snapshot
	}
	if entry.quotaProbes != probes {
		// A probe settled while this caller waited for the gate; its
		// answer is already the freshest one to report.
		snapshot := entry.quota
		service.mu.Unlock()
		return snapshot
	}
	service.mu.Unlock()

	service.mu.Lock()
	version := service.versionLocked(accountID)
	service.mu.Unlock()
	usage, err := service.probeUsage(ctx, accountID)

	if errors.Is(err, context.Canceled) {
		// The caller walked away; record nothing and let the next
		// interested caller probe again.
		return snapshot
	}

	release, lockErr := service.lockAccountDisk(ctx, accountID)
	if lockErr != nil {
		return snapshot
	}
	defer release()
	service.mu.Lock()
	entry = service.accounts[accountID]
	if entry == nil || entry.pendingDeletion || !service.versionMatchesLocked(accountID, version) {
		// The account disconnected while the probe was in flight; there
		// is no card left to record.
		service.mu.Unlock()
		return snapshot
	}
	before := service.snapshotLocked()
	entry.quotaProbes++
	rotated := false
	if err != nil {
		snapshot = QuotaSnapshot{
			FetchedAt: snapshot.FetchedAt,
			Usage:     snapshot.Usage,
			Err:       err.Error(),
		}
	} else {
		if domain.Rotated(snapshot.Usage, usage) {
			// The upstream opened a later primary window: the account
			// spent a limit cycle, and the counter the owner watches
			// must say so. Rotation is counted here — the one settle
			// point every probe passes — so no caller can charge it
			// twice. A failed probe carries the last good Usage, so
			// detection spans an outage; the first probe after a
			// restart has no baseline and counts nothing by design.
			entry.resets++
			rotated = true
		}
		snapshot = QuotaSnapshot{
			FetchedAt: service.now().Unix(),
			Usage:     usage,
		}
	}
	entry.quota = snapshot
	persist := StoredAccount{}
	if rotated {
		persist = StoredAccount{Session: entry.session, LoginMethod: entry.loginMethod, Resets: entry.resets}
	}
	after, listeners, changed := service.diffLocked(before)
	service.mu.Unlock()
	if rotated {
		// The counter is already live in memory; the disk copy is
		// best-effort. A failing save keeps the in-memory value and the
		// probe's own result, and the next counted rotation retries —
		// the same trade applyRefresh makes for tokens.
		saveCtx, cancel := context.WithTimeout(context.Background(), refreshTimeout)
		service.store.Save(saveCtx, persist)
		cancel()
	}
	release()
	if changed {
		service.fire(listeners, after)
	}
	return snapshot
}

// probeUsage runs one usage probe with the 401 protocol: acquire a
// token, ask the account's usage endpoint, and on an explicit rejection
// rotate the token once and ask again — the same budget the acquire path
// gives a refresh. Every other failure stands as reported; the caller
// decides whether the caller's ctx cancels it out.
func (service *Service) probeUsage(ctx context.Context, accountID string) (domain.Usage, error) {
	for attempt := 0; ; attempt++ {
		accessToken, err := service.AcquireAccessToken(ctx, accountID)
		if err != nil {
			return domain.Usage{}, err
		}

		probeCtx, cancel := context.WithTimeout(ctx, service.quotaTimeout)
		usage, err := service.authorizer.FetchUsage(probeCtx, accessToken, accountID)
		cancel()
		if err == nil {
			return usage, nil
		}
		if !errors.Is(err, ErrUsageUnauthorized) || attempt > 0 {
			return domain.Usage{}, err
		}
		// The access token was rejected: rotate it and try once more.
		// RefreshQuota is an on-demand card, not a request path, so a
		// second rejection simply becomes the reported error.
		service.InvalidateAccessToken(accessToken)
	}
}

// WakeOutcome is one account's answer to a wake request: the state the
// account ended in, and the failure text when the forced refresh could
// not deliver a usable token. Err is empty on success.
type WakeOutcome struct {
	AccountID string
	State     ConnState
	Err       string
}

// Wake forces a token rotation now: the account's access token is
// flagged as spent, so the acquire path refreshes instead of serving
// the cached one. An empty accountID wakes every account. This is what
// the owner reaches for when the relay stopped serving an account that
// looks signed in — the token went stale out-of-band and waiting for
// the next request is not an answer. It is a token action, not a usage
// probe: a dead refresh token reclassifies the account to reauth-needed
// exactly as the acquire path would, and a reauth-needed account is not
// retried at all — that account's refresh already failed once, and only
// a fresh login fixes it. Failures ride the per-account outcomes; the
// command-level answer is always the list.
func (service *Service) Wake(ctx context.Context, accountID string) []WakeOutcome {
	type wakeTarget struct {
		id          string
		refreshable bool
	}

	service.mu.Lock()
	targets := []wakeTarget{}
	if accountID != "" {
		if entry := service.accounts[accountID]; entry != nil {
			targets = append(targets, wakeTarget{id: accountID, refreshable: entry.connState == StateSignedIn})
		}
	} else {
		for _, id := range accountIDsLocked(service.accounts) {
			targets = append(targets, wakeTarget{id: id, refreshable: service.accounts[id].connState == StateSignedIn})
		}
	}
	for _, target := range targets {
		// Only a signed-in account has a rotation to force; the flag
		// itself is an implementation detail of the acquire path.
		if target.refreshable {
			service.accounts[target.id].invalidated = true
		}
	}
	service.mu.Unlock()

	if len(targets) == 0 {
		// No account to wake. An explicit id that matches nothing reads
		// as not signed in — the same answer a acquire would give.
		if accountID == "" {
			return []WakeOutcome{}
		}
		return []WakeOutcome{{AccountID: accountID, State: StateSignedOut, Err: errNotSignedIn.Error()}}
	}

	outcomes := make([]WakeOutcome, 0, len(targets))
	for _, target := range targets {
		if !target.refreshable {
			// Reauth-needed: the refresh token already failed. The
			// honest answer is "sign in again", delivered without a
			// network round-trip the upstream is guaranteed to refuse.
			service.mu.Lock()
			state := StateSignedOut
			if entry := service.accounts[target.id]; entry != nil {
				state = entry.connState
			}
			service.mu.Unlock()
			outcomes = append(outcomes, WakeOutcome{AccountID: target.id, State: state, Err: errNeedsSignIn.Error()})
			continue
		}
		outcome := WakeOutcome{AccountID: target.id}
		if _, err := service.AcquireAccessToken(ctx, target.id); err != nil {
			outcome.Err = err.Error()
		}
		// The state is read after the refresh: a dead refresh token
		// reclassifies the account right here, and the outcome must
		// report what the wake actually left behind.
		service.mu.Lock()
		state := StateSignedOut
		if entry := service.accounts[target.id]; entry != nil {
			state = entry.connState
		}
		service.mu.Unlock()
		outcome.State = state
		outcomes = append(outcomes, outcome)
	}
	return outcomes
}

// QuotaOutcome pairs one account with the snapshot its probe settled
// on, so a fleet-wide check can answer every card in one command.
type QuotaOutcome struct {
	AccountID string
	Snapshot  QuotaSnapshot
}

// quotaProbeWorkers bounds how many usage probes run at once: enough
// that a fleet check finishes like a small one, few enough that the
// token refreshes behind the probes cannot look like a burst to the
// upstream.
const quotaProbeWorkers = 4

// RefreshQuotaAll probes every account's usage windows, bounded in
// parallel, and reports each account's settled snapshot in account-id
// order — the order the status list renders. Each probe is a plain
// RefreshQuota, so per-account gates coalesce duplicate callers and a
// cancelled ctx leaves the last settled answers standing. Every account
// is probed, including reauth-needed ones: their cards report the
// failure, which is the answer an owner checking the fleet wants.
func (service *Service) RefreshQuotaAll(ctx context.Context) []QuotaOutcome {
	service.mu.Lock()
	ids := accountIDsLocked(service.accounts)
	service.mu.Unlock()

	outcomes := make([]QuotaOutcome, len(ids))
	if len(ids) == 0 {
		return outcomes
	}
	position := make(map[string]int, len(ids))
	for i, id := range ids {
		position[id] = i
	}

	work := make(chan string)
	workers := min(quotaProbeWorkers, len(ids))
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for id := range work {
				// Distinct slice slots: no two workers write the same
				// outcome, so the shared slice needs no lock.
				outcomes[position[id]] = QuotaOutcome{AccountID: id, Snapshot: service.RefreshQuota(ctx, id)}
			}
		}()
	}
	for _, id := range ids {
		work <- id
	}
	close(work)
	wg.Wait()
	return outcomes
}

// runLogin is the login flow's goroutine. It is bounded by loginCtx; on
// any landing — cancel, timeout, error or success — it settles the phase
// exactly once and stops the redirect server while its flow is still the
// current one.
func (service *Service) runLogin(loginCtx context.Context, cancel context.CancelFunc, verifier string, generation uint64) {
	// Releases the timeout timer promptly; LoginCancel also holds this
	// cancel func, but the goroutine owns the normal (non-cancel) path.
	defer cancel()
	// The server is stopped while this login still owns it: after a
	// cancel-and-immediate-restart, the loopback listener can already
	// belong to a newer flow, and stopping that one would break a login
	// this goroutine never started.
	defer service.stopLoginServer(generation)

	code, err := service.redirects.AwaitCode(loginCtx)
	if service.loginCancelled(loginCtx) {
		// The user (or a logout) stopped the flow: quiet idle, not error.
		service.landLogin(PhaseIdle, "")
		return
	}
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || loginCtx.Err() == context.DeadlineExceeded {
			service.landLogin(PhaseError, "codex login timed out")
			return
		}
		// An AwaitCode failure is the listener adapter's to explain: its
		// text passes through as-is.
		service.landLogin(PhaseError, err.Error())
		return
	}

	// The exchanging landing keeps the flow fields — the flow is still
	// live, and LoginStart stays idempotent on its URL.
	service.landLogin(PhaseExchanging, "")

	exchangeCtx, cancelExchange := context.WithTimeout(loginCtx, exchangeTimeout)
	session, exchangeErr := service.authorizer.ExchangeCode(exchangeCtx, code, verifier)
	cancelExchange()

	service.settleExchange(loginCtx, LoginMethodBrowser, session, exchangeErr)
}

// settleExchange lands the phase both sign-in flows share once their code
// has been granted: either the exchange failed, or its session is saved,
// checked against a racing logout and provisioned. The browser and device
// flows differ only in how they got here.
func (service *Service) settleExchange(loginCtx context.Context, method LoginMethod, session domain.Session, exchangeErr error) {
	release, err := service.lockAccountDisk(loginCtx, session.Identity.AccountID)
	if err != nil {
		service.landLogin(PhaseIdle, "")
		return
	}
	defer release()
	if service.loginCancelled(loginCtx) {
		// Cancellation outranks the exchange result: a completed exchange
		// for a login the user abandoned is never persisted.
		service.landLogin(PhaseIdle, "")
		return
	}
	if exchangeErr != nil {
		// The token endpoint's text passes through as-is; it is OAuth
		// error codes and status text, never token material.
		service.landLogin(PhaseError, exchangeErr.Error())
		return
	}

	// The exchange and the save use fresh contexts: the login deadline
	// may be nearly spent while the tokens are perfectly good, and these
	// machine-speed calls have their own (shorter) bound.
	saveCtx, cancelSave := context.WithTimeout(context.Background(), exchangeTimeout)
	saveErr := service.store.Save(saveCtx, service.storedAccountForSave(session, method))
	cancelSave()
	if saveErr != nil {
		// Nothing is persisted and nothing is signed in; provisioning is
		// not attempted for a session the disk rejected.
		service.landLogin(PhaseError, "codex session could not be stored")
		return
	}

	// The save raced a logout: Logout's cancel and Clear can land while
	// this Save is in flight, so the store may now hold a session the user
	// just disconnected from. The cancel is permanent, so the context
	// witnesses it even after Logout reset the cancelRequested flag.
	// Compensate by clearing again before anything is signed in — the
	// login goroutine loses the race by design, the user's disconnect
	// wins. (A plain LoginCancel that lands in this same window is also
	// covered: cancelling was the user's last word either way.)
	if service.loginCancelled(loginCtx) {
		clearCtx, cancelClear := context.WithTimeout(context.Background(), exchangeTimeout)
		clearErr := service.store.Clear(clearCtx, session)
		cancelClear()
		if clearErr != nil {
			service.landLogin(PhaseError, "codex session could not be cleared")
			return
		}
		service.landLogin(PhaseIdle, "")
		return
	}

	ensureCtx, cancelEnsure := context.WithTimeout(context.Background(), exchangeTimeout)
	providerID, ensureErr := service.provisioner.EnsureCodexProvider(ensureCtx)
	cancelEnsure()

	service.completeLogin(loginCtx, method, session, providerID, ensureErr, release)
}

// completeLogin commits a login whose session was stored: the tokens are
// already durable, so a provisioning failure leaves a deliberate half
// state — signed in with no provider — rather than rolling back a
// session the user just fought a browser for. The next login or restore
// re-attempts provisioning and heals it. The providerID the provisioner
// returned ("" on failure) is committed with the account; the row it
// names is the one fleet entry, so a re-login of an existing account
// relinks to the row the fleet already shares.
func (service *Service) completeLogin(loginCtx context.Context, method LoginMethod, session domain.Session, providerID string, providerErr error, release func()) {
	service.mu.Lock()
	if service.cancelRequested || loginCtx.Err() == context.Canceled {
		service.mu.Unlock()
		// Still inside the account's disk gate: compensation cannot erase
		// a newer login, and logout cannot return ahead of this cleanup.
		clearCtx, cancel := context.WithTimeout(context.Background(), exchangeTimeout)
		clearErr := service.store.Clear(clearCtx, session)
		cancel()
		release()
		if clearErr != nil {
			service.landLogin(PhaseError, "codex session could not be cleared")
		} else {
			service.landLogin(PhaseIdle, "")
		}
		return
	}
	before := service.snapshotLocked()
	service.commitAccountLocked(session, providerID, method)
	if providerErr == nil {
		service.phase = PhaseSuccess
		service.loginErr = ""
	} else {
		service.phase = PhaseError
		service.loginErr = "codex provider could not be provisioned: " + providerErr.Error()
	}
	service.clearLoginFlowLocked()
	snapshot, listeners, changed := service.diffLocked(before)
	service.mu.Unlock()
	release()
	if changed {
		service.fire(listeners, snapshot)
	}
}

// landLogin settles the login goroutine into a phase. Terminal landings
// clear the flow fields; the exchanging landing keeps them.
func (service *Service) landLogin(phase LoginPhase, errMsg string) {
	service.mu.Lock()
	before := service.snapshotLocked()
	service.phase = phase
	service.loginErr = errMsg
	if phase != PhaseExchanging {
		service.clearLoginFlowLocked()
	}
	snapshot, listeners, changed := service.diffLocked(before)
	service.mu.Unlock()
	if changed {
		service.fire(listeners, snapshot)
	}
}

// clearLoginFlowLocked forgets the in-flight login's working data: the
// browser flow's URL and verifier, the device flow's user code, and the
// shared cancel handle. Called only with mu held.
func (service *Service) clearLoginFlowLocked() {
	service.authorizeURL = ""
	service.verifier = ""
	service.loginCancel = nil
	service.cancelRequested = false
	service.loginFlow = loginFlowNone
	service.deviceUserCode = ""
	service.deviceVerificationURL = ""
}

// cancelActiveLogin stops the current login, if any: it flags the
// cancellation, cancels the login context and stops the redirect server.
// It does not itself move the phase — the login goroutine owns phase
// transitions, so a landing can never race a transition from here.
func (service *Service) cancelActiveLogin() {
	service.mu.Lock()
	active := service.phase == PhaseWaiting || service.phase == PhaseExchanging
	cancel := service.loginCancel
	if active {
		service.cancelRequested = true
	}
	service.mu.Unlock()
	if !active {
		return
	}
	// Both the cancel func and the server stop are adapter-facing; they
	// run outside mu. LoginCancel stops the server unconditionally
	// because it always targets the login that is current right now.
	if cancel != nil {
		cancel()
	}
	service.redirects.Stop()
}

// loginCancelled reports whether this login was stopped by the user or a
// logout. The context is authoritative — logout resets the flag but
// always cancels the context — and the flag covers the moment between
// LoginCancel setting it and the context propagation reaching the
// goroutine.
func (service *Service) loginCancelled(loginCtx context.Context) bool {
	service.mu.Lock()
	requested := service.cancelRequested
	service.mu.Unlock()
	return requested || loginCtx.Err() == context.Canceled
}

// loginHandbackStale reports whether the login flow a start call is about
// to hand back has already been taken away from that caller. Only three
// things make it stale: a cancellation was requested against it, a newer
// start superseded it, or it already landed idle — the one phase a
// cancelled login ends in. The login context is deliberately not
// consulted: the goroutine's own deferred cancel runs on every exit path,
// a successful login included, so a dead context alone cannot say who
// cancelled it. PhaseIdle is the durable witness of an external cancel
// instead: the goroutine lands idle only through loginCancelled, and the
// flag reset that comes with that landing clears the flags but cannot
// un-cancel the flow. A flow that settled on its own — success or a
// genuine flow error — is not stale and must hand its URL back.
func (service *Service) loginHandbackStale(generation uint64) bool {
	service.mu.Lock()
	stale := service.cancelRequested || service.loginGen != generation || service.phase == PhaseIdle
	service.mu.Unlock()
	return stale
}

// stopLoginServer stops the redirect server only while this login still
// owns the flow. After a cancel-and-restart the loopback listener can
// already belong to a newer login, and stopping that one would kill a
// flow this goroutine never started.
func (service *Service) stopLoginServer(generation uint64) {
	service.mu.Lock()
	current := service.loginGen
	service.mu.Unlock()
	if current == generation {
		service.redirects.Stop()
	}
}

// refreshGate is a mutex whose wait is cancellable: a caller that gives
// up stops waiting and reports its ctx's error without taking the gate,
// leaving it to whoever is still live. Zero value is ready to use. The
// gate's own mu is a leaf lock — never held across adapter calls — and
// only two locks ever wrap it: Service.mu inside it, and the quota gate
// outside it (never the other way round, so the nesting cannot cycle).
type refreshGate struct {
	mu   sync.Mutex
	held bool
	// waiting counts callers parked on done. It is pure accounting —
	// acquire and release decide nothing from it — and exists so a test
	// can pin the schedule coalescing depends on: "a waiter is parked"
	// is otherwise unobservable from outside the gate.
	waiting int
	done    chan struct{}
}

// acquire waits for the gate to open, or for ctx to end first. On error
// the gate was NOT taken: the caller must not release it.
func (gate *refreshGate) acquire(ctx context.Context) error {
	for {
		gate.mu.Lock()
		if !gate.held {
			gate.held = true
			gate.done = make(chan struct{})
			gate.mu.Unlock()
			return nil
		}
		release := gate.done
		gate.waiting++
		gate.mu.Unlock()
		select {
		case <-release:
			// The gate opened; contend for it again.
		case <-ctx.Done():
			// The caller is gone. Whoever holds the gate keeps it.
			gate.mu.Lock()
			gate.waiting--
			gate.mu.Unlock()
			return ctx.Err()
		}
		gate.mu.Lock()
		gate.waiting--
		gate.mu.Unlock()
	}
}

// release opens the gate. It is safe against a double release only in
// the sense that closing a nil channel would panic; the contract —
// release exactly once per successful acquire — is local to this
// package's two call sites, both of which pair the calls lexically.
func (gate *refreshGate) release() {
	gate.mu.Lock()
	gate.held = false
	done := gate.done
	gate.done = nil
	gate.mu.Unlock()
	if done != nil {
		close(done)
	}
}

// waiterCount reports how many callers are parked on done right now.
// Callers that contend and take the gate are not waiting, so this is 0
// for an uncontended gate and 1 while exactly one caller coalesces
// behind whoever holds it.
func (gate *refreshGate) waiterCount() int {
	gate.mu.Lock()
	defer gate.mu.Unlock()
	return gate.waiting
}

// startRefreshLoop launches the refresh loop once, bound to the lifetime
// of the context the first Restore was given. Later Restore calls (or
// logins and logouts) do not start a second loop.
func (service *Service) startRefreshLoop(ctx context.Context) {
	service.mu.Lock()
	if service.refreshRunning {
		service.mu.Unlock()
		return
	}
	service.refreshRunning = true
	service.mu.Unlock()
	go service.refreshLoop(ctx)
}

// refreshLoop ticks until the application context ends. It survives
// logins and logouts: a login that lands later still gets refreshed.
func (service *Service) refreshLoop(ctx context.Context) {
	interval := service.refreshInterval
	if interval <= 0 {
		// A hand-built Service with no cadence must not panic the ticker.
		interval = defaultRefreshInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			service.tickRefresh()
		}
	}
}

// tickRefresh refreshes every signed-in account whose token is stale or
// expiring; every other tick is a cheap read. The targets are snapshot
// under one lock and refreshed outside it, so one account's network call
// never holds another's read hostage.
func (service *Service) tickRefresh() {
	type refreshTarget struct {
		accountID string
		session   domain.Session
		version   lifecycleVersion
	}
	service.mu.Lock()
	targets := make([]refreshTarget, 0, len(service.accounts))
	for accountID, entry := range service.accounts {
		if entry.connState != StateSignedIn {
			continue
		}
		if !entry.session.NeedsRefresh(service.now(), refreshSkew) {
			continue
		}
		targets = append(targets, refreshTarget{accountID: accountID, session: entry.session, version: service.versionLocked(accountID)})
	}
	service.mu.Unlock()
	for _, target := range targets {
		service.refreshWithRetries(target.accountID, target.version, target.session)
	}
}

// refreshWithRetries is the ticker's refresh: up to refreshRetries
// attempts with backoff, because a token endpoint blip should not wait a
// full tick to be retried. Each attempt settles inside that account's
// refresh gate — a concurrent acquire must not slip a refresh between
// retries using a token the server may have rotated, which would
// invalidate the very cycle in flight — but the backoff sleeps run
// outside it, so a caller waiting on the gate during a retry cycle holds
// it only for one attempt's duration, never the whole cycle's.
func (service *Service) refreshWithRetries(accountID string, version lifecycleVersion, session domain.Session) {
	if session.RefreshToken == "" {
		// A session with no refresh token is an imported access-token
		// login: it is renewed only by a fresh sign-in, and marking it
		// reauth-needed would treat a working session as broken —
		// the access token itself may still be perfectly good.
		return
	}
	for attempt := 0; attempt < refreshRetries; attempt++ {
		if attempt > 0 {
			time.Sleep(service.retryBackoff(attempt))
		}
		if service.gatedRefreshAttempt(accountID, version, session) {
			return
		}
	}
}

// gatedRefreshAttempt runs one refresh attempt under the account's
// refresh gate. It reports whether the cycle is settled — the attempt
// succeeded, the account vanished, or the token was classified dead —
// so the retry loop knows to stop. The gate is never held across the
// backoff sleeps that separate attempts.
func (service *Service) gatedRefreshAttempt(accountID string, version lifecycleVersion, session domain.Session) bool {
	service.mu.Lock()
	entry := service.accounts[accountID]
	var gate *refreshGate
	if entry != nil {
		gate = &entry.refreshGate
	}
	service.mu.Unlock()
	if gate == nil {
		// The account was logged out while this cycle was being
		// scheduled; there is nothing left to refresh.
		return true
	}
	// The ticker has no caller ctx to honour: it owns the loop and
	// Background is the lifetime the loop already runs on. Acquiring
	// cannot fail on Background, but the gate is only released when it
	// was taken.
	if err := gate.acquire(context.Background()); err != nil {
		return true
	}
	defer gate.release()

	service.mu.Lock()
	entry = service.accounts[accountID]
	current := entry != nil && entry.connState == StateSignedIn && service.versionMatchesLocked(accountID, version) && entry.session.RefreshToken == session.RefreshToken
	service.mu.Unlock()
	if !current {
		return true
	}
	ctx, cancel := context.WithTimeout(context.Background(), refreshTimeout)
	refreshed, err := service.authorizer.Refresh(ctx, session.RefreshToken)
	cancel()
	if err == nil {
		// A save failure keeps the rotation live in memory: the token is
		// real and the server has already moved on, so the next acquire
		// serves it instead of presenting the old refresh token again.
		// There is no logger at this layer — the observable answer to a
		// store outage is that the acquire path surfaces the save error.
		_, _ = service.applyRefresh(accountID, version, session, refreshed)
		return true
	}
	if domain.IsReauthError(err.Error()) {
		// The refresh token is dead; retrying cannot change that.
		service.markReauthNeeded(accountID, version, session.RefreshToken)
		return true
	}
	// Transient: the gate opens before the caller retries, or the next
	// tick takes over once the budget is spent — the ticker bounds the
	// retry rate instead of spinning.
	return false
}

// retryBackoff returns the pause before retry attempt n. The first retry
// absorbs a blip; later ones give a struggling endpoint real room without
// turning the ticker into a busy loop. Tests zero the backoffs to run
// instantly.
func (service *Service) retryBackoff(attempt int) time.Duration {
	if len(service.refreshBackoffs) == 0 {
		return 0
	}
	index := attempt - 1
	if index < 0 {
		return 0
	}
	if index >= len(service.refreshBackoffs) {
		index = len(service.refreshBackoffs) - 1
	}
	return service.refreshBackoffs[index]
}

// applyRefresh merges a successful refresh for one account and persists
// it. It returns whether this result became that account's live session,
// along with the store's save error when there was one: a rotation the
// disk refused still becomes the live session — the token endpoint has
// already rotated, so keeping the old session in memory would present a
// refresh token that no longer exists — while the save error travels to
// the acquire path, which is the seam that surfaces it. A refresh whose
// account was logged out or re-logged-in mid-flight is dropped without
// overwriting the newer state, and its unused save error is dropped too.
func (service *Service) applyRefresh(accountID string, version lifecycleVersion, previous, refreshed domain.Session) (bool, error) {
	release, err := service.lockAccountDisk(context.Background(), accountID)
	if err != nil {
		return false, err
	}
	defer release()
	service.mu.Lock()
	entry := service.accounts[accountID]
	if entry == nil || entry.connState == StateSignedOut || !service.versionMatchesLocked(accountID, version) || entry.session.RefreshToken != previous.RefreshToken {
		// The session this refresh renewed is gone; a login or logout has
		// spoken since.
		service.mu.Unlock()
		return false, nil
	}
	// The save runs outside mu, so the account facts travel with it: a
	// rotation that dropped the login method or reset count would erase
	// them from disk — memory would keep them until the next restart
	// silently reset both.
	loginMethod := entry.loginMethod
	resets := entry.resets
	service.mu.Unlock()

	merged := mergeRefreshed(previous, refreshed)
	stored := StoredAccount{Session: merged, LoginMethod: loginMethod, Resets: resets}
	saveCtx, cancel := context.WithTimeout(context.Background(), refreshTimeout)
	err = service.store.Save(saveCtx, stored)
	cancel()

	service.mu.Lock()
	// Logout marks the target nonservable before waiting for this save.
	entry = service.accounts[accountID]
	if entry == nil || entry.connState == StateSignedOut || !service.versionMatchesLocked(accountID, version) || entry.session.RefreshToken != previous.RefreshToken {
		service.mu.Unlock()
		return false, nil
	}
	before := service.snapshotLocked()
	// Committed even when the save failed: the rotation is live on the
	// server side, and the in-memory session is the one that serves
	// tokens. What the disk says only matters at the next restart, and
	// the acquire path reports the save error rather than hiding it.
	entry.session = merged
	entry.invalidated = false
	snapshot, listeners, changed := service.diffLocked(before)
	service.mu.Unlock()
	release()
	if changed {
		service.fire(listeners, snapshot)
	}
	return true, err
}

// mergeRefreshed folds a refresh response into the session it renewed.
// The token endpoint may omit the refresh token and the id token on a
// response that only rotates the access token; dropping the previous
// values then would strand the session, so empties fall back. The
// access expiry falls back too: a refresh that delivered no id_token
// delivered no date either, and a zero expiry reads as "always refresh"
// in NeedsRefresh — which would turn one dateless refresh into a full
// OAuth round-trip on every request from then on. The kept date
// describes the previous token, so it errs toward refreshing too early,
// never toward serving a lapsed token.
func mergeRefreshed(previous, refreshed domain.Session) domain.Session {
	merged := refreshed
	if merged.RefreshToken == "" {
		merged.RefreshToken = previous.RefreshToken
	}
	if merged.IDToken == "" {
		merged.IDToken = previous.IDToken
	}
	if merged.Identity.Email == "" {
		merged.Identity = previous.Identity
	}
	if merged.AccessExpiry.IsZero() {
		merged.AccessExpiry = previous.AccessExpiry
	}
	return merged
}

// RejectAccessToken moves the account whose token the upstream named as
// revoked to reauth_needed: no rotation of the same session can serve
// another request, and only a fresh login fixes it. The identity stays
// so the UI can say who needs to sign in again. The rejected token is
// required for the same staleness reason as markReauthNeeded: a
// rejection that arrives after a newer login (or a refresh that landed
// a different access token) describes the old token, not the new one,
// and must not sign the live account out from under whoever just landed
// it. Accounts whose tokens do not match are untouched — the scan finds
// the account this token was served from, and only that one flips. The
// snapshot diff makes a repeated call for the same token a silent
// no-op. This is deliberately not a reason field on the status: the UI
// already renders reauth_needed as "sign in again", and two entries for
// one user question would be a lie about which question was asked.
func (service *Service) RejectAccessToken(rejectedAccessToken string) {
	service.mu.Lock()
	for _, id := range accountIDsLocked(service.accounts) {
		entry := service.accounts[id]
		if entry.connState == StateSignedOut || entry.session.AccessToken != rejectedAccessToken {
			// Signed out, or a newer session has spoken since this token
			// was served: the rejection is stale for this account.
			continue
		}
		before := service.snapshotLocked()
		entry.connState = StateReauthNeeded
		snapshot, listeners, changed := service.diffLocked(before)
		service.mu.Unlock()
		if changed {
			service.fire(listeners, snapshot)
		}
		return
	}
	service.mu.Unlock()
}

// markReauthNeeded moves the account whose refresh token died to
// reauth_needed: only a fresh login fixes it. The identity stays so the
// UI can say who needs to sign in again. The failed token is required:
// when the account's session was replaced by a fresher login or cleared
// by a logout while the failing refresh was in flight, the failure
// belongs to the old token and must not clobber the new state — a stale
// flip would sign the live account out from under whoever just landed
// it. Accounts whose refresh tokens do not match are untouched. A
// repeated call for the same dead token is a no-op: the diff does not
// fire twice.
func (service *Service) markReauthNeeded(accountID string, version lifecycleVersion, failedRefreshToken string) {
	service.mu.Lock()
	entry := service.accounts[accountID]
	if entry == nil || entry.connState == StateSignedOut || !service.versionMatchesLocked(accountID, version) || entry.session.RefreshToken != failedRefreshToken {
		// Token equality is not a session identity: a later import can
		// legitimately return the same token after logout or re-sign-in.
		service.mu.Unlock()
		return
	}
	before := service.snapshotLocked()
	entry.connState = StateReauthNeeded
	snapshot, listeners, changed := service.diffLocked(before)
	service.mu.Unlock()
	if changed {
		service.fire(listeners, snapshot)
	}
}

// tokenReport is the acquire path's read of one account under one lock.
type tokenReport struct {
	accountID   string
	session     domain.Session
	signedIn    bool
	refreshWork bool
	refresh     *refreshGate
	version     lifecycleVersion
}

// readAccountForAcquire decides whether the named account's token can
// be served as is, must be refreshed, or does not exist. The gate
// pointer travels with the report so the caller serializes on the very
// entry it read: the account may be deleted and re-created by a login
// meanwhile, and pairing the acquire with this pointer is what keeps
// one account's refreshes single-file even then.
func (service *Service) readAccountForAcquire(accountID string) tokenReport {
	service.mu.Lock()
	defer service.mu.Unlock()
	entry := service.accounts[accountID]
	if entry == nil || entry.connState != StateSignedIn || entry.session.Empty() {
		return tokenReport{}
	}
	report := tokenReport{
		accountID: accountID,
		session:   entry.session,
		signedIn:  true,
		refresh:   &entry.refreshGate,
		version:   service.versionLocked(accountID),
	}
	if entry.invalidated || entry.session.NeedsRefresh(service.now(), refreshSkew) {
		report.refreshWork = true
	}
	return report
}

// snapshotLocked captures the observable state. Called only with mu held.
func (service *Service) snapshotLocked() Snapshot {
	login := LoginStatus{Phase: service.phase, Err: service.loginErr}
	if service.loginFlow == loginFlowDevice && (service.phase == PhaseWaiting || service.phase == PhaseExchanging) {
		login.DeviceUserCode = service.deviceUserCode
		login.DeviceVerificationURL = service.deviceVerificationURL
	}
	return Snapshot{
		Login: login,
		Conn:  service.statusLocked(),
	}
}

// statusLocked renders the connection side of the snapshot: the
// aggregate state over every account plus one row per account, sorted
// by AccountID so the diff and the UI both see a stable order. Called
// only with mu held.
func (service *Service) statusLocked() Status {
	rows := make([]AccountStatus, 0, len(service.accounts))
	for _, id := range accountIDsLocked(service.accounts) {
		entry := service.accounts[id]
		rows = append(rows, AccountStatus{
			AccountID:   id,
			Email:       entry.session.Identity.Email,
			Plan:        entry.session.Identity.Plan,
			State:       entry.connState,
			ProviderID:  entry.providerID,
			LoginMethod: string(entry.loginMethod),
			Resets:      entry.resets,
		})
	}
	return Status{
		State:    aggregateStateLocked(service.accounts),
		Accounts: rows,
	}
}

// accountIDsLocked returns the account map's keys, sorted. Called only
// with mu held.
func accountIDsLocked(accounts map[string]*account) []string {
	ids := make([]string, 0, len(accounts))
	for id := range accounts {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// aggregateStateLocked collapses the per-account states into the one
// state the provider list renders: signed in while at least one account
// is, reauth-needed when only broken accounts remain, signed out when
// nothing is left. Called only with mu held.
func aggregateStateLocked(accounts map[string]*account) ConnState {
	if len(accounts) == 0 {
		return StateSignedOut
	}
	state := StateSignedOut
	for _, entry := range accounts {
		if entry.connState == StateSignedIn {
			return StateSignedIn
		}
		if entry.connState == StateReauthNeeded {
			state = StateReauthNeeded
		}
	}
	return state
}

// commitAccountLocked lands a session as the (possibly new) entry for
// its account: one account is one row, so a re-login replaces that
// account's fields in place instead of adding a second row. The
// provider id travels with the session — "" when provisioning failed
// or was skipped. The login method records how this session arrived;
// the reset counter is deliberately untouched — it belongs to the
// account's history, not to any one session, so a re-login keeps the
// cycles the account already spent. Called only with mu held.
func (service *Service) commitAccountLocked(session domain.Session, providerID string, method LoginMethod) {
	entry, ok := service.accounts[session.Identity.AccountID]
	if !ok {
		entry = &account{}
		service.accounts[session.Identity.AccountID] = entry
	}
	service.lifecycleLocked(session.Identity.AccountID).generation++
	entry.pendingDeletion = false
	entry.session = session
	entry.connState = StateSignedIn
	entry.invalidated = false
	entry.providerID = providerID
	entry.loginMethod = method
}

// storedAccountForSave builds the record a save site persists: the
// session, the login method that produced it, and the account's current
// reset count. The save sites run outside mu, so the account facts are
// read under mu here — resets belong to the account, not the session,
// and a re-login or re-import of an identity that already exists keeps
// the cycles it already burned through, while a new identity starts at
// zero. Call before the session is committed to memory; the committed
// entry then agrees with what the disk holds.
func (service *Service) storedAccountForSave(session domain.Session, method LoginMethod) StoredAccount {
	service.mu.Lock()
	resets := 0
	if entry := service.accounts[session.Identity.AccountID]; entry != nil {
		resets = entry.resets
	}
	service.mu.Unlock()
	return StoredAccount{Session: session, LoginMethod: method, Resets: resets}
}

// diffLocked compares the observable state against a before snapshot and,
// when it changed, copies the listener list for delivery. OnChanged fires
// on transitions, not on re-assertions: a cancel whose goroutine lands on
// an already-idle phase must not re-announce idle. LoginStatus is a
// comparable all-strings struct; the connection side carries a slice, so
// its comparison is spelled out — the aggregate state plus the per-account
// rows.
func (service *Service) diffLocked(before Snapshot) (Snapshot, []func(Snapshot), bool) {
	after := service.snapshotLocked()
	if after.Login == before.Login &&
		after.Conn.State == before.Conn.State &&
		slices.Equal(after.Conn.Accounts, before.Conn.Accounts) {
		return after, nil, false
	}
	return after, append([]func(Snapshot){}, service.listeners...), true
}

// fire delivers a snapshot to the listeners, outside every lock. A
// panicking listener is skipped rather than allowed to take the auth flow
// down: listeners are UI wiring added long after this code was written,
// and one broken subscriber must not kill token refresh. There is no
// logger at this layer to report the panic to; the recover is the
// containment.
func (service *Service) fire(listeners []func(Snapshot), snapshot Snapshot) {
	for _, listener := range listeners {
		safelyNotify(listener, snapshot)
	}
}

func safelyNotify(listener func(Snapshot), snapshot Snapshot) {
	defer func() { _ = recover() }()
	listener(snapshot)
}

// randomLoginState mints the OAuth state parameter: 32 random bytes as
// hex. The state is what ties the browser round-trip to this flow.
func randomLoginState() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", errors.New("codex login state could not be generated")
	}
	return hex.EncodeToString(raw), nil
}

// randomCodeVerifier mints the PKCE verifier from an independent draw:
// deriving it from the state bytes would let one leak reconstruct the
// other. 32 bytes as unpadded base64url is 43 characters, inside the RFC
// 7636 bounds.
func randomCodeVerifier() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", errors.New("codex login verifier could not be generated")
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// codeChallenge derives the PKCE S256 challenge: the SHA-256 of the ASCII
// verifier, unpadded base64url. OpenAI's authorize endpoint only accepts
// this method for this flow.
func codeChallenge(verifier string) string {
	digest := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(digest[:])
}
