package bootstrap

import (
	"os"
	"path/filepath"
	"testing"
)

// A broken request history must degrade, never stop the app.
func TestUnusableHistoryDegradesInsteadOfCrashingStartup(t *testing.T) {
	broken := filepath.Join(t.TempDir(), "request_history.v1.db")
	if err := os.WriteFile(broken, []byte("this is not a sqlite database"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SWITCHBOARD_HISTORY_PATH", broken)
	requests, _, err := defaultHistory(30, func(string) {})
	if err == nil {
		t.Fatal("a corrupt request history opened successfully")
	}
	if requests != nil {
		t.Fatal("a failed request history returned a non-nil interface")
	}
}
