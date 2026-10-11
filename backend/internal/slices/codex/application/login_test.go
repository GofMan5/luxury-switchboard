package application

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"testing"
	"time"
)

func TestLoginStartOpensTheAuthorizeURLAndWaitsForTheCode(t *testing.T) {
	env := newTestEnv(t)

	authorizeURL, err := env.service.LoginStart()
	if err != nil {
		t.Fatalf("LoginStart() error = %v, want nil", err)
	}

	// The loopback server is started before the URL is handed out: the
	// browser redirect needs a listener to land on.
	started := env.redirects.startedStates()
	if len(started) != 1 {
		t.Fatalf("redirects.Start called %d times, want 1", len(started))
	}
	state := started[0]
	if len(state) != 64 {
		t.Fatalf("state is %d characters, want 64 hex chars", len(state))
	}

	// The same state went into the URL the service returned and into the
	// URL the authorizer composed; those must agree or the browser round
	// trip cannot be tied to this flow.
	states := env.authorizer.recordedStates()
	if len(states) != 1 || states[0] != state {
		t.Fatalf("authorizer saw states %v, want [%s]", states, state)
	}
	if authorizeURL != "https://auth.openai.test/authorize?state="+state+"&code_challenge="+env.authorizer.recordedChallenges()[0] {
		t.Fatalf("LoginStart() URL = %q, want the authorizer-composed URL", authorizeURL)
	}

	// The service is now waiting for the browser, signed out.
	if status := env.service.LoginStatus(); status.Phase != PhaseWaiting {
		t.Fatalf("LoginStatus().Phase = %q, want %q", status.Phase, PhaseWaiting)
	}
	if snapshot := env.service.Status(); snapshot.State != StateSignedOut {
		t.Fatalf("Status().State = %q, want %q", snapshot.State, StateSignedOut)
	}

	// The very first snapshot the UI receives already carries waiting.
	events := env.events.snapshots()
	if len(events) == 0 {
		t.Fatal("no OnChanged events after LoginStart")
	}
	first := events[0]
	if first.Login.Phase != PhaseWaiting {
		t.Fatalf("first event login phase = %q, want %q", first.Login.Phase, PhaseWaiting)
	}
	if first.Conn.State != StateSignedOut {
		t.Fatalf("first event conn state = %q, want %q", first.Conn.State, StateSignedOut)
	}

	env.service.LoginCancel()
	waitFor(t, "login cancellation", func() bool {
		return env.service.LoginStatus().Phase == PhaseIdle
	})
}

func TestALoginAlreadyWaitingReturnsItsURLWithoutStartingASecondFlow(t *testing.T) {
	env := newTestEnv(t)

	firstURL, err := env.service.LoginStart()
	if err != nil {
		t.Fatalf("first LoginStart() error = %v, want nil", err)
	}
	secondURL, err := env.service.LoginStart()
	if err != nil {
		t.Fatalf("second LoginStart() error = %v, want nil", err)
	}

	// The user re-opening the sign-in dialog gets the same URL, not a
	// second loopback listener fighting over port 1455.
	if secondURL != firstURL {
		t.Fatalf("second LoginStart() URL = %q, want the same %q", secondURL, firstURL)
	}
	if starts := env.redirects.startedStates(); len(starts) != 1 {
		t.Fatalf("redirects.Start called %d times, want 1", len(starts))
	}
	if states := env.authorizer.recordedStates(); len(states) != 1 {
		t.Fatalf("AuthorizeURL composed %d times, want 1", len(states))
	}

	env.service.LoginCancel()
	waitFor(t, "login cancellation", func() bool {
		return env.service.LoginStatus().Phase == PhaseIdle
	})
}

