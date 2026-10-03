// Package cloudflared publishes the local public gateway through a Cloudflare
// quick tunnel. No account, no host of ours, no SSH keys, no dashboard: the
// connector binary is downloaded once from the pinned release, verified
// against its checksum, and run with a single flag. The trade is honest and
// stated in the UI: a quick tunnel's public address is re-rolled every start.
package cloudflared

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"sync"
	"time"

	tunnelapp "github.com/luxuryprivate/switchboard/backend/internal/slices/tunnel/application"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/tunnel/domain"
)

// quickTunnelURL is the address Cloudflare assigns a nameless tunnel.
var quickTunnelURL = regexp.MustCompile(`https://[-a-z0-9]+\.trycloudflare\.com`)

const (
	// urlWait is how long Start waits for the connector to answer with its
	// public address before calling it a failed start.
	urlWait = 60 * time.Second
	// restartBackoff caps the reconnect ladder after a dropped session.
	restartBackoff = 30 * time.Second
)

// Runtime owns the connector process for as long as the tunnel is up. The
// gateway it fronts is a separate runtime; this one only manages the edge.
type Runtime struct {
	gateway tunnelapp.Runtime

	mu        sync.Mutex
	cmd       *exec.Cmd
	exited    chan error
	runCancel context.CancelFunc
	listeners []func(domain.State, string, string)
}

func NewRuntime(gateway tunnelapp.Runtime) *Runtime {
	return &Runtime{gateway: gateway}
}

// OnState wires the connector's state transitions into the tunnel service:
// installing, starting, online with the address, reconnecting after a drop.
func (runtime *Runtime) OnState(listener func(domain.State, string, string)) {
	if listener == nil {
		return
	}
	runtime.mu.Lock()
	runtime.listeners = append(runtime.listeners, listener)
	runtime.mu.Unlock()
}

func (runtime *Runtime) emit(state domain.State, address, message string) {
	runtime.mu.Lock()
	listeners := append([]func(domain.State, string, string){}, runtime.listeners...)
	runtime.mu.Unlock()
	for _, listener := range listeners {
		listener(state, address, message)
	}
}

func (runtime *Runtime) Start(ctx context.Context, config domain.Config) (string, error) {
	runtime.mu.Lock()
	if runtime.cmd != nil {
		runtime.mu.Unlock()
		return "", errors.New("tunnel already running")
	}
	runtime.mu.Unlock()

	local, err := runtime.gateway.Start(ctx, config)
	if err != nil {
		return "", err
	}
	binary, err := runtime.ensureConnector(ctx)
	if err != nil {
		_ = runtime.gateway.Stop(ctx)
		return "", err
	}
	runCtx, cancel := context.WithCancel(context.Background())
	runtime.mu.Lock()
	runtime.runCancel = cancel
	runtime.mu.Unlock()

	address, err := runtime.serve(runCtx, binary, local)
	if err != nil {
		cancel()
		_ = runtime.gateway.Stop(context.Background())
		return "", err
	}
	go runtime.supervise(runCtx, binary, local)
	return address, nil
}

func (runtime *Runtime) Stop(ctx context.Context) error {
	runtime.mu.Lock()
	cancel := runtime.runCancel
	cmd := runtime.cmd
	runtime.runCancel = nil
	runtime.cmd = nil
	runtime.exited = nil
	runtime.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	// Whoever kills waits — and that is never Stop: supervise owns the reap of
	// every process it spawned, because exec.Cmd.Wait is not concurrency-safe.
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	return runtime.gateway.Stop(ctx)
}

// serve starts one connector instance and waits for the address it prints.
// The scanner and the waiter goroutines stay with the process: the address
// line is the goal of this call, and everything that follows it — the log,
// the exit — is supervision's business. A failed start reaps its own process
// and clears the handle: a connector that never answered must not wedge the
// next start behind "already running".
func (runtime *Runtime) serve(ctx context.Context, binary, local string) (string, error) {
	command := exec.CommandContext(ctx, binary, "tunnel", "--no-autoupdate", "--url", local)
	hideConsole(command)
	output, err := command.StderrPipe()
	if err != nil {
		return "", err
	}
	if err := command.Start(); err != nil {
		return "", fmt.Errorf("the tunnel connector could not start: %w", err)
	}
	// The waiter owns the reap from the first breath: exec.Cmd.Wait is not
	// concurrency-safe, so exactly one goroutine ever calls it per process,
	// and everyone else reads its answer.
	exited := make(chan error, 1)
	go func() { exited <- command.Wait() }()
	runtime.mu.Lock()
	runtime.cmd = command
	runtime.exited = exited
	runtime.mu.Unlock()

	address := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(output)
		scanner.Buffer(make([]byte, 64*1024), 256*1024)
		for scanner.Scan() {
			if match := quickTunnelURL.FindString(scanner.Text()); match != "" {
				select {
				case address <- match + "/v1":
				default:
				}
			}
		}
	}()

	// fail reaps unless the caller already holds the exit: the exited channel
	// delivers once, and a second read of it would wait forever.
	fail := func(err error, reaped bool) (string, error) {
		runtime.mu.Lock()
		if runtime.cmd == command {
			runtime.cmd = nil
			runtime.exited = nil
		}
		runtime.mu.Unlock()
		if !reaped {
			_ = command.Process.Kill()
			<-exited
		}
		return "", err
	}

	select {
	case <-ctx.Done():
		return fail(ctx.Err(), false)
	case found := <-address:
		return found, nil
	case exitErr := <-exited:
		// The connector died before answering: a missing binary, a flag the
		// pinned version rejected, a port it could not claim. Waiting out the
		// address deadline would only be slower wrong.
		return fail(fmt.Errorf("the tunnel connector exited before answering: %v", exitErr), true)
	case <-time.After(urlWait):
		return fail(errors.New("the tunnel connector never answered with a public address"), false)
	}
}

// supervise keeps a dropped session alive: quick tunnels drop, and the answer
// is a fresh connector with a fresh address, reported as reconnecting until
// the new one is online.
func (runtime *Runtime) supervise(ctx context.Context, binary, local string) {
	backoff := 2 * time.Second
	for {
		runtime.mu.Lock()
		exited := runtime.exited
		runtime.mu.Unlock()
		if exited == nil {
			return
		}
		<-exited
		if ctx.Err() != nil {
			return
		}
		runtime.emit(domain.StateStarting, "", "The tunnel dropped; reconnecting")
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, restartBackoff)
		address, err := runtime.serve(ctx, binary, local)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			continue
		}
		backoff = 2 * time.Second
		runtime.emit(domain.StateOnline, address, "")
	}
}
