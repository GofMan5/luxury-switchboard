package application

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/codex/domain"
)

// fakeStore records every session it was asked to persist, keyed by the
// account the session itself names — plural accounts are the contract it
// stands in for. saves appends only on success so tests can assert
// exactly which sessions landed; clearCount counts Clear and ClearAll
// together so logout arithmetic stays comparable.
type fakeStore struct {
	mu          sync.Mutex
	accounts    map[string]domain.Session
	order       []string
	loadErr     error
	saveErr     error
	clearErr    error
	clearAllErr error
	// saveHook runs before a save records, parked without holding mu, so a
	// test can hold one save in flight while Logout's Clear and other
	// store traffic keep working.
	saveHook func(domain.Session)
	saves    []domain.Session
	clears   int
}

func (store *fakeStore) Load(ctx context.Context) ([]domain.Session, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.loadErr != nil {
		return nil, store.loadErr
	}
	sessions := make([]domain.Session, 0, len(store.order))
	for _, key := range store.order {
		sessions = append(sessions, store.accounts[key])
	}
	return sessions, nil
}

func (store *fakeStore) Save(ctx context.Context, session domain.Session) error {
	store.mu.Lock()
	hook := store.saveHook
	store.mu.Unlock()
	if hook != nil {
		// Deliberately outside mu: the hook parks, and a Logout that runs
		// meanwhile must reach Clear without queueing behind this save.
		hook(session)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.saveErr != nil {
		return store.saveErr
	}
	store.insertLocked(session)
	store.saves = append(store.saves, session)
	return nil
}

func (store *fakeStore) Clear(ctx context.Context, session domain.Session) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.clearErr != nil {
		return store.clearErr
	}
	key := session.Identity.AccountID
	delete(store.accounts, key)
	for i, existing := range store.order {
		if existing == key {
			store.order = append(store.order[:i], store.order[i+1:]...)
			break
		}
	}
	store.clears++
	return nil
}

func (store *fakeStore) ClearAll(ctx context.Context) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.clearAllErr != nil {
		return store.clearAllErr
	}
	store.accounts = nil
	store.order = nil
	store.clears++
	return nil
}

// insertLocked places or replaces one account; callers hold mu.
func (store *fakeStore) insertLocked(session domain.Session) {
	if store.accounts == nil {
		store.accounts = map[string]domain.Session{}
	}
	key := session.Identity.AccountID
	if _, ok := store.accounts[key]; !ok {
		store.order = append(store.order, key)
	}
	store.accounts[key] = session
}

// seed lands a stored account without going through Save: tests use it
// to place pre-existing sessions the service must Restore from.
func (store *fakeStore) seed(session domain.Session) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.insertLocked(session)
}

// setSaveHook installs (nil removes) the pre-record save hook.
func (store *fakeStore) setSaveHook(hook func(domain.Session)) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.saveHook = hook
}

// savedSessions snapshots what the service persisted, in order.
func (store *fakeStore) savedSessions() []domain.Session {
	store.mu.Lock()
	defer store.mu.Unlock()
	return append([]domain.Session{}, store.saves...)
}

// storedSessions snapshots the store's current contents, in Load order.
func (store *fakeStore) storedSessions() []domain.Session {
	sessions, _ := store.Load(context.Background())
	return sessions
}

func (store *fakeStore) wasCleared() bool {
	store.mu.Lock()
	defer store.mu.Unlock()
	return len(store.accounts) == 0
}

// clearCount reports how many successful clears the store served — Clear
// or ClearAll alike — so a test can tell the logout's clear from a
// compensation clear apart.
func (store *fakeStore) clearCount() int {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.clears
}

// exchangeCall pins the pair the service handed the authorizer: the code
// from the loopback redirect and the PKCE verifier it belongs with.
type exchangeCall struct {
	code     string
	verifier string
}

// fakeAuthorizer records calls and lets tests flip its behavior mid-test
// through mu-guarded setters, so the fakes themselves stay race-clean.
type fakeAuthorizer struct {
	mu          sync.Mutex
	session     domain.Session
	exchangeErr error
	refreshErr  error
	// refreshBlock, when set, is what a Refresh call parks on outside mu:
	// the test holds one refresh in flight while a login or another
	// acquire proceeds — they must not queue on this mu behind it.
	refreshBlock  chan struct{}
	exchangeCalls []exchangeCall
	refreshCalls  []string
	states        []string
	challenges    []string
	// deviceStartErr makes RequestDeviceUserCode fail before any flow
	// state exists; the service must surface it and change nothing.
	deviceStartErr error
	// deviceAwaitErr and deviceAuthorization answer the poll; the
	// zero authorization means "grant the default trio", so the default
	// fake lets a device login finish without any setup.
	deviceAwaitErr      error
	deviceAuthorization DeviceAuthorization
	// deviceBlock, like refreshBlock, parks the poll outside mu so the
	// test can flip the answer or end the flow while it is held.
	deviceBlock     chan struct{}
	deviceStarts    int
	deviceExchanges []DeviceAuthorization
	// usageScript scripts FetchUsage answers, consumed in order; once the
	// script runs dry, the usage/usageErr pair stands. usageBlock, like
	// refreshBlock, parks a probe outside mu so a test can hold one
	// probe in flight while a second caller asks or the first cancels.
	usageScript []usageAnswer
	usage       domain.Usage
	usageErr    error
	usageBlock  chan struct{}
	usageCalls  int
	usageTokens []string
	usageIDs    []string
}

func (authorizer *fakeAuthorizer) AuthorizeURL(state, codeChallenge string) string {
	authorizer.mu.Lock()
	defer authorizer.mu.Unlock()
	authorizer.states = append(authorizer.states, state)
	authorizer.challenges = append(authorizer.challenges, codeChallenge)
	return "https://auth.openai.test/authorize?state=" + state + "&code_challenge=" + codeChallenge
}

func (authorizer *fakeAuthorizer) ExchangeCode(ctx context.Context, code, codeVerifier string) (domain.Session, error) {
	authorizer.mu.Lock()
	defer authorizer.mu.Unlock()
	authorizer.exchangeCalls = append(authorizer.exchangeCalls, exchangeCall{code: code, verifier: codeVerifier})
	if authorizer.exchangeErr != nil {
		return domain.Session{}, authorizer.exchangeErr
	}
	return authorizer.session, nil
}

func (authorizer *fakeAuthorizer) Refresh(ctx context.Context, refreshToken string) (domain.Session, error) {
	authorizer.mu.Lock()
	authorizer.refreshCalls = append(authorizer.refreshCalls, refreshToken)
	block := authorizer.refreshBlock
	err := authorizer.refreshErr
	session := authorizer.session
	authorizer.mu.Unlock()
	if block != nil {
		// Parked outside mu: re-snapshot afterwards, so a test can flip the
		// result while the refresh is held open.
		<-block
		authorizer.mu.Lock()
		err = authorizer.refreshErr
		session = authorizer.session
		authorizer.mu.Unlock()
	}
	if err != nil {
		return domain.Session{}, err
	}
	return session, nil
}

// setRefreshBlock installs (nil removes) the park a Refresh call waits on.
func (authorizer *fakeAuthorizer) setRefreshBlock(block chan struct{}) {
	authorizer.mu.Lock()
	defer authorizer.mu.Unlock()
	authorizer.refreshBlock = block
}

// setRefreshResult swaps what Refresh answers, for the tests whose point
// is a specific rotation: the default response is a full validSession.
// An access-only rotation — OpenAI may return no refresh token, id token
// or identity of its own — is injected here to exercise the merge
// fallback.
func (authorizer *fakeAuthorizer) setRefreshResult(session domain.Session, err error) {
	authorizer.mu.Lock()
	defer authorizer.mu.Unlock()
	authorizer.session = session
	authorizer.refreshErr = err
}

func (authorizer *fakeAuthorizer) exchanges() []exchangeCall {
	authorizer.mu.Lock()
	defer authorizer.mu.Unlock()
	return append([]exchangeCall{}, authorizer.exchangeCalls...)
}

func (authorizer *fakeAuthorizer) refreshes() []string {
	authorizer.mu.Lock()
	defer authorizer.mu.Unlock()
	return append([]string{}, authorizer.refreshCalls...)
}

