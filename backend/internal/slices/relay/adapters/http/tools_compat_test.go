package relayhttp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	relayapp "github.com/luxuryprivate/switchboard/backend/internal/slices/relay/application"
)

const codexToolRequest = `{
 "model":"gpt-test",
 "stream":false,
 "input":[
  {"type":"additional_tools","role":"developer","tools":[
    {"type":"namespace","name":"functions","description":"","tools":[
      {"type":"custom","name":"exec","description":"run","format":{"type":"grammar","syntax":"lark","definition":"start: TEXT"}},
      {"type":"function","name":"wait","strict":false,"parameters":{"type":"object","properties":{}}},
      {"type":"namespace","name":"collaboration","description":"","tools":[
        {"type":"function","name":"spawn_agent","strict":false,"parameters":{"type":"object","properties":{}}}
      ]}
    ]}
  ]},
  {"type":"message","role":"user","content":[{"type":"input_text","text":"run git status"}]}
 ]
}`

func upstreamCapture(t *testing.T, reply func(http.ResponseWriter)) (*Server, *[]byte) {
	t.Helper()
	captured := new([]byte)
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, err := readRequestBody(request, absoluteMaxRequestBytes)
		if err != nil {
			t.Errorf("upstream could not read the request: %v", err)
		}
		*captured = body
		reply(writer)
	}))
	t.Cleanup(upstream.Close)
	parsed, _ := url.Parse(upstream.URL)
	return NewServer("127.0.0.1:0", Dependencies{
		Routes:      fixedRoute{route: relayapp.Route{ProviderID: "echo", BaseURL: parsed, AuthMode: "passthrough"}},
		Credentials: &credentialSource{values: []string{""}},
	}), captured
}

func toolNamesOf(t *testing.T, body []byte) []string {
	t.Helper()
	var payload struct {
		Tools []struct {
			Type string `json:"type"`
			Name string `json:"name"`
		} `json:"tools"`
		Input []map[string]any `json:"input"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("upstream request is not valid JSON: %v", err)
	}
	for _, item := range payload.Input {
		if item["type"] == "additional_tools" {
			t.Fatal("additional_tools item was forwarded to the provider")
		}
	}
	names := make([]string, 0, len(payload.Tools))
	for _, tool := range payload.Tools {
		names = append(names, tool.Type+":"+tool.Name)
	}
	return names
}

func TestNamespacedClientToolsReachTheProviderAsDocumentedTools(t *testing.T) {
	server, captured := upstreamCapture(t, func(writer http.ResponseWriter) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"id":"resp_1","object":"response","status":"completed","output":[]}`))
	})
	request := httptest.NewRequest(http.MethodPost, "http://relay/v1/responses", strings.NewReader(codexToolRequest))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("normalized tool request failed: status=%d", response.Code)
	}
	names := strings.Join(toolNamesOf(t, *captured), " ")
	for _, expected := range []string{"custom:exec", "function:wait", "function:collaboration__spawn_agent"} {
		if !strings.Contains(names, expected) {
			t.Fatalf("provider never received %q: %s", expected, names)
		}
	}
	var payload struct {
		Tools []struct {
			Name       string         `json:"name"`
			Format     map[string]any `json:"format"`
			Parameters map[string]any `json:"parameters"`
		} `json:"tools"`
	}
	if json.Unmarshal(*captured, &payload) != nil {
		t.Fatal("provider request is not valid JSON")
	}
	for _, tool := range payload.Tools {
		if tool.Name != "exec" {
			continue
		}
		// The grammar is the contract the payload has to follow. Without it the model
		// answers with an empty argument string or a JSON object, every call fails and
		// the assistant starts narrating instead of acting.
		if tool.Format == nil || tool.Parameters != nil {
			t.Fatalf("freeform tool lost the grammar it declared: %+v", tool)
		}
	}
}

