package tunnelhttp

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/publictunnel/domain"
	relayapp "github.com/luxuryprivate/switchboard/backend/internal/slices/relay/application"
)

func TestPublicStreamRefusesAMarkerReassembledByTheClient(t *testing.T) {
	dispatcher := &fakeDispatcher{response: relayapp.DispatchResponse{
		Status: http.StatusOK, Headers: http.Header{"Content-Type": {"text/event-stream"}},
		Body: []byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Secret\"}}]}\n\n" +
			"data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Provider\"}}]}\n\ndata: [DONE]\n\n"),
	}}
	response := httptest.NewRecorder()
	gatewayForTest(t, dispatcher).ServeHTTP(response, authorizedRequest(http.MethodPost, "http://tunnel/v1/chat/completions", `{"model":"public-gpt","stream":true}`))
	if response.Code != http.StatusBadGateway || response.Body.String() != "{\"error\":\"Request could not be completed\"}" {
		t.Fatalf("split marker reached the public client: status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestLogicalStreamMarkersStayWithinTheirChoiceItemToolAndField(t *testing.T) {
	tests := []struct {
		name, path string
		events     []string
	}{
		{"completion", "/v1/completions", []string{`{"choices":[{"index":1,"text":"Secret"}]}`, `{"choices":[{"index":1,"text":"Provider"}]}`, `[DONE]`}},
		{"chat tool", "/v1/chat/completions", []string{`{"choices":[{"index":1,"delta":{"tool_calls":[{"index":2,"id":"call_keep","function":{"arguments":"Secret"}}]}}]}`, `{"choices":[{"index":1,"delta":{"tool_calls":[{"index":2,"function":{"arguments":"Provider"}}]}}]}`, `[DONE]`}},
		{"legacy function", "/v1/chat/completions", []string{`{"choices":[{"delta":{"function_call":{"name":"Secret"}}}]}`, `{"choices":[{"delta":{"function_call":{"name":"Provider"}}}]}`, `[DONE]`}},
		{"responses text", "/v1/responses", []string{`{"type":"response.output_text.delta","item_id":"i1","content_index":2,"delta":"Secret"}`, `{"type":"response.output_text.delta","item_id":"i1","content_index":2,"delta":"Provider"}`, `{"type":"response.completed"}`}},
		{"responses arguments", "/v1/responses", []string{`{"type":"response.function_call_arguments.delta","output_index":2,"item_id":"i1","delta":"Secret"}`, `{"type":"response.function_call_arguments.delta","output_index":2,"delta":"Provider"}`, `{"type":"response.completed"}`}},
		{"custom input", "/v1/responses", []string{`{"type":"response.custom_tool_call_input.delta","item_id":"i1","delta":"Secret"}`, `{"type":"response.custom_tool_call_input.delta","item_id":"i1","delta":"Provider"}`, `{"type":"response.completed"}`}},
		{"anthropic text", "/v1/messages", []string{`{"type":"content_block_start","index":2,"content_block":{"type":"text","text":"Secret"}}`, `{"type":"content_block_delta","index":2,"delta":{"type":"text_delta","text":"Provider"}}`, `{"type":"message_stop"}`}},
		{"anthropic tool", "/v1/messages", []string{`{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"Secret"}}`, `{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"Provider"}}`, `{"type":"message_stop"}`}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if body, err := sanitizeTestStream(test.path, streamEvents(test.events...), []string{"secretprovider"}); err == nil || body != nil {
				t.Fatalf("client can reconstruct marker: %s err=%v", body, err)
			}
		})
	}
	safe := []struct {
		path   string
		events []string
	}{
		{"/v1/chat/completions", []string{`{"choices":[{"index":0,"delta":{"content":"Secret","refusal":"Provider","tool_calls":[{"index":0,"function":{"name":"Secret","arguments":"Provider"}},{"index":1,"function":{"name":"Provider","arguments":"Secret"}}]}}]}`, `{"choices":[{"index":1,"delta":{"content":"Provider"}}]}`, `[DONE]`}},
		{"/v1/responses", []string{`{"type":"response.output_text.delta","item_id":"i1","content_index":0,"delta":"Secret"}`, `{"type":"response.output_text.delta","item_id":"i2","content_index":0,"delta":"Provider"}`, `{"type":"response.output_text.delta","item_id":"i1","content_index":1,"delta":"Provider"}`, `{"type":"response.refusal.delta","item_id":"i1","content_index":0,"delta":"Provider"}`, `{"type":"response.completed"}`}},
		{"/v1/messages", []string{`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Secret"}}`, `{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Provider"}}`, `{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"Provider"}}`, `{"type":"message_stop"}`}},
	}
	for _, test := range safe {
		if _, err := sanitizeTestStream(test.path, streamEvents(test.events...), []string{"SecretProvider"}); err != nil {
			t.Fatalf("unrelated streams joined for %s: %v", test.path, err)
		}
	}
}

