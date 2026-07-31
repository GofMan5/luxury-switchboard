package openssh

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBaseArgsDisableAmbientTrustAndAuthentication(t *testing.T) {
	command := strings.Join((Client{Identity: "key", KnownHosts: "hosts"}).BaseArgs(), " ")
	for _, required := range []string{"-F", "StrictHostKeyChecking=yes", "UserKnownHostsFile=hosts", "IdentityAgent=none", "PasswordAuthentication=no", "ForwardAgent=no"} {
		if !strings.Contains(command, required) {
			t.Fatalf("SSH hardening missing %q: %s", required, command)
		}
	}
}

func TestKnownHostsIsNotRewrittenWhenAlreadyPinned(t *testing.T) {
	path := filepath.Join(t.TempDir(), "known_hosts")
	if err := ensureKnownHosts(path); err != nil {
		t.Fatal(err)
	}
	stamp := time.Unix(1_000, 0)
	if err := os.Chtimes(path, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if err := ensureKnownHosts(path); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.ModTime().Equal(stamp) {
		t.Fatal("unchanged host trust was rewritten")
	}
}