func TestAFreeformRefusalDowngradesTheToolsAndTheReplayedHistory(t *testing.T) {
	refusals := 0
	server, captured := upstreamCapture(t, func(writer http.ResponseWriter) {
		refusals++
		if refusals == 1 {
			writer.Header().Set("Content-Type", "application/json")
			writer.WriteHeader(http.StatusBadRequest)
			_, _ = writer.Write([]byte(`{"error":{"message":"Unsupported tool type: custom"}}`))
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"status":"completed","output":[]}`))
	})
	history := strings.Replace(codexToolRequest, `{"type":"message","role":"user"`,
		`{"type":"custom_tool_call","name":"exec","call_id":"call_0","input":"ls"},{"type":"custom_tool_call_output","call_id":"call_0","output":"probe.txt"},{"type":"message","role":"user"`, 1)
	request := httptest.NewRequest(http.MethodPost, "http://relay/v1/responses", strings.NewReader(history))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("the retry after a freeform refusal did not succeed: status=%d", response.Code)
	}
	forwarded := string(*captured)
	if strings.Contains(forwarded, `"type":"custom"`) || strings.Contains(forwarded, "custom_tool_call") {
		t.Fatalf("the retry still carried freeform tools: %s", forwarded)
	}
	// The downgrade has to reach the history too: a documented tool answered by a
	// freeform call in the replay is the exact mismatch providers reject.
	if !strings.Contains(forwarded, `"type":"function_call_output"`) || !strings.Contains(forwarded, `{\"input\":\"ls\"}`) {
		t.Fatalf("the downgraded history lost its payload: %s", forwarded)
	}
	if !strings.Contains(forwarded, `"required":["input"]`) {
		t.Fatalf("the downgraded tool has no documented schema: %s", forwarded)
	}
}

func TestAnUnrelatedRefusalKeepsFreeformToolsIntact(t *testing.T) {
	// Downgrading on any 400 would silently strip the grammar from a provider that
	// supports freeform tools and only complained about something else.
	body, _ := normalizeResponsesTools(http.MethodPost, "/v1/responses", "application/json", []byte(codexToolRequest))
	if freeformToolRejected([]byte(`{"error":{"message":"context length exceeded"}}`)) {
		t.Fatal("an unrelated refusal was read as a freeform refusal")
	}
	if !freeformToolRejected([]byte(`{"error":{"message":"tools[0].format: lark grammar is not supported"}}`)) {
		t.Fatal("a grammar refusal was not recognised")
	}
	// A bare "custom" is not about the tool type: providers complain about
	// custom instructions, custom aliases and custom models, and each of those
	// used to spend a downgrade the provider never asked for.
	if freeformToolRejected([]byte(`{"error":{"message":"custom instructions are limited to 256 characters"}}`)) {
		t.Fatal("a complaint about custom instructions was read as a freeform refusal")
	}
	if freeformToolRejected([]byte(`{"error":{"message":"custom model alias is unknown"}}`)) {
		t.Fatal("a complaint about a custom alias was read as a freeform refusal")
	}
	// The naming form providers actually use, both orders.
	if !freeformToolRejected([]byte(`{"error":{"message":"custom tool type is not supported"}}`)) {
		t.Fatal("a freeform refusal naming the type was not recognised")
	}
	if !strings.Contains(string(body), `"type":"custom"`) {
		t.Fatalf("the freeform tool was downgraded without any refusal: %s", body)
	}
}

func TestFreeformToolCallsRoundTripInBothDirections(t *testing.T) {
	server, captured := upstreamCapture(t, func(writer http.ResponseWriter) {
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = writer.Write([]byte("event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"item\":{\"id\":\"fc_1\",\"type\":\"function_call\",\"name\":\"exec\",\"call_id\":\"call_1\",\"arguments\":\"{\\\"input\\\":\\\"git status\\\"}\"}}\n\n" +
			"event: response.function_call_arguments.done\ndata: {\"type\":\"response.function_call_arguments.done\",\"item_id\":\"fc_1\",\"arguments\":\"{\\\"input\\\":\\\"git status\\\"}\"}\n\n" +
			"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[{\"id\":\"fc_1\",\"type\":\"function_call\",\"name\":\"exec\",\"call_id\":\"call_1\",\"arguments\":\"{\\\"input\\\":\\\"git status\\\"}\"}]}}\n\n"))
	})
	history := strings.Replace(codexToolRequest, `{"type":"message","role":"user"`,
		`{"type":"custom_tool_call","name":"exec","call_id":"call_0","input":"ls"},{"type":"custom_tool_call_output","call_id":"call_0","output":"probe.txt"},{"type":"message","role":"user"`, 1)
	streaming := strings.Replace(history, `"stream":false`, `"stream":true`, 1)
	request := httptest.NewRequest(http.MethodPost, "http://relay/v1/responses", strings.NewReader(streaming))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)

	forwarded := string(*captured)
	// The provider declared the tool as freeform, so the replayed call must stay
	// freeform: converting it here is what made the follow-up turn get rejected.
	if !strings.Contains(forwarded, `"type":"custom_tool_call"`) || !strings.Contains(forwarded, `"input":"ls"`) {
		t.Fatalf("replayed freeform call lost its shape or payload: %s", forwarded)
	}
	if !strings.Contains(forwarded, `"type":"custom_tool_call_output"`) {
		t.Fatalf("replayed freeform output was documented against a freeform tool: %s", forwarded)
	}

	body := response.Body.String()
	if !strings.Contains(body, `"type":"custom_tool_call"`) || !strings.Contains(body, `"input":"git status"`) {
		t.Fatalf("client never received its freeform call back: %s", body)
	}
	if strings.Contains(body, `"arguments"`) {
		t.Fatalf("freeform call kept documented function arguments: %s", body)
	}
	if !strings.Contains(body, "event: response.custom_tool_call_input.done") {
		t.Fatalf("freeform argument event was not translated: %s", body)
	}
}

// A Responses body may carry input as a bare string. Its tools still have to be
// read, or the provider's documented function_call reaches the client as a shape
// it declared no tool for and cannot execute.
func TestAFreeformToolIsRegisteredEvenWhenInputIsAString(t *testing.T) {
	server, _ := upstreamCapture(t, func(writer http.ResponseWriter) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"status":"completed","output":[{"id":"fc_1","type":"function_call","name":"exec","call_id":"call_1","arguments":"{\"input\":\"git status\"}"}]}`))
	})
	body := `{"model":"gpt-test","stream":false,"input":"run git status",` +
		`"tools":[{"type":"custom","name":"exec","description":"run","format":{"type":"grammar","syntax":"lark","definition":"start: TEXT"}}]}`
	request := httptest.NewRequest(http.MethodPost, "http://relay/v1/responses", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)

	answer := response.Body.String()
	if !strings.Contains(answer, `"type":"custom_tool_call"`) {
		t.Fatalf("client declared a freeform tool and got a documented call back: %s", answer)
	}
	if !strings.Contains(answer, `"input":"git status"`) {
		t.Fatalf("freeform payload was not restored: %s", answer)
	}
}

func TestASealedReasoningRefusalIsRetriedWithoutTheSeal(t *testing.T) {
	// The first turn of a conversation carries no reasoning and always passes; the
	// follow-up replays a blob sealed for whichever account answered before. This is
	// the shape that made an agent run one tool and then stop.
	attempts := 0
	var second []byte
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, _ := readRequestBody(request, absoluteMaxRequestBytes)
		attempts++
		if attempts == 1 {
			writer.Header().Set("Content-Type", "application/json")
			writer.WriteHeader(http.StatusBadRequest)
			_, _ = writer.Write([]byte(`{"error":{"message":"Something went wrong"}}`))
			return
		}
		second = body
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"status":"completed","output":[]}`))
	}))
	t.Cleanup(upstream.Close)
	parsed, _ := url.Parse(upstream.URL)
	server := NewServer("127.0.0.1:0", Dependencies{
		Routes:      fixedRoute{route: relayapp.Route{ProviderID: "echo", BaseURL: parsed, AuthMode: "passthrough"}},
		Credentials: &credentialSource{values: []string{""}},
	})
	followUp := `{"model":"gpt-test","stream":false,"include":["reasoning.encrypted_content"],"input":[
	 {"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"planning"}],"encrypted_content":"gAAAAAB"},
	 {"type":"function_call","name":"exec","call_id":"c1","arguments":"{}"},
	 {"type":"function_call_output","call_id":"c1","output":"done"}
	]}`
	request := httptest.NewRequest(http.MethodPost, "http://relay/v1/responses", strings.NewReader(followUp))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("the turn was not recovered: status=%d", response.Code)
	}
	retried := string(second)
	if strings.Contains(retried, "encrypted_content") || strings.Contains(retried, "reasoning.encrypted_content") {
		t.Fatalf("the retry still carried the sealed reasoning: %s", retried)
	}
	// Only the seal goes. The summary is unsealed text about the turn — the only
	// surviving account of it, dropped with the seal before — and the tool
	// exchange is what the turn is about.
	if !strings.Contains(retried, `"type":"reasoning"`) || !strings.Contains(retried, `"text":"planning"`) {
		t.Fatalf("the retry dropped the reasoning summary with the seal: %s", retried)
	}
	if !strings.Contains(retried, `"call_id":"c1"`) || !strings.Contains(retried, `"output":"done"`) {
		t.Fatalf("the retry dropped the tool exchange with the seal: %s", retried)
	}
}

