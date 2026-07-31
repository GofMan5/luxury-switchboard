package openssh

import (
	"strings"
	"testing"
)

func TestBaseArgsDisableAmbientTrustAndAuthentication(t *testing.T) {
	command := strings.Join((Client{Identity: "key", KnownHosts: "hosts"}).BaseArgs(), " ")
	for _, required := range []string{"-F", "StrictHostKeyChecking=yes", "UserKnownHostsFile=hosts", "IdentityAgent=none", "PasswordAuthentication=no", "ForwardAgent=no"} {
		if !strings.Contains(command, required) {
			t.Fatalf("SSH hardening missing %q: %s", required, command)
		}
	}
}
