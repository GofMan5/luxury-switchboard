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
	runtime.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}
	return runtime.gateway.Stop(ctx)
}

// serve starts one connector instance and waits for the address it prints.
// The scanner stays open after Start returns: the address line is the goal of
// this call, and the log that follows it is supervision's business.
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
	runtime.mu.Lock()
	runtime.cmd = command
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

	select {
	case <-ctx.Done():
		_ = command.Process.Kill()
		return "", ctx.Err()
	case found := <-address:
		return found, nil
	case <-time.After(urlWait):
		_ = command.Process.Kill()
		return "", errors.New("the tunnel connector never answered with a public address")
	}
}

// supervise keeps a dropped session alive: quick tunnels drop, and the answer
// is a fresh connector with a fresh address, reported as reconnecting until
// the new one is online.
func (runtime *Runtime) supervise(ctx context.Context, binary, local string) {
	backoff := 2 * time.Second
	for {
		runtime.mu.Lock()
		command := runtime.cmd
		runtime.mu.Unlock()
		if command == nil {
			return
		}
		_ = command.Wait()
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
