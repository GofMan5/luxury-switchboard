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
	for _, expected := range []string{"function:exec", "function:wait", "function:collaboration__spawn_agent"} {
		if !strings.Contains(names, expected) {
			t.Fatalf("provider never received %q: %s", expected, names)
		}
	}
	if strings.Contains(names, "custom:") {
		t.Fatalf("a freeform tool reached the provider undocumented: %s", names)
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
		if tool.Format != nil || tool.Parameters == nil {
			t.Fatalf("freeform tool was not expressed as a documented function: %+v", tool)
		}
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
	if strings.Contains(forwarded, "custom_tool_call") {
		t.Fatalf("replayed freeform history was not documented for the provider: %s", forwarded)
	}
	if !strings.Contains(forwarded, `"type":"function_call_output"`) || !strings.Contains(forwarded, `{\"input\":\"ls\"}`) {
		t.Fatalf("replayed freeform call lost its payload: %s", forwarded)
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
	 "tools":[{"type":"function","name":"exec","parameters":{"type":"object"}}],
	 "input":[
	  {"type":"function_call","name":"exec","call_id":"c1","arguments":"{}"},
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
	if payload.Input[1]["type"] != "function_call_output" || payload.Input[1]["name"] != nil {
		t.Fatalf("tool output item is not documented: %v", payload.Input[1])
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
	if len(payload.Input) != 1 || payload.Input[0]["type"] != "function_call_output" || payload.Input[0]["output"] != "kept" {
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