func TestASuccessfulLoginExchangesTheRedirectCodeWithTheStoredVerifierAndSignsIn(t *testing.T) {
	env := newTestEnv(t)

	env.signIn(t)

	// The code from the loopback redirect is exchanged together with the
	// verifier minted in LoginStart; PKCE dies without that pairing.
	exchanges := env.authorizer.exchanges()
	if len(exchanges) != 1 {
		t.Fatalf("ExchangeCode called %d times, want 1", len(exchanges))
	}
	if exchanges[0].code != "the-auth-code" {
		t.Fatalf("exchanged code = %q, want %q", exchanges[0].code, "the-auth-code")
	}
	if len(exchanges[0].verifier) != 43 {
		t.Fatalf("verifier is %d characters, want 43", len(exchanges[0].verifier))
	}

	// The challenge sent to the authorize URL is the SHA-256 of that
	// verifier, unpadded base64url.
	challenges := env.authorizer.recordedChallenges()
	digest := sha256.Sum256([]byte(exchanges[0].verifier))
	if want := base64.RawURLEncoding.EncodeToString(digest[:]); challenges[0] != want {
		t.Fatalf("code challenge = %q, want %q", challenges[0], want)
	}

	// The session was persisted exactly once and the provider was
	// provisioned with the identity from the id token.
	saves := env.store.savedSessions()
	if len(saves) != 1 {
		t.Fatalf("store.Save called %d times, want 1", len(saves))
	}
	ensured := env.provisioner.ensuredProviderIDs()
	if len(ensured) != 1 || ensured[0] != CodexProviderID {
		t.Fatalf("provisioner ensured %v, want one call answering the fleet id %q", ensured, CodexProviderID)
	}

	// The connection is signed in with the identity and the provider.
	status := env.service.Status()
	if status.State != StateSignedIn {
		t.Fatalf("Status().State = %q, want %q", status.State, StateSignedIn)
	}
	signedIn := accountStatus(t, status, "account-1")
	if signedIn.State != StateSignedIn {
		t.Fatalf("account state = %q, want %q", signedIn.State, StateSignedIn)
	}
	if signedIn.Email != "user@example.com" {
		t.Fatalf("account email = %q, want user@example.com", signedIn.Email)
	}
	if signedIn.Plan != "plus" {
		t.Fatalf("account plan = %q, want plus", signedIn.Plan)
	}
	if signedIn.ProviderID != CodexProviderID {
		t.Fatalf("account provider id = %q, want %q", signedIn.ProviderID, CodexProviderID)
	}

	// The sign-in arrives as its own snapshots: waiting, exchanging, then
	// success with the connection and the provider in the same event —
	// exchanging must be pushed so the dialog can lock dismissal while
	// the code is being redeemed.
	events := env.events.snapshots()
	if len(events) != 3 {
		t.Fatalf("got %d OnChanged events, want 3 (waiting, exchanging, then success)", len(events))
	}
	last := events[len(events)-1]
	if last.Login.Phase != PhaseSuccess {
		t.Fatalf("last event login phase = %q, want %q", last.Login.Phase, PhaseSuccess)
	}
	if last.Conn.State != StateSignedIn {
		t.Fatalf("last event conn state = %q, want %q", last.Conn.State, StateSignedIn)
	}
	lastAccount := accountStatus(t, last.Conn, "account-1")
	if lastAccount.ProviderID != CodexProviderID {
		t.Fatalf("last event provider id = %q, want %q", lastAccount.ProviderID, CodexProviderID)
	}
}

func TestCancellingALoginReturnsToIdleWithoutTouchingTheSession(t *testing.T) {
	env := newTestEnv(t)

	if _, err := env.service.LoginStart(); err != nil {
		t.Fatalf("LoginStart() error = %v, want nil", err)
	}
	env.service.LoginCancel()
	waitFor(t, "login cancellation", func() bool {
		return env.service.LoginStatus().Phase == PhaseIdle
	})

	// A cancelled login leaves no half-open flow behind.
	if status := env.service.LoginStatus(); status.Err != "" {
		t.Fatalf("LoginStatus().Err = %q, want empty", status.Err)
	}
	if status := env.service.Status(); status.State != StateSignedOut {
		t.Fatalf("Status().State = %q, want %q", status.State, StateSignedOut)
	}
	// Nothing was exchanged, nothing was stored, and the loopback server
	// was released for the next attempt.
	if exchanges := env.authorizer.exchanges(); len(exchanges) != 0 {
		t.Fatalf("ExchangeCode called %d times, want 0", len(exchanges))
	}
	if saves := env.store.savedSessions(); len(saves) != 0 {
		t.Fatalf("store.Save called %d times, want 0", len(saves))
	}
	if stops := env.redirects.stopCount(); stops < 1 {
		t.Fatalf("redirects.Stop called %d times, want at least 1", stops)
	}
	events := env.events.snapshots()
	if len(events) == 0 {
		t.Fatal("no OnChanged events for a cancelled login")
	}
	if last := events[len(events)-1]; last.Login.Phase != PhaseIdle {
		t.Fatalf("last event login phase = %q, want %q", last.Login.Phase, PhaseIdle)
	}
}

