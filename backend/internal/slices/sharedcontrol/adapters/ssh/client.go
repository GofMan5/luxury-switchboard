package ssh

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"regexp"
	"sync"

	"github.com/luxuryprivate/switchboard/backend/internal/platform/openssh"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/sharedcontrol/domain"
)

const (
	controlIdentity = "model-tunnel_control_ed25519"
	selfName        = "Ваш коннект"
	maxOutput       = 64 * 1024
)

var tunnelName = regexp.MustCompile(`^Tunnel [1-9][0-9]{0,3}$`)

type Client struct{}

func NewClient() *Client { return &Client{} }

func (*Client) Request(ctx context.Context, args ...string) (domain.Snapshot, error) {
	client, err := openssh.Prepare(controlIdentity)
	if err != nil {
		return domain.Snapshot{}, errors.New("shared control unavailable")
	}
	commandArgs := append(client.BaseArgs(), "-o", "ClearAllForwardings=yes", "-o", "ConnectTimeout=5", "tunnel-control@"+openssh.Host, "v1")
	commandArgs = append(commandArgs, args...)
	command := exec.CommandContext(ctx, client.Executable, commandArgs...)
	openssh.Configure(command)
	output := &limitedBuffer{limit: maxOutput + 1}
	command.Stdout = output
	command.Stderr = io.Discard
	if err := command.Start(); err != nil {
		return domain.Snapshot{}, errors.New("shared control unavailable")
	}
	guard, err := openssh.Guard(command.Process)
	if err != nil {
		_ = command.Process.Kill()
		_ = command.Wait()
		return domain.Snapshot{}, errors.New("shared control unavailable")
	}
	err = command.Wait()
	_ = guard.Close()
	if err != nil || output.Overflowed() {
		return domain.Snapshot{}, errors.New("shared control unavailable")
	}
	return parseSnapshot(output.Bytes())
}

func parseSnapshot(raw []byte) (domain.Snapshot, error) {
	if len(raw) == 0 || len(raw) > maxOutput || raw[len(raw)-1] != '\n' || bytes.Contains(raw[:len(raw)-1], []byte{'\n'}) || bytes.Contains(raw, []byte{'\r'}) {
		return domain.Snapshot{}, errors.New("invalid shared control response")
	}
	var wire struct {
		Version  int   `json:"v"`
		OK       bool  `json:"ok"`
		Revision int64 `json:"revision"`
		Tunnels  []struct {
			Name  string `json:"name"`
			State string `json:"state"`
		} `json:"tunnels"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw[:len(raw)-1]))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&wire) != nil || decoder.Decode(&struct{}{}) != io.EOF || wire.Version != 1 || !wire.OK || wire.Revision < 0 || len(wire.Tunnels) > 256 {
		return domain.Snapshot{}, errors.New("invalid shared control response")
	}
	seen := make(map[string]struct{}, len(wire.Tunnels))
	selfCount := 0
	result := domain.Snapshot{Available: true, Revision: wire.Revision, Tunnels: make([]domain.Tunnel, 0, len(wire.Tunnels))}
	for position, tunnel := range wire.Tunnels {
		if tunnel.Name != selfName && !tunnelName.MatchString(tunnel.Name) {
			return domain.Snapshot{}, errors.New("invalid shared control response")
		}
		if _, duplicate := seen[tunnel.Name]; duplicate || (tunnel.State != "running" && tunnel.State != "paused" && tunnel.State != "stopped") {
			return domain.Snapshot{}, errors.New("invalid shared control response")
		}
		seen[tunnel.Name] = struct{}{}
		if tunnel.Name == selfName {
			selfCount++
		}
		if selfCount > 1 {
			return domain.Snapshot{}, errors.New("invalid shared control response")
		}
		result.Tunnels = append(result.Tunnels, domain.Tunnel{Position: position, Name: tunnel.Name, State: tunnel.State})
	}
	return result, nil
}

type limitedBuffer struct {
	mu       sync.Mutex
	value    []byte
	limit    int
	overflow bool
}

func (buffer *limitedBuffer) Write(value []byte) (int, error) {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	remaining := max(buffer.limit-len(buffer.value), 0)
	buffer.value = append(buffer.value, value[:min(remaining, len(value))]...)
	buffer.overflow = buffer.overflow || len(value) > remaining
	return len(value), nil
}
func (buffer *limitedBuffer) Bytes() []byte {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return bytes.Clone(buffer.value)
}
func (buffer *limitedBuffer) Overflowed() bool {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.overflow
}
