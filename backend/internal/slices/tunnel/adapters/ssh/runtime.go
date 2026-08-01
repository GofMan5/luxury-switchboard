package ssh

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/luxuryprivate/switchboard/backend/internal/platform/openssh"
	publicdomain "github.com/luxuryprivate/switchboard/backend/internal/slices/publictunnel/domain"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/tunnel/domain"
)

const (
	sshDestination = "model-tunnel@" + openssh.Host
	identityName   = "model-tunnel_ed25519"
	maxModelsBody  = 128 * 1024
)

type LocalRuntime interface {
	Start(context.Context, domain.Config) (string, error)
	Stop(context.Context) error
}

type Routes interface{ List() []publicdomain.Route }

type Runtime struct {
	local       LocalRuntime
	routes      Routes
	client      *http.Client
	opMu        sync.Mutex
	mu          sync.Mutex
	cancel      context.CancelFunc
	desired     bool
	process     *sshProcess
	url         string
	handler     func(domain.State, string, string)
	beforeStart func(context.Context) error
}

type sshProcess struct {
	cmd    *exec.Cmd
	done   <-chan error
	stderr *limitedBuffer
}

func NewRuntime(local LocalRuntime, routes Routes, beforeStart func(context.Context) error) *Runtime {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	return &Runtime{local: local, routes: routes, client: &http.Client{Transport: transport, Timeout: 8 * time.Second, CheckRedirect: rejectRedirect}, beforeStart: beforeStart}
}

func rejectRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

func (runtime *Runtime) OnState(handler func(domain.State, string, string)) {
	runtime.mu.Lock()
	runtime.handler = handler
	runtime.mu.Unlock()
}

func (runtime *Runtime) Start(startupCtx context.Context, config domain.Config) (string, error) {
	runtime.opMu.Lock()
	defer runtime.opMu.Unlock()
	if err := startupCtx.Err(); err != nil {
		return "", err
	}
	if config.PublisherProfile != "" && runtime.beforeStart != nil {
		ctx, cancel := context.WithTimeout(startupCtx, 18*time.Second)
		err := runtime.beforeStart(ctx)
		cancel()
		if err != nil {
			if startupCtx.Err() != nil {
				return "", startupCtx.Err()
			}
			return "", errors.New("shared tunnel control is unavailable")
		}
	}
	localAddress, err := runtime.local.Start(startupCtx, config)
	if err != nil {
		if startupCtx.Err() != nil {
			return "", startupCtx.Err()
		}
		return "", errors.New("local tunnel port is unavailable")
	}
	if err := startupCtx.Err(); err != nil {
		_ = runtime.local.Stop(context.Background())
		return "", err
	}
	if config.PublisherProfile == "" {
		return localAddress, nil
	}
	remotePort, _, err := domain.ParsePublisherProfile(config.PublisherProfile)
	if err != nil {
		_ = runtime.local.Stop(context.Background())
		return "", err
	}
	publicURL, _ := domain.PublisherURL(config.PublisherProfile)
	localPort, err := loopbackPort(localAddress)
	if err != nil {
		_ = runtime.local.Stop(context.Background())
		return "", errors.New("local tunnel address is invalid")
	}
	sshClient, err := publisherClient()
	if err != nil {
		_ = runtime.local.Stop(context.Background())
		return "", err
	}
	ctx, cancel := context.WithCancel(context.Background())
	stopStartupCancel := context.AfterFunc(startupCtx, cancel)
	runtime.mu.Lock()
	if runtime.desired {
		runtime.mu.Unlock()
		cancel()
		_ = runtime.local.Stop(context.Background())
		return "", errors.New("tunnel publisher is already running")
	}
	runtime.desired, runtime.cancel, runtime.url = true, cancel, publicURL
	runtime.mu.Unlock()
	process, err := runtime.connect(ctx, sshClient, localPort, remotePort, publicURL, config.Token)
	if err != nil {
		stopStartupCancel()
		cancel()
		runtime.mu.Lock()
		runtime.desired, runtime.cancel = false, nil
		runtime.mu.Unlock()
		_ = runtime.local.Stop(context.Background())
		return "", err
	}
	if !stopStartupCancel() {
		cancel()
		_ = process.cmd.Process.Kill()
		runtime.mu.Lock()
		runtime.desired, runtime.cancel = false, nil
		runtime.mu.Unlock()
		_ = runtime.local.Stop(context.Background())
		return "", startupCtx.Err()
	}
	runtime.mu.Lock()
	runtime.process = process
	runtime.mu.Unlock()
	go runtime.monitor(ctx, sshClient, localPort, remotePort, publicURL, config.Token, process)
	return publicURL, nil
}

func (runtime *Runtime) Stop(ctx context.Context) error {
	runtime.opMu.Lock()
	defer runtime.opMu.Unlock()
	runtime.mu.Lock()
	runtime.desired = false
	cancel := runtime.cancel
	process := runtime.process
	runtime.cancel, runtime.process, runtime.url = nil, nil, ""
	runtime.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if process != nil && process.cmd.Process != nil {
		_ = process.cmd.Process.Kill()
	}
	return runtime.local.Stop(ctx)
}

func (runtime *Runtime) monitor(ctx context.Context, sshClient openssh.Client, localPort, remotePort int, publicURL, token string, process *sshProcess) {
	delay := time.Second
	for {
		select {
		case <-ctx.Done():
			return
		case <-process.done:
		}
		if !runtime.isDesired() {
			return
		}
		runtime.notify(domain.StateStarting, "", "")
		for runtime.isDesired() {
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			next, err := runtime.connect(ctx, sshClient, localPort, remotePort, publicURL, token)
			if err == nil {
				runtime.mu.Lock()
				if !runtime.desired {
					runtime.mu.Unlock()
					_ = next.cmd.Process.Kill()
					return
				}
				runtime.process = next
				runtime.mu.Unlock()
				runtime.notify(domain.StateOnline, publicURL, "")
				process = next
				delay = time.Second
				break
			}
			delay = min(delay*2, 30*time.Second)
		}
	}
}

