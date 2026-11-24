package relayhttp

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	relayapp "github.com/luxuryprivate/switchboard/backend/internal/slices/relay/application"
)

// A chat client decides "did this stream finish?" from the finish_reason of
// its last choice. When the provider breaks a stream AFTER content reached
// the client, the relay ends the stream with its own failure event — and
// that event has to carry a terminal choice, or the client reads the tail as
// a stream that died mid-answer and re-requests it on its own retry loop
// (measured in the field: "Stream ended without finish_reason", retry delay,
// again). The bytes are read over a real listener because the live path
// writes through a Flusher a plain recorder does not have.
func TestABrokenLiveChatStreamEndsInATerminalChoice(t *testing.T) {
	release := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"Hel\"},\"finish_reason\":null}]}\n\n"))
		w.(http.Flusher).Flush()
		<-release
		// The provider dies mid-stream: no finish_reason, no [DONE].
	}))
	defer upstream.Close()
	base, _ := url.Parse(upstream.URL)
	server := NewServer("127.0.0.1:0", Dependencies{
		Routes:      fixedRoute{route: relayapp.Route{ProviderID: "echo", BaseURL: base, AuthMode: "passthrough"}},
		Credentials: &credentialSource{values: []string{""}},
		Config:      Config{LiveStreamProbation: time.Millisecond, RetryBase: time.Millisecond, RetryMax: time.Millisecond, PermanentAttempts: 1},
	})
	relay := httptest.NewServer(server)
	defer relay.Close()
	request, _ := http.NewRequest("POST", relay.URL+"/v1/chat/completions", strings.NewReader(`{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	request.Header.Set("Content-Type", "application/json")
	go func() { time.Sleep(30 * time.Millisecond); close(release) }()
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("relay request: %v", err)
	}
	wire, _ := io.ReadAll(response.Body)
	response.Body.Close()

	// Every data line that carries a choice is decoded, and the last one
	// must be the terminal the client is waiting for.
	var reasons []any
	var sawError, sawDone bool
	for _, block := range strings.Split(string(wire), "\n\n") {
		data := ""
		for _, line := range strings.Split(block, "\n") {
			if after, found := strings.CutPrefix(line, "data: "); found {
				data += after
			}
		}
		switch {
		case data == "[DONE]":
			sawDone = true
		case data == "":
		default:
			var chunk struct {
				Choices []struct {
					FinishReason any `json:"finish_reason"`
				} `json:"choices"`
				Error map[string]any `json:"error"`
			}
			if json.Unmarshal([]byte(data), &chunk) != nil {
				t.Fatalf("a data line is not JSON: %q", data)
			}
			if chunk.Error != nil {
				sawError = true
			}
			for _, choice := range chunk.Choices {
				reasons = append(reasons, choice.FinishReason)
			}
		}
	}
	if !sawError || !sawDone {
		t.Fatalf("the failure lost its error object or its terminator:\n%s", wire)
	}
	if len(reasons) == 0 || reasons[len(reasons)-1] != "error" {
		t.Fatalf("the stream ended without a terminal finish_reason (got %v):\n%s", reasons, wire)
	}
	// The content the client already read is still first: the terminal is
	// appended, never substituted for the answer.
	if !strings.HasPrefix(string(wire), "data: {\"id\":\"c1\"") {
		t.Fatalf("the delivered content is not at the head of the stream:\n%s", wire)
	}
}

// The Responses dialect already ends in response.failed and the Anthropic
// dialect in an error event; neither changes, and a stream that completed
// normally never gains a terminal choice.
func TestStreamFailureKeepsTheOtherDialectsAsTheyWere(t *testing.T) {
	responses := httptest.NewRecorder()
	writeStreamFailure(responses, "/v1/responses", "m")
	if strings.Contains(responses.Body.String(), "finish_reason") || !strings.Contains(responses.Body.String(), "response.failed") {
		t.Fatalf("the Responses failure changed:\n%s", responses.Body.String())
	}
	messages := httptest.NewRecorder()
	writeStreamFailure(messages, "/v1/messages", "m")
	if strings.Contains(messages.Body.String(), "finish_reason") || !strings.Contains(messages.Body.String(), "event: error") {
		t.Fatalf("the Anthropic failure changed:\n%s", messages.Body.String())
	}
	chat := httptest.NewRecorder()
	writeStreamFailure(chat, "/v1/chat/completions", "m")
	body := chat.Body.String()
	if strings.Count(body, "data: ") != 3 || strings.Index(body, "finish_reason") > strings.Index(body, "\"error\"") {
		t.Fatalf("the chat failure must be terminal choice, error, [DONE]:\n%s", body)
	}
}
