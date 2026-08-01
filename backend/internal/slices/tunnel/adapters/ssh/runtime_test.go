package ssh

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/luxuryprivate/switchboard/backend/internal/platform/openssh"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/tunnel/domain"
)

type deadlineLocal struct{ bounded bool }

func (*deadlineLocal) Start(context.Context, domain.Config) (string, error) { return "", nil }
func (local *deadlineLocal) Stop(ctx context.Context) error {
	_, local.bounded = ctx.Deadline()
	return nil
}

func TestSSHCommandIgnoresUserConfigAgentAndPasswords(t *testing.T) {
	command := strings.Join(sshArgs(openssh.Client{Identity: "publisher-key", KnownHosts: "pinned-hosts"}, 8797, 23456), " ")
	for _, required := range []string{"-F", "StrictHostKeyChecking=yes", "UserKnownHostsFile=pinned-hosts", "IdentityAgent=none", "PasswordAuthentication=no", "ExitOnForwardFailure=yes", "127.0.0.1:23456:127.0.0.1:8797"} {
		if !strings.Contains(command, required) {
			t.Fatalf("SSH hardening missing %q: %s", required, command)
		}
	}
}

func TestPublicReadinessProbeRejectsRedirects(t *testing.T) {
	runtime := NewRuntime(nil, nil, nil)
	if err := runtime.client.CheckRedirect(&http.Request{}, nil); !errors.Is(err, http.ErrUseLastResponse) {
		t.Fatalf("public readiness probe followed a redirect: %v", err)
	}
}

func TestFailedPublisherStartupUsesBoundedLocalCleanup(t *testing.T) {
	local := &deadlineLocal{}
	runtime := NewRuntime(local, nil, nil)
	runtime.stopLocal()
	if !local.bounded {
		t.Fatal("failed publisher startup used an unbounded local shutdown")
	}
}
