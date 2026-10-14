package application

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/codex/domain"
)

func TestLateSameTokenRefreshCannotReplaceANewerLogin(t *testing.T) {
	for _, failure := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "reauth failure"}[failure], func(t *testing.T) {
			env := newTestEnv(t)
			old := staleSession()
			env.store.seed(old)
			env.restore(t)
			block := make(chan struct{})
			env.authorizer.setRefreshBlock(block)
			refreshed := old
			refreshed.AccessToken = "stale-completion"
			refreshed.AccessExpiry = time.Now().Add(time.Hour)
			var outcome error
			if failure {
				outcome = errors.New("invalid_grant")
			}
			env.authorizer.setRefreshResult(refreshed, outcome)
			done := make(chan struct{})
			go func() { env.service.AcquireAccessToken(context.Background(), "account-1"); close(done) }()
			waitFor(t, "refresh to start", func() bool { return len(env.authorizer.refreshes()) > 0 })
			newer := old
			newer.AccessToken = "new-login"
			newer.AccessExpiry = time.Now().Add(time.Hour)
			env.service.mu.Lock()
			env.service.commitAccountLocked(newer, "codex", LoginMethodImport)
			env.service.mu.Unlock()
			close(block)
			<-done
			if token, err := env.service.AcquireAccessToken(context.Background(), "account-1"); err != nil || token != "new-login" {
				t.Fatalf("stale refresh replaced newer same-token login: token matches=%v err=%v", token == "new-login", err)
			}
		})
	}
}

func TestLateQuotaSaveCannotRollBackRotatedTokens(t *testing.T) {
	env := newTestEnv(t)
	env.store.seed(validSession("user@example.invalid"))
	env.restore(t)
	env.authorizer.setUsageResult(sampleUsage(), nil)
	env.service.RefreshQuota(context.Background(), "account-1")
	entered, unblock := make(chan struct{}), make(chan struct{})
	env.store.setSaveHook(func(session domain.Session) {
		if session.AccessToken == "access-user@example.invalid" {
			close(entered)
			<-unblock
		}
	})
	env.authorizer.setUsageResult(laterPrimaryWindow(), nil)
	quotaDone := make(chan struct{})
	go func() { env.service.RefreshQuota(context.Background(), "account-1"); close(quotaDone) }()
	<-entered
	env.authorizer.setRefreshResult(domain.Session{AccessToken: "rotated-access", RefreshToken: "rotated-refresh", AccessExpiry: time.Now().Add(time.Hour)}, nil)
	env.service.InvalidateAccessToken("access-user@example.invalid")
	refreshDone := make(chan error, 1)
	go func() {
		_, err := env.service.AcquireAccessToken(context.Background(), "account-1")
		refreshDone <- err
	}()
	waitFor(t, "refresh to finish or wait behind quota save", func() bool {
		if len(env.store.savedAccounts()) > 0 {
			return true
		}
		env.service.mu.Lock()
		lifecycle := env.service.diskGates["account-1"]
		env.service.mu.Unlock()
		return lifecycle != nil && lifecycle.gate.waiterCount() > 0
	})
	close(unblock)
	<-quotaDone
	if err := <-refreshDone; err != nil {
		t.Fatal(err)
	}
	stored := env.store.storedAccounts()
	if len(stored) != 1 || stored[0].Session.RefreshToken != "rotated-refresh" || stored[0].Resets != 1 {
		t.Fatal("late quota save rolled back rotated tokens or reset count")
	}
}

func TestFailedSpecificLogoutRemainsNonservableAndRetryable(t *testing.T) {
	env := newTestEnv(t)
	env.store.seed(validSession("user@example.invalid"))
	env.restore(t)
	env.store.mu.Lock()
	env.store.clearErr = errors.New("temporary clear failure")
	env.store.mu.Unlock()
	if err := env.service.Logout(context.Background(), "account-1", false); err == nil {
		t.Fatal("failed durable clear reported success")
	}
	row := accountStatus(t, env.service.Status(), "account-1")
	if row.State != StateSignedOut || env.service.Status().State != StateSignedOut {
		t.Fatal("pending deletion still reports a serving account")
	}
	if token, err := env.service.AcquireAccessToken(context.Background(), "account-1"); err == nil || token != "" {
		t.Fatal("pending deletion served a credential")
	}
	// A same-process restore must not re-enable a known pending deletion.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := env.service.Restore(ctx); err != nil {
		t.Fatal(err)
	}
	if token, err := env.service.AcquireAccessToken(context.Background(), "account-1"); err == nil || token != "" {
		t.Fatal("restore re-enabled pending deletion")
	}
	env.store.mu.Lock()
	env.store.clearErr = nil
	env.store.mu.Unlock()
	if err := env.service.Logout(context.Background(), "account-1", false); err != nil {
		t.Fatal(err)
	}
	if len(env.store.storedAccounts()) != 0 || len(env.service.Status().Accounts) != 0 {
		t.Fatal("successful retry left durable credential or pending identity")
	}
}

func TestSuccessfulLogoutOrdersAfterAnAlreadyStartedRefreshSave(t *testing.T) {
	for _, id := range []string{"account-1", ""} {
		t.Run("logout_"+id, func(t *testing.T) {
			env := newTestEnv(t)
			env.store.seed(staleSession())
			env.store.seed(accountSession("account-2", "second@example.invalid"))
			env.restore(t)
			entered, release := make(chan struct{}), make(chan struct{})
			env.authorizer.setRefreshResult(domain.Session{AccessToken: "rotated-access", RefreshToken: "rotated-refresh", AccessExpiry: time.Now().Add(time.Hour)}, nil)
			env.store.setSaveHook(func(session domain.Session) {
				if session.AccessToken == "rotated-access" {
					close(entered)
					<-release
				}
			})
			acquired := make(chan struct{})
			go func() { env.service.AcquireAccessToken(context.Background(), "account-1"); close(acquired) }()
			<-entered
			loggedOut := make(chan error, 1)
			go func() { loggedOut <- env.service.Logout(context.Background(), id, false) }()
			waitFor(t, "logout to stop serving its target", func() bool {
				for _, row := range env.service.Status().Accounts {
					if row.AccountID == "account-1" {
						return row.State == StateSignedOut
					}
				}
				return true
			})
			// A blocked disk save on account 1 cannot hold status or account 2.
			if id != "" {
				if _, err := env.service.AcquireAccessToken(context.Background(), "account-2"); err != nil {
					t.Fatal(err)
				}
			}
			close(release)
			<-acquired
			if err := <-loggedOut; err != nil {
				t.Fatal(err)
			}
			restart := NewService(env.store, env.authorizer, env.redirects, env.provisioner, env.parser, env.files, env.clock.Now)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if err := restart.Restore(ctx); err != nil {
				t.Fatal(err)
			}
			for _, row := range restart.Status().Accounts {
				if row.AccountID == "account-1" {
					t.Fatal("late refresh save resurrected logged-out account on restart")
				}
			}
		})
	}
}
