package appdata

import (
	"path/filepath"
	"runtime"
	"testing"
)

// Each fixture drives the environment variable its platform actually reads
// through os.UserConfigDir: darwin builds its root from HOME and ignores the
// XDG variable, so pointing XDG_CONFIG_HOME at a temp dir there would test
// nothing while the assertions fail against the real home directory.

func TestRootUsesTheNativeUserConfigDirectory(t *testing.T) {
	base := t.TempDir()
	var want string
	switch runtime.GOOS {
	case "windows":
		t.Setenv("LOCALAPPDATA", base)
		want = filepath.Join(base, "ProviderSwitchboard")
	case "darwin":
		t.Setenv("HOME", base)
		want = filepath.Join(base, "Library", "Application Support", "provider-switchboard")
	default:
		t.Setenv("XDG_CONFIG_HOME", base)
		want = filepath.Join(base, "provider-switchboard")
	}
	if root, err := Root(); err != nil || root != want {
		t.Fatalf("unexpected data root: root=%q want=%q err=%v", root, want, err)
	}
}

func TestRootRejectsRelativePlatformDirectories(t *testing.T) {
	switch runtime.GOOS {
	case "windows":
		t.Setenv("LOCALAPPDATA", "relative")
	case "darwin":
		t.Setenv("HOME", "relative")
	default:
		t.Setenv("XDG_CONFIG_HOME", "relative")
	}
	if _, err := Root(); err == nil {
		t.Fatal("relative user data directory was accepted")
	}
}
