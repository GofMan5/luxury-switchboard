//go:build windows

package pythonconfig

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOversizedLegacyConfigIsRejectedBeforeDecrypting(t *testing.T) {
	root := t.TempDir()
	t.Setenv("LOCALAPPDATA", root)
	directory := filepath.Join(root, "ProviderSwitchboard")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "config.v1.dpapi")
	if err := os.WriteFile(path, make([]byte, 4*1024*1024+1), 0o600); err != nil {
		t.Fatal(err)
	}
	_, found, err := LoadDefault()
	if err == nil || found {
		t.Fatalf("oversized legacy config was accepted: found=%v err=%v", found, err)
	}
}