func TestALoginThatRunsOutOfTimeTimesOut(t *testing.T) {
	env := newTestEnv(t)
	env.service.loginTimeout = 40 * time.Millisecond

	if _, err := env.service.LoginStart(); err != nil {
		t.Fatalf("LoginStart() error = %v, want nil", err)
	}

	waitFor(t, "login timeout", func() bool {
		return env.service.LoginStatus().Phase == PhaseError
	})
	if err := env.service.LoginStatus().Err; err != "codex login timed out" {
		t.Fatalf("LoginStatus().Err = %q, want %q", err, "codex login timed out")
	}
}

func TestACancelledLoginLandsOnIdleEvenWhenAwaitReportsItAsAnError(t *testing.T) {
	env := newTestEnv(t)
	// The listener adapter surfaces an abandonment as an error rather than
	// a plain context error; a user cancel is still a quiet idle.
	env.redirects.setAwaitFn(func(ctx context.Context) (string, error) {
		<-ctx.Done()
		return "", errors.New("awaiting the code was abandoned")
	})

	if _, err := env.service.LoginStart(); err != nil {
		t.Fatalf("LoginStart() error = %v, want nil", err)
	}
	env.service.LoginCancel()

	waitFor(t, "login cancellation", func() bool {
		return env.service.LoginStatus().Phase == PhaseIdle
	})
	if err := env.service.LoginStatus().Err; err != "" {
		t.Fatalf("LoginStatus().Err = %q, want empty: the user chose to stop, nothing failed", err)
	}
}

func TestAFailedExchangeReportsTheAuthErrorAndStoresNothing(t *testing.T) {
	env := newTestEnv(t)
	env.authorizer.mu.Lock()
	env.authorizer.exchangeErr = errors.New("token endpoint returned 503")
	env.authorizer.mu.Unlock()

	env.signInFailingAtExchange(t)

	// The token endpoint's own text reaches the user verbatim: it carries
	// OAuth error codes, which is exactly what they need to see.
	status := env.service.LoginStatus()
	if status.Phase != PhaseError {
		t.Fatalf("LoginStatus().Phase = %q, want %q", status.Phase, PhaseError)
	}
	if want := "token endpoint returned 503"; status.Err != want {
		t.Fatalf("LoginStatus().Err = %q, want %q", status.Err, want)
	}
	// Nothing was persisted and no provider was registered for a login
	// that never produced tokens.
	if saves := env.store.savedSessions(); len(saves) != 0 {
		t.Fatalf("store.Save called %d times, want 0", len(saves))
	}
	if ensured := env.provisioner.ensuredProviderIDs(); len(ensured) != 0 {
		t.Fatalf("EnsureCodexProvider called %d times, want 0", len(ensured))
	}
	if conn := env.service.Status(); conn.State != StateSignedOut {
		t.Fatalf("Status().State = %q, want %q", conn.State, StateSignedOut)
	}
}

func TestALoginWhoseSessionCannotBeStoredFailsBeforeProvisioning(t *testing.T) {
	env := newTestEnv(t)
	env.store.mu.Lock()
	env.store.saveErr = errors.New("dpapi unavailable")
	env.store.mu.Unlock()

	env.signInFailingAtExchange(t)

	if status := env.service.LoginStatus(); status.Err != "codex session could not be stored" {
		t.Fatalf("LoginStatus().Err = %q, want %q", status.Err, "codex session could not be stored")
	}
	// A session the disk rejected gets no provider entry: provisioning is
	// only for durable sessions.
	if ensured := env.provisioner.ensuredProviderIDs(); len(ensured) != 0 {
		t.Fatalf("EnsureCodexProvider called %d times, want 0", len(ensured))
	}
	if conn := env.service.Status(); conn.State != StateSignedOut {
		t.Fatalf("Status().State = %q, want %q", conn.State, StateSignedOut)
	}
}

