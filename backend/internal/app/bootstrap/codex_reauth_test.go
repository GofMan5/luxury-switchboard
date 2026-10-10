package bootstrap

import (
	"reflect"
	"testing"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/codex/application"
)

// acc is a one-line AccountStatus fixture.
func acc(id string, state application.ConnState) application.AccountStatus {
	return application.AccountStatus{AccountID: id, State: state}
}

func TestOneAccountSlidingIntoReauthIsCaughtWhileItsSiblingStaysSignedIn(t *testing.T) {
	// The aggregate connection state stays signed-in while one session
	// expires, because the old single-state edge watched exactly that
	// aggregate — this is the multi-account case it would have missed.
	watcher := newCodexReauthWatcher()
	if got := watcher.observe([]application.AccountStatus{
		acc("acc-1", application.StateSignedIn),
		acc("acc-2", application.StateSignedIn),
	}); got != nil {
		t.Fatalf("signed-in accounts reported reauths: %v", got)
	}
	got := watcher.observe([]application.AccountStatus{
		acc("acc-1", application.StateReauthNeeded),
		acc("acc-2", application.StateSignedIn),
	})
	if !reflect.DeepEqual(got, []string{"acc-1"}) {
		t.Fatalf("expected [acc-1] to be caught, got %v", got)
	}
}

func TestAReauthSnapshotDoesNotRepeatTheNotification(t *testing.T) {
	watcher := newCodexReauthWatcher()
	if got := watcher.observe([]application.AccountStatus{acc("acc-1", application.StateReauthNeeded)}); !reflect.DeepEqual(got, []string{"acc-1"}) {
		t.Fatalf("the transition into reauth must be caught once, got %v", got)
	}
	if got := watcher.observe([]application.AccountStatus{acc("acc-1", application.StateReauthNeeded)}); got != nil {
		t.Fatalf("a repeated reauth snapshot re-notified: %v", got)
	}
	if got := watcher.observe([]application.AccountStatus{acc("acc-1", application.StateSignedOut)}); got != nil {
		t.Fatalf("signing the account out must not re-notify: %v", got)
	}
	// A second expiry — after a fresh sign-in — is a new transition.
	if got := watcher.observe([]application.AccountStatus{acc("acc-1", application.StateReauthNeeded)}); !reflect.DeepEqual(got, []string{"acc-1"}) {
		t.Fatalf("a second expiry after a fresh sign-in must notify again, got %v", got)
	}
}

func TestSignedOutAccountsReportNothing(t *testing.T) {
	watcher := newCodexReauthWatcher()
	if got := watcher.observe([]application.AccountStatus{acc("acc-1", application.StateSignedOut)}); got != nil {
		t.Fatalf("a signed-out account reported reauths: %v", got)
	}
	if got := watcher.observe(nil); got != nil {
		t.Fatalf("an empty account list reported reauths: %v", got)
	}
}
