//go:build windows

package atomicfile

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A reader that holds the target for a moment — Go opens without delete
// sharing, as antivirus scanners do — must delay the replace, not fail it.
func TestReplaceWaitsOutABriefHolderOfTheTarget(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.bin")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	holder, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(20 * time.Millisecond)
		_ = holder.Close()
	}()
	if err := Replace(path, []byte("new"), 0o600); err != nil {
		t.Fatalf("a brief holder failed the replace: %v", err)
	}
	if got, _ := os.ReadFile(path); string(got) != "new" {
		t.Fatalf("target holds %q", got)
	}
}