func (authorizer *fakeAuthorizer) recordedStates() []string {
	authorizer.mu.Lock()
	defer authorizer.mu.Unlock()
	return append([]string{}, authorizer.states...)
}

func (authorizer *fakeAuthorizer) recordedChallenges() []string {
	authorizer.mu.Lock()
	defer authorizer.mu.Unlock()
	return append([]string{}, authorizer.challenges...)
}

// usageAnswer is one scripted usage-probe answer.
type usageAnswer struct {
	usage domain.Usage
	err   error
}

// FetchUsage records the pair the service probed with and answers from
// the script, the standing result, or the park. The park honours its
// ctx, so a caller that cancels mid-probe gets the wrapped cancellation
// the real adapter would return.
func (authorizer *fakeAuthorizer) FetchUsage(ctx context.Context, accessToken, accountID string) (domain.Usage, error) {
	authorizer.mu.Lock()
	authorizer.usageCalls++
	authorizer.usageTokens = append(authorizer.usageTokens, accessToken)
	authorizer.usageIDs = append(authorizer.usageIDs, accountID)
	if len(authorizer.usageScript) > 0 {
		answer := authorizer.usageScript[0]
		authorizer.usageScript = authorizer.usageScript[1:]
		authorizer.usage, authorizer.usageErr = answer.usage, answer.err
	}
	block := authorizer.usageBlock
	usage := authorizer.usage
	err := authorizer.usageErr
	authorizer.mu.Unlock()
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return domain.Usage{}, fmt.Errorf("usage probe: %w", ctx.Err())
		}
	}
	if err != nil {
		return domain.Usage{}, err
	}
	return usage, nil
}

// setUsageResult swaps the standing FetchUsage answer.
func (authorizer *fakeAuthorizer) setUsageResult(usage domain.Usage, err error) {
	authorizer.mu.Lock()
	defer authorizer.mu.Unlock()
	authorizer.usage, authorizer.usageErr = usage, err
}

// setUsageScript installs a scripted FetchUsage answer queue.
func (authorizer *fakeAuthorizer) setUsageScript(script []usageAnswer) {
	authorizer.mu.Lock()
	defer authorizer.mu.Unlock()
	authorizer.usageScript = script
}

// setUsageBlock installs (nil removes) the park a FetchUsage call waits on.
func (authorizer *fakeAuthorizer) setUsageBlock(block chan struct{}) {
	authorizer.mu.Lock()
	defer authorizer.mu.Unlock()
	authorizer.usageBlock = block
}

// usageProbeCount reports how many probes the service has issued.
func (authorizer *fakeAuthorizer) usageProbeCount() int {
	authorizer.mu.Lock()
	defer authorizer.mu.Unlock()
	return authorizer.usageCalls
}

// usageSeen reports the access tokens and account ids the service probed with.
func (authorizer *fakeAuthorizer) usageSeen() (tokens, ids []string) {
	authorizer.mu.Lock()
	defer authorizer.mu.Unlock()
	return append([]string{}, authorizer.usageTokens...), append([]string{}, authorizer.usageIDs...)
}

// RequestDeviceUserCode answers the device flow's first hop. The default
// user code mirrors what the real endpoint hands back: a pair the user
// types plus the poll cadence the service clamps, never obeys raw.
func (authorizer *fakeAuthorizer) RequestDeviceUserCode(ctx context.Context) (DeviceUserCode, error) {
	authorizer.mu.Lock()
	defer authorizer.mu.Unlock()
	authorizer.deviceStarts++
	if authorizer.deviceStartErr != nil {
		return DeviceUserCode{}, authorizer.deviceStartErr
	}
	return DeviceUserCode{
		DeviceAuthID:    "device-auth-1",
		UserCode:        "WLXB-DQK2",
		VerificationURL: "https://auth.openai.test/codex/device",
		PollInterval:    0,
	}, nil
}

// AwaitDeviceAuthorization stands in for the endpoint's poll. Without a
// block it answers at once — the default grant — and with one it parks
// outside mu, honouring the flow's deadline so timeout tests end.
func (authorizer *fakeAuthorizer) AwaitDeviceAuthorization(ctx context.Context, start DeviceUserCode) (DeviceAuthorization, error) {
	authorizer.mu.Lock()
	block := authorizer.deviceBlock
	err := authorizer.deviceAwaitErr
	authorization := authorizer.deviceAuthorization
	authorizer.mu.Unlock()
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return DeviceAuthorization{}, ctx.Err()
		}
	}
	if err != nil {
		return DeviceAuthorization{}, err
	}
	if authorization == (DeviceAuthorization{}) {
		authorization = DeviceAuthorization{
			AuthorizationCode: "device-auth-code",
			CodeVerifier:      "device-verifier",
			CodeChallenge:     "device-challenge",
		}
	}
	return authorization, nil
}

// ExchangeDeviceCode trades the granted authorization for the session the
// same way ExchangeCode does, so exchange failures are set up once.
func (authorizer *fakeAuthorizer) ExchangeDeviceCode(ctx context.Context, authorization DeviceAuthorization) (domain.Session, error) {
	authorizer.mu.Lock()
	defer authorizer.mu.Unlock()
	authorizer.deviceExchanges = append(authorizer.deviceExchanges, authorization)
	if authorizer.exchangeErr != nil {
		return domain.Session{}, authorizer.exchangeErr
	}
	return authorizer.session, nil
}

// setDeviceAwaitResult swaps what the poll answers, for tests whose point
// is a grant that arrives, fails, or is denied mid-flight.
func (authorizer *fakeAuthorizer) setDeviceAwaitResult(authorization DeviceAuthorization, err error) {
	authorizer.mu.Lock()
	defer authorizer.mu.Unlock()
	authorizer.deviceAuthorization = authorization
	authorizer.deviceAwaitErr = err
}

// setDeviceBlock installs (nil removes) the park a poll waits on.
func (authorizer *fakeAuthorizer) setDeviceBlock(block chan struct{}) {
	authorizer.mu.Lock()
	defer authorizer.mu.Unlock()
	authorizer.deviceBlock = block
}

func (authorizer *fakeAuthorizer) deviceUserCodeRequests() int {
	authorizer.mu.Lock()
	defer authorizer.mu.Unlock()
	return authorizer.deviceStarts
}

func (authorizer *fakeAuthorizer) deviceExchanged() []DeviceAuthorization {
	authorizer.mu.Lock()
	defer authorizer.mu.Unlock()
	return append([]DeviceAuthorization{}, authorizer.deviceExchanges...)
}

// fakeRedirects owns the loopback side. The default AwaitCode just
// honours cancellation, standing in for "the browser never redirected".
type fakeRedirects struct {
	mu       sync.Mutex
	starts   []string
	stops    int
	startErr error
	awaitFn  func(ctx context.Context) (string, error)
}

func (redirects *fakeRedirects) Start(expectedState string) error {
	redirects.mu.Lock()
	defer redirects.mu.Unlock()
	if redirects.startErr != nil {
		return redirects.startErr
	}
	redirects.starts = append(redirects.starts, expectedState)
	return nil
}

func (redirects *fakeRedirects) AwaitCode(ctx context.Context) (string, error) {
	redirects.mu.Lock()
	awaitFn := redirects.awaitFn
	redirects.mu.Unlock()
	if awaitFn == nil {
		<-ctx.Done()
		return "", ctx.Err()
	}
	return awaitFn(ctx)
}

func (redirects *fakeRedirects) Stop() {
	redirects.mu.Lock()
	defer redirects.mu.Unlock()
	redirects.stops++
}

// setAwaitFn swaps the loopback answer mid-test; the default blocks on
// cancellation, standing in for "the browser never redirected".
func (redirects *fakeRedirects) setAwaitFn(await func(ctx context.Context) (string, error)) {
	redirects.mu.Lock()
	defer redirects.mu.Unlock()
	redirects.awaitFn = await
}

func (redirects *fakeRedirects) startedStates() []string {
	redirects.mu.Lock()
	defer redirects.mu.Unlock()
	return append([]string{}, redirects.starts...)
}

