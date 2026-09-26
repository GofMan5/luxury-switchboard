package bootstrap

import (
	"path/filepath"
	"testing"
)

func TestUnknownEnvironmentProviderFallsBackToLocal(t *testing.T) {
	t.Setenv("SWITCHBOARD_PROVIDER", "removed-provider")
	_, active, err := defaultProviders()
	if err != nil || active != "local" {
		t.Fatalf("stale environment provider blocked startup: active=%q err=%v", active, err)
	}
}

func TestProviderRateEnvironmentUsesTheDomainLimit(t *testing.T) {
	t.Setenv("SWITCHBOARD_ECHO_RPM", "120000")
	providers, _, err := defaultProviders()
	if err != nil || providers[1].RPM != 120000 {
		t.Fatalf("valid provider RPM was replaced: rpm=%d err=%v", providers[1].RPM, err)
	}
}

func TestEnvironmentCredentialIsNeverAutoLoaded(t *testing.T) {
	t.Setenv("FREEMODEL_API_KEY", "must-not-be-loaded")
	t.Setenv("SWITCHBOARD_KEYS_PATH", filepath.Join(t.TempDir(), "keys.dpapi"))
	providers, _, err := defaultProviders()
	if err != nil {
		t.Fatal(err)
	}
	_, manager, _, _, err := defaultKeyManager(providers, 100)
	if err != nil {
		t.Fatal(err)
	}
	if count := manager.Count("echo"); count != 0 {
		t.Fatalf("environment credential was auto-loaded: %d keys", count)
	}
}
