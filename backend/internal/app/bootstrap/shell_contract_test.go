package bootstrap

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Every control-plane command reaches the sidecar through the desktop shell, which
// drops methods outside its allowlist. A handler that is not listed there is dead
// in the packaged app while every Go and frontend test still passes, so the two
// sides are compared directly.
func TestEveryControlPlaneCommandIsAllowedByTheDesktopShell(t *testing.T) {
	shell, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "src-tauri", "src", "sidecar.rs"))
	if err != nil {
		t.Skipf("desktop shell is unavailable: %v", err)
	}
	allowlist := string(shell)
	handled := regexp.MustCompile(`server\.Handle\("([a-zA-Z0-9_.]+)"`)
	slices := filepath.Join("..", "..", "slices")
	missing := make([]string, 0, 4)
	seen := make(map[string]struct{}, 64)
	walkErr := filepath.WalkDir(slices, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		source, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		for _, match := range handled.FindAllStringSubmatch(string(source), -1) {
			method := match[1]
			if _, duplicate := seen[method]; duplicate {
				continue
			}
			seen[method] = struct{}{}
			if !strings.Contains(allowlist, `"`+method+`"`) {
				missing = append(missing, method)
			}
		}
		return nil
	})
	if walkErr != nil {
		t.Fatal(walkErr)
	}
	if len(seen) < 10 {
		t.Fatalf("no control-plane commands were discovered, the scan is broken: %d", len(seen))
	}
	if len(missing) > 0 {
		t.Fatalf("commands are unreachable from the desktop shell: %s", strings.Join(missing, ", "))
	}
}
