package bootstrap

import "testing"

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