func TestALoginWhoseProviderCannotBeProvisionedKeepsTheStoredSession(t *testing.T) {
	env := newTestEnv(t)
	env.provisioner.mu.Lock()
	env.provisioner.ensureErr = errors.New("providers registry is read-only")
	env.provisioner.mu.Unlock()

	env.signInFailingAtExchange(t)

	// The failure is reported in the login status...
	status := env.service.LoginStatus()
	if status.Phase != PhaseError {
		t.Fatalf("LoginStatus().Phase = %q, want %q", status.Phase, PhaseError)
	}
	if want := "codex provider could not be provisioned: providers registry is read-only"; status.Err != want {
		t.Fatalf("LoginStatus().Err = %q, want %q", status.Err, want)
	}
	// ...but the session is kept: the tokens are durable and usable, and
	// only the provider entry is missing.
	// ...but the account row is kept: the tokens are durable and usable,
	// and only the provider entry is missing.
	conn := env.service.Status()
	if conn.State != StateSignedIn {
		t.Fatalf("Status().State = %q, want %q", conn.State, StateSignedIn)
	}
	kept := accountStatus(t, conn, "account-1")
	if kept.Email != "user@example.com" {
		t.Fatalf("account email = %q, want user@example.com", kept.Email)
	}
	if kept.ProviderID != "" {
		t.Fatalf("account provider id = %q, want empty until provisioning succeeds", kept.ProviderID)
	}
	if saves := env.store.savedSessions(); len(saves) != 1 {
		t.Fatalf("store.Save called %d times, want 1", len(saves))
	}
}

func TestALogoutDuringLoginNeverResurrectsTheSession(t *testing.T) {
	env := newTestEnv(t)
	// The redirect answer is gated: the login goroutine is still waiting
	// for the browser when Logout fires.
	gate := make(chan struct{})
	env.redirects.setAwaitFn(func(ctx context.Context) (string, error) {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-gate:
			return "the-auth-code", nil
		}
	})

	if _, err := env.service.LoginStart(); err != nil {
		t.Fatalf("LoginStart() error = %v, want nil", err)
	}
	if err := env.service.Logout(context.Background(), "", false); err != nil {
		t.Fatalf("Logout() error = %v, want nil", err)
	}
	if conn := env.service.Status(); conn.State != StateSignedOut {
		t.Fatalf("Status().State = %q, want %q", conn.State, StateSignedOut)
	}

	// The code lands after the logout: the exchange must not resurrect
	// what the user just tore down.
	close(gate)
	waitFor(t, "the login goroutine to finish", func() bool {
		return env.redirects.stopCount() >= 2
	})

	if exchanges := env.authorizer.exchanges(); len(exchanges) != 0 {
		t.Fatalf("ExchangeCode called %d times, want 0: a login aborted by logout is never exchanged", len(exchanges))
	}
	if saves := env.store.savedSessions(); len(saves) != 0 {
		t.Fatalf("store.Save called %d times, want 0", len(saves))
	}
	if login := env.service.LoginStatus(); login.Phase != PhaseIdle {
		t.Fatalf("LoginStatus().Phase = %q, want %q", login.Phase, PhaseIdle)
	}
	if conn := env.service.Status(); conn.State != StateSignedOut {
		t.Fatalf("Status().State = %q, want %q", conn.State, StateSignedOut)
	}
}

// signInFailingAtExchange drives a login whose redirect answers but whose
// outcome is an error: it fails at (or after) the exchange.
func (env *testEnv) signInFailingAtExchange(t *testing.T) {
	t.Helper()
	env.redirects.setAwaitFn(func(ctx context.Context) (string, error) {
		return "the-auth-code", nil
	})
	if _, err := env.service.LoginStart(); err != nil {
		t.Fatalf("LoginStart() failed: %v", err)
	}
	waitFor(t, "login error", func() bool {
		return env.service.LoginStatus().Phase == PhaseError
	})
}