func TestATurnWithoutSealedReasoningIsNotRetriedTwice(t *testing.T) {
	// Nothing to strip means nothing to retry: a plain refusal has to reach the
	// caller instead of being sent again unchanged.
	body := []byte(`{"model":"gpt-test","input":[{"type":"reasoning","id":"rs_1","summary":[]}]}`)
	if _, changed := stripEncryptedReasoning(body); changed {
		t.Fatal("a reasoning item without a seal was dropped")
	}
	if _, changed := stripEncryptedReasoning([]byte(`{"model":"gpt-test","input":"plain prompt"}`)); changed {
		t.Fatal("a request without replayed items was rewritten")
	}
}

func TestNestedToolCallNamesAreRestoredForTheClient(t *testing.T) {
	server, _ := upstreamCapture(t, func(writer http.ResponseWriter) {
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = writer.Write([]byte("event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"function_call\",\"name\":\"collaboration__spawn_agent\",\"call_id\":\"call_1\",\"arguments\":\"{}\"}}\n\n" +
			"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[{\"type\":\"function_call\",\"name\":\"collaboration__spawn_agent\",\"call_id\":\"call_1\",\"arguments\":\"{}\"}]}}\n\n"))
	})
	streaming := strings.Replace(codexToolRequest, `"stream":false`, `"stream":true`, 1)
	request := httptest.NewRequest(http.MethodPost, "http://relay/v1/responses", strings.NewReader(streaming))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	body := response.Body.String()
	if strings.Contains(body, "collaboration__spawn_agent") {
		t.Fatalf("client received the provider alias instead of its own tool name: %s", body)
	}
	if strings.Count(body, `"name":"collaboration.spawn_agent"`) != 2 {
		t.Fatalf("nested tool name was not restored in every event: %s", body)
	}
}

// The label a provider puts on the answer is not evidence about the bytes —
// the guardrail layer reads text/plain answers precisely because the client
// reads the body, not the header. The tool restore used to trust the header
// both ways, and each direction lost the client its tool names.
func TestToolNamesAreRestoredWhateverTheAnswerIsLabelled(t *testing.T) {
	streamBytes := "event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"function_call\",\"name\":\"collaboration__spawn_agent\",\"call_id\":\"call_1\",\"arguments\":\"{}\"}}\n\n" +
		"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[{\"type\":\"function_call\",\"name\":\"collaboration__spawn_agent\",\"call_id\":\"call_1\",\"arguments\":\"{}\"}]}}\n\n"
	for _, testCase := range []struct {
		name   string
		label  string
		body   string
		stream bool
	}{
		{name: "an event stream labelled json", label: "application/json", body: streamBytes, stream: true},
		{name: "an event stream labelled text/plain", label: "text/plain", body: streamBytes, stream: true},
		{name: "a json answer labelled text/plain", label: "text/plain", body: `{"status":"completed","output":[{"type":"function_call","name":"collaboration__spawn_agent","call_id":"call_1","arguments":"{}"}]}`},
		// The fourth corner: a JSON body under a stream label. The label alone
		// used to route it into the stream repair, which found no data lines
		// and returned the body unrewritten — the client kept provider
		// aliases. The bytes decide, not the label.
		{name: "a json answer labelled event-stream", label: "text/event-stream", body: `{"status":"completed","output":[{"type":"function_call","name":"collaboration__spawn_agent","call_id":"call_1","arguments":"{}"}]}`},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			server, _ := upstreamCapture(t, func(writer http.ResponseWriter) {
				writer.Header().Set("Content-Type", testCase.label)
				_, _ = writer.Write([]byte(testCase.body))
			})
			request := codexToolRequest
			if testCase.stream {
				request = strings.Replace(codexToolRequest, `"stream":false`, `"stream":true`, 1)
			}
			incoming := httptest.NewRequest(http.MethodPost, "http://relay/v1/responses", strings.NewReader(request))
			incoming.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			server.ServeHTTP(response, incoming)
			if testCase.stream {
				// A stream the client asked for stays a stream even when the
				// provider labelled the answer wrong: the dialect is the caller's.
				if !strings.Contains(response.Header().Get("Content-Type"), "event-stream") {
					t.Fatalf("the answer stopped being an event stream: %s", response.Header().Get("Content-Type"))
				}
			}
			body := response.Body.String()
			if strings.Contains(body, "collaboration__spawn_agent") {
				t.Fatalf("client received the provider alias instead of its own tool name: %s", body)
			}
			expected := 2
			if !testCase.stream {
				// The buffered JSON answer carries one item; the stream
				// announces it in the item event and again on completion.
				expected = 1
			}
			if strings.Count(body, `"name":"collaboration.spawn_agent"`) != expected {
				t.Fatalf("nested tool name was not restored in every place: %s", body)
			}
		})
	}
}