func (redirects *fakeRedirects) stopCount() int {
	redirects.mu.Lock()
	defer redirects.mu.Unlock()
	return redirects.stops
}

// fakeProvisioner records every identity the service registered and
// which release mode Logout picked. Successful Ensure calls mint
// distinct provider ids the way the real provisioner does — codex,
// codex2, codex3… — so multi-account logins land on separate slots,
// unless the test scripted an explicit providerIDs queue.
type fakeProvisioner struct {
	mu          sync.Mutex
	ensureErr   error
	retireErr   error
	removeErr   error
	providerIDs []string
	nextMint    int
	ensured     []domain.Identity
	ensuredIDs  []string
	retiredIDs  []string
	removedIDs  []string
	retired     int
	removed     int
}

func (provisioner *fakeProvisioner) EnsureCodexProvider(ctx context.Context, identity domain.Identity) (string, error) {
	provisioner.mu.Lock()
	defer provisioner.mu.Unlock()
	provisioner.ensured = append(provisioner.ensured, identity)
	if provisioner.ensureErr != nil {
		return "", provisioner.ensureErr
	}
	var providerID string
	if len(provisioner.providerIDs) > 0 {
		providerID = provisioner.providerIDs[0]
		provisioner.providerIDs = provisioner.providerIDs[1:]
	} else {
		provisioner.nextMint++
		if provisioner.nextMint == 1 {
			providerID = CodexProviderID
		} else {
			providerID = fmt.Sprintf("%s%d", CodexProviderID, provisioner.nextMint)
		}
	}
	provisioner.ensuredIDs = append(provisioner.ensuredIDs, providerID)
	return providerID, nil
}

func (provisioner *fakeProvisioner) RetireCodexProvider(ctx context.Context, accountID string) error {
	provisioner.mu.Lock()
	defer provisioner.mu.Unlock()
	provisioner.retired++
	provisioner.retiredIDs = append(provisioner.retiredIDs, accountID)
	return provisioner.retireErr
}

func (provisioner *fakeProvisioner) RemoveCodexProvider(ctx context.Context, accountID string) error {
	provisioner.mu.Lock()
	defer provisioner.mu.Unlock()
	provisioner.removed++
	provisioner.removedIDs = append(provisioner.removedIDs, accountID)
	return provisioner.removeErr
}

func (provisioner *fakeProvisioner) ensuredIdentities() []domain.Identity {
	provisioner.mu.Lock()
	defer provisioner.mu.Unlock()
	return append([]domain.Identity{}, provisioner.ensured...)
}

// ensuredProviderIDs snapshots the ids successful Ensure calls returned,
// in order — one row per sign-in, codex then codex2.
func (provisioner *fakeProvisioner) ensuredProviderIDs() []string {
	provisioner.mu.Lock()
	defer provisioner.mu.Unlock()
	return append([]string{}, provisioner.ensuredIDs...)
}

// retiredAccountIDs snapshots the account ids Logout asked to retire.
func (provisioner *fakeProvisioner) retiredAccountIDs() []string {
	provisioner.mu.Lock()
	defer provisioner.mu.Unlock()
	return append([]string{}, provisioner.retiredIDs...)
}

// removedAccountIDs snapshots the account ids Logout asked to remove.
func (provisioner *fakeProvisioner) removedAccountIDs() []string {
	provisioner.mu.Lock()
	defer provisioner.mu.Unlock()
	return append([]string{}, provisioner.removedIDs...)
}

func (provisioner *fakeProvisioner) retireCount() int {
	provisioner.mu.Lock()
	defer provisioner.mu.Unlock()
	return provisioner.retired
}

func (provisioner *fakeProvisioner) removeCount() int {
	provisioner.mu.Lock()
	defer provisioner.mu.Unlock()
	return provisioner.removed
}

// fakeParser answers with whatever the test installed: candidates for
// what the import found, an error for text the parser rejected. The
// default text finds nothing, which keeps the import tests explicit
// about what they feed the service.
type fakeParser struct {
	mu         sync.Mutex
	candidates []CredentialCandidate
	err        error
	texts      []string
}

func (parser *fakeParser) ParseCredentials(text string) ([]CredentialCandidate, error) {
	parser.mu.Lock()
	defer parser.mu.Unlock()
	parser.texts = append(parser.texts, text)
	return append([]CredentialCandidate{}, parser.candidates...), parser.err
}

func (parser *fakeParser) parsedTexts() []string {
	parser.mu.Lock()
	defer parser.mu.Unlock()
	return append([]string{}, parser.texts...)
}

// fakeFiles stands in for the adapter that reads auth files: contents by
// path, failures by path, and the order the service asked for them.
type fakeFiles struct {
	mu       sync.Mutex
	contents map[string]string
	failures map[string]error
	reads    []string
}

func (files *fakeFiles) ReadAuthFile(path string) (string, error) {
	files.mu.Lock()
	defer files.mu.Unlock()
	files.reads = append(files.reads, path)
	if err, ok := files.failures[path]; ok {
		return "", err
	}
	return files.contents[path], nil
}

func (files *fakeFiles) readPaths() []string {
	files.mu.Lock()
	defer files.mu.Unlock()
	return append([]string{}, files.reads...)
}

// fakeClock lets tests hold time still while expiry math runs.
type fakeClock struct {
	mu      sync.Mutex
	current time.Time
}

func (clock *fakeClock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	return clock.current
}

func (clock *fakeClock) advance(by time.Duration) {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	clock.current = clock.current.Add(by)
}

// recorder collects every snapshot OnChanged delivered.
type recorder struct {
	mu     sync.Mutex
	events []Snapshot
}

func (rec *recorder) OnChanged(snapshot Snapshot) {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	rec.events = append(rec.events, snapshot)
}

func (rec *recorder) snapshots() []Snapshot {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return append([]Snapshot{}, rec.events...)
}

// testEnv wires the service against all six fakes. The backoffs are
// zeroed so the ticker's retry budget spends no wall-clock time in tests;
// the login timeout and refresh interval are shortened per-test when the
// behavior under test is time itself.
type testEnv struct {
	service     *Service
	store       *fakeStore
	authorizer  *fakeAuthorizer
	redirects   *fakeRedirects
	provisioner *fakeProvisioner
	parser      *fakeParser
	files       *fakeFiles
	clock       *fakeClock
	events      *recorder
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	store := &fakeStore{}
	authorizer := &fakeAuthorizer{session: validSession("user@example.com")}
	redirects := &fakeRedirects{}
	provisioner := &fakeProvisioner{}
	parser := &fakeParser{}
	files := &fakeFiles{}
	clock := &fakeClock{current: time.Now()}
	service := NewService(store, authorizer, redirects, provisioner, parser, files, clock.Now)
	events := &recorder{}
	service.OnChanged(events.OnChanged)
	env := &testEnv{
		service:     service,
		store:       store,
		authorizer:  authorizer,
		redirects:   redirects,
		provisioner: provisioner,
		parser:      parser,
		files:       files,
		clock:       clock,
		events:      events,
	}
	env.service.refreshBackoffs = []time.Duration{0, 0}
	return env
}

// validSession is a session whose access token is comfortably fresh.
func validSession(email string) domain.Session {
	return domain.Session{
		AccessToken:  "access-" + email,
		RefreshToken: "refresh-" + email,
		IDToken:      "id-" + email,
		AccessExpiry: time.Now().Add(time.Hour),
		Identity: domain.Identity{
			Email:          email,
			ChatGPTUserID:  "user-1",
			Plan:           "plus",
			AccountID:      "account-1",
			OrganizationID: "org-1",
		},
	}
}

// staleSession has the same shape with an already-expired access token.
func staleSession() domain.Session {
	session := validSession("user@example.com")
	session.AccessExpiry = time.Now().Add(-10 * time.Minute)
	return session
}

// accountStatus finds one account's row in a Status: multi-account tests
// address their rows by account id, never by position.
func accountStatus(t *testing.T, status Status, accountID string) AccountStatus {
	t.Helper()
	for _, account := range status.Accounts {
		if account.AccountID == accountID {
			return account
		}
	}
	t.Fatalf("Status().Accounts has no %q: %+v", accountID, status.Accounts)
	return AccountStatus{}
}

