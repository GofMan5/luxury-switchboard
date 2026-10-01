package bootstrap

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
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

// The per-IP ban, the RPM queue and client telemetry all key on the address the
// publisher edge reports, so that header must be set from the observed peer and
// never accepted from the public client.
func TestPublishedHubOverwritesTheClientAddressHeader(t *testing.T) {
	template, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "deploy", "Caddyfile.tunnel-hub.example"))
	if err != nil {
		t.Skipf("publisher template is unavailable: %v", err)
	}
	pattern := regexp.MustCompile(`(?m)^\s*header_up\s+X-Tunnel-Client-IP\s+\{http\.request\.remote\.host\}\s*$`)
	if !pattern.Match(template) {
		t.Fatal("the publisher template no longer overwrites X-Tunnel-Client-IP, so a public client can name itself")
	}
	if strings.Contains(string(template), "header_up +X-Tunnel-Client-IP") {
		t.Fatal("the publisher template appends to X-Tunnel-Client-IP instead of replacing it")
	}
}
