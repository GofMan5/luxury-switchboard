package relayhttp

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	relayapp "github.com/luxuryprivate/switchboard/backend/internal/slices/relay/application"
)

func TestF09AllResponsesOutputPartsReachTheChatClient(t *testing.T) {
	const first = `{"id":"msg_1","type":"message","content":[{"type":"output_text","text":"first"},{"type":"output_text","text":"second"},{"type":"refusal","refusal":"not "}]}`
	const second = `{"id":"msg_2","type":"message","content":[{"type":"output_text","text":"third"},{"type":"refusal","refusal":"allowed"}]}`
	const snapshot = `{"id":"resp_1","status":"completed","output":[` + first + `,` + second + `]}`
	const preview = "data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"content_index\":0,\"delta\":\"fir\"}\n\n" +
		"data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"content_index\":0,\"delta\":\"st\"}\n\n" +
		"data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":" + first + "}\n\n" +
		"data: {\"type\":\"response.output_item.done\",\"output_index\":1,\"item\":" + second + "}\n\n"
	for _, wire := range []string{snapshot, "data: {\"type\":\"response.completed\",\"response\":" + snapshot + "}\n\n", preview + "data: {\"type\":\"response.completed\",\"response\":" + snapshot + "}\n\n", preview + "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[]}}\n\n"} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("wire=%d/stream=%v", len(wire), stream), func(t *testing.T) {
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					if strings.HasPrefix(wire, "data:") {
						w.Header().Set("Content-Type", "text/event-stream")
					}
					fmt.Fprint(w, wire)
				}))
				defer upstream.Close()
				target, _ := url.Parse(upstream.URL)
				server := NewServer("127.0.0.1:0", Dependencies{Routes: fixedRoute{route: relayapp.Route{ProviderID: "echo", BaseURL: target, AuthMode: "bearer", Format: "responses"}}, Credentials: &credentialSource{values: []string{"fixture-key"}}})
				request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(fmt.Sprintf(`{"model":"public","stream":%v,"messages":[]}`, stream)))
				request.Header.Set("Content-Type", "application/json")
				response := httptest.NewRecorder()
				server.ServeHTTP(response, request)
				var content, refusal string
				consume := func(payload map[string]any) {
					choices, _ := payload["choices"].([]any)
					if len(choices) == 0 {
						return
					}
					choice, _ := choices[0].(map[string]any)
					message, _ := choice["message"].(map[string]any)
					if stream {
						message, _ = choice["delta"].(map[string]any)
					}
					if value, _ := message["content"].(string); value != "" {
						content += value
					}
					if value, _ := message["refusal"].(string); value != "" {
						refusal += value
					}
				}
				if stream {
					for _, block := range strings.Split(response.Body.String(), "\n\n") {
						var payload map[string]any
						if json.Unmarshal(sseData([]byte(block)), &payload) == nil {
							consume(payload)
						}
					}
				} else {
					var payload map[string]any
					if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
						t.Fatal(err)
					}
					consume(payload)
				}
				if content != "firstsecondthird" || refusal != "not allowed" {
					t.Fatalf("parts truncated or preview duplicated: text=%q refusal=%q body=%s", content, refusal, response.Body.String())
				}
			})
		}
	}
}
