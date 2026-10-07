package loopback

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// awaitTimeout bounds every AwaitCode that is expected to resolve
// immediately: a flow that finished never takes longer than the bound,
// and a bug that wedges the wait fails the test instead of hanging it.
const awaitTimeout = 2 * time.Second

// callbackURL returns the address the running session is serving, so
// tests issue real HTTP requests exactly the way the browser will.
func callbackURL(t *testing.T, server *RedirectServer) string {
	t.Helper()
	server.mu.Lock()
	active := server.active
	server.mu.Unlock()
	if active == nil {
		t.Fatal("the redirect server is not running")
	}
	return "http://" + active.listener.Addr().String()
}

// startTestServer starts a server on an ephemeral loopback port — the
// host stays 127.0.0.1, only the port differs from production — and
// arranges for it to be stopped again.
func startTestServer(t *testing.T, state string) *RedirectServer {
	t.Helper()
	server := newRedirectServerAt("127.0.0.1:0")
	if err := server.Start(state); err != nil {
		t.Fatalf("Start(%q) error = %v, want nil", state, err)
	}
	t.Cleanup(server.Stop)
	return server
}

func TestNewRedirectServerPinsTheLoopbackAddress(t *testing.T) {
	server := NewRedirectServer()

	if server.laddr != "127.0.0.1:1455" {
		t.Fatalf("new server address = %q, want %q", server.laddr, "127.0.0.1:1455")
	}
	// No listener was opened: the port is bound only when a login flow
	// starts, and constructing the adapter must not cost anything.
	server.mu.Lock()
	active := server.active
	server.mu.Unlock()
	if active != nil {
		t.Fatal("a fresh RedirectServer must not have a running session")
	}
}

func TestStartWhileAlreadyRunningIsRejected(t *testing.T) {
	server := startTestServer(t, "state-1")

	err := server.Start("state-2")
	if err == nil {
		t.Fatal("Start() on an already running server must be rejected")
	}
	if err.Error() != "codex oauth server already running" {
		t.Fatalf("Start() error = %q, want %q", err.Error(), "codex oauth server already running")
	}
}

func TestConcurrentStartsInstallExactlyOneSession(t *testing.T) {
	// Ephemeral ports let every concurrent bind succeed, so the race is
	// decided entirely by the install re-check — the interleaving the
	// pinned production port makes unlikely (its second bind fails at the
	// listen) but which the re-check must resolve regardless. Every
	// starter passes the same state: whichever one wins, the flow it
	// installs is the one the callback below completes.
	server := newRedirectServerAt("127.0.0.1:0")
	t.Cleanup(server.Stop)

	const starters = 8
	results := make(chan error, starters)
	for i := 0; i < starters; i++ {
		go func() { results <- server.Start("race-state") }()
	}

	wins := 0
	for i := 0; i < starters; i++ {
		if err := <-results; err == nil {
			wins++
		} else if !errors.Is(err, errAlreadyRunning) {
			t.Fatalf("concurrent Start() error = %v, want the already-running rejection", err)
		}
	}
	if wins != 1 {
		t.Fatalf("concurrent Starts won %d times, want exactly 1", wins)
	}

	// The winner owns the port and serves the flow; the losers' listeners
	// were closed, so nothing else answers.
	response, err := http.Get(callbackURL(t, server) + "/auth/callback?state=race-state&code=race-code")
	if err != nil {
		t.Fatalf("callback GET on the racing server: %v", err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("callback GET status = %d, want %d", response.StatusCode, http.StatusOK)
	}

	code, err := server.AwaitCode(context.Background())
	if err != nil {
		t.Fatalf("AwaitCode() error = %v, want nil", err)
	}
	if code != "race-code" {
		t.Fatalf("AwaitCode() code = %q, want %q", code, "race-code")
	}
}

func TestStartOnAnOccupiedPortNamesThePort(t *testing.T) {
	blocker, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("binding the blocker: %v", err)
	}
	t.Cleanup(func() { _ = blocker.Close() })

	addr := blocker.Addr().String()
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("splitting %q: %v", addr, err)
	}
	server := newRedirectServerAt(addr)

	err = server.Start("state-1")
	if err == nil {
		t.Fatal("Start() on an occupied port must fail")
	}
	if prefix := "codex oauth port " + port + " in use: "; !strings.HasPrefix(err.Error(), prefix) {
		t.Fatalf("Start() error = %q, want prefix %q", err.Error(), prefix)
	}
	if !strings.Contains(err.Error(), "127.0.0.1") {
		t.Fatalf("Start() error = %q, want the underlying listen error inside", err.Error())
	}
}

