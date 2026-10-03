package cloudflared

import (
	"context"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/tunnel/domain"
)

// The checksum pins are compared against hex.EncodeToString, which speaks
// lowercase. An uppercase pin is a pin that never matches — and the connector
// then refuses to start on every machine.
func TestTheChecksumPinsAreLowercaseHex(t *testing.T) {
	for platform, pin := range connectorSHA256 {
		decoded, err := hex.DecodeString(pin)
		if err != nil || len(decoded) != 32 {
			t.Fatalf("%s pin is not a SHA-256 hex digest: %q", platform, pin)
		}
		if pin != strings.ToLower(pin) {
			t.Fatalf("%s pin is uppercase; it can never match EncodeToString", platform)
		}
	}
}

// A start whose connector never answers must reap the process and clear the
// handle — otherwise every later start wedges behind "tunnel already running".
func TestAFailedStartDoesNotWedgeTheNextOne(t *testing.T) {
	runtime := NewRuntime(&fakeGateway{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancelled before the start begins: it fails immediately
	if _, err := runtime.Start(ctx, domain.Config{Port: 1, Token: strings.Repeat("t", 32)}); err == nil {
		t.Fatal("a cancelled start reported success")
	}
	runtime.mu.Lock()
	wedged := runtime.cmd != nil
	runtime.mu.Unlock()
	if wedged {
		t.Fatal("a failed start left a connector handle behind")
	}
}

// Stop during a start kills the connector and serve reaps it: Stop never waits
// on the process itself, because supervise owns the reap (exec.Cmd.Wait is not
// concurrency-safe).
func TestStopDuringServeLeavesNoProcessBehind(t *testing.T) {
	runtime := NewRuntime(&fakeGateway{})
	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := runtime.serve(runCtx, helperProcess(t), "http://127.0.0.1:1")
		done <- err
	}()
	// The helper compiles itself on first use, and the full suite starves this
	// machine of CPU exactly then; the windows are sized for that, not for an
	// idle machine.
	deadline := time.After(30 * time.Second)
	for {
		runtime.mu.Lock()
		started := runtime.cmd != nil
		runtime.mu.Unlock()
		if started {
			break
		}
		select {
		case <-deadline:
			t.Fatal("the connector never started")
		case <-time.After(5 * time.Millisecond):
		}
	}
	if err := runtime.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a stopped start reported an address")
		}
	case <-time.After(15 * time.Second):
		t.Fatal("the stopped connector was never reaped")
	}
	runtime.mu.Lock()
	wedged := runtime.cmd != nil
	runtime.mu.Unlock()
	if wedged {
		t.Fatal("a stopped start left a connector handle behind")
	}
}

// helperProcess builds a tiny binary that just sleeps: the connector's
// lifecycle needs a real process, and "the connector" in a test is this.
func helperProcess(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	source := filepath.Join(dir, "main.go")
	if err := os.WriteFile(source, []byte("package main\nimport \"time\"\nfunc main() { time.Sleep(time.Hour) }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(dir, "helper")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	build := exec.Command("go", "build", "-o", binary, source)
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("the helper process did not build: %v\n%s", err, output)
	}
	return binary
}

type fakeGateway struct{}

func (gateway *fakeGateway) Start(context.Context, domain.Config) (string, error) {
	return "http://127.0.0.1:1/v1", nil
}
func (gateway *fakeGateway) Stop(context.Context) error { return nil }
