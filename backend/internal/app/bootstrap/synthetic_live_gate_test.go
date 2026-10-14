//go:build windows

package bootstrap

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	platform "github.com/luxuryprivate/switchboard/backend/internal/platform/stdio"
)

// This gate exercises the assembled app and real loopback sockets with only
// synthetic credentials and temporary stores. It never opts into vendor tests.
func TestSyntheticLiveGateLargeParallelReasoningCancellationAndImage(t *testing.T) {
	large := strings.Repeat("stable synthetic context ", 30_000)
	upstreamCancelled := make(chan struct{}, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Input string `json:"input"`
			Model string `json:"model"`
			Tools []struct {
				Type string `json:"type"`
			} `json:"tools"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			t.Error("fixture received malformed JSON")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if r.Header.Get("Authorization") != "Bearer synthetic-gate-key" {
			t.Error("credential contract lost")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Path != "/v1/responses" {
			t.Errorf("compatibility target %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flush := w.(http.Flusher)
		fmt.Fprint(w, "data: {\"type\":\"response.created\",\"response\":{\"id\":\"gate\",\"status\":\"in_progress\"}}\n\n")
		flush.Flush()
		if len(body.Tools) > 0 && body.Tools[0].Type == "image_generation" {
			image := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte("synthetic image bytes"), 16))
			fmt.Fprintf(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"gate\",\"status\":\"completed\",\"output\":[{\"type\":\"image_generation_call\",\"result\":%q}]}}\n\n", image)
			flush.Flush()
			return
		}
		if body.Input == "wait-cancel" {
			fmt.Fprint(w, "data: {\"type\":\"response.reasoning_summary_text.delta\",\"delta\":\"thinking\"}\n\n")
			flush.Flush()
			<-r.Context().Done()
			select {
			case upstreamCancelled <- struct{}{}:
			default:
			}
			return
		}
		if body.Input != large {
			t.Errorf("large context lost: %d bytes", len(body.Input))
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		for range 5 {
			select {
			case <-r.Context().Done():
				return
			case <-time.After(80 * time.Millisecond):
			}
			fmt.Fprint(w, "data: {\"type\":\"response.reasoning_summary_text.delta\",\"delta\":\"thinking \"}\n\n")
			flush.Flush()
		}
		fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"content_index\":0,\"delta\":\"OK\"}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"gate\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"OK\"}]}],\"usage\":{\"input_tokens\":180000,\"output_tokens\":6,\"total_tokens\":180006}}}\n\n")
		flush.Flush()
	}))
	defer upstream.Close()
	call, address, _ := liveSettingsApp(t)
	requireReply := func(method string, payload any) platform.Response {
		t.Helper()
		reply := call(method, payload)
		if !reply.OK {
			t.Fatalf("%s failed: %+v", method, reply.Error)
		}
		return reply
	}
	added := requireReply("providers.add", map[string]any{"name": "Synthetic Gate", "baseUrl": upstream.URL, "authMode": "bearer", "dialect": "openai", "format": "responses", "imageCompat": true, "enabled": true})
	raw, _ := json.Marshal(added.Payload)
	var provider struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(raw, &provider) != nil || provider.ID == "" {
		t.Fatal("fixture provider missing")
	}
	requireReply("keys.add", map[string]any{"providerId": provider.ID, "label": "fixture", "secret": "synthetic-gate-key"})
	for _, model := range []string{"gate", "paint"} {
		requireReply("routes.upsert", map[string]any{"target": "relay", "publicModel": model, "upstreamModel": "fixture-model", "providerId": provider.ID, "enabled": true, "priority": 0})
	}
	requireReply("providers.activate", map[string]any{"id": provider.ID})
	client := &http.Client{Timeout: 10 * time.Second}
	defer client.CloseIdleConnections()
	run := func(input string) error {
		payload, _ := json.Marshal(map[string]any{"model": "gate", "stream": true, "input": input})
		request, _ := http.NewRequest(http.MethodPost, "http://"+address+"/v1/responses", bytes.NewReader(payload))
		request.Header.Set("Content-Type", "application/json")
		response, err := client.Do(request)
		if err != nil {
			return err
		}
		defer response.Body.Close()
		body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		if err != nil {
			return err
		}
		if response.StatusCode != http.StatusOK || bytes.Count(body, []byte(`"type":"response.completed"`)) != 1 || bytes.Contains(body, []byte(`"response.failed"`)) || !bytes.Contains(body, []byte(`"delta":"OK"`)) {
			return fmt.Errorf("synthetic stream incomplete: status=%d bytes=%d", response.StatusCode, len(body))
		}
		return nil
	}
	for range 2 {
		if err := run(large); err != nil {
			t.Fatal(err)
		}
	}
	outcomes := make(chan error, 3)
	var group sync.WaitGroup
	for range 3 {
		group.Add(1)
		go func() { defer group.Done(); outcomes <- run(large) }()
	}
	group.Wait()
	close(outcomes)
	for err := range outcomes {
		if err != nil {
			t.Fatal(err)
		}
	}
	payload := strings.NewReader(`{"model":"gate","stream":true,"input":"wait-cancel"}`)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request, _ := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+address+"/v1/responses", payload)
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	requireReply("providers.activate", map[string]any{"id": "local"})
	body, readErr := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	_ = response.Body.Close()
	if readErr != nil || !bytes.Contains(body, []byte(`"response.failed"`)) {
		t.Fatalf("provider switch did not send cancellation terminal: bytes=%d err=%v", len(body), readErr)
	}
	select {
	case <-upstreamCancelled:
	case <-time.After(time.Second):
		t.Fatal("provider switch left upstream socket active")
	}
	imageRequest, _ := http.NewRequest(http.MethodPost, "http://"+address+"/v1/images/generations", strings.NewReader(`{"model":"paint","prompt":"synthetic circle"}`))
	imageRequest.Header.Set("Content-Type", "application/json")
	imageResponse, err := client.Do(imageRequest)
	if err != nil {
		t.Fatal(err)
	}
	defer imageResponse.Body.Close()
	var image struct {
		Data []struct {
			Base64 string `json:"b64_json"`
		} `json:"data"`
	}
	if json.NewDecoder(imageResponse.Body).Decode(&image) != nil || imageResponse.StatusCode != 200 || len(image.Data) != 1 {
		t.Fatal("assembled image compatibility did not complete")
	}
	if decoded, err := base64.StdEncoding.DecodeString(image.Data[0].Base64); err != nil || !bytes.Equal(decoded, bytes.Repeat([]byte("synthetic image bytes"), 16)) {
		t.Fatalf("image bytes lost: %d %v", len(decoded), err)
	}
	t.Logf("synthetic gate passed: context=%d bytes; sequential=2 parallel=3; delayed reasoning; provider-switch cancellation; image conversion", len(large))
}