func TestDispatchRestoresNestedToolNamesForTunnelClients(t *testing.T) {
	server, captured := upstreamCapture(t, func(writer http.ResponseWriter) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"status":"completed","output":[{"type":"function_call","name":"collaboration__spawn_agent","call_id":"c1","arguments":"{}"}]}`))
	})
	headers := http.Header{}
	headers.Set("Content-Type", "application/json")
	result, err := server.Dispatch(context.Background(), relayapp.DispatchRequest{
		Method: http.MethodPost, Path: "/v1/responses", Headers: headers,
		Body: []byte(codexToolRequest), ProviderID: "echo", PublicModel: "alias", UpstreamModel: "gpt-test",
	})
	if err != nil {
		t.Fatalf("dispatch failed: %v", err)
	}
	if !strings.Contains(string(result.Body), `"name":"collaboration.spawn_agent"`) {
		t.Fatalf("tunnel client kept the provider alias: %s", result.Body)
	}
	if !strings.Contains(string(*captured), `"collaboration__spawn_agent"`) {
		t.Fatalf("provider never received a documented tool name: %s", *captured)
	}
}

func TestRequestsWithoutNamespacedToolsAreForwardedUnchanged(t *testing.T) {
	classic := `{"model":"gpt-test","stream":false,"tools":[{"type":"function","name":"exec","parameters":{"type":"object"}}],"input":[{"type":"message","role":"user","content":[]}]}`
	server, captured := upstreamCapture(t, func(writer http.ResponseWriter) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"status":"completed","output":[]}`))
	})
	request := httptest.NewRequest(http.MethodPost, "http://relay/v1/responses", strings.NewReader(classic))
	request.Header.Set("Content-Type", "application/json")
	server.ServeHTTP(httptest.NewRecorder(), request)
	if string(*captured) != classic {
		t.Fatalf("a documented request was rewritten:\n got %s\nwant %s", *captured, classic)
	}
}

func TestClientToolsMergeWithoutDroppingDocumentedDefinitions(t *testing.T) {
	body, compat := normalizeResponsesTools(http.MethodPost, "/v1/responses", "application/json", []byte(`{
	 "tools":[{"type":"function","name":"exec","parameters":{"type":"object"}}],
	 "input":[{"type":"additional_tools","tools":[{"type":"namespace","name":"functions","tools":[
	   {"type":"function","name":"exec"},
	   {"type":"function","name":"view_image"}
	 ]}]}]}`))
	if !compat.empty() {
		t.Fatalf("documented function tools need no translation: %+v", compat)
	}
	listed := strings.Join(toolNamesOf(t, body), " ")
	if strings.Count(listed, "function:exec") != 1 || !strings.Contains(listed, "function:view_image") {
		t.Fatalf("tool merge lost or duplicated definitions: %s", listed)
	}
	var payload struct {
		Tools []struct {
			Parameters map[string]any `json:"parameters"`
		} `json:"tools"`
	}
	if json.Unmarshal(body, &payload) != nil || payload.Tools[0].Parameters == nil {
		t.Fatalf("the documented definition lost its schema: %s", body)
	}
}

func TestReplayedToolOutputsBecomeOneDocumentedStringPerCall(t *testing.T) {
	body, _ := normalizeResponsesTools(http.MethodPost, "/v1/responses", "application/json", []byte(`{
	 "model":"gpt-test",
	 "tools":[{"type":"custom","name":"exec","format":{"type":"grammar","syntax":"lark","definition":"start: TEXT"}}],
	 "input":[
	  {"type":"custom_tool_call","name":"exec","call_id":"c1","input":"ls"},
	  {"type":"custom_tool_call_output","call_id":"c1","output":[{"type":"input_text","text":"header"},{"type":"input_text","text":"body"}]},
	  {"type":"custom_tool_call_output","call_id":"c1","name":"exec","output":"NOTIFY"},
	  {"type":"function_call_output","call_id":"orphan","output":"lost call"}
	 ]}`))
	var payload struct {
		Input []map[string]any `json:"input"`
	}
	if json.Unmarshal(body, &payload) != nil {
		t.Fatalf("normalized request is not valid JSON: %s", body)
	}
	if len(payload.Input) != 2 {
		t.Fatalf("duplicate and orphan outputs were kept: %s", body)
	}
	output, _ := payload.Input[1]["output"].(string)
	if output != "header\nbody\nNOTIFY" {
		t.Fatalf("tool output was not merged into one documented string: %q", output)
	}
	// The output has to keep answering the kind of call the client made: retyping it
	// against a freeform tool is the mismatch that killed every follow-up turn.
	if payload.Input[1]["type"] != "custom_tool_call_output" {
		t.Fatalf("a freeform output was retyped: %v", payload.Input[1])
	}
}

