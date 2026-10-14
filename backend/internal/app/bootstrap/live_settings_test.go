//go:build windows

package bootstrap

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	platform "github.com/luxuryprivate/switchboard/backend/internal/platform/stdio"
	providerapp "github.com/luxuryprivate/switchboard/backend/internal/slices/providers/application"
	providerdomain "github.com/luxuryprivate/switchboard/backend/internal/slices/providers/domain"
	settingsstore "github.com/luxuryprivate/switchboard/backend/internal/slices/settings/adapters/dpapi"
	settingsdomain "github.com/luxuryprivate/switchboard/backend/internal/slices/settings/domain"
)

func liveSettingsApp(t *testing.T, monitor ...*providerapp.HealthMonitor) (func(string, any) platform.Response, string, settingsdomain.Settings) {
	t.Helper()
	requireSecureStorage(t)
	root := t.TempDir()
	t.Setenv("LOCALAPPDATA", root)
	t.Setenv("SWITCHBOARD_PORT", "")
	for name, file := range map[string]string{
		"SWITCHBOARD_SETTINGS_PATH": "settings.dpapi", "SWITCHBOARD_PROVIDERS_PATH": "providers.dpapi", "SWITCHBOARD_KEYS_PATH": "keys.dpapi", "SWITCHBOARD_CODEX_PATH": "codex.dpapi", "SWITCHBOARD_ROUTES_PATH": "routes.dpapi", "SWITCHBOARD_TUNNEL_PATH": "tunnel.dpapi", "SWITCHBOARD_HISTORY_PATH": "history.db", "SWITCHBOARD_TUNNEL_HISTORY_PATH": "clients.db",
	} {
		t.Setenv(name, filepath.Join(root, file))
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	settings := settingsdomain.Defaults()
	settings.ListenerPort = listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	settings.ProviderHealthEnabled = false
	settings.UpdateCheckInterval = "off"
	if err := settingsstore.New(os.Getenv("SWITCHBOARD_SETTINGS_PATH")).Save(context.Background(), settings); err != nil {
		t.Fatal(err)
	}
	input, requests := io.Pipe()
	responses, output := io.Pipe()
	app, err := New(input, output, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if len(monitor) > 0 {
		app.healthMonitor = monitor[0]
	}
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan error, 1)
	go func() { finished <- app.Run(ctx) }()
	replies := make(chan platform.Response, 64)
	go func() {
		defer close(replies)
		scanner := bufio.NewScanner(responses)
		scanner.Buffer(make([]byte, 65536), platform.MaxFrameBytes+1)
		for scanner.Scan() {
			var reply platform.Response
			if json.Unmarshal(scanner.Bytes(), &reply) == nil && reply.Type == "result" {
				replies <- reply
			}
		}
	}()
	t.Cleanup(func() {
		cancel()
		_ = requests.Close()
		select {
		case <-finished:
		case <-time.After(10 * time.Second):
			t.Error("app shutdown timed out")
		}
		_ = output.Close()
		_ = responses.Close()
	})
	sequence := 0
	call := func(method string, payload any) platform.Response {
		t.Helper()
		sequence++
		id := fmt.Sprintf("settings-%d", sequence)
		frame, err := json.Marshal(map[string]any{"v": 1, "id": id, "type": "command", "method": method, "payload": payload})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := requests.Write(append(frame, '\n')); err != nil {
			t.Fatal(err)
		}
		deadline := time.NewTimer(10 * time.Second)
		defer deadline.Stop()
		for {
			select {
			case reply, ok := <-replies:
				if !ok {
					t.Fatal("stdio closed")
				}
				if reply.ID == id {
					return reply
				}
			case <-deadline.C:
				t.Fatalf("%s timed out", method)
			}
		}
	}
	initial := call("relay.status", nil)
	raw, _ := json.Marshal(initial.Payload)
	var state struct {
		Address string `json:"address"`
		State   string `json:"state"`
	}
	if json.Unmarshal(raw, &state) != nil || !initial.OK || state.State != "live" {
		t.Fatalf("relay did not start: %s", raw)
	}
	return call, strings.TrimPrefix(state.Address, "http://"), settings
}

type bootHealthProber struct{ calls chan struct{} }

func (probe bootHealthProber) ProbeReachability(context.Context, providerdomain.Provider) (bool, string) {
	probe.calls <- struct{}{}
	return true, "reachable"
}
func TestHealthRunStartsEvenWhenDisabledAtBoot(t *testing.T) {
	provider, err := providerdomain.New(providerdomain.Params{ID: "fixture", Name: "Fixture", BaseURL: "https://fixture.invalid/v1", AuthMode: providerdomain.AuthBearer, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	catalog, _ := providerapp.NewCatalog([]providerdomain.Provider{provider}, provider.ID)
	calls := make(chan struct{}, 8)
	monitor, _ := providerapp.NewHealthMonitor(catalog, bootHealthProber{calls})
	monitor.SetEnabled(false)
	call, _, _ := liveSettingsApp(t, monitor)
	select {
	case <-calls:
		t.Fatal("disabled monitor probed at boot")
	default:
	}
	monitor.SetEnabled(true)
	select {
	case <-calls:
	case <-time.After(time.Second):
		t.Fatal("application did not start the disabled monitor loop")
	}
	if reply := call("relay.status", nil); !reply.OK {
		t.Fatal("health monitor blocked control plane")
	}
}

func TestOccupiedPortRejectsSettingsAndRestoresPreviousListener(t *testing.T) {
	call, previous, settings := liveSettingsApp(t)
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	oldPort := settings.ListenerPort
	settings.ListenerPort = occupied.Addr().(*net.TCPAddr).Port
	if reply := call("settings.update", settings); reply.OK {
		t.Fatal("occupied-port update reported success")
	}
	status := call("relay.status", nil)
	raw, _ := json.Marshal(status.Payload)
	var state struct {
		State string `json:"state"`
	}
	_ = json.Unmarshal(raw, &state)
	if state.State != "live" {
		t.Fatalf("previous relay not live: %s", raw)
	}
	connection, err := net.DialTimeout("tcp", previous, time.Second)
	if err != nil {
		t.Fatalf("previous listener not restored: %v", err)
	}
	_ = connection.Close()
	get := call("settings.get", nil)
	raw, _ = json.Marshal(get.Payload)
	var saved settingsdomain.Settings
	_ = json.Unmarshal(raw, &saved)
	if saved.ListenerPort != oldPort {
		t.Fatal("rejected port persisted in memory")
	}
	durable, found, err := settingsstore.New(os.Getenv("SWITCHBOARD_SETTINGS_PATH")).Load(context.Background())
	if err != nil || !found || durable.ListenerPort != oldPort {
		t.Fatalf("rejected port persisted on disk: %d %v", durable.ListenerPort, err)
	}
}
