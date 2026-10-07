package application

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/codex/domain"
	providerapp "github.com/luxuryprivate/switchboard/backend/internal/slices/providers/application"
)

// The ghost-session race: Logout runs while the login's Save is still in
// flight. Logout clears the store, then the late Save lands the session
// back on disk. The login must notice it was cancelled after the fact and
// clear again before anything is signed in — the user's disconnect wins
// over the login goroutine that lost the race.
func TestALoginSavedDuringLogoutDoesNotResurrectTheSession(t *testing.T) {
	env := newTestEnv(t)

	// Hold the login's save open: the store only records the session
	// after Logout has already run its clear.
	entered := make(chan domain.Session, 1)
	release := make(chan struct{})
	env.store.setSaveHook(func(session domain.Session) {
		entered <- session
		<-release
	})
	env.redirects.setAwaitFn(func(ctx context.Context) (string, error) {
		return "the-auth-code", nil
	})

	if _, err := env.service.LoginStart(); err != nil {
		t.Fatalf("LoginStart() failed: %v", err)
	}
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for the login to reach its save")
	}

	// The disconnect lands while the save is still in flight.
	if err := env.service.Logout(context.Background()); err != nil {
		t.Fatalf("Logout() error = %v, want nil", err)
	}
	close(release)

	// The late save must not resurrect the session. Gate on the second
	// clear — the compensation one — because Logout's own reset already
	// reports PhaseIdle and its own clear already reports wasCleared:
	// neither distinguishes the parked login goroutine. The compensation
	// clear only fires after the save comes back and the login notices it
	// was cancelled, so this is the earliest true "settled" signal.
	waitFor(t, "the login's compensation clear", func() bool {
		return env.store.clearCount() == 2
	})

	sessions := env.store.savedSessions()
	if len(sessions) != 1 {
		t.Fatalf("store saved %d sessions, want the login's single save", len(sessions))
	}
	if clears := env.store.clearCount(); clears != 2 {
		t.Fatalf("store cleared %d times, want 2: the logout's clear plus the login's compensation", clears)
	}
	// The login never reached provisioning: it was dead before its save
	// came back, so no provider entry was created for it.
	if identities := env.provisioner.ensuredIdentities(); len(identities) != 0 {
		t.Fatalf("EnsureCodexProvider called for %d identities, want 0", len(identities))
	}
	if state := env.service.Status().State; state != StateSignedOut {
		t.Fatalf("State = %q, want %q", state, StateSignedOut)
	}
}

// A reauth_needed account is still a live account: its disconnect retires
// the provider entry (keeping the preset identity for the next sign-in)
// rather than removing it as a leftover.
func TestALogoutFromReauthNeededRetires(t *testing.T) {
	env := newTestEnv(t)
	session := validSession("user@example.com")
	session.RefreshToken = ""
	session.AccessExpiry = time.Now().Add(-10 * time.Minute)
	env.store.mu.Lock()
	env.store.session = session
	env.store.present = true
	env.store.mu.Unlock()
	env.restore(t)

	if _, err := env.service.AcquireAccessToken(context.Background()); !errors.Is(err, errNeedsSignIn) {
		t.Fatalf("AcquireAccessToken() error = %v, want errNeedsSignIn", err)
	}
	if state := env.service.Status().State; state != StateReauthNeeded {
		t.Fatalf("State = %q, want %q before the logout", state, StateReauthNeeded)
	}

	if err := env.service.Logout(context.Background()); err != nil {
		t.Fatalf("Logout() error = %v, want nil", err)
	}
	if retires := env.provisioner.retireCount(); retires != 1 {
		t.Fatalf("RetireCodexProvider called %d times, want 1", retires)
	}
	if removes := env.provisioner.removeCount(); removes != 0 {
		t.Fatalf("RemoveCodexProvider called %d times, want 0", removes)
	}
	if state := env.service.Status().State; state != StateSignedOut {
		t.Fatalf("State = %q, want %q", state, StateSignedOut)
	}
	if !env.store.wasCleared() {
		t.Fatal("the stored session was not cleared")
	}
	if clears := env.store.clearCount(); clears != 1 {
		t.Fatalf("store cleared %d times, want 1", clears)
	}
}