func TestStreamedItemsStayActiveForDeltaConsumers(t *testing.T) {
	stream := []byte("event: response.output_item.added\n" +
		"data: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"id\":\"msg_1\",\"type\":\"message\",\"status\":\"completed\",\"role\":\"assistant\",\"content\":[]}}\n\n" +
		"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg_1\",\"delta\":\"hi\"}\n\n" +
		"event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"id\":\"msg_1\",\"type\":\"message\",\"status\":\"completed\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"hi\"}]}}\n\n")
	repaired := string(restoreClientToolCalls(stream, toolCompat{}, "/v1/responses", true))
	if !strings.Contains(repaired, `"status":"in_progress"`) {
		t.Fatalf("announced item stayed completed: %s", repaired)
	}
	if strings.Count(repaired, `"status":"completed"`) != 1 {
		t.Fatalf("a terminal item lost its completed status: %s", repaired)
	}
	if !strings.Contains(repaired, `"content":[]`) {
		t.Fatalf("a null content vector was not repaired into an empty one: %s", repaired)
	}
	if !strings.Contains(repaired, "event: response.output_text.delta") || !strings.Contains(repaired, `"delta":"hi"`) {
		t.Fatalf("unrelated events were disturbed: %s", repaired)
	}
}

func TestServerSideHistoryKeepsOutputsWithoutALocalCall(t *testing.T) {
	body, _ := normalizeResponsesTools(http.MethodPost, "/v1/responses", "application/json", []byte(`{
	 "model":"gpt-test",
	 "previous_response_id":"resp_earlier",
	 "tools":[{"type":"custom","name":"exec","format":{"type":"grammar","syntax":"lark","definition":"start: TEXT"}}],
	 "input":[{"type":"custom_tool_call_output","call_id":"c1","output":"kept"}]}`))
	var payload struct {
		Input []map[string]any `json:"input"`
	}
	if json.Unmarshal(body, &payload) != nil {
		t.Fatalf("normalized request is not valid JSON: %s", body)
	}
	if len(payload.Input) != 1 || payload.Input[0]["type"] != "custom_tool_call_output" || payload.Input[0]["output"] != "kept" {
		t.Fatalf("an output of a provider-side call was dropped: %s", body)
	}
}

func TestStreamedTextGetsTheContentPartClientsWaitFor(t *testing.T) {
	stream := []byte("event: response.output_item.added\n" +
		"data: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"id\":\"msg_1\",\"type\":\"message\",\"status\":\"completed\",\"role\":\"assistant\",\"content\":[]}}\n\n" +
		"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg_1\",\"output_index\":0,\"content_index\":0,\"delta\":\"hel\"}\n\n" +
		"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg_1\",\"output_index\":0,\"content_index\":0,\"delta\":\"lo\"}\n\n" +
		"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[]}}\n\n")
	repaired := string(restoreClientToolCalls(stream, toolCompat{}, "/v1/responses", true))
	if strings.Count(repaired, "event: response.content_part.added") != 1 {
		t.Fatalf("the missing content part was not synthesized exactly once: %s", repaired)
	}
	if strings.Count(repaired, "event: response.output_item.added") != 1 {
		t.Fatalf("an announced item was announced twice: %s", repaired)
	}
	if strings.Index(repaired, "content_part.added") > strings.Index(repaired, `"delta":"hel"`) {
		t.Fatalf("the content part must arrive before the first delta: %s", repaired)
	}
	if !strings.Contains(repaired, `"item_id":"msg_1"`) || !strings.Contains(repaired, `"content_index":0`) {
		t.Fatalf("the synthesized part does not address the streamed item: %s", repaired)
	}

	native := []byte("event: response.content_part.added\ndata: {\"type\":\"response.content_part.added\",\"item_id\":\"msg_1\",\"output_index\":0,\"content_index\":0,\"part\":{\"type\":\"output_text\",\"text\":\"\"}}\n\n" +
		"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg_1\",\"output_index\":0,\"content_index\":0,\"delta\":\"hi\"}\n\n")
	if untouched := restoreClientToolCalls(native, toolCompat{}, "/v1/responses", true); string(untouched) != string(native) {
		t.Fatalf("a provider that announces its content parts was rewritten: %s", untouched)
	}
}

func TestDispatchedStreamsStayConsumableAfterTunnelSanitizing(t *testing.T) {
	server, _ := upstreamCapture(t, func(writer http.ResponseWriter) {
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = writer.Write([]byte("event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"id\":\"msg_1\",\"type\":\"message\",\"status\":\"completed\",\"role\":\"assistant\",\"content\":[]}}\n\n" +
			"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg_1\",\"output_index\":0,\"content_index\":0,\"delta\":\"hi\"}\n\n" +
			"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[{\"id\":\"msg_1\",\"type\":\"message\",\"status\":\"completed\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"hi\"}]}]}}\n\n"))
	})
	headers := http.Header{}
	headers.Set("Content-Type", "application/json")
	result, err := server.Dispatch(context.Background(), relayapp.DispatchRequest{
		Method: http.MethodPost, Path: "/v1/responses", Headers: headers,
		Body:       []byte(`{"model":"gpt-test","stream":true,"input":[{"type":"message","role":"user","content":[]}]}`),
		ProviderID: "echo", PublicModel: "alias", UpstreamModel: "gpt-test",
	})
	if err != nil {
		t.Fatalf("dispatch failed: %v", err)
	}
	body := string(result.Body)
	if !strings.Contains(body, `"status":"in_progress"`) {
		t.Fatalf("the announced item was not repaired on the tunnel path: %s", body)
	}
	if strings.Count(body, "event: response.content_part.added") != 1 {
		t.Fatalf("the missing content part was not synthesized once on the tunnel path: %s", body)
	}
	// The tunnel rebuilds and privacy-checks every event, so a repaired stream has to
	// survive that second pass instead of being rejected as a failed response.
	if !strings.Contains(result.Terminal, "response.completed") {
		t.Fatalf("a repaired stream lost its terminal event: %q", result.Terminal)
	}
}