func (runtime *Runtime) connect(ctx context.Context, sshClient openssh.Client, localPort, remotePort int, publicURL, token string) (*sshProcess, error) {
	process, err := startSSH(ctx, sshClient, localPort, remotePort)
	if err != nil {
		return nil, errors.New("tunnel SSH client could not start")
	}
	deadline := time.NewTimer(20 * time.Second)
	ticker := time.NewTicker(200 * time.Millisecond)
	defer deadline.Stop()
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			_ = process.cmd.Process.Kill()
			return nil, ctx.Err()
		case <-process.done:
			return nil, sshFailure(process.stderr.String())
		case <-deadline.C:
			_ = process.cmd.Process.Kill()
			return nil, errors.New("tunnel public route did not become ready")
		case <-ticker.C:
			if runtime.modelsReady(ctx, publicURL, token) {
				return process, nil
			}
		}
	}
}

func (runtime *Runtime) modelsReady(ctx context.Context, baseURL, token string) bool {
	requestCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, strings.TrimRight(baseURL, "/")+"/models", nil)
	if err != nil {
		return false
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Accept-Encoding", "identity")
	response, err := runtime.client.Do(request)
	if err != nil {
		return false
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.Header.Get("Content-Encoding") != "" || !strings.HasPrefix(strings.ToLower(response.Header.Get("Content-Type")), "application/json") {
		return false
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxModelsBody+1))
	if err != nil || len(body) > maxModelsBody {
		return false
	}
	var payload struct {
		Object string `json:"object"`
		Data   []struct {
			ID      string `json:"id"`
			Object  string `json:"object"`
			Created int64  `json:"created"`
		} `json:"data"`
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&payload) != nil || payload.Object != "list" {
		return false
	}
	actual := make([]string, 0, len(payload.Data))
	for _, model := range payload.Data {
		if model.ID == "" || model.Object != "model" {
			return false
		}
		actual = append(actual, model.ID)
	}
	expected := make([]string, 0)
	for _, route := range runtime.routes.List() {
		expected = append(expected, route.PublicModel)
	}
	sort.Strings(actual)
	sort.Strings(expected)
	return slices.Equal(actual, expected)
}

func startSSH(ctx context.Context, sshClient openssh.Client, localPort, remotePort int) (*sshProcess, error) {
	args := sshArgs(sshClient, localPort, remotePort)
	cmd := exec.CommandContext(ctx, sshClient.Executable, args...)
	openssh.Configure(cmd)
	stderr := &limitedBuffer{limit: 8 * 1024}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	guard, err := openssh.Guard(cmd.Process)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait(); _ = guard.Close(); close(done) }()
	return &sshProcess{cmd: cmd, done: done, stderr: stderr}, nil
}

func sshArgs(sshClient openssh.Client, localPort, remotePort int) []string {
	return append(sshClient.BaseArgs(),
		"-N", "-R",
		net.JoinHostPort("127.0.0.1", strconv.Itoa(remotePort))+":"+net.JoinHostPort("127.0.0.1", strconv.Itoa(localPort)),
		sshDestination,
	)
}

func publisherClient() (openssh.Client, error) {
	client, err := openssh.Prepare(identityName)
	if err == nil {
		return client, nil
	}
	switch err.Error() {
	case "SSH client is not installed":
		return openssh.Client{}, errors.New("tunnel SSH client is not installed")
	case "SSH identity is not installed":
		return openssh.Client{}, errors.New("tunnel publisher key is not installed")
	default:
		return openssh.Client{}, errors.New("tunnel host trust could not be prepared")
	}
}

func loopbackPort(address string) (int, error) {
	parsed, err := url.Parse(address)
	if err != nil || parsed.Scheme != "http" || !isLoopback(parsed.Hostname()) {
		return 0, errors.New("invalid local tunnel address")
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil || port < 1 || port > 65535 {
		return 0, errors.New("invalid local tunnel port")
	}
	return port, nil
}

func isLoopback(host string) bool {
	return strings.EqualFold(host, "localhost") || (net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback())
}

func sshFailure(stderr string) error {
	text := strings.ToLower(stderr)
	switch {
	case strings.Contains(text, "remote port forwarding failed"), strings.Contains(text, "cannot listen to port"), strings.Contains(text, "failed to listen on"):
		return errors.New("tunnel publisher profile is already active")
	case strings.Contains(text, "permission denied"):
		return errors.New("tunnel publisher key was rejected")
	case strings.Contains(text, "host key verification failed"), strings.Contains(text, "no matching host key"):
		return errors.New("tunnel host verification failed")
	default:
		return errors.New("tunnel SSH connection failed")
	}
}

func (runtime *Runtime) isDesired() bool {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	return runtime.desired
}

func (runtime *Runtime) notify(state domain.State, address, message string) {
	runtime.mu.Lock()
	handler := runtime.handler
	runtime.mu.Unlock()
	if handler != nil {
		handler(state, address, message)
	}
}

type limitedBuffer struct {
	mu    sync.Mutex
	value []byte
	limit int
}

func (buffer *limitedBuffer) Write(value []byte) (int, error) {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	remaining := max(buffer.limit-len(buffer.value), 0)
	buffer.value = append(buffer.value, value[:min(len(value), remaining)]...)
	return len(value), nil
}

func (buffer *limitedBuffer) String() string {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return string(buffer.value)
}
