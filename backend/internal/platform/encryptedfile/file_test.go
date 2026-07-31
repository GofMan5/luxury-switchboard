package encryptedfile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadRejectsOversizedFileBeforeDecrypting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "oversized.dpapi")
	if err := os.WriteFile(path, make([]byte, 65), 0o600); err != nil {
		t.Fatal(err)
	}
	var target map[string]any
	if found, err := Load(path, []byte("MAGIC"), 32, &target); err == nil || found {
		t.Fatalf("oversized encrypted file was accepted: found=%v err=%v", found, err)
	}
}