func TestLogicalStreamSweepPreservesRedactionIDsTerminalAndShortIdentifierException(t *testing.T) {
	body := streamEvents(`{"id":"provider_answer","model":"o3","choices":[{"index":0,"delta":{"content":"SecretProvider o"}},{"index":1,"delta":{"tool_calls":[{"index":0,"id":"call_keep","function":{"name":"lookup","arguments":"{"}}]}}]}`, `{"id":"provider_answer","choices":[{"index":0,"delta":{"content":"3"},"finish_reason":"stop"},{"index":1,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"}"}}]},"finish_reason":"tool_calls"}]}`)
	markers := append(identifyingMarkers("o3"), "SecretProvider")
	clean, err := sanitizeTestStream("/v1/chat/completions", body, markers)
	if err != nil || strings.Contains(string(clean), "SecretProvider") || strings.Contains(string(clean), "provider_answer") || !strings.Contains(string(clean), "call_keep") || !strings.Contains(string(clean), `"model":"public"`) || strings.Count(string(clean), "data: [DONE]") != 1 {
		t.Fatalf("stream contracts lost: %s err=%v", clean, err)
	}
	for _, credential := range []string{"o3", "a", "o3x"} {
		events := []string{`{"choices":[{"delta":{"content":"` + credential[:1] + `"}}]}`, `{"choices":[{"delta":{"content":"` + credential[1:] + `"}}]}`, `[DONE]`}
		if _, err := sanitizeTestStream("/v1/chat/completions", streamEvents(events...), []string{credential}); err == nil {
			t.Fatalf("short credential %q passed", credential)
		}
	}
	if _, err := sanitizeTestStream("/v1/chat/completions", streamEvents(`{"choices":[{"index":"0","delta":{"content":"Secret"}}]}`, `[DONE]`), []string{"SecretProvider"}); err == nil {
		t.Fatal("malformed assembly identity passed")
	}
}

func TestResponseItemInitialStringsAreSweptWithTheirDeltas(t *testing.T) {
	for _, test := range []struct{ item, event string }{
		{`{"id":"i1","type":"function_call","arguments":"Secret"}`, "response.function_call_arguments.delta"},
		{`{"id":"i1","type":"custom_tool_call","input":"Secret"}`, "response.custom_tool_call_input.delta"},
		{`{"id":"i1","type":"message","content":[{"type":"output_text","text":"Secret"}]}`, "response.output_text.delta"},
	} {
		body := streamEvents(`{"type":"response.output_item.added","output_index":0,"item":`+test.item+`}`, `{"type":"`+test.event+`","item_id":"i1","content_index":0,"delta":"Provider"}`, `{"type":"response.completed"}`)
		if clean, err := sanitizeTestStream("/v1/responses", body, []string{"SecretProvider"}); err == nil || clean != nil {
			t.Fatalf("initial item plus delta reached client: %s err=%v", clean, err)
		}
	}
}

func TestResponseReasoningInitialStringsAreSweptWithTheirDeltas(t *testing.T) {
	for _, initial := range [][]string{
		{`{"type":"response.output_item.added","output_index":0,"item":{"id":"i1","type":"reasoning","summary":[{"type":"summary_text","text":"Safe"},{"type":"summary_text","text":"Secret"}]}}`},
		{`{"type":"response.output_item.added","output_index":0,"item":{"id":"i1","type":"reasoning","summary":[]}}`, `{"type":"response.reasoning_summary_part.added","item_id":"i1","summary_index":1,"part":{"type":"summary_text","text":"Secret"}}`},
	} {
		for _, index := range []string{"0", "1"} {
			// A delta from the same summary joins its initial text; another
			// summary is independent and must not cause a false refusal.
			events := append([]string{}, initial...)
			events = append(events, `{"type":"response.reasoning_summary_text.delta","output_index":0,"summary_index":`+index+`,"delta":"Provider"}`, `{"type":"response.completed"}`)
			body := streamEvents(events...)
			clean, err := sanitizeTestStream("/v1/responses", body, []string{"SecretProvider"})
			if index == "1" {
				if err == nil || clean != nil {
					t.Fatalf("initial reasoning plus delta reached client: %s err=%v", clean, err)
				}
			} else if err != nil {
				t.Fatalf("independent summaries were joined: %v", err)
			}
		}
	}
}

