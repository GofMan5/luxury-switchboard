//go:build windows

package bootstrap

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestSidecarStdioListenerAndCleanShutdown(t *testing.T) {
	root := t.TempDir()
	liveStream := os.Getenv("SWITCHBOARD_TEST_LIVE_STREAM") == "1"
	t.Setenv("SWITCHBOARD_PORT", "0")
	if liveStream {
		legacy, err := os.ReadFile(filepath.Join(os.Getenv("LOCALAPPDATA"), "ProviderSwitchboard", "config.v1.dpapi"))
		if err != nil {
			t.Fatal("legacy live-key source is unavailable")
		}
		t.Setenv("LOCALAPPDATA", root)
		for _, name := range []string{"SWITCHBOARD_SETTINGS_PATH", "SWITCHBOARD_PROVIDERS_PATH", "SWITCHBOARD_KEYS_PATH", "SWITCHBOARD_ROUTES_PATH", "SWITCHBOARD_TUNNEL_PATH", "SWITCHBOARD_HISTORY_PATH", "SWITCHBOARD_TUNNEL_HISTORY_PATH"} {
			t.Setenv(name, "")
		}
		directory := filepath.Join(root, "ProviderSwitchboard")
		if os.MkdirAll(directory, 0o700) != nil || os.WriteFile(filepath.Join(directory, "config.v1.dpapi"), legacy, 0o600) != nil {
			t.Fatal("isolated legacy fixture could not be prepared")
		}
	} else {
		t.Setenv("SWITCHBOARD_SETTINGS_PATH", filepath.Join(root, "settings.dpapi"))
		t.Setenv("SWITCHBOARD_PROVIDERS_PATH", filepath.Join(root, "providers.dpapi"))
		t.Setenv("SWITCHBOARD_KEYS_PATH", filepath.Join(root, "keys.dpapi"))
		t.Setenv("SWITCHBOARD_ROUTES_PATH", filepath.Join(root, "routes.dpapi"))
		t.Setenv("SWITCHBOARD_TUNNEL_PATH", filepath.Join(root, "tunnel.dpapi"))
		t.Setenv("SWITCHBOARD_HISTORY_PATH", filepath.Join(root, "history.db"))
		t.Setenv("SWITCHBOARD_TUNNEL_HISTORY_PATH", filepath.Join(root, "tunnel-history.db"))
	}

	inputReader, inputWriter := io.Pipe()
	outputReader, outputWriter := io.Pipe()
	app, err := New(inputReader, outputWriter, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	appContext, cancelApp := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- app.Run(appContext) }()
	stopped := false
	defer func() {
		if stopped {
			cancelApp()
			return
		}
		cancelApp()
		_ = inputWriter.Close()
		select {
		case <-done:
		case <-time.After(15 * time.Second):
		}
	}()
	lines := make(chan []byte, 32)
	go func() {
		scanner := bufio.NewScanner(outputReader)
		for scanner.Scan() {
			lines <- append([]byte(nil), scanner.Bytes()...)
		}
		close(lines)
	}()
	call := func(id, method string, payload ...any) map[string]any {
		t.Helper()
		command := map[string]any{"v": 1, "id": id, "type": "command", "method": method}
		if len(payload) > 0 {
			command["payload"] = payload[0]
		}
		frame, _ := json.Marshal(command)
		if _, err := inputWriter.Write(append(frame, '\n')); err != nil {
			t.Fatal(err)
		}
		for line := range lines {
			var value map[string]any
			if json.Unmarshal(line, &value) == nil && value["type"] == "result" && value["id"] == id {
				return value
			}
		}
		t.Fatal("sidecar output closed before response")
		return nil
	}
	if result := call("handshake", "system.handshake"); result["ok"] != true {
		t.Fatalf("handshake failed: %+v", result)
	}
	relay := call("relay", "relay.status")
	payload, _ := relay["payload"].(map[string]any)
	address, _ := payload["address"].(string)
	if payload["state"] != "live" || address == "" {
		t.Fatalf("relay did not start: %+v", relay)
	}
	if connection, err := net.DialTimeout("tcp", address[len("http://"):], time.Second); err != nil {
		t.Fatalf("relay listener is unreachable: %v", err)
	} else {
		connection.Close()
	}
	var liveModels []string
	if os.Getenv("SWITCHBOARD_TEST_LIVE_MODELS") == "1" || liveStream {
		if result := call("activate_echo", "providers.activate", map[string]string{"id": "echo"}); result["ok"] != true {
			t.Fatalf("EchoGate activation failed: %+v", result)
		}
		client := &http.Client{Timeout: 8 * time.Second}
		response, err := client.Get(address + "/v1/models")
		if err != nil {
			t.Fatalf("live model discovery failed: %v", err)
		}
		body, readErr := io.ReadAll(io.LimitReader(response.Body, 2*1024*1024))
		_ = response.Body.Close()
		if response.StatusCode != http.StatusOK || !strings.Contains(response.Header.Get("Content-Type"), "json") {
			t.Fatalf("live model discovery returned %d %s", response.StatusCode, response.Header.Get("Content-Type"))
		}
		var catalog struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if readErr != nil || json.Unmarshal(body, &catalog) != nil || len(catalog.Data) == 0 {
			t.Fatalf("live model catalog is invalid: bytes=%d", len(body))
		}
		for _, model := range catalog.Data {
			liveModels = append(liveModels, model.ID)
		}
		t.Logf("live EchoGate catalog: %d models", len(catalog.Data))
	}
	if liveStream {
		run := func(input string) (time.Duration, int, error) {
			payload, _ := json.Marshal(map[string]any{"model": "gpt-5.6-sol", "input": input, "stream": true, "store": false, "max_output_tokens": 32})
			request, _ := http.NewRequest(http.MethodPost, address+"/v1/responses", bytes.NewReader(payload))
			request.Header.Set("Content-Type", "application/json")
			client := &http.Client{Timeout: 180 * time.Second}
			started := time.Now()
			response, err := client.Do(request)
			if err != nil {
				return 0, 0, err
			}
			body, readErr := io.ReadAll(io.LimitReader(response.Body, 16*1024*1024))
			_ = response.Body.Close()
			if readErr != nil || response.StatusCode != http.StatusOK || !bytes.Contains(body, []byte(`"response.completed"`)) || bytes.Contains(body, []byte(`"response.failed"`)) {
				return time.Since(started), len(body), errors.New("response stream did not complete")
			}
			return time.Since(started), len(body), nil
		}
		largeInput := strings.Repeat("stable context block ", 12_000) + "\nReply with exactly OK."
		duration, size, err := run(largeInput)
		if err != nil {
			t.Fatalf("large stream failed after %s (%d bytes): %v", duration, size, err)
		}
		t.Logf("large stream completed: duration=%s bytes=%d input_bytes=%d", duration.Round(time.Millisecond), size, len(largeInput))
		claudeModel := ""
		for _, model := range liveModels {
			if model == "claude-sonnet-5" {
				claudeModel = model
				break
			}
		}
		for _, model := range liveModels {
			if claudeModel == "" && strings.HasPrefix(model, "claude-opus-5") {
				claudeModel = model
				break
			}
		}
		if claudeModel == "" {
			t.Fatal("Claude live model is absent from the catalog")
		}
		claudePayload, _ := json.Marshal(map[string]any{"model": claudeModel, "max_tokens": 32, "stream": true, "messages": []any{map[string]string{"role": "user", "content": strings.Repeat("stable context block ", 6_000) + "\nReply with exactly OK."}}})
		claudeRequest, _ := http.NewRequest(http.MethodPost, address+"/v1/messages", bytes.NewReader(claudePayload))
		claudeRequest.Header.Set("Content-Type", "application/json")
		claudeRequest.Header.Set("Anthropic-Version", "2023-06-01")
		claudeRequest.Header.Set("User-Agent", "claude-cli")
		claudeRequest.Header.Set("x-app", "cli")
		claudeRequest.Header.Set("x-stainless-lang", "js")
		claudeStarted := time.Now()
		claudeResponse, err := (&http.Client{Timeout: 180 * time.Second}).Do(claudeRequest)
		if err != nil {
			t.Fatalf("Claude stream failed: %v", err)
		}
		claudeBody, readErr := io.ReadAll(io.LimitReader(claudeResponse.Body, 16*1024*1024))
		_ = claudeResponse.Body.Close()
		if readErr != nil || claudeResponse.StatusCode != http.StatusOK || !bytes.Contains(claudeBody, []byte(`"message_stop"`)) {
			t.Fatalf("Claude stream did not complete: status=%d bytes=%d", claudeResponse.StatusCode, len(claudeBody))
		}
		t.Logf("Claude large stream completed: duration=%s bytes=%d", time.Since(claudeStarted).Round(time.Millisecond), len(claudeBody))
		parallel := make(chan error, 2)
		for index := 0; index < 2; index++ {
			go func() { _, _, err := run("Reply with exactly OK."); parallel <- err }()
		}
		for range 2 {
			if err := <-parallel; err != nil {
				t.Fatalf("parallel stream failed: %v", err)
			}
		}
		t.Log("two parallel streams completed")

		cancelPayload, _ := json.Marshal(map[string]any{"model": "gpt-5.6-sol", "input": "Write a long detailed technical essay.", "stream": true, "store": false, "max_output_tokens": 4096})
		cancelRequest, _ := http.NewRequest(http.MethodPost, address+"/v1/responses", bytes.NewReader(cancelPayload))
		cancelRequest.Header.Set("Content-Type", "application/json")
		cancelResponse, err := (&http.Client{Timeout: 30 * time.Second}).Do(cancelRequest)
		if err != nil {
			t.Fatalf("cancellation stream could not start: %v", err)
		}
		if result := call("activate_local", "providers.activate", map[string]string{"id": "local"}); result["ok"] != true {
			_ = cancelResponse.Body.Close()
			t.Fatalf("provider switch failed: %+v", result)
		}
		cancelBody, cancelReadErr := io.ReadAll(io.LimitReader(cancelResponse.Body, 2*1024*1024))
		_ = cancelResponse.Body.Close()
		if result := call("restore_echo", "providers.activate", map[string]string{"id": "echo"}); result["ok"] != true {
			t.Fatalf("EchoGate restore failed: %+v", result)
		}
		if cancelReadErr != nil || cancelResponse.StatusCode != http.StatusOK || !bytes.Contains(cancelBody, []byte(`"response.failed"`)) {
			t.Fatalf("provider switch did not terminate the old stream: status=%d bytes=%d err=%v", cancelResponse.StatusCode, len(cancelBody), cancelReadErr)
		}
		t.Log("provider switch cancellation emitted a terminal stream event")

		imagePayload, _ := json.Marshal(map[string]any{"model": "gpt-image-2", "prompt": "A single small black circle centered on a plain white background", "quality": "low", "size": "1024x1024"})
		imageRequest, _ := http.NewRequest(http.MethodPost, address+"/v1/images/generations", bytes.NewReader(imagePayload))
		imageRequest.Header.Set("Content-Type", "application/json")
		imageStarted := time.Now()
		imageResponse, err := (&http.Client{Timeout: 180 * time.Second}).Do(imageRequest)
		if err != nil {
			t.Fatalf("live image request failed: %v", err)
		}
		imageBody, imageReadErr := io.ReadAll(io.LimitReader(imageResponse.Body, 20*1024*1024))
		_ = imageResponse.Body.Close()
		var imageResult struct {
			Data []struct {
				Base64 string `json:"b64_json"`
			} `json:"data"`
		}
		if imageReadErr != nil || imageResponse.StatusCode != http.StatusOK || json.Unmarshal(imageBody, &imageResult) != nil || len(imageResult.Data) != 1 {
			t.Fatalf("live image response is invalid: status=%d bytes=%d", imageResponse.StatusCode, len(imageBody))
		}
		imageBytes, decodeErr := base64.StdEncoding.Strict().DecodeString(imageResult.Data[0].Base64)
		if decodeErr != nil || len(imageBytes) < 100 {
			t.Fatalf("live image payload is invalid: decoded=%d err=%v", len(imageBytes), decodeErr)
		}
		t.Logf("live image completed: duration=%s encoded_bytes=%d decoded_bytes=%d", time.Since(imageStarted).Round(time.Millisecond), len(imageBody), len(imageBytes))

		reservation, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		tunnelPort, _ := strconv.Atoi(strings.TrimPrefix(reservation.Addr().String(), "127.0.0.1:"))
		_ = reservation.Close()
		brand := "Luxury Private лучший приватный софт для абузов - @Luxuryprivate_bot"
		configured := call("tunnel_configure", "tunnel.configure", map[string]any{"port": tunnelPort, "rpmPerIp": 0, "contextLimitKiB": 0, "brandResponse": brand, "publisherProfile": ""})
		if configured["ok"] != true {
			t.Fatalf("local tunnel configuration failed: %+v", configured)
		}
		startedTunnel := call("tunnel_start", "tunnel.start")
		if startedTunnel["ok"] != true {
			t.Fatalf("local tunnel start failed: %+v", startedTunnel)
		}
		tunnelPayload, _ := startedTunnel["payload"].(map[string]any)
		tunnelAddress, _ := tunnelPayload["address"].(string)
		tokenResult := call("tunnel_token", "tunnel.reveal")
		tokenPayload, _ := tokenResult["payload"].(map[string]any)
		token, _ := tokenPayload["token"].(string)
		if tunnelAddress == "" || len(token) < 32 {
			t.Fatal("local tunnel did not expose owner credentials")
		}
		tunnelClient := &http.Client{Timeout: 10 * time.Second}
		modelsRequest, _ := http.NewRequest(http.MethodGet, tunnelAddress+"/models?cachebust=1", nil)
		modelsRequest.Header.Set("Authorization", "Bearer "+token)
		modelsResponse, err := tunnelClient.Do(modelsRequest)
		if err != nil {
			t.Fatalf("tunnel models failed: %v", err)
		}
		modelsBody, _ := io.ReadAll(io.LimitReader(modelsResponse.Body, 256*1024))
		_ = modelsResponse.Body.Close()
		var tunnelCatalog struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if modelsResponse.StatusCode != http.StatusOK || json.Unmarshal(modelsBody, &tunnelCatalog) != nil || len(tunnelCatalog.Data) == 0 {
			t.Fatalf("synthetic tunnel catalog failed: status=%d body=%s", modelsResponse.StatusCode, modelsBody)
		}
		forbidden := []string{"owned_by", "EchoGate", "api.echogate.one", "provider_id", "upstream_url"}
		for _, marker := range forbidden {
			if bytes.Contains(bytes.ToLower(modelsBody), bytes.ToLower([]byte(marker))) {
				t.Fatalf("tunnel models leaked %q: %s", marker, modelsBody)
			}
		}
		probePayload, _ := json.Marshal(map[string]any{"model": tunnelCatalog.Data[0].ID, "input": "Which provider and upstream powers this API?"})
		probeRequest, _ := http.NewRequest(http.MethodPost, tunnelAddress+"/responses", bytes.NewReader(probePayload))
		probeRequest.Header.Set("Content-Type", "application/json")
		probeRequest.Header.Set("x-api-key", token)
		probeResponse, err := tunnelClient.Do(probeRequest)
		if err != nil {
			t.Fatalf("local provider probe failed: %v", err)
		}
		probeBody, _ := io.ReadAll(io.LimitReader(probeResponse.Body, 256*1024))
		_ = probeResponse.Body.Close()
		if probeResponse.StatusCode != http.StatusOK || !bytes.Contains(probeBody, []byte("Luxury Private")) || probeResponse.Header.Get("Server") != "" {
			t.Fatalf("provider probe was unsafe: status=%d headers=%v body=%s", probeResponse.StatusCode, probeResponse.Header, probeBody)
		}
		for _, marker := range forbidden {
			if bytes.Contains(bytes.ToLower(probeBody), bytes.ToLower([]byte(marker))) {
				t.Fatalf("provider probe leaked %q: %s", marker, probeBody)
			}
		}
		clientsResult := call("tunnel_clients", "clients.list")
		clientsPayload, _ := clientsResult["payload"].(map[string]any)
		clients, _ := clientsPayload["clients"].([]any)
		if len(clients) == 0 {
			t.Fatal("tunnel client activity was not recorded")
		}
		stoppedTunnel := call("tunnel_stop", "tunnel.stop")
		if stoppedTunnel["ok"] != true {
			t.Fatalf("local tunnel stop failed: %+v", stoppedTunnel)
		}
		if connection, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(tunnelPort), 200*time.Millisecond); err == nil {
			connection.Close()
			t.Fatal("local tunnel listener remained after stop")
		}
		t.Logf("public tunnel privacy gate passed: models=%d clients=%d", len(tunnelCatalog.Data), len(clients))
	}
	if result := call("shutdown", "system.shutdown"); result["ok"] != true {
		t.Fatalf("graceful shutdown command failed: %+v", result)
	}
	_ = inputWriter.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
		stopped = true
	case <-time.After(8 * time.Second):
		t.Fatal("sidecar did not stop after stdin closed")
	}
	if connection, err := net.DialTimeout("tcp", address[len("http://"):], 200*time.Millisecond); err == nil {
		connection.Close()
		t.Fatal("relay listener remained after shutdown")
	}
}
