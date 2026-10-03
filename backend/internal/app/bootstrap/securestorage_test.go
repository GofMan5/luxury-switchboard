package bootstrap

import (
	"testing"

	"github.com/luxuryprivate/switchboard/backend/internal/platform/secretstore"
)

// requireSecureStorage skips the whole app-level test on a machine with no
// user keyring: headless Linux CI has no Secret Service, and the encrypted
// stores the scenario drives cannot exist there. The property under test —
// the relay, the protocol, the accounting — is platform-independent; storage
// availability has its own coverage in the secretstore package.
func requireSecureStorage(t *testing.T) {
	t.Helper()
	if _, err := secretstore.Protect([]byte("probe")); err != nil {
		t.Skipf("secure storage is unavailable on this machine: %v", err)
	}
}
