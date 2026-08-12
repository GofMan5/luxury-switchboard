package bootstrap

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	systemstdio "github.com/luxuryprivate/switchboard/backend/internal/slices/system/adapters/stdio"
)

// The interface can hide a workspace, a build cannot. Whatever this binary answers
// over its real protocol is the whole truth about what the edition can do, so the
// commands are asked for instead of assumed.
func TestEditionAnswersOnlyItsOwnCommands(t *testing.T) {
	root := t.TempDir()
	t.Setenv("SWITCHBOARD_PORT", "0")
	for name, file := range map[string]string{
		"SWITCHBOARD_SETTINGS_PATH":       "settings.dpapi",
		"SWITCHBOARD_PROVIDERS_PATH":      "providers.dpapi",
		"SWITCHBOARD_KEYS_PATH":           "keys.dpapi",
		"SWITCHBOARD_ROUTES_PATH":         "routes.dpapi",
		"SWITCHBOARD_TUNNEL_PATH":         "tunnel.dpapi",
		"SWITCHBOARD_HISTORY_PATH":        "history.db",
		"SWITCHBOARD_TUNNEL_HISTORY_PATH": "tunnel-history.db",
	} {
		t.Setenv(name, filepath.Join(root, file))
	}

	requests, toApp := io.Pipe()
	fromApp, responses := io.Pipe()
	app, err := New(requests, responses, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	appContext, stopApp := context.WithCancel(context.Background())
	finished := make(chan error, 1)
	go func() { finished <- app.Run(appContext) }()

	// Events flow the moment the relay starts, so the stream is drained on its own
	// goroutine; otherwise the writer blocks and no command is ever read.
	answers := make(chan protocolAnswer, 64)
	go func() {
		defer close(answers)
		reader := bufio.NewReader(fromApp)
		for {
			line, err := reader.ReadBytes('\n')
			if len(line) > 0 {
				var answer protocolAnswer
				if json.Unmarshal(line, &answer) == nil && answer.Type == "result" {
					answers <- answer
				}
			}
			if err != nil {
				return
			}
		}
	}()
	defer func() {
		stopApp()
		_ = toApp.Close()
		select {
		case <-finished:
		case <-time.After(10 * time.Second):
			t.Error("the control plane did not stop")
		}
		_ = responses.Close()
	}()

	handshake := call(t, toApp, answers, "system.handshake")
	if handshake.Payload.Edition != systemstdio.Edition {
		t.Fatalf("handshake reports %q while the binary is %q", handshake.Payload.Edition, systemstdio.Edition)
	}

	owner := systemstdio.Edition == "owner"
	for _, method := range []string{"tunnel.get", "clients.list", "shared.list"} {
		answer := call(t, toApp, answers, method)
		if owner && !answer.OK {
			t.Fatalf("the owner edition refused %s: %s", method, answer.Error.Code)
		}
		if !owner && (answer.OK || answer.Error.Code != "method_not_found") {
			t.Fatalf("the public edition still serves %s: ok=%v code=%q", method, answer.OK, answer.Error.Code)
		}
	}
	// A command every edition owns must keep working next to the refusals.
	if relay := call(t, toApp, answers, "relay.status"); !relay.OK {
		t.Fatalf("a shared command broke: %s", relay.Error.Code)
	}
}

type protocolAnswer struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	OK      bool   `json:"ok"`
	Payload struct {
		Edition string `json:"edition"`
	} `json:"payload"`
	Error struct {
		Code string `json:"code"`
	} `json:"error"`
}

func call(t *testing.T, requests io.Writer, answers <-chan protocolAnswer, method string) protocolAnswer {
	t.Helper()
	id := strings.ReplaceAll(method, ".", "_")
	frame, err := json.Marshal(map[string]any{"v": 1, "id": id, "type": "command", "method": method})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := requests.Write(append(frame, '\n')); err != nil {
		t.Fatalf("%s could not be sent: %v", method, err)
	}
	deadline := time.After(20 * time.Second)
	for {
		select {
		case answer, open := <-answers:
			if !open {
				t.Fatalf("the control plane closed before answering %s", method)
			}
			if answer.ID == id {
				return answer
			}
		case <-deadline:
			t.Fatalf("%s was never answered", method)
		}
	}
}