func TestTextDeltasForAnUnannouncedItemGetTheirWholeLifecycle(t *testing.T) {
	// A provider that emits a message after a tool call sometimes skips the item
	// announcement entirely; the client then discards the whole answer.
	stream := []byte("event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"id\":\"fc_1\",\"type\":\"function_call\",\"name\":\"exec\",\"call_id\":\"c1\",\"arguments\":\"{}\"}}\n\n" +
		"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg_2\",\"output_index\":1,\"content_index\":0,\"delta\":\"done\"}\n\n" +
		"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[]}}\n\n")
	repaired := string(restoreClientToolCalls(stream, toolCompat{}, "/v1/responses", true))
	item := strings.Index(repaired, `"id":"msg_2"`)
	part := strings.Index(repaired, `"type":"response.content_part.added"`)
	delta := strings.Index(repaired, `"delta":"done"`)
	if item < 0 || part < 0 || item > part || part > delta {
		t.Fatalf("the unannounced item did not get an item and part before its delta: %s", repaired)
	}
	if !strings.Contains(repaired, `"output_index":1`) {
		t.Fatalf("the synthesized item lost the output index of its deltas: %s", repaired)
	}
	if strings.Count(repaired, `"id":"fc_1"`) != 1 {
		t.Fatalf("the already announced call was announced again: %s", repaired)
	}
}

func TestUnsafeToolNamesGetStableBoundedAliases(t *testing.T) {
	long := strings.Repeat("namespace_segment", 6)
	reserved := map[string]struct{}{"a__b": {}}
	if alias := providerToolName("a.b", reserved); alias == "a__b" || !strings.HasPrefix(alias, "a__b_") {
		t.Fatalf("alias collision was not resolved: %s", alias)
	}
	alias := providerToolName(long+".call", map[string]struct{}{})
	if len(alias) > maxToolNameBytes || !providerSafeToolName(alias) {
		t.Fatalf("alias is not a documented tool name: %q (%d bytes)", alias, len(alias))
	}
	if alias != providerToolName(long+".call", map[string]struct{}{}) {
		t.Fatal("alias is not stable across requests")
	}
}

func TestToolNormalizationIgnoresOtherEndpointsAndPayloads(t *testing.T) {
	cases := []struct{ method, path, contentType, body string }{
		{http.MethodPost, "/v1/chat/completions", "application/json", codexToolRequest},
		{http.MethodGet, "/v1/responses", "application/json", codexToolRequest},
		{http.MethodPost, "/v1/responses", "multipart/form-data; boundary=x", codexToolRequest},
		{http.MethodPost, "/v1/responses", "application/json", `{"model":"gpt-test","input":"plain prompt"}`},
		{http.MethodPost, "/v1/responses", "application/json", `not json`},
	}
	for _, testCase := range cases {
		body, compat := normalizeResponsesTools(testCase.method, testCase.path, testCase.contentType, []byte(testCase.body))
		if string(body) != testCase.body || !compat.empty() {
			t.Fatalf("%s %s (%s) was rewritten", testCase.method, testCase.path, testCase.contentType)
		}
	}
}

// W3C lets one SSE event carry its data across several `data:` lines, joined with
// a newline. Every other reader here already does that — sseInspector for the
// terminal event, the guardrail extractor for the payload — but the tool restore
// used to give up on such a block and forward it untouched. Three repairs died
// there at once: the item stayed announced as `completed` so the client discarded
// every delta of it, the tool kept its provider-side alias so the client rejected
// a call it never declared, and the prologue was synthesized on top of an
// announcement that was already there.
//
// The assertion is equality with the single-line form rather than a list of
// expected substrings: whatever the repairs do, the framing must not decide it.
func TestAnEventSplitAcrossDataLinesIsRepairedLikeAnyOther(t *testing.T) {
	const item = `{"id":"i1","type":"message","status":"completed","content":null}`
	const call = `{"id":"c1","type":"function_call","name":"prov_exec","call_id":"c1","arguments":"{\"input\":\"ls\"}"}`
	compat := toolCompat{
		toClient: map[string]string{"prov_exec": "exec"},
		freeform: map[string]struct{}{"prov_exec": {}},
	}
	for _, testCase := range []struct {
		name   string
		head   string
		tail   string
		follow string
	}{
		{
			name: "an item announced as already finished",
			head: `{"type":"response.output_item.added","output_index":0,`, tail: `"item":` + item + `}`,
			follow: `data: {"type":"response.output_text.delta","item_id":"i1","output_index":0,"content_index":0,"delta":"hello"}` + "\n\n",
		},
		{
			name: "a freeform call under its provider alias",
			head: `{"type":"response.output_item.done","output_index":0,`, tail: `"item":` + call + `}`,
		},
		{
			name: "an event split into three lines",
			head: `{"type":"response.output_item.done",`, tail: `"output_index":0,` + "\n" + `data: "item":` + call + `}`,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			single := "data: " + testCase.head + strings.ReplaceAll(testCase.tail, "\ndata: ", "") + "\n\n" + testCase.follow
			split := "data: " + testCase.head + "\ndata: " + testCase.tail + "\n\n" + testCase.follow
			want := restoreClientToolCalls([]byte(single), compat, "/v1/responses", true)
			got := restoreClientToolCalls([]byte(split), compat, "/v1/responses", true)
			if string(got) != string(want) {
				t.Fatalf("the framing changed the repair:\n split gave:\n%s\n single gave:\n%s", got, want)
			}
			// A sanity floor, so the test cannot pass by both forms being left alone.
			if !strings.Contains(string(got), `"in_progress"`) && !strings.Contains(string(got), `"name":"exec"`) {
				t.Fatalf("neither repair happened, so this proves nothing:\n%s", got)
			}
		})
	}
}

