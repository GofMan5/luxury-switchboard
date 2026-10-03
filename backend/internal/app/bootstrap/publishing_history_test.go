package bootstrap

import (
	"os"
	"path/filepath"
	"testing"
)

// A broken client history must degrade, never stop the app: the history factory has
// to return a truly nil interface so every downstream guard stays honest.
func TestUnusableTunnelHistoryDegradesInsteadOfCrashingStartup(t *testing.T) {
	corrupt := filepath.Join(t.TempDir(), "tunnel_history.v1.db")
	if err := os.WriteFile(corrupt, []byte("this is not a sqlite database"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SWITCHBOARD_TUNNEL_HISTORY_PATH", corrupt)
	history, err := defaultTunnelHistory(72)
	if err == nil {
		t.Fatal("a corrupt tunnel history opened successfully")
	}
	if history != nil {
		t.Fatal("a failed tunnel history returned a non-nil interface, so every nil guard downstream lies")
	}
}
