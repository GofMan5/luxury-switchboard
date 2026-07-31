package bootstrap

import "testing"

func TestUnknownEnvironmentProviderFallsBackToLocal(t *testing.T) {
	t.Setenv("SWITCHBOARD_PROVIDER", "removed-provider")
	_, active, err := defaultProviders()
	if err != nil || active != "local" {
		t.Fatalf("stale environment provider blocked startup: active=%q err=%v", active, err)
	}
}
