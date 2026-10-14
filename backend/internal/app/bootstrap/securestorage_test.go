package bootstrap

import (
	"context"
	"os"
	"testing"

	settingsstore "github.com/luxuryprivate/switchboard/backend/internal/slices/settings/adapters/dpapi"
	settingsdomain "github.com/luxuryprivate/switchboard/backend/internal/slices/settings/domain"

	"github.com/luxuryprivate/switchboard/backend/internal/platform/secretstore"
)

// requireSecureStorage skips the whole app-level test on a machine with no
// user keyring: headless Linux CI has no Secret Service, and the encrypted
// stores the scenario drives cannot exist there. The property under test —
// the relay, the protocol, the accounting — is platform-independent; storage
// availability has its own coverage in the secretstore package.
// Synthetic app tests do not need vendor liveness or a release feed. Persist
// their disabled state at the explicitly isolated test path before wiring.
func disableBackgroundChecks(t *testing.T) {
	t.Helper()
	settings := settingsdomain.Defaults()
	settings.ProviderHealthEnabled = false
	settings.UpdateCheckInterval = "off"
	path := os.Getenv("SWITCHBOARD_SETTINGS_PATH")
	if path == "" {
		t.Fatal("background-check isolation requires a temporary settings path")
	}
	if err := settingsstore.New(path).Save(context.Background(), settings); err != nil {
		t.Skipf("isolated secure settings are unavailable: %v", err)
	}
}

func requireSecureStorage(t *testing.T) {
	t.Helper()
	if _, err := secretstore.Protect([]byte("probe")); err != nil {
		t.Skipf("secure storage is unavailable on this machine: %v", err)
	}
}