func TestStopIsIdempotentAndAnswersAStoppedAwait(t *testing.T) {
	server := newRedirectServerAt("127.0.0.1:0")
	if err := server.Start("state-1"); err != nil {
		t.Fatalf("Start() error = %v, want nil", err)
	}

	// Stopping twice — once from the test, once from the cleanup — must
	// neither panic nor fail: the teardown path runs Stop exactly this
	// redundantly whenever a login ends early.
	server.Stop()
	server.Stop()

	code, err := server.AwaitCode(context.Background())
	if code != "" {
		t.Fatalf("AwaitCode() code = %q, want empty", code)
	}
	if err == nil {
		t.Fatal("AwaitCode() on a stopped server must fail")
	}
	if err.Error() != "codex oauth server stopped" {
		t.Fatalf("AwaitCode() error = %q, want %q", err.Error(), "codex oauth server stopped")
	}
}

func TestStartAfterStopRebindsTheSamePortForTheNextFlow(t *testing.T) {
	// Reserve a fixed free port, then release it: the test needs the
	// address to be predictable across the restart.
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("binding the probe: %v", err)
	}
	_, port, err := net.SplitHostPort(probe.Addr().String())
	if err != nil {
		t.Fatalf("splitting the probe address: %v", err)
	}
	picked := "127.0.0.1:" + port
	_ = probe.Close()

	server := newRedirectServerAt(picked)
	t.Cleanup(server.Stop)

	if err := server.Start("first"); err != nil {
		t.Fatalf("first Start() error = %v, want nil", err)
	}
	bound := strings.TrimPrefix(callbackURL(t, server), "http://")
	if bound != picked {
		t.Fatalf("first flow bound %q, want %q", bound, picked)
	}
	// No request is served on the first flow: the port goes through Stop
	// clean, so the rebind below cannot collide with a connection the
	// first flow left behind.
	server.Stop()

	if err := server.Start("second"); err != nil {
		t.Fatalf("second Start() error = %v, want nil: the port must be rebindable", err)
	}
	response, err := http.Get(callbackURL(t, server) + "/auth/callback?state=second&code=code-2")
	if err != nil {
		t.Fatalf("callback GET on the rebound server: %v", err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("callback GET status = %d, want %d", response.StatusCode, http.StatusOK)
	}

	code, err := server.AwaitCode(context.Background())
	if err != nil {
		t.Fatalf("AwaitCode() error = %v, want nil", err)
	}
	if code != "code-2" {
		t.Fatalf("AwaitCode() code = %q, want %q", code, "code-2")
	}
}

func TestAwaitCodeFailsWhenTheCallerCancelsFirst(t *testing.T) {
	server := startTestServer(t, "state-1")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	code, err := server.AwaitCode(ctx)
	if code != "" {
		t.Fatalf("AwaitCode() code = %q, want empty", code)
	}
	if err == nil {
		t.Fatal("AwaitCode() with a cancelled context must fail")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("AwaitCode() error = %v, want context.Canceled inside", err)
	}
	if err.Error() != "codex login await: context canceled" {
		t.Fatalf("AwaitCode() error = %q, want %q", err.Error(), "codex login await: context canceled")
	}
}
