package appdata

import (
	"path/filepath"
	"runtime"
	"testing"
)

func TestRootUsesTheNativeUserConfigDirectory(t *testing.T) {
	base := t.TempDir()
	wantName := "provider-switchboard"
	if runtime.GOOS == "windows" {
		t.Setenv("LOCALAPPDATA", base)
		wantName = "ProviderSwitchboard"
	} else {
		t.Setenv("XDG_CONFIG_HOME", base)
	}
	root, err := Root()
	if err != nil || root != filepath.Join(base, wantName) {
		t.Fatalf("unexpected data root: root=%q err=%v", root, err)
	}
}

func TestRootRejectsRelativePlatformDirectories(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Setenv("LOCALAPPDATA", "relative")
	} else {
		t.Setenv("XDG_CONFIG_HOME", "relative")
	}
	if _, err := Root(); err == nil {
		t.Fatal("relative user data directory was accepted")
	}
}
