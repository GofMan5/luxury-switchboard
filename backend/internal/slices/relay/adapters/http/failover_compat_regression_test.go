package relayhttp

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestF04FailoverRebuildsCompatibilityFromClientSemantics(t *testing.T) {
	for _, formats := range [][2]string{{"chat", "chat"}, {"chat", "responses"}, {"responses", "chat"}, {"responses", "responses"}} {
		for _, clientChat := range []bool{false, true} {
			for _, stream := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s-to-%s/chat=%v/stream=%v", formats[0], formats[1], clientChat, stream), func(t *testing.T) {
					primary := refusedPrimary()
					defer primary.Close()
					var receivedPath string
					var receivedBody []byte
					sibling := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						receivedPath = r.URL.Path
						receivedBody, _ = io.ReadAll(r.Body)
						var wire struct {
							Stream bool `json:"stream"`
						}
						if err := json.Unmarshal(receivedBody, &wire); err != nil {
							t.Error(err)
						}
						if !wire.Stream {
							w.Header().Set("Content-Type", "application/json")
							if formats[1] == "chat" {
								fmt.Fprint(w, `{"choices":[{"message":{"content":"served"},"finish_reason":"stop"}]}`)
							} else {
								fmt.Fprint(w, `{"id":"resp_1","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"served"}]}]}`)
							}
							return
						}
						w.Header().Set("Content-Type", "text/event-stream")
						if formats[1] == "chat" {
							fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"served\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
						} else {
							fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"served\"}]}]}}\n\n")
						}
					}))
					defer sibling.Close()
					server, _ := failoverFixture(t, primary, sibling, formats[0], formats[1])
					path := "/v1/responses"
					body := fmt.Sprintf(`{"model":"public","stream":%v,"input":[{"type":"function_call","call_id":"call_1","name":"lookup","arguments":"{}"},{"type":"function_call_output","call_id":"call_1","output":"history"}]}`, stream)
					if clientChat {
						path = "/v1/chat/completions"
						body = fmt.Sprintf(`{"model":"public","stream":%v,"messages":[{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call_1","content":"history"}]}`, stream)
					}
					request := httptest.NewRequest(http.MethodPost, "http://relay"+path, strings.NewReader(body))
					request.Header.Set("Content-Type", "application/json")
					response := httptest.NewRecorder()
					server.ServeHTTP(response, request)
					wantPath := "/v1/" + formats[1]
					if formats[1] == "chat" {
						wantPath = "/v1/chat/completions"
					}
					if receivedPath != wantPath || !strings.Contains(string(receivedBody), "history") || !strings.Contains(string(receivedBody), "next-model") {
						t.Fatalf("sibling request lost contract: path=%s body=%s", receivedPath, receivedBody)
					}
					if !strings.Contains(response.Body.String(), "served") || (strings.Contains(response.Body.String(), `"choices"`) != clientChat) || (!stream && !json.Valid(response.Body.Bytes())) {
						t.Fatalf("client dialect lost: status=%d body=%s", response.Code, response.Body.String())
					}
					if request.URL.Path != path {
						t.Fatal("client URL was mutated")
					}
				})
			}
		}
	}
}