func TestPublicStreamDistinguishesShortIdentifiersFromShortCredentials(t *testing.T) {
	for _, credential := range []bool{false, true} {
		dispatcher := &fakeDispatcher{response: relayapp.DispatchResponse{
			Status: http.StatusOK, Headers: http.Header{"Content-Type": {"text/event-stream"}},
			Body: []byte(streamEvents(`{"model":"o3","choices":[{"index":0,"delta":{"content":"o"}}]}`, `{"choices":[{"index":0,"delta":{"content":"3"},"finish_reason":"stop"}]}`, `[DONE]`)),
		}}
		if credential {
			dispatcher.response.SensitiveMarkers = []string{"o3"}
		}
		gateway := gatewayForTest(t, dispatcher)
		gateway.markers = fakeMarkers{[]string{"o3", "ai", "tor"}}
		gateway.routes = fakeRoutes{[]domain.Route{{PublicModel: "public-gpt", UpstreamModel: "o3", ProviderID: "ai"}}}
		response := httptest.NewRecorder()
		gateway.ServeHTTP(response, authorizedRequest(http.MethodPost, "http://tunnel/v1/chat/completions", `{"model":"public-gpt","stream":true}`))
		if credential {
			if response.Code != http.StatusBadGateway || response.Body.String() != `{"error":"Request could not be completed"}` {
				t.Fatalf("short split credential did not fail closed: %d %s", response.Code, response.Body.String())
			}
		} else if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"model":"public-gpt"`) {
			t.Fatalf("short identifier exception or structural alias lost: %d %s", response.Code, response.Body.String())
		}
	}
}

func TestLogicalSweepDoesNotRejectNumericAnnotations(t *testing.T) {
	body := streamEvents(`{"choices":[{"index":0,"delta":{"content":"Safe","annotations":[{"start_index":0,"end_index":4,"url":"https://example.invalid"}]}}]}`, `[DONE]`)
	if _, err := sanitizeTestStream("/v1/chat/completions", body, []string{"SecretProvider"}); err != nil {
		t.Fatalf("legitimate non-concatenated annotation rejected: %v", err)
	}
}

func TestMalformedLogicalStreamShapesFailClosed(t *testing.T) {
	for _, event := range []string{
		`{"choices":"unreadable"}`,
		`{"choices":[{"delta":7}]}`,
		`{"choices":[{"delta":{"tool_calls":["unreadable"]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"function":{"arguments":7}}]}}]}`,
	} {
		if body, err := sanitizeTestStream("/v1/chat/completions", streamEvents(event, `[DONE]`), []string{"SecretProvider"}); err == nil || body != nil {
			t.Fatalf("unreadable logical stream passed: %s err=%v", body, err)
		}
	}
}

func TestLogicalStreamIdentityAmbiguityFailsClosed(t *testing.T) {
	for _, events := range [][]string{
		{`{"type":"response.output_text.delta","output_index":0,"delta":"Secret"}`, `{"type":"response.output_text.delta","item_id":"i1","delta":"Provider"}`, `{"type":"response.output_text.delta","output_index":0,"item_id":"i1","delta":""}`, `{"type":"response.completed"}`},
		{`{"type":"response.output_text.delta","output_index":-1,"delta":"Secret"}`, `{"type":"response.completed"}`},
		{`{"type":"response.output_text.delta","item_id":7,"delta":"Secret"}`, `{"type":"response.completed"}`},
		{`{"type":"response.output_text.delta","delta":{"text":"Secret"}}`, `{"type":"response.completed"}`},
	} {
		if body, err := sanitizeTestStream("/v1/responses", streamEvents(events...), []string{"SecretProvider"}); err == nil || body != nil {
			t.Fatalf("malformed or ambiguous assembly passed: %s err=%v", body, err)
		}
	}
	// Content-part initialization and multiline event framing share the same
	// logical stream as the subsequent delta, even when the wire ID changes.
	body := "data: {\"type\":\"response.output_item.added\",\"output_index\":0,\n" +
		"data: \"item\":{\"id\":\"i1\",\"type\":\"message\",\"content\":[]}}\n\n" +
		streamEvents(`{"type":"response.content_part.added","item_id":"i1","content_index":0,"part":{"type":"output_text","text":"sEcReT"}}`, `{"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":"pRoViDeR"}`, `{"type":"response.completed"}`)
	if clean, err := sanitizeTestStream("/v1/responses", body, []string{"SecretProvider"}); err == nil || clean != nil {
		t.Fatalf("multiline/part initial split bypassed sweep: %s err=%v", clean, err)
	}
}

func sanitizeTestStream(path, body string, markers []string) ([]byte, error) {
	result, _, err := sanitizeResponse(relayapp.DispatchResponse{Status: http.StatusOK, Headers: http.Header{"Content-Type": {"text/event-stream"}}, Body: []byte(body)}, path, "public", markers, "")
	return result, err
}

func streamEvents(events ...string) string {
	return "data: " + strings.Join(events, "\n\ndata: ") + "\n\n"
}