// waitFor polls a condition with a deadline instead of sleeping blindly:
// a deadlock surfaces as this timeout, and happy paths finish in the
// first few polls.
func waitFor(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

// signIn drives a full successful login: the redirect server answers the
// code, the flow runs to PhaseSuccess.
func (env *testEnv) signIn(t *testing.T) string {
	t.Helper()
	env.redirects.setAwaitFn(func(ctx context.Context) (string, error) {
		return "the-auth-code", nil
	})
	authorizeURL, err := env.service.LoginStart()
	if err != nil {
		t.Fatalf("LoginStart() failed: %v", err)
	}
	waitFor(t, "login success", func() bool {
		return env.service.LoginStatus().Phase == PhaseSuccess
	})
	return authorizeURL
}

// restore runs Restore with the test's lifetime as the loop context.
func (env *testEnv) restore(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := env.service.Restore(ctx); err != nil {
		t.Fatalf("Restore() failed: %v", err)
	}
}

func TestAListenerMayQueryTheServiceWhileBeingNotified(t *testing.T) {
	env := newTestEnv(t)
	queried := make(chan struct{}, 8)
	// A listener that immediately queries back is the UI's actual
	// behavior; notifying under a lock would deadlock right here.
	env.service.OnChanged(func(snapshot Snapshot) {
		env.service.Status()
		env.service.LoginStatus()
		queried <- struct{}{}
	})

	env.signIn(t)

	// The login produced two notifications (waiting, then success); both
	// must have run their queries to completion for the flow to finish.
	seen := 0
	for seen < 2 {
		select {
		case <-queried:
			seen++
		case <-time.After(3 * time.Second):
			t.Fatalf("listener did not run: only %d of 2 queries completed", seen)
		}
	}
}

func TestConcurrentAcquiresInvalidationsAndStatusQueriesStayConsistent(t *testing.T) {
	env := newTestEnv(t)
	// A stale session with a live refresh token: every acquire races the
	// others into the single-flight refresh.
	env.store.seed(staleSession())
	env.service.refreshInterval = 5 * time.Millisecond
	env.restore(t)

	const goroutines = 8
	const rounds = 25
	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < rounds; j++ {
				_, _ = env.service.AcquireAccessToken(context.Background(), "account-1")
				env.service.InvalidateAccessToken("access-user@example.com")
				env.service.Status()
				env.service.LoginStatus()
			}
		}()
	}
	wg.Wait()

	// Every thread asked for a token concurrently and none of the state
	// queries may end in a torn state: the connection stays signed in.
	if state := env.service.Status().State; state != StateSignedIn {
		t.Fatalf("State = %q after concurrent use, want %q", state, StateSignedIn)
	}
	if refreshes := env.authorizer.refreshes(); len(refreshes) == 0 {
		t.Fatal("no refresh ever ran under concurrency")
	}
}

// A logout from a live session retires the provider entry (it stays,
// disabled and unbound, for a later sign-in to relink to); a logout with
// no live session removes the leftovers instead. The mode is picked from
// the connection state, not from what happened to be provisioned.
func TestLogoutRetiresALiveSessionButRemovesLeftovers(t *testing.T) {
	env := newTestEnv(t)

	if err := env.service.Logout(context.Background(), "", false); err != nil {
		t.Fatalf("Logout() with no live account error = %v, want nil", err)
	}
	if retired := env.provisioner.retireCount(); retired != 0 {
		t.Fatalf("retireCount() = %d, want 0: no live account means remove", retired)
	}
	if removed := env.provisioner.removeCount(); removed != 1 {
		t.Fatalf("removeCount() = %d, want 1", removed)
	}

	env.signIn(t)
	if err := env.service.Logout(context.Background(), "", false); err != nil {
		t.Fatalf("Logout() with a live account error = %v, want nil", err)
	}
	if retired := env.provisioner.retireCount(); retired != 1 {
		t.Fatalf("retireCount() = %d, want 1: a live account means retire", retired)
	}
	if removed := env.provisioner.removeCount(); removed != 1 {
		t.Fatalf("removeCount() = %d, want still 1: a live account must not remove", removed)
	}
	if conn := env.service.Status(); conn.State != StateSignedOut {
		t.Fatalf("Status().State = %q, want %q", conn.State, StateSignedOut)
	}
}

// An access-only rotation — OpenAI answered the refresh with no new
// refresh token, id token or identity — must still hand out the fresh
// access token exactly as the endpoint delivered it, and the next
// refresh must present the refresh token the service already owned, not
// the empty one the rotation declined to return.
func TestAnAccessOnlyRotationKeepsTheStoredRefreshTokenAndIdentity(t *testing.T) {
	env := newTestEnv(t)
	env.authorizer.setRefreshResult(domain.Session{
		AccessToken:  "rotated-access",
		RefreshToken: "",
		IDToken:      "",
		AccessExpiry: time.Now().Add(time.Hour),
	}, nil)
	env.store.seed(staleSession())
	env.restore(t)

	token, err := env.service.AcquireAccessToken(context.Background(), "account-1")
	if err != nil {
		t.Fatalf("AcquireAccessToken() error = %v, want nil", err)
	}
	if token != "rotated-access" {
		t.Fatalf("AcquireAccessToken() = %q, want the adapter-delivered rotated token", token)
	}

	// The rotation answered no identity of its own: the account stays the
	// one the stored id token described.
	conn := env.service.Status()
	if conn.State != StateSignedIn {
		t.Fatalf("Status().State = %q, want %q", conn.State, StateSignedIn)
	}
	if account := accountStatus(t, conn, "account-1"); account.Email != "user@example.com" {
		t.Fatalf("account email = %q, want the one the stored id token described", account.Email)
	}

	// Force the next acquire through the refresh path again: it must still
	// present the stored refresh token, because the access-only rotation
	// must not have blanked it.
	env.service.InvalidateAccessToken("rotated-access")
	second, err := env.service.AcquireAccessToken(context.Background(), "account-1")
	if err != nil {
		t.Fatalf("second AcquireAccessToken() error = %v, want nil", err)
	}
	if second != "rotated-access" {
		t.Fatalf("second AcquireAccessToken() = %q, want %q", second, "rotated-access")
	}
	refreshes := env.authorizer.refreshes()
	if len(refreshes) != 2 {
		t.Fatalf("Refresh called %d times, want 2", len(refreshes))
	}
	for i, presented := range refreshes {
		if presented != "refresh-user@example.com" {
			t.Fatalf("refresh %d presented %q, want the stored refresh token", i+1, presented)
		}
	}
}

// The ticker must react to a refresh that comes back dead: the session
// moves to reauth_needed, the UI learns who has to sign in again, and no
// retry budget is burned on a token the endpoint already rejected.
// A rotation that carries no date must not read as "always refresh"
// from then on: the merged session keeps the previous expiry, so the
// next acquire is served from memory instead of burning a token-endpoint
// round-trip on every request.
func TestADatelessRotationKeepsThePreviousExpiry(t *testing.T) {
	env := newTestEnv(t)
	env.authorizer.setRefreshResult(domain.Session{
		AccessToken:  "rotated-access",
		RefreshToken: "rotated-refresh",
	}, nil)
	env.store.seed(validSession("user@example.com"))
	env.restore(t)

	// The invalidate is what sends the first acquire to the token
	// endpoint; the rotation that answers it has no expiry of its own.
	env.service.InvalidateAccessToken("access-user@example.com")
	first, err := env.service.AcquireAccessToken(context.Background(), "account-1")
	if err != nil {
		t.Fatalf("AcquireAccessToken() error = %v, want nil", err)
	}
	if first != "rotated-access" {
		t.Fatalf("AcquireAccessToken() = %q, want %q", first, "rotated-access")
	}

	second, err := env.service.AcquireAccessToken(context.Background(), "account-1")
	if err != nil {
		t.Fatalf("second AcquireAccessToken() error = %v, want nil", err)
	}
	if second != "rotated-access" {
		t.Fatalf("second AcquireAccessToken() = %q, want %q", second, "rotated-access")
	}
	if refreshes := env.authorizer.refreshes(); len(refreshes) != 1 {
		t.Fatalf("Refresh called %d times, want 1: the kept date is the whole point", len(refreshes))
	}

	saves := env.store.savedSessions()
	if len(saves) == 0 {
		t.Fatal("the rotation was never stored")
	}
	if saves[len(saves)-1].AccessExpiry.IsZero() {
		t.Fatal("stored rotation kept a zero access expiry, want the previous date")
	}
}

