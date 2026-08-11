package bootstrap

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// A broken client history must degrade, never stop the app: the history factories
// have to return a truly nil interface so every downstream guard stays honest.
func TestUnusableHistoryDegradesInsteadOfCrashingStartup(t *testing.T) {
	root := t.TempDir()
	corrupt := filepath.Join(root, "tunnel_history.v1.db")
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

	broken := filepath.Join(root, "request_history.v1.db")
	if err := os.WriteFile(broken, []byte("this is not a sqlite database"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SWITCHBOARD_HISTORY_PATH", broken)
	requests, err := defaultHistory(30)
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