// The active-route refusal: the logout aborts before the store is
// cleared, while the session is still signed in with its tokens intact —
// the caller resolves the dependency and disconnects again.
func TestALogoutRefusedWhileCodexIsTheActiveProviderKeepsTheSession(t *testing.T) {
	env := newTestEnv(t)
	env.signIn(t)
	env.provisioner.retireErr = providerapp.ErrActiveProvider

	err := env.service.Logout(context.Background())
	var refusal *LogoutError
	if !errors.As(err, &refusal) {
		t.Fatalf("Logout() error = %v, want a *LogoutError refusal", err)
	}
	if refusal.Code != "codex_active_route" {
		t.Fatalf("refusal.Code = %q, want codex_active_route", refusal.Code)
	}
	wantMessage := "Codex is the active provider. Switch the active route away from Codex before disconnecting."
	if refusal.Message != wantMessage {
		t.Fatalf("refusal.Message = %q, want %q", refusal.Message, wantMessage)
	}
	if refusal.Error() != wantMessage {
		t.Fatalf("refusal.Error() = %q, want the message", refusal.Error())
	}

	if state := env.service.Status().State; state != StateSignedIn {
		t.Fatalf("State = %q, want %q: a refusal must not reset the session", state, StateSignedIn)
	}
	if env.store.wasCleared() {
		t.Fatal("the store was cleared despite the refusal")
	}
	if clears := env.store.clearCount(); clears != 0 {
		t.Fatalf("store cleared %d times, want 0", clears)
	}
	if retires := env.provisioner.retireCount(); retires != 1 {
		t.Fatalf("RetireCodexProvider called %d times, want 1", retires)
	}
	if removes := env.provisioner.removeCount(); removes != 0 {
		t.Fatalf("RemoveCodexProvider called %d times, want 0", removes)
	}

	// The refusal kept the tokens: the next acquire still serves them.
	token, err := env.service.AcquireAccessToken(context.Background())
	if err != nil {
		t.Fatalf("AcquireAccessToken() error = %v, want nil: the refusal kept the tokens", err)
	}
	if token != "access-user@example.com" {
		t.Fatalf("AcquireAccessToken() = %q, want the signed-in session's token", token)
	}
}

// A retirement failure that is not a refusal keeps the deliberate seam:
// the tokens are still cleared and the state still resets, because a
// broken registry must not leave dead tokens on disk.
func TestALogoutStillCleansUpWhenRetirementFailsForAnotherReason(t *testing.T) {
	env := newTestEnv(t)
	env.signIn(t)
	env.provisioner.retireErr = errors.New("registry lockfile is stuck")

	err := env.service.Logout(context.Background())
	if err == nil {
		t.Fatal("Logout() = nil error, want the retirement failure reported")
	}
	var refusal *LogoutError
	if errors.As(err, &refusal) {
		t.Fatalf("Logout() error = %v, want a plain joined failure, not a refusal", err)
	}
	if !strings.Contains(err.Error(), "codex provider could not be retired") {
		t.Fatalf("Logout() error = %q, want the retirement failure wrapped", err.Error())
	}
	if !strings.Contains(err.Error(), "registry lockfile is stuck") {
		t.Fatalf("Logout() error = %q, want the underlying cause carried through", err.Error())
	}

	if !env.store.wasCleared() {
		t.Fatal("the store was not cleared: a non-refusal failure must still disconnect")
	}
	if state := env.service.Status().State; state != StateSignedOut {
		t.Fatalf("State = %q, want %q", state, StateSignedOut)
	}
}

// The leftover-entry refusals: a signed-out disconnect whose provider
// entry still holds routes, keys, or the active route aborts with the
// typed error naming the dependency, leaving the stored session exactly
// as it was.
func TestALogoutFromLeftoversNamesTheDependencyThatRefused(t *testing.T) {
	cases := []struct {
		name        string
		removeErr   error
		wantCode    string
		wantMessage string
	}{
		{
			name:        "routes",
			removeErr:   providerapp.ErrProviderHasRoutes,
			wantCode:    "codex_provider_has_routes",
			wantMessage: "The Codex provider still has model routes. Remove its routes before disconnecting.",
		},
		{
			name:        "keys",
			removeErr:   providerapp.ErrProviderHasKeys,
			wantCode:    "codex_provider_has_keys",
			wantMessage: "The Codex provider still has API keys. Remove its keys before disconnecting.",
		},
		{
			name:        "active route",
			removeErr:   providerapp.ErrActiveProvider,
			wantCode:    "codex_active_route",
			wantMessage: "Codex is the active provider. Switch the active route away from Codex before disconnecting.",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			env := newTestEnv(t)
			// A leftover disk session with no live state: the remove
			// branch is the one that talks to the provider manager.
			env.store.mu.Lock()
			env.store.session = staleSession()
			env.store.present = true
			env.store.mu.Unlock()
			env.provisioner.removeErr = testCase.removeErr

			err := env.service.Logout(context.Background())
			var refusal *LogoutError
			if !errors.As(err, &refusal) {
				t.Fatalf("Logout() error = %v, want a *LogoutError refusal", err)
			}
			if refusal.Code != testCase.wantCode {
				t.Fatalf("refusal.Code = %q, want %q", refusal.Code, testCase.wantCode)
			}
			if refusal.Message != testCase.wantMessage {
				t.Fatalf("refusal.Message = %q, want %q", refusal.Message, testCase.wantMessage)
			}

			if removes := env.provisioner.removeCount(); removes != 1 {
				t.Fatalf("RemoveCodexProvider called %d times, want 1", removes)
			}
			if clears := env.store.clearCount(); clears != 0 {
				t.Fatalf("store cleared %d times, want 0: the refusal aborts before the clear", clears)
			}
			if env.store.wasCleared() {
				t.Fatal("the leftover stored session was cleared despite the refusal")
			}
			if state := env.service.Status().State; state != StateSignedOut {
				t.Fatalf("State = %q, want %q", state, StateSignedOut)
			}
		})
	}
}