// The upstream named the access token itself as revoked: no rotation of
// the same session can serve another request, so the acquire path must
// refuse before any refresh runs, and only a fresh login clears it.
func TestARevokedAccessTokenStopsServingUntilASignIn(t *testing.T) {
	env := newTestEnv(t)
	env.authorizer.setRefreshResult(domain.Session{
		AccessToken:  "rotated-access",
		RefreshToken: "rotated-refresh",
		AccessExpiry: time.Now().Add(time.Hour),
	}, nil)
	env.store.seed(staleSession())
	env.restore(t)

	token, err := env.service.AcquireAccessToken(context.Background(), "account-1")
	if err != nil {
		t.Fatalf("AcquireAccessToken() error = %v, want nil", err)
	}
	if token != "rotated-access" {
		t.Fatalf("AcquireAccessToken() = %q, want %q", token, "rotated-access")
	}

	env.service.RejectAccessToken("rotated-access")

	conn := env.service.Status()
	if conn.State != StateReauthNeeded {
		t.Fatalf("Status().State = %q, want %q", conn.State, StateReauthNeeded)
	}
	if account := accountStatus(t, conn, "account-1"); account.Email != "user@example.com" {
		t.Fatalf("account email = %q, want the identity kept for the re-sign-in dialog", account.Email)
	}

	// The failure loop from before the fix — acquire, refresh, serve the
	// doomed token, 401, repeat — must not start: the acquire refuses
	// outright and no second refresh is minted.
	if _, err := env.service.AcquireAccessToken(context.Background(), "account-1"); !errors.Is(err, errNotSignedIn) {
		t.Fatalf("AcquireAccessToken() error = %v, want errNotSignedIn", err)
	}
	if refreshes := env.authorizer.refreshes(); len(refreshes) != 1 {
		t.Fatalf("Refresh called %d times, want 1: a revoked session must not mint another token", len(refreshes))
	}

	// A fresh login replaces the session and the verdict with it.
	env.authorizer.setRefreshResult(validSession("user@example.com"), nil)
	env.signIn(t)
	if conn := env.service.Status(); conn.State != StateSignedIn {
		t.Fatalf("after a re-login Status().State = %q, want %q", conn.State, StateSignedIn)
	}
	served, err := env.service.AcquireAccessToken(context.Background(), "account-1")
	if err != nil {
		t.Fatalf("AcquireAccessToken() after re-login error = %v, want nil", err)
	}
	if served != "access-user@example.com" {
		t.Fatalf("AcquireAccessToken() after re-login = %q, want the logged-in token", served)
	}
}

// A revocation is a statement about one token: one that names a token
// nobody serves any more — the superseded token of a rotation that
// already landed, or a session that no longer exists — is inert.
func TestAStaleRevocationCannotSignOutANewerSession(t *testing.T) {
	env := newTestEnv(t)
	env.authorizer.setRefreshResult(domain.Session{
		AccessToken:  "rotated-access",
		RefreshToken: "rotated-refresh",
		AccessExpiry: time.Now().Add(time.Hour),
	}, nil)
	env.store.seed(staleSession())
	env.restore(t)

	token, err := env.service.AcquireAccessToken(context.Background(), "account-1")
	if err != nil {
		t.Fatalf("AcquireAccessToken() error = %v, want nil", err)
	}
	if token != "rotated-access" {
		t.Fatalf("AcquireAccessToken() = %q, want %q", token, "rotated-access")
	}

	// The 401 that started this rotation arrives back from the dead
	// after the rotation already landed: it names the old token.
	env.service.RejectAccessToken("access-user@example.com")
	if conn := env.service.Status(); conn.State != StateSignedIn {
		t.Fatalf("Status().State = %q, want %q: a stale revocation must not clobber the rotation", conn.State, StateSignedIn)
	}
	served, err := env.service.AcquireAccessToken(context.Background(), "account-1")
	if err != nil {
		t.Fatalf("AcquireAccessToken() after a stale revocation error = %v, want nil", err)
	}
	if served != "rotated-access" {
		t.Fatalf("AcquireAccessToken() after a stale revocation = %q, want %q", served, "rotated-access")
	}

	env.service.RejectAccessToken("a-token-nobody-served")
	if conn := env.service.Status(); conn.State != StateSignedIn {
		t.Fatalf("Status().State = %q, want %q: a revocation for an unknown token is inert", conn.State, StateSignedIn)
	}
	if refreshes := env.authorizer.refreshes(); len(refreshes) != 1 {
		t.Fatalf("Refresh called %d times, want 1: inert revocations trigger no work", len(refreshes))
	}
}

