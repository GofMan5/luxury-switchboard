package bootstrap

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The whole app, driven the way the user drives it: a provider is added, a key
// is added, the relay starts, a real HTTP request goes through it, and the
// activity the screens are built from is read back over the real protocol.
// Written while hunting a report that token counts (input, output, cached)
// stopped appearing on every screen.
func TestLiveRelayTokenCountingEndToEnd(t *testing.T) {
	const chatStream = "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"created\":1700000000,\"model\":\"glm\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"Hello\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":100,\"completion_tokens\":40,\"total_tokens\":140,\"prompt_tokens_details\":{\"cached_tokens\":30},\"completion_tokens_details\":{\"reasoning_tokens\":10}}}\n\n" +
		"data: [DONE]\n\n"
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		_, _ = writer.Write([]byte(chatStream))
	}))
	defer upstream.Close()

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

	type answer struct {
		ID      string          `json:"id"`
		Type    string          `json:"type"`
		OK      bool            `json:"ok"`
		Payload json.RawMessage `json:"payload"`
		Error   struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	answers := make(chan answer, 256)
	go func() {
		defer close(answers)
		reader := bufio.NewReader(fromApp)
		for {
			line, err := reader.ReadBytes('\n')
			if len(line) > 0 {
				var one answer
				if json.Unmarshal(line, &one) == nil && one.Type == "result" {
					answers <- one
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

	sequence := 0
	command := func(method string, payload any) answer {
		t.Helper()
		sequence++
		id := fmt.Sprintf("cmd%d", sequence)
		frame, err := json.Marshal(map[string]any{"v": 1, "id": id, "type": "command", "method": method, "payload": payload})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := toApp.Write(append(frame, '\n')); err != nil {
			t.Fatalf("%s could not be sent: %v", method, err)
		}
		deadline := time.After(20 * time.Second)
		for {
			select {
			case one, open := <-answers:
				if !open {
					t.Fatalf("the control plane closed before answering %s", method)
				}
				if one.ID == id {
					if !one.OK {
						t.Fatalf("%s failed: %s %s", method, one.Error.Code, one.Error.Message)
					}
					return one
				}
			case <-deadline:
				t.Fatalf("%s was never answered", method)
			}
		}
	}

	// The alpha-relay shape: a chat-only provider the client reaches through the
	// Responses endpoint.
	added := command("providers.add", map[string]any{
		"name": "Alpha Relay", "baseUrl": upstream.URL, "authMode": "bearer",
		"dialect": "openai", "format": "chat", "rpm": 600, "enabled": true,
	})
	var provider struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(added.Payload, &provider); err != nil || provider.ID == "" {
		t.Fatalf("providers.add answered unreadably: %s", added.Payload)
	}
	command("keys.add", map[string]any{"providerId": provider.ID, "label": "primary", "secret": "sk-live"})
	command("routes.upsert", map[string]any{
		"target": "relay", "publicModel": "glm-5.3", "upstreamModel": "glm-5.3",
		"providerId": provider.ID, "enabled": true, "priority": 0,
	})
	command("relay.start", nil)
	status := command("relay.status", nil)
	var snapshot struct {
		State   string `json:"state"`
		Address string `json:"address"`
	}
	if err := json.Unmarshal(status.Payload, &snapshot); err != nil || snapshot.State != "live" || snapshot.Address == "" {
		t.Fatalf("the relay did not start: %s", status.Payload)
	}

	client := &http.Client{Timeout: 20 * time.Second}
	request, err := http.NewRequest(http.MethodPost, snapshot.Address+"/v1/responses", strings.NewReader(`{"model":"glm-5.3","stream":true,"input":"hi"}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("the request through the live relay failed: %v", err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("the live request did not succeed: status=%d body=%s", response.StatusCode, body)
	}

	// The screens read this; the user reads the screens. The row settles
	// asynchronously to the last byte the client read, so poll for the
	// terminal state instead of racing the deferred finish. The token fields
	// are flat on the row — the same shape the frontend renders.
	deadline := time.Now().Add(10 * time.Second)
	var row struct {
		InputTokens     int64  `json:"inputTokens"`
		OutputTokens    int64  `json:"outputTokens"`
		CachedTokens    int64  `json:"cachedTokens"`
		ReasoningTokens int64  `json:"reasoningTokens"`
		TotalTokens     int64  `json:"totalTokens"`
		State           string `json:"state"`
		ErrorCode       string `json:"errorCode"`
		Status          int    `json:"status"`
	}
	for {
		listed := command("activity.list", map[string]any{"limit": 10})
		var activity struct {
			Requests []json.RawMessage `json:"requests"`
		}
		if err := json.Unmarshal(listed.Payload, &activity); err != nil || len(activity.Requests) == 0 {
			t.Fatalf("activity.list answered unreadably: %s", listed.Payload)
		}
		if err := json.Unmarshal(activity.Requests[0], &row); err != nil {
			t.Fatalf("the activity row is unreadable: %s", activity.Requests[0])
		}
		if row.State != "active" && row.State != "retrying" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the activity row never settled: %s", activity.Requests[0])
		}
		time.Sleep(50 * time.Millisecond)
	}
	if row.InputTokens != 100 {
		t.Errorf("input tokens were not counted: %d", row.InputTokens)
	}
	if row.OutputTokens != 40 {
		t.Errorf("output tokens were not counted: %d", row.OutputTokens)
	}
	if row.CachedTokens != 30 {
		t.Errorf("cached tokens were not counted: %d", row.CachedTokens)
	}
	if row.ReasoningTokens != 10 {
		t.Errorf("reasoning tokens were not counted: %d", row.ReasoningTokens)
	}
	if row.TotalTokens != 140 {
		t.Errorf("total tokens were not counted: %d", row.TotalTokens)
	}
	if row.Status != http.StatusOK {
		t.Errorf("the recorded status was %d, not 200", row.Status)
	}
}