// The cancellation sliver: LoginCancel lands while the login goroutine is
// still parked on the loopback await, so the flow is being torn down but
// has not yet settled. A LoginStart in that window must refuse instead of
// handing back a URL whose flow — and whose redirect server — is already
// dying.
func TestALoginStartDuringACancellationSliverDoesNotHandBackADeadURL(t *testing.T) {
	env := newTestEnv(t)

	// The browser is slow: the loopback await parks, ignoring its ctx, so
	// the flow stays in Waiting even after LoginCancel has torn it down.
	browser := make(chan struct{})
	env.redirects.setAwaitFn(func(ctx context.Context) (string, error) {
		<-browser
		return "", ctx.Err()
	})

	if _, err := env.service.LoginStart(); err != nil {
		t.Fatalf("LoginStart() failed: %v", err)
	}
	env.service.LoginCancel()

	if _, err := env.service.LoginStart(); !errors.Is(err, errLoginAlreadyCancelled) {
		t.Fatalf("LoginStart() error = %v, want errLoginAlreadyCancelled", err)
	}
	// The refusal must not have started a second redirect server: the
	// dying flow still owns the loopback listener.
	if started := env.redirects.startedStates(); len(started) != 1 {
		t.Fatalf("redirects.Start called %d times, want 1: the refusal must not open a second flow", len(started))
	}

	// The first flow lands idle once its await unparks.
	close(browser)
	waitFor(t, "the cancelled login to settle idle", func() bool {
		return env.service.LoginStatus().Phase == PhaseIdle
	})
}

// The handback staleness rule, as a truth table. The old rule also read
// the login context, and the login goroutine's own deferred cancel kills
// that context on every exit path — success included. In the fast-fake
// tests the goroutine can finish before the start call re-checks, and a
// successful login was misreported as errLoginAlreadyCancelled: the CI
// flake. The rule now reads only the flow: an external cancel, a
// superseding start, or a landing on idle (the one phase a cancelled
// login ends in). Nothing else is stale, a settled flow's URL included.
func TestLoginHandbackStaleReadsTheFlowNotTheContext(t *testing.T) {
	env := newTestEnv(t)

	// Each row is the service state at the moment a start call re-checks
	// its handback, for the generation that call committed.
	cases := []struct {
		name       string
		cancelled  bool   // cancelRequested flag
		callerGen  uint64 // the generation the start call committed
		currentGen uint64 // service.loginGen at re-check time
		phase      LoginPhase
		want       bool
	}{
		{
			// LoginCancel set the flag under mu before cancelling the
			// context outside it; the goroutine has not landed yet.
			name:       "cancel requested while still waiting",
			cancelled:  true,
			callerGen:  1,
			currentGen: 1,
			phase:      PhaseWaiting,
			want:       true,
		},
		{
			// A newer start superseded this one and moved the gen on.
			name:       "superseded by a newer start",
			callerGen:  1,
			currentGen: 2,
			phase:      PhaseWaiting,
			want:       true,
		},
		{
			// The cancel outlived the flags: the goroutine landed idle,
			// and that landing reset cancelRequested — but the flow this
			// caller started is still the one that died.
			name:       "landed idle after a cancel reset the flag",
			callerGen:  1,
			currentGen: 1,
			phase:      PhaseIdle,
			want:       true,
		},
		{
			// The flake this rule replaced: the login completed while
			// the caller was between commit and re-check. The goroutine
			// deferred cancel has killed the context, yet the URL is
			// live and must be handed back.
			name:       "settled with success",
			callerGen:  1,
			currentGen: 1,
			phase:      PhaseSuccess,
			want:       false,
		},
		{
			// The flow failed on its own; the error text lives in
			// loginErr and the next start begins fresh. Not a cancel.
			name:       "settled with a flow error",
			callerGen:  1,
			currentGen: 1,
			phase:      PhaseError,
			want:       false,
		},
		{
			// The ordinary alive phases: waiting, or exchanging the
			// authorization code.
			name:       "still waiting",
			callerGen:  1,
			currentGen: 1,
			phase:      PhaseWaiting,
			want:       false,
		},
		{
			name:       "exchanging the code",
			callerGen:  1,
			currentGen: 1,
			phase:      PhaseExchanging,
			want:       false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env.service.mu.Lock()
			env.service.cancelRequested = tc.cancelled
			env.service.loginGen = tc.currentGen
			env.service.phase = tc.phase
			env.service.mu.Unlock()

			if got := env.service.loginHandbackStale(tc.callerGen); got != tc.want {
				t.Fatalf("loginHandbackStale(%d) = %v, want %v", tc.callerGen, got, tc.want)
			}
		})
	}
}