// The fast-path trigger scan reads the deframed bytes, because the framing is
// exactly what the repair removes: a literal broken up by `data:` markers and
// newlines is still a trigger for attempting the repair, and the attempt is
// inert when the event turns out unreadable. A raw scan skipped the repair
// entirely on framing alone — the decision the code's own comment forbids.
func TestTheTriggerScanReadsTheDeframedBytes(t *testing.T) {
	split := "data: {\"type\":\"response.output_\ndata: item.added\",\"output_index\":0}\n\n"
	if !streamAnnouncesRepairableEvents([]byte(split)) {
		t.Fatal("a trigger literal broken by the framing was invisible to the fast-path scan")
	}
	if !streamAnnouncesRepairableEvents([]byte("data: {\"type\":\"response.output_text.delta\"}\n\n")) {
		t.Fatal("a plain delta stopped being a trigger")
	}
	if streamAnnouncesRepairableEvents([]byte("data: {\"type\":\"response.completed\"}\n\n")) {
		t.Fatal("a stream with no repairable lifecycle tripped the fast path")
	}
}

// The separator matters as much as the joining. A split that falls inside a JSON
// string literal is only valid JSON when the newline the spec names is put back;
// joining the fragments flush would parse an event the client itself reads as
// malformed, and the relay would then announce items and content parts for text
// nobody is ever going to see.
func TestDataLinesAreJoinedWithTheNewlineTheSpecNames(t *testing.T) {
	body := "data: {\"type\":\"response.output_text.delta\",\"item_id\":\"i1\",\"delta\":\"one\n" +
		"data: two\"}\n\n"
	got := restoreClientToolCalls([]byte(body), toolCompat{}, "/v1/responses", true)
	if strings.Contains(string(got), "response.output_item.added") {
		t.Fatalf("an event the client discards as malformed was given a prologue:\n%s", got)
	}
	if string(got) != body {
		t.Fatalf("a block the relay cannot read was rewritten anyway:\n%s", got)
	}
}

// A strict upstream validates input[].name even when the tool definition is gone:
// a renamed or removed tool replays at depth (input[326]) with its dotted MCP name
// and the whole request dies with 400 input[N].name invalid_string. The alias has
// to be synthesized from history alone and still restore on the way back.
func TestAHistoryOnlyDottedNameIsAliasedBeforeUpstream(t *testing.T) {
	body, compat := normalizeResponsesTools(http.MethodPost, "/v1/responses", "application/json", []byte(`{
	 "model":"gpt-test",
	 "input":[
	  {"type":"function_call","name":"collaboration.spawn_agent","call_id":"c1","arguments":"{}"},
	  {"type":"function_call_output","call_id":"c1","output":"done"},
	  {"type":"custom_tool_call","name":"team/reviewer:check task","call_id":"c2","input":"hi"}
	 ]}`))
	var payload struct {
		Input []map[string]any `json:"input"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("normalized request is not valid JSON: %s", body)
	}
	if len(payload.Input) != 3 {
		t.Fatalf("history items were dropped: %s", body)
	}
	for _, item := range payload.Input {
		if name, ok := item["name"].(string); ok && !providerSafeToolName(name) {
			t.Fatalf("history name reached the provider outside ^[a-zA-Z0-9_-]+$: %q", name)
		}
	}
	first, _ := payload.Input[0]["name"].(string)
	if first == "collaboration.spawn_agent" {
		t.Fatalf("dotted history name was forwarded verbatim: %s", body)
	}
	if compat.toClient[first] != "collaboration.spawn_agent" {
		t.Fatalf("history alias is not reversible: %+v", compat.toClient)
	}
	third, _ := payload.Input[2]["name"].(string)
	if compat.toClient[third] != "team/reviewer:check task" {
		t.Fatalf("unsafe custom history name is not reversible: %+v", compat.toClient)
	}
	restored := string(restoreClientToolCalls(
		[]byte(`{"output":[{"type":"function_call","name":"`+first+`","call_id":"c9","arguments":"{}"}]}`),
		compat, "/v1/responses", false))
	if !strings.Contains(restored, `"name":"collaboration.spawn_agent"`) {
		t.Fatalf("provider alias was not restored for the client: %s", restored)
	}
}

// Top-level tools travel verbatim to a strict upstream, so an unsafe tools[].name
// is the same 400 as a dotted history replay. It has to be aliased before the
// request leaves, not repaired after the refusal.
func TestATopLevelUnsafeToolNameIsAliasedBeforeUpstream(t *testing.T) {
	body, compat := normalizeResponsesTools(http.MethodPost, "/v1/responses", "application/json", []byte(`{
	 "model":"gpt-test",
	 "tools":[{"type":"function","name":"collaboration.spawn_agent","parameters":{"type":"object"}}],
	 "input":[{"type":"message","role":"user","content":[]}]}`))
	var payload struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("normalized request is not valid JSON: %s", body)
	}
	if len(payload.Tools) != 1 {
		t.Fatalf("tool definition was dropped: %s", body)
	}
	if !providerSafeToolName(payload.Tools[0].Name) {
		t.Fatalf("unsafe top-level tool name reached the provider: %q", payload.Tools[0].Name)
	}
	if compat.toClient[payload.Tools[0].Name] != "collaboration.spawn_agent" {
		t.Fatalf("top-level alias is not reversible: %+v", compat.toClient)
	}
}

// A declared unsafe name must not mint the verbatim name of an unrelated
// history-only tool: both would reach the provider under one name for two
// distinct tools. The safe history name reserves the alias before any alias
// is minted, whatever order the items arrive in.
func TestADeclaredAliasDoesNotCollideWithASafeHistoryName(t *testing.T) {
	body, compat := normalizeResponsesTools(http.MethodPost, "/v1/responses", "application/json", []byte(`{
	 "model":"gpt-test",
	 "input":[
	  {"type":"additional_tools","tools":[{"type":"function","name":"a/b"}]},
	  {"type":"function_call","name":"a_b","call_id":"c0","arguments":"{}"},
	  {"type":"function_call_output","call_id":"c0","name":"a_b","output":"old"},
	  {"type":"function_call","name":"a/b","call_id":"c1","arguments":"{}"},
	  {"type":"function_call_output","call_id":"c1","name":"a/b","output":"new"}
	 ]}`))
	var payload struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
		Input []map[string]any `json:"input"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("normalized request is not valid JSON: %s", body)
	}
	if len(payload.Input) != 4 {
		t.Fatalf("history items were dropped: %s", body)
	}
	safeCall, _ := payload.Input[0]["name"].(string)
	safeOutput, _ := payload.Input[1]["name"].(string)
	if safeCall != "a_b" || safeOutput != "a_b" {
		t.Fatalf("safe history names must travel verbatim: %s", body)
	}
	aliasedCall, _ := payload.Input[2]["name"].(string)
	aliasedOutput, _ := payload.Input[3]["name"].(string)
	if !providerSafeToolName(aliasedCall) || !providerSafeToolName(aliasedOutput) {
		t.Fatalf("unsafe history names must be aliased: %s", body)
	}
	if aliasedCall == "a_b" || aliasedOutput == "a_b" || aliasedCall != aliasedOutput {
		t.Fatalf("declared and history tools share one provider name: %q vs %q", aliasedCall, safeCall)
	}
	if len(aliasedCall) > maxToolNameBytes {
		t.Fatalf("alias broke the 64-byte documented shape: %q", aliasedCall)
	}
	if compat.toClient[aliasedCall] != "a/b" {
		t.Fatalf("history alias is not reversible: %+v", compat.toClient)
	}
	if len(payload.Tools) != 1 || payload.Tools[0].Name != aliasedCall {
		t.Fatalf("declared tool lost its alias: %+v", payload.Tools)
	}
	restored := string(restoreClientToolCalls(
		[]byte(`{"output":[{"type":"function_call","name":"`+aliasedCall+`","call_id":"c9","arguments":"{}"}]}`),
		compat, "/v1/responses", false))
	if !strings.Contains(restored, `"name":"a/b"`) {
		t.Fatalf("provider alias was not restored for the client: %s", restored)
	}
}

