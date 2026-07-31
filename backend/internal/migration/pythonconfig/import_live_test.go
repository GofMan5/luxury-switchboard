//go:build windows

package pythonconfig

import (
	"os"
	"testing"
)

func TestCurrentUserLegacyConfigIsImportable(t *testing.T) {
	if os.Getenv("SWITCHBOARD_TEST_LEGACY_IMPORT") != "1" {
		t.Skip("live current-user migration gate")
	}
	state, found, err := LoadDefault()
	if err != nil || !found || len(state.Providers) == 0 {
		t.Fatalf("legacy config is not importable: found=%v providers=%d err=%v", found, len(state.Providers), err)
	}
	t.Logf("legacy import ready: providers=%d keys=%d routes=%d tunnel=%v", len(state.Providers), len(state.Keys), len(state.Routes), state.HasTunnel)
}
