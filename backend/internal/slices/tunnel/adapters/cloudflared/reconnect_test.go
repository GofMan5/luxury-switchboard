package cloudflared

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"
	"time"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/tunnel/domain"
)

func TestSupervisorRetriesRepeatedFailedReconnectsWithBackoff(t *testing.T) {
	binary := reconnectHelper(t)
	counter := filepath.Join(t.TempDir(), "starts")
	runtime := NewRuntime(&fakeGateway{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runtime.runCancel = cancel
	if _, err := runtime.serve(ctx, binary, counter); err != nil {
		t.Fatal(err)
	}
	defer runtime.Stop(context.Background())
	online := make(chan string, 1)
	runtime.OnState(func(state domain.State, address, _ string) {
		if state == domain.StateOnline {
			online <- address
		}
	})
	done := make(chan struct{})
	go func() { defer close(done); runtime.supervise(ctx, binary, counter) }()
	runtime.mu.Lock()
	process := runtime.cmd.Process
	runtime.mu.Unlock()
	dropped := time.Now()
	if err := process.Kill(); err != nil {
		t.Fatal(err)
	}
	select {
	case address := <-online:
		if address != "https://fixture-reconnect.trycloudflare.com/v1" {
			t.Fatalf("wrong new address: %s", address)
		}
		if elapsed := time.Since(dropped); elapsed < 14*time.Second {
			t.Fatalf("failed starts skipped backoff: %v", elapsed)
		}
		starts, err := os.ReadFile(counter)
		if err != nil || string(starts) != "4" {
			t.Fatalf("did not retry through two failed starts: %q %v", starts, err)
		}
	case <-done:
		t.Fatal("supervisor abandoned reconnect after a failed start while context is alive")
	case <-time.After(45 * time.Second):
		t.Fatal("reconnect never recovered")
	}
	if err := runtime.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("stopped supervisor did not exit")
	}
}

func TestSupervisorStopsDuringReconnectBackoff(t *testing.T) {
	runtime := NewRuntime(&fakeGateway{})
	runtime.exited = make(chan error, 1)
	runtime.exited <- nil
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runtime.runCancel = cancel
	reconnecting := make(chan struct{})
	runtime.OnState(func(state domain.State, _, _ string) {
		if state == domain.StateStarting {
			close(reconnecting)
		}
	})
	done := make(chan struct{})
	go func() { defer close(done); runtime.supervise(ctx, "must-not-run", "fixture") }()
	select {
	case <-reconnecting:
	case <-time.After(time.Second):
		t.Fatal("drop not observed")
	}
	if err := runtime.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Stop waited for the reconnect timer")
	}
}

func reconnectHelper(t *testing.T) string {
	t.Helper()
	// This executable only writes a fixture counter/address and sleeps. It never
	// opens a socket or talks to Cloudflare; two middle starts exit before a URL.
	dir := t.TempDir()
	source := filepath.Join(dir, "main.go")
	code := `package main
import("fmt"; "os"; "strconv"; "time")
func main() {
 path:=os.Args[len(os.Args)-1]
 data,_:=os.ReadFile(path); n,_:=strconv.Atoi(string(data))
 if os.WriteFile(path,[]byte(strconv.Itoa(n+1)),0600)!=nil { os.Exit(2) }
 if n==1 || n==2 { os.Exit(1) }
 fmt.Fprintln(os.Stderr,"https://fixture-reconnect.trycloudflare.com")
 time.Sleep(time.Hour)
}
`
	if err := os.WriteFile(source, []byte(code), 0600); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(dir, "helper")
	if goruntime.GOOS == "windows" {
		binary += ".exe"
	}
	command := exec.Command("go", "build", "-o", binary, source)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("fixture build failed: %v\n%s", err, strings.TrimSpace(string(output)))
	}
	return binary
}