// Two distinct originals must never share one provider name: the second alias
// carries a hash suffix so each restores to its own client name.
func TestDistinctUnsafeNamesGetDistinctAliases(t *testing.T) {
	body, compat := normalizeResponsesTools(http.MethodPost, "/v1/responses", "application/json", []byte(`{
	 "model":"gpt-test",
	 "tools":[
	  {"type":"function","name":"a/b","parameters":{"type":"object"}},
	  {"type":"function","name":"a:b","parameters":{"type":"object"}}
	 ],
	 "input":[
	  {"type":"function_call","name":"a/b","call_id":"c1","arguments":"{}"},
	  {"type":"function_call","name":"a:b","call_id":"c2","arguments":"{}"}
	 ]}`))
	var payload struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
		Input []map[string]any `json:"input"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("normalized request is not valid JSON: %s", body)
	}
	if len(payload.Tools) != 2 || payload.Tools[0].Name == payload.Tools[1].Name {
		t.Fatalf("colliding tool names were not separated: %+v", payload.Tools)
	}
	for _, tool := range payload.Tools {
		if !providerSafeToolName(tool.Name) || len(tool.Name) > maxToolNameBytes {
			t.Fatalf("alias broke the 64-byte documented shape: %q", tool.Name)
		}
	}
	first, _ := payload.Input[0]["name"].(string)
	second, _ := payload.Input[1]["name"].(string)
	if first == "a/b" || second == "a:b" || first == second {
		t.Fatalf("history collision was not separated: %q vs %q", first, second)
	}
	if compat.toClient[first] != "a/b" || compat.toClient[second] != "a:b" {
		t.Fatalf("colliding aliases do not restore distinctly: %+v", compat.toClient)
	}
}

// An empty tool name is already outside the documented shape, and aliasing it
// would mint a hash that restores to nothing — so replayed history with no
// name travels untouched instead of gaining an alias. The same holds for a
// top-level definition with an empty name: it passes verbatim with no minted
// alias.
func TestAnEmptyHistoryToolNameIsLeftUntouched(t *testing.T) {
	body, compat := normalizeResponsesTools(http.MethodPost, "/v1/responses", "application/json", []byte(`{
	 "model":"gpt-test",
	 "tools":[{"type":"function","name":"","parameters":{"type":"object"}}],
	 "input":[
	  {"type":"function_call","name":"","call_id":"c1","arguments":"{}"},
	  {"type":"function_call_output","call_id":"c1","name":"","output":"done"},
	  {"type":"custom_tool_call","name":"","call_id":"c2","input":"hi"},
	  {"type":"custom_tool_call_output","call_id":"c2","name":"","output":"done"}
	 ]}`))
	var payload struct {
		Tools []map[string]any `json:"tools"`
		Input []map[string]any `json:"input"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("normalized request is not valid JSON: %s", body)
	}
	if len(payload.Input) != 4 {
		t.Fatalf("nameless history items were dropped: %s", body)
	}
	for _, item := range payload.Input {
		if name, hasName := item["name"].(string); hasName && name != "" {
			t.Fatalf("empty history name gained an alias: %q in %s", name, body)
		}
	}
	if len(payload.Tools) != 1 {
		t.Fatalf("nameless tool definition was dropped: %s", body)
	}
	if name, _ := payload.Tools[0]["name"].(string); name != "" {
		t.Fatalf("empty definition name gained an alias: %q in %s", name, body)
	}
	if len(compat.toClient) != 0 || len(compat.toProvider) != 0 {
		t.Fatalf("empty names minted aliases that restore to nothing: %+v", compat.toClient)
	}
}