// A revocation that lands while a rotation is already in flight wins
// over that rotation: the refresh commits its tokens (the endpoint
// rotated them; memory must serve what the server knows), but the
// connection stays in reauth_needed — the verdict is not undone by a
// session it did not describe, and no further token is served from it.
func TestARevocationThatLandsMidRotationLeavesTheNewSessionUnserved(t *testing.T) {
	env := newTestEnv(t)
	env.store.seed(staleSession())
	env.restore(t)

	block := make(chan struct{})
	env.authorizer.setRefreshBlock(block)
	acquireDone := make(chan struct {
		token string
		err   error
	}, 1)
	go func() {
		token, err := env.service.AcquireAccessToken(context.Background(), "account-1")
		acquireDone <- struct {
			token string
			err   error
		}{token, err}
	}()
	waitFor(t, "the mid-flight rotation to start", func() bool {
		return len(env.authorizer.refreshes()) >= 1
	})

	// The 401 with the revocation verdict arrives while the old token is
	// still the live one, so the verdict applies.
	env.service.RejectAccessToken("access-user@example.com")
	if conn := env.service.Status(); conn.State != StateReauthNeeded {
		t.Fatalf("Status().State = %q, want %q", conn.State, StateReauthNeeded)
	}

	// The rotation answers after the verdict already landed.
	env.authorizer.setRefreshResult(domain.Session{
		AccessToken:  "rotated-access",
		RefreshToken: "rotated-refresh",
		AccessExpiry: time.Now().Add(time.Hour),
	}, nil)
	close(block)

	select {
	case result := <-acquireDone:
		if result.err != nil {
			t.Fatalf("AcquireAccessToken() error = %v, want nil: the in-flight rotation serves its caller", result.err)
		}
		if result.token != "rotated-access" {
			t.Fatalf("AcquireAccessToken() = %q, want the in-flight rotation's token", result.token)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for the mid-flight acquire")
	}

	if conn := env.service.Status(); conn.State != StateReauthNeeded {
		t.Fatalf("Status().State = %q, want %q: a landed rotation must not undo the verdict", conn.State, StateReauthNeeded)
	}
	if _, err := env.service.AcquireAccessToken(context.Background(), "account-1"); !errors.Is(err, errNotSignedIn) {
		t.Fatalf("AcquireAccessToken() error = %v, want errNotSignedIn: the rotated session stays unserved", err)
	}
}

func TestTheTickerMarksAStaleSessionForReSignIn(t *testing.T) {
	env := newTestEnv(t)
	env.service.refreshInterval = 5 * time.Millisecond
	env.authorizer.setRefreshResult(domain.Session{}, errors.New("invalid_grant"))
	env.store.seed(staleSession())
	env.restore(t)

	waitFor(t, "the ticker to mark the session for re-sign-in", func() bool {
		return env.service.Status().State == StateReauthNeeded
	})

	// The identity survives the flip: the dialog must still be able to
	// say who needs to sign in again.
	conn := env.service.Status()
	if account := accountStatus(t, conn, "account-1"); account.Email != "user@example.com" {
		t.Fatalf("account-1 email = %q, want user@example.com after the flip", account.Email)
	}
	seen := false
	for _, snapshot := range env.events.snapshots() {
		if snapshot.Conn.State == StateReauthNeeded {
			seen = true
			if accountStatus(t, snapshot.Conn, "account-1").Email != "user@example.com" {
				t.Fatalf("reauth_needed snapshot = %+v, want the identity intact", snapshot.Conn)
			}
		}
	}
	if !seen {
		t.Fatal("no reauth_needed snapshot was delivered to listeners")
	}
	if refreshes := env.authorizer.refreshes(); len(refreshes) != 1 {
		t.Fatalf("Refresh called %d times, want 1: a reauth verdict settles the cycle", len(refreshes))
	}
}

// A session with no refresh token cannot be renewed by anyone: the
// acquire path must say "sign in" without presenting an empty token to
// the endpoint, and must record that the account needs re-auth.
func TestAnUnrefreshableRestoredSessionAsksForSignIn(t *testing.T) {
	env := newTestEnv(t)
	session := validSession("user@example.com")
	session.RefreshToken = ""
	session.AccessExpiry = time.Now().Add(-10 * time.Minute)
	env.store.seed(session)
	env.restore(t)

	_, err := env.service.AcquireAccessToken(context.Background(), "account-1")
	if !errors.Is(err, errNeedsSignIn) {
		t.Fatalf("AcquireAccessToken() error = %v, want errNeedsSignIn", err)
	}
	if state := env.service.Status().State; state != StateReauthNeeded {
		t.Fatalf("State = %q, want %q", state, StateReauthNeeded)
	}
	if refreshes := env.authorizer.refreshes(); len(refreshes) != 0 {
		t.Fatalf("Refresh called %d times, want 0: an empty refresh token is never presented", len(refreshes))
	}
}

// The stale-refresh clobber: a refresh in flight on the OLD token when a
// fresh login lands must not drag the new session down with it when the
// old token's verdict comes back. The failure belongs to the old token,
// and the state that replaced it is what stays.
func TestAStaleRefreshFailureDoesNotClobberAFreshLogin(t *testing.T) {
	env := newTestEnv(t)
	stale := staleSession()
	stale.RefreshToken = "old-refresh"
	env.store.seed(stale)
	env.restore(t)

	block := make(chan struct{})
	env.authorizer.setRefreshBlock(block)

	acquireDone := make(chan error, 1)
	go func() {
		_, err := env.service.AcquireAccessToken(context.Background(), "account-1")
		acquireDone <- err
	}()
	waitFor(t, "the stale refresh to start", func() bool {
		return len(env.authorizer.refreshes()) >= 1
	})

	// A fresh login lands while the old token's refresh is in flight.
	env.signIn(t)

	// Now the old token comes back dead.
	env.authorizer.setRefreshResult(domain.Session{}, errors.New("invalid_grant"))
	close(block)

	select {
	case err := <-acquireDone:
		if err == nil {
			t.Fatal("AcquireAccessToken() = nil error, want the refresh failure surfaced")
		}
		if !strings.Contains(err.Error(), "codex access token could not be refreshed") {
			t.Fatalf("AcquireAccessToken() error = %q, want the refresh failure wrapped", err.Error())
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for the acquire to return")
	}

	// The dead verdict belonged to the old token: the fresh login's
	// session is still the live one.
	if conn := env.service.Status(); conn.State != StateSignedIn {
		t.Fatalf("Status().State = %q, want still signed in", conn.State)
	}
	if account := accountStatus(t, env.service.Status(), "account-1"); account.Email != "user@example.com" {
		t.Fatalf("account-1 email = %q, want the fresh login's identity", account.Email)
	}
	for _, snapshot := range env.events.snapshots() {
		if snapshot.Conn.State == StateReauthNeeded {
			t.Fatalf("a reauth_needed snapshot was delivered: %+v — a stale refresh failure clobbered the fresh login", snapshot.Conn)
		}
	}
}

// A refresh superseded by a login: the login's session is served, the
// store holds exactly what the login wrote, and the superseded rotation
// is dropped rather than persisted over it.
func TestARefreshSupersededByALoginServesTheLoginSession(t *testing.T) {
	env := newTestEnv(t)
	stale := staleSession()
	stale.RefreshToken = "old-refresh"
	env.store.seed(stale)
	env.restore(t)

	block := make(chan struct{})
	env.authorizer.setRefreshBlock(block)

	type acquireResult struct {
		token string
		err   error
	}
	acquireDone := make(chan acquireResult, 1)
	go func() {
		token, err := env.service.AcquireAccessToken(context.Background(), "account-1")
		acquireDone <- acquireResult{token: token, err: err}
	}()
	waitFor(t, "the superseded refresh to start", func() bool {
		return len(env.authorizer.refreshes()) >= 1
	})

	// The login lands while the refresh is still in flight. Its exchange
	// answers the default session; the rotation below is installed only
	// after the login committed, while the refresh is still parked — the
	// fake shares one session field between exchange and refresh, so
	// setting it earlier would hand the login the rotation too.
	env.signIn(t)
	env.authorizer.setRefreshResult(domain.Session{
		AccessToken:  "rotated-access",
		RefreshToken: "rotated-refresh",
		AccessExpiry: time.Now().Add(time.Hour),
	}, nil)
	close(block)

	select {
	case result := <-acquireDone:
		if result.err != nil {
			t.Fatalf("AcquireAccessToken() error = %v, want nil: the login's session is served", result.err)
		}
		if result.token != "access-user@example.com" {
			t.Fatalf("AcquireAccessToken() = %q, want the login session's token", result.token)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for the acquire to return")
	}

	// The store holds exactly the login's session: the superseded
	// rotation was dropped, not persisted over it.
	sessions := env.store.savedSessions()
	if len(sessions) != 1 {
		t.Fatalf("store saved %d sessions, want 1: only the login's", len(sessions))
	}
	if sessions[0].RefreshToken != "refresh-user@example.com" {
		t.Fatalf("saved session refresh token = %q, want the login's", sessions[0].RefreshToken)
	}
	if refreshes := env.authorizer.refreshes(); len(refreshes) != 1 {
		t.Fatalf("Refresh called %d times, want 1", len(refreshes))
	}
	if conn := env.service.Status(); conn.State != StateSignedIn {
		t.Fatalf("State = %q, want %q", conn.State, StateSignedIn)
	}
}

// A cancelled caller waiting on the refresh gate must be released by its
// own cancellation, and must leave the gate to the refresh that holds it.
func TestAnAcquireWaitingOnTheRefreshGateHonoursItsOwnCancellation(t *testing.T) {
	env := newTestEnv(t)
	env.service.refreshInterval = 5 * time.Millisecond
	env.store.seed(staleSession())
	env.restore(t)

	// Hold the gate from the ticker's side: its refresh parks mid-call.
	block := make(chan struct{})
	env.authorizer.setRefreshBlock(block)
	waitFor(t, "the ticker's refresh to start", func() bool {
		return len(env.authorizer.refreshes()) >= 1
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	token, err := env.service.AcquireAccessToken(ctx, "account-1")
	if !errors.Is(err, context.Canceled) || token != "" {
		t.Fatalf("cancelled AcquireAccessToken() = (%q, %v), want (\"\", context.Canceled)", token, err)
	}

	// Release the parked refresh; the gate must still be usable — the
	// cancelled caller left it to whoever was still live.
	close(block)
	waitFor(t, "the parked refresh to land", func() bool {
		// The parked refresh commits the default valid session: the
		// store's save is the observable landing.
		return len(env.store.savedSessions()) >= 1
	})
	env.service.InvalidateAccessToken("access-user@example.com")

	acquireDone := make(chan struct {
		token string
		err   error
	}, 1)
	go func() {
		token, err := env.service.AcquireAccessToken(context.Background(), "account-1")
		acquireDone <- struct {
			token string
			err   error
		}{token, err}
	}()
	select {
	case result := <-acquireDone:
		if result.err != nil {
			t.Fatalf("AcquireAccessToken() error = %v, want nil", result.err)
		}
		if result.token != "access-user@example.com" {
			t.Fatalf("AcquireAccessToken() = %q, want the refreshed token", result.token)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the acquire never got through the gate: the cancelled caller left it held")
	}
}

// The gate opens between refresh retries: a caller that arrives while the
// ticker's retry cycle is sleeping off a failure is served within that
// backoff window instead of waiting out the whole cycle.
func TestTheGateOpensBetweenRefreshRetries(t *testing.T) {
	env := newTestEnv(t)
	env.service.refreshInterval = 5 * time.Millisecond
	env.service.refreshBackoffs = []time.Duration{400 * time.Millisecond, 400 * time.Millisecond}
	env.authorizer.setRefreshResult(domain.Session{}, errors.New("transient blip"))
	env.store.seed(staleSession())
	env.restore(t)

	// The ticker's cycle is now in flight: attempt 1 failed and the
	// backoff sleep runs outside the gate.
	waitFor(t, "the first refresh attempt to run", func() bool {
		return len(env.authorizer.refreshes()) >= 1
	})

	started := time.Now()
	acquireDone := make(chan error, 1)
	go func() {
		_, err := env.service.AcquireAccessToken(context.Background(), "account-1")
		acquireDone <- err
	}()
	select {
	case err := <-acquireDone:
		if err == nil {
			t.Fatal("AcquireAccessToken() = nil error, want the transient refresh failure")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the acquire never got through the gate: it was held across the retry backoff")
	}
	if elapsed := time.Since(started); elapsed >= 150*time.Millisecond {
		t.Fatalf("acquire waited %v, want it served inside the 400ms backoff window", elapsed)
	}

	// The retry cycle still spends its budget in the background.
	waitFor(t, "the retry cycle to spend its budget", func() bool {
		return len(env.authorizer.refreshes()) >= 3
	})
}

// A rotation the disk refused stays live in memory: the token endpoint
// already rotated, so re-presenting the old refresh token would strand
// the account. The ticker must not keep re-requesting either.
func TestTheTickerAppliesARotationTheStoreCouldNotPersist(t *testing.T) {
	env := newTestEnv(t)
	env.service.refreshInterval = 5 * time.Millisecond
	env.store.saveErr = errors.New("disk full")
	env.store.seed(staleSession())
	env.authorizer.setRefreshResult(domain.Session{
		AccessToken:  "rotated-access",
		RefreshToken: "rotated-refresh",
		AccessExpiry: time.Now().Add(time.Hour),
	}, nil)
	env.restore(t)

	waitFor(t, "the ticker to attempt a refresh", func() bool {
		return len(env.authorizer.refreshes()) >= 1
	})
	// Give the ticker a few cadences to prove it does not loop on the
	// rotation it already holds.
	time.Sleep(20 * time.Millisecond)
	if refreshes := env.authorizer.refreshes(); len(refreshes) != 1 {
		t.Fatalf("Refresh called %d times, want 1: the in-memory rotation must not be re-requested", len(refreshes))
	}

	// The acquire path serves the rotated token the disk refused to keep.
	token, err := env.service.AcquireAccessToken(context.Background(), "account-1")
	if err != nil {
		t.Fatalf("AcquireAccessToken() error = %v, want nil: the rotation is served from memory", err)
	}
	if token != "rotated-access" {
		t.Fatalf("AcquireAccessToken() = %q, want the rotated access token", token)
	}
}

// The save seam: the first acquire after a failed save reports the store
// failure — the rotation is live, but pretending the disk agreed would
// hide a persistence problem — and the next acquire serves the rotation
// without paying for another refresh.
func TestAnAcquireSurfacesTheSaveErrorAndServesTheRotationAfter(t *testing.T) {
	env := newTestEnv(t)
	env.store.saveErr = errors.New("disk full")
	env.store.seed(staleSession())
	env.authorizer.setRefreshResult(domain.Session{
		AccessToken:  "rotated-access",
		RefreshToken: "rotated-refresh",
		AccessExpiry: time.Now().Add(time.Hour),
	}, nil)
	env.restore(t)

	_, err := env.service.AcquireAccessToken(context.Background(), "account-1")
	if err == nil {
		t.Fatal("AcquireAccessToken() = nil error, want the save failure surfaced")
	}
	if !strings.Contains(err.Error(), "codex session could not be stored") {
		t.Fatalf("AcquireAccessToken() error = %q, want the store failure wrapped", err.Error())
	}

	token, err := env.service.AcquireAccessToken(context.Background(), "account-1")
	if err != nil {
		t.Fatalf("second AcquireAccessToken() error = %v, want nil", err)
	}
	if token != "rotated-access" {
		t.Fatalf("second AcquireAccessToken() = %q, want the rotated token served from memory", token)
	}
	if refreshes := env.authorizer.refreshes(); len(refreshes) != 1 {
		t.Fatalf("Refresh called %d times, want 1: the in-memory rotation must be reused", len(refreshes))
	}
}

// sampleUsage is one full usage answer: a named plan, both windows
// present, with numbers chosen to exercise the normalization's clamps
// and roundings rather than the identity mapping.
func sampleUsage() domain.Usage {
	return domain.Usage{
		PlanType: "plus",
		Primary: domain.QuotaWindow{
			Present:          true,
			RemainingPercent: 40,
			WindowMinutes:    300,
			ResetAt:          time.Unix(1_800_000_000, 0).UTC(),
		},
		Secondary: domain.QuotaWindow{
			Present:          true,
			RemainingPercent: 75,
			WindowMinutes:    10080,
			ResetAt:          time.Unix(1_800_060_000, 0).UTC(),
		},
	}
}

// TestRefreshQuotaReportsTheAccountWindows probes the happy path end to
// end: the probe carries the live access token and the account id, the
// snapshot gets the fake clock's time, and no OnChanged event fires —
// the quota card is a pull, and Snapshot listeners are not its audience.
func TestRefreshQuotaReportsTheAccountWindows(t *testing.T) {
	env := newTestEnv(t)
	env.signIn(t)
	env.authorizer.setUsageResult(sampleUsage(), nil)
	env.clock.advance(time.Hour)
	signInEvents := len(env.events.snapshots())

	snapshot := env.service.RefreshQuota(context.Background(), "account-1")
	if snapshot.Err != "" {
		t.Fatalf("RefreshQuota() Err = %q, want none", snapshot.Err)
	}
	if snapshot.FetchedAt != env.clock.Now().Unix() {
		t.Fatalf("FetchedAt = %d, want the fake clock's now %d", snapshot.FetchedAt, env.clock.Now().Unix())
	}
	want := sampleUsage()
	if snapshot.Usage != want {
		t.Fatalf("Usage = %+v, want %+v", snapshot.Usage, want)
	}

	tokens, ids := env.authorizer.usageSeen()
	if len(tokens) != 1 || tokens[0] != "access-user@example.com" {
		t.Fatalf("FetchUsage saw tokens %v, want exactly the live access token", tokens)
	}
	if len(ids) != 1 || ids[0] != "account-1" {
		t.Fatalf("FetchUsage saw account ids %v, want the signed-in account id", ids)
	}
	if events := env.events.snapshots(); len(events) != signInEvents {
		t.Fatalf("OnChanged fired %d more times, want 0: a quota probe is not a session change", len(events)-signInEvents)
	}
}

// TestAFailedQuotaProbeKeepsTheLastGoodUsage pins the stale-card
// contract: a probe that fails reports the failure text and keeps both
// the previous windows and their fetch time, so the card degrades to
// "stale" instead of blanking.
func TestAFailedQuotaProbeKeepsTheLastGoodUsage(t *testing.T) {
	env := newTestEnv(t)
	env.signIn(t)
	env.authorizer.setUsageResult(sampleUsage(), nil)
	first := env.service.RefreshQuota(context.Background(), "account-1")

	env.authorizer.setUsageResult(domain.Usage{}, errors.New("codex oauth usage probe failed: http 503"))
	second := env.service.RefreshQuota(context.Background(), "account-1")
	if second.Err != "codex oauth usage probe failed: http 503" {
		t.Fatalf("second RefreshQuota() Err = %q, want the probe failure verbatim", second.Err)
	}
	if second.Usage != first.Usage {
		t.Fatalf("second Usage = %+v, want the last good %+v", second.Usage, first.Usage)
	}
	if second.FetchedAt != first.FetchedAt {
		t.Fatalf("second FetchedAt = %d, want the last good %d", second.FetchedAt, first.FetchedAt)
	}
}

// TestARejectedQuotaAccessTokenIsRotatedAndProbedOnceMore walks the
// 401 protocol: the first probe is rejected, the token is rotated
// through the ordinary refresh path, and the retry succeeds. A probe is
// on-demand, so it earns exactly one rotation — not the acquire path's
// full budget.
func TestARejectedQuotaAccessTokenIsRotatedAndProbedOnceMore(t *testing.T) {
	env := newTestEnv(t)
	env.signIn(t)
	env.authorizer.setUsageScript([]usageAnswer{
		{err: ErrUsageUnauthorized},
		{usage: sampleUsage()},
	})
	env.authorizer.setRefreshResult(domain.Session{
		AccessToken:  "rotated-access",
		RefreshToken: "rotated-refresh",
		AccessExpiry: time.Now().Add(time.Hour),
	}, nil)

	snapshot := env.service.RefreshQuota(context.Background(), "account-1")
	if snapshot.Err != "" {
		t.Fatalf("RefreshQuota() Err = %q, want none: the rotated probe must settle the card", snapshot.Err)
	}
	if snapshot.Usage != sampleUsage() {
		t.Fatalf("Usage = %+v, want the retried probe's answer", snapshot.Usage)
	}
	if refreshes := env.authorizer.refreshes(); len(refreshes) != 1 {
		t.Fatalf("Refresh called %d times, want 1 rotation", len(refreshes))
	}
	tokens, _ := env.authorizer.usageSeen()
	if len(tokens) != 2 {
		t.Fatalf("FetchUsage saw %d probes, want 2 (reject, retry)", len(tokens))
	} else if tokens[1] != "rotated-access" {
		t.Fatalf("retry probed with %q, want the rotated access token", tokens[1])
	}
}

// A second rejection after rotation must surface as the reported error
// rather than a third probe.
func TestAQuotaProbeRejectedTwiceReportsTheRejection(t *testing.T) {
	env := newTestEnv(t)
	env.signIn(t)
	env.authorizer.setUsageScript([]usageAnswer{
		{err: ErrUsageUnauthorized},
		{err: fmt.Errorf("codex oauth usage probe failed: http 401: %w", ErrUsageUnauthorized)},
	})

	snapshot := env.service.RefreshQuota(context.Background(), "account-1")
	if !strings.Contains(snapshot.Err, ErrUsageUnauthorized.Error()) {
		t.Fatalf("RefreshQuota() Err = %q, want the wrapped unauthorized rejection", snapshot.Err)
	}
	if count := env.authorizer.usageProbeCount(); count != 2 {
		t.Fatalf("usage probes = %d, want 2: one rejection, one retry", count)
	}
}

// TestRefreshQuotaWithoutASignedInAccountReportsNotSignedIn pins the
// signed-out copy: no session means no probe at all and the not-signed-in
// text the card can act on.
func TestRefreshQuotaWithoutASignedInAccountReportsNotSignedIn(t *testing.T) {
	env := newTestEnv(t)

	snapshot := env.service.RefreshQuota(context.Background(), "account-1")
	if snapshot.Err != errNotSignedIn.Error() {
		t.Fatalf("RefreshQuota() Err = %q, want %q", snapshot.Err, errNotSignedIn.Error())
	}
	if count := env.authorizer.usageProbeCount(); count != 0 {
		t.Fatalf("usage probes = %d, want 0: nothing to probe without a session", count)
	}
}

// TestConcurrentQuotaRefreshesShareOneProbe pins the coalescing: two
// callers racing while one probe is in flight produce exactly one
// request, and both receive the winner's settled answer.
func TestConcurrentQuotaRefreshesShareOneProbe(t *testing.T) {
	env := newTestEnv(t)
	env.signIn(t)
	env.authorizer.setUsageResult(sampleUsage(), nil)
	block := make(chan struct{})
	env.authorizer.setUsageBlock(block)

	results := make(chan QuotaSnapshot, 2)
	for i := 0; i < 2; i++ {
		go func() { results <- env.service.RefreshQuota(context.Background(), "account-1") }()
	}
	waitFor(t, "the winner's probe to start", func() bool {
		return env.authorizer.usageProbeCount() == 1
	})
	close(block)

	first, second := <-results, <-results
	for _, snapshot := range []QuotaSnapshot{first, second} {
		if snapshot.Err != "" {
			t.Fatalf("concurrent RefreshQuota() Err = %q, want none", snapshot.Err)
		}
		if snapshot.Usage != sampleUsage() {
			t.Fatalf("concurrent RefreshQuota() Usage = %+v, want the shared answer", snapshot.Usage)
		}
	}
	if count := env.authorizer.usageProbeCount(); count != 1 {
		t.Fatalf("usage probes = %d, want 1: the waiter must share the winner's probe", count)
	}
}

// TestACancelledQuotaProbeRecordsNothing pins the cancelled-winner rule:
// a probe whose caller walked away records neither success, failure nor
// the counter bump — the next interested caller probes afresh — and
// nothing leaks into OnChanged.
func TestACancelledQuotaProbeRecordsNothing(t *testing.T) {
	env := newTestEnv(t)
	env.signIn(t)
	env.authorizer.setUsageResult(sampleUsage(), nil)
	block := make(chan struct{})
	env.authorizer.setUsageBlock(block)
	signInEvents := len(env.events.snapshots())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan QuotaSnapshot, 1)
	go func() { done <- env.service.RefreshQuota(ctx, "account-1") }()
	waitFor(t, "the cancelled probe to start", func() bool {
		return env.authorizer.usageProbeCount() == 1
	})
	cancel()
	snapshot := <-done
	if snapshot.FetchedAt != 0 || snapshot.Err != "" {
		t.Fatalf("cancelled RefreshQuota() = %+v, want the untouched zero snapshot", snapshot)
	}

	// The counter never moved, so a later caller still probes — with the
	// park lifted, the fresh caller must be served a real answer.
	env.authorizer.setUsageBlock(nil)
	after := env.service.RefreshQuota(context.Background(), "account-1")
	if after.Err != "" || after.Usage != sampleUsage() {
		t.Fatalf("RefreshQuota() after a cancelled probe = %+v, want a fresh successful probe", after)
	}
	if count := env.authorizer.usageProbeCount(); count != 2 {
		t.Fatalf("usage probes = %d, want 2: the cancelled winner must not consume the probe", count)
	}
	if events := env.events.snapshots(); len(events) != signInEvents {
		t.Fatalf("OnChanged fired %d more times, want 0", len(events)-signInEvents)
	}
}

// TestAQuotaCallerThatStopsWaitingGetsTheLastSettledAnswer pins the
// waiting-caller rule: a caller whose ctx dies while queued on the gate
// never takes it, gets the previously settled answer, and does not
// disturb the winner's probe.
func TestAQuotaCallerThatStopsWaitingGetsTheLastSettledAnswer(t *testing.T) {
	env := newTestEnv(t)
	env.signIn(t)
	env.authorizer.setUsageResult(sampleUsage(), nil)
	block := make(chan struct{})
	env.authorizer.setUsageBlock(block)

	winnerDone := make(chan QuotaSnapshot, 1)
	go func() { winnerDone <- env.service.RefreshQuota(context.Background(), "account-1") }()
	waitFor(t, "the winner's probe to start", func() bool {
		return env.authorizer.usageProbeCount() == 1
	})

	waiterCtx, cancelWaiter := context.WithCancel(context.Background())
	waiterDone := make(chan QuotaSnapshot, 1)
	go func() { waiterDone <- env.service.RefreshQuota(waiterCtx, "account-1") }()
	cancelWaiter()
	waiter := <-waiterDone
	if waiter.FetchedAt != 0 {
		t.Fatalf("waiting caller snapshot = %+v, want the previously settled zero snapshot", waiter)
	}

	close(block)
	winner := <-winnerDone
	if winner.Err != "" || winner.Usage != sampleUsage() {
		t.Fatalf("winner snapshot = %+v, want the successful probe", winner)
	}
	if count := env.authorizer.usageProbeCount(); count != 1 {
		t.Fatalf("usage probes = %d, want 1: the departing waiter must not have probed", count)
	}
}
