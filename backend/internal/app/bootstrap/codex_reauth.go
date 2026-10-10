package bootstrap

import (
	"sync"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/codex/application"
)

// codexReauthWatcher watches per-account connection states and reports
// the account IDs that slid into reauth-needed. Accounts are plural, so
// the aggregate connection state is not the edge: one expiring session
// must be caught while its siblings stay signed in, and only the
// transition into reauth-needed counts — OnChanged reports every
// observable change, login phases included, and may fire from whichever
// goroutine moved the session, so the watcher's map is its own lock.
type codexReauthWatcher struct {
	mu     sync.Mutex
	states map[string]application.ConnState
}

// newCodexReauthWatcher returns a watcher with no observed accounts.
func newCodexReauthWatcher() *codexReauthWatcher {
	return &codexReauthWatcher{states: map[string]application.ConnState{}}
}

// observe records one snapshot's per-account states and returns the IDs
// that transitioned into reauth-needed: an account that was missing
// (signed out or never seen) and now reports reauth, or one that held a
// different state before. A signed-out account reports nothing, and
// reauth snapshots do not repeat the notification.
func (watcher *codexReauthWatcher) observe(accounts []application.AccountStatus) []string {
	watcher.mu.Lock()
	defer watcher.mu.Unlock()
	var reauths []string
	for _, account := range accounts {
		previous, known := watcher.states[account.AccountID]
		watcher.states[account.AccountID] = account.State
		if account.State == application.StateReauthNeeded && (!known || previous != application.StateReauthNeeded) {
			reauths = append(reauths, account.AccountID)
		}
	}
	return reauths
}
