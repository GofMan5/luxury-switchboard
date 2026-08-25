package relayhttp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	relayapp "github.com/luxuryprivate/switchboard/backend/internal/slices/relay/application"
)

const chatStreamText = "" +
	"data: {\"id\":\"chatcmpl-7\",\"object\":\"chat.completion.chunk\",\"created\":1700000000,\"model\":\"gpt-test\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"Hello\"},\"finish_reason\":null}]}\n\n" +
	"data: {\"id\":\"chatcmpl-7\",\"object\":\"chat.completion.chunk\",\"created\":1700000000,\"model\":\"gpt-test\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\" world\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":3,\"total_tokens\":13}}\n\n" +
	"data: [DONE]\n"

const chatStreamToolCall = "" +
	"data: {\"id\":\"chatcmpl-8\",\"object\":\"chat.completion.chunk\",\"created\":1700000001,\"model\":\"gpt-test\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"tool_calls\":[{\"index\":0,\"id\":\"call_9\",\"type\":\"function\",\"function\":{\"name\":\"sh_cmd\",\"arguments\":\"\"}}]},\"finish_reason\":null}]}\n\n" +
	"data: {\"id\":\"chatcmpl-8\",\"object\":\"chat.completion.chunk\",\"created\":1700000001,\"model\":\"gpt-test\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"{\\\"cmd\\\":\\\"ls\"}}]},\"finish_reason\":null}]}\n\n" +
	"data: {\"id\":\"chatcmpl-8\",\"object\":\"chat.completion.chunk\",\"created\":1700000001,\"model\":\"gpt-test\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"\\\"}\"}}]},\"finish_reason\":\"tool-calls\"}]}\n\n" +
	"data: [DONE]\n"

func sseEventTypes(body []byte) []string {
	types := make([]string, 0, 8)
	for _, block := range strings.Split(strings.ReplaceAll(string(body), "\r\n", "\n"), "\n\n") {
		data := sseData([]byte(block))
		if len(data) == 0 {
			continue
		}
		var envelope struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(data, &envelope) == nil && envelope.Type != "" {
			types = append(types, envelope.Type)
		}
	}
	return types
}

func translatedBody(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var payload map[string]any
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.UseNumber()
	if err := decoder.Decode(&payload); err != nil {
		t.Fatalf("translated request is not valid JSON: %v", err)
	}
	return payload
}

func TestResponsesRequestTranslatesToChatCompletions(t *testing.T) {
	request := `{
  "model":"gpt-test","instructions":"Be brief","stream":true,"store":false,"max_output_tokens":64,
  "reasoning":{"effort":"medium"},"tool_choice":{"type":"function","name":"sh_cmd"},
  "tools":[
    {"type":"function","name":"sh_cmd","description":"Run a shell command","parameters":{"type":"object","properties":{"cmd":{"type":"string"}}}},
    {"type":"function","name":"noop"},
    {"type":"web_search"}
  ],
  "input":[
    {"type":"message","role":"user","content":[{"type":"input_text","text":"run git status"}]},
    {"type":"function_call","call_id":"call_1","name":"sh_cmd","arguments":{"cmd":"ls"}},
    {"type":"function_call_output","call_id":"call_1","output":{"status":"ok"}}
  ]
}`
	converted, err := responsesToChat([]byte(request))
	if err != nil {
		t.Fatalf("translation failed: %v", err)
	}
	chat := translatedBody(t, converted)
	if chat["stream"] != true {
		t.Fatal("chat request must force streaming")
	}
	if chat["max_tokens"] != json.Number("64") {
		t.Fatalf("max_output_tokens was not mapped to max_tokens: %v", chat["max_tokens"])
	}
	if chat["reasoning_effort"] != "medium" {
		t.Fatalf("reasoning effort was not mapped: %v", chat["reasoning_effort"])
	}
	if _, leaked := chat["store"]; leaked {
		t.Fatal("responses-only field leaked into the chat request")
	}
	tools, _ := chat["tools"].([]any)
	if len(tools) != 2 {
		t.Fatalf("hosted tools must be dropped and functions kept, got %d: %v", len(tools), tools)
	}
	first, _ := tools[0].(map[string]any)
	if first["type"] != "function" {
		t.Fatalf("tool was not wrapped for chat completions: %+v", first)
	}
	declared, _ := first["function"].(map[string]any)
	if declared["name"] != "sh_cmd" || declared["description"] != "Run a shell command" {
		t.Fatalf("tool definition was not nested: %+v", first)
	}
	if _, flat := first["name"]; flat {
		t.Fatalf("the responses-flat name survived at the top level: %+v", first)
	}
	if parameters, _ := declared["parameters"].(map[string]any); parameters["type"] != "object" {
		t.Fatalf("tool parameters were lost: %+v", declared)
	}
	second, _ := tools[1].(map[string]any)["function"].(map[string]any)
	if parameters, _ := second["parameters"].(map[string]any); parameters["type"] != "object" {
		t.Fatalf("a tool without parameters needs an empty schema: %+v", second)
	}
	choice, _ := chat["tool_choice"].(map[string]any)
	if choice["type"] != "function" {
		t.Fatalf("tool_choice was not remapped: %+v", choice)
	}
	if function, _ := choice["function"].(map[string]any); function["name"] != "sh_cmd" {
		t.Fatalf("tool_choice function name missing: %+v", choice)
	}
	messages, _ := chat["messages"].([]any)
	if len(messages) != 4 {
		t.Fatalf("expected 4 messages, got %d: %v", len(messages), messages)
	}
	system := messages[0].(map[string]any)
	if system["role"] != "system" || system["content"] != "Be brief" {
		t.Fatalf("instructions did not become a system message: %+v", system)
	}
	assistant := messages[2].(map[string]any)
	calls, _ := assistant["tool_calls"].([]any)
	if len(calls) != 1 || calls[0].(map[string]any)["id"] != "call_1" {
		t.Fatalf("function_call item did not become a tool call message: %+v", assistant)
	}
	function := calls[0].(map[string]any)["function"].(map[string]any)
	if function["name"] != "sh_cmd" || function["arguments"] != "{\"cmd\":\"ls\"}" {
		t.Fatalf("tool call payload is wrong: %+v", function)
	}
	tool := messages[3].(map[string]any)
	if tool["role"] != "tool" || tool["tool_call_id"] != "call_1" || tool["content"] != "{\"status\":\"ok\"}" {
		t.Fatalf("function_call_output did not become a tool message: %+v", tool)
	}
}

// The chat dialect knows only documented functions, so a freeform tool has to be
// re-expressed rather than dropped: a model given no definition for the tool it
// is about to be asked to use answers by narrating the call in its prose, which
// is the failure the whole tool-compat layer exists to prevent.
func TestFreeformToolsSurviveTheChatTranslation(t *testing.T) {
	request := `{
  "model":"gpt-test",
  "tools":[
    {"type":"custom","name":"apply_patch","description":"Edit files","format":{"type":"grammar","syntax":"lark","definition":"start: /(.|\\n)+/"}},
    {"type":"function","name":"noop"}
  ],
  "input":[
    {"type":"message","role":"user","content":[{"type":"input_text","text":"patch it"}]},
    {"type":"custom_tool_call","call_id":"call_1","name":"apply_patch","input":"*** Begin Patch\nhello\n*** End Patch"},
    {"type":"custom_tool_call_output","call_id":"call_1","output":"applied"}
  ]
}`
	chat := translatedBody(t, mustTranslate(t, request))
	tools, _ := chat["tools"].([]any)
	if len(tools) != 2 {
		t.Fatalf("expected the freeform tool to survive beside the function, got %d: %v", len(tools), tools)
	}
	freeform, _ := tools[0].(map[string]any)["function"].(map[string]any)
	if freeform["name"] != "apply_patch" {
		t.Fatalf("freeform tool was not translated: %+v", tools[0])
	}
	parameters, _ := freeform["parameters"].(map[string]any)
	properties, _ := parameters["properties"].(map[string]any)
	if _, ok := properties["input"]; !ok {
		t.Fatalf("freeform tool needs the documented single-string schema: %+v", parameters)
	}
	if _, leaked := freeform["format"]; leaked {
		t.Fatalf("the lark grammar has no place in a chat function: %+v", freeform)
	}
	messages, _ := chat["messages"].([]any)
	if len(messages) != 3 {
		t.Fatalf("expected the replayed freeform exchange to survive, got %d: %v", len(messages), messages)
	}
	assistant, _ := messages[1].(map[string]any)
	calls, _ := assistant["tool_calls"].([]any)
	if len(calls) != 1 {
		t.Fatalf("custom_tool_call did not become a tool call message: %+v", assistant)
	}
	function, _ := calls[0].(map[string]any)["function"].(map[string]any)
	if function["name"] != "apply_patch" {
		t.Fatalf("replayed freeform call lost its name: %+v", function)
	}
	if arguments, _ := function["arguments"].(string); !strings.Contains(arguments, "Begin Patch") || !strings.HasPrefix(arguments, `{"input":`) {
		t.Fatalf("freeform payload must be replayed as the documented input string, got %q", function["arguments"])
	}
	output, _ := messages[2].(map[string]any)
	if output["role"] != "tool" || output["tool_call_id"] != "call_1" || output["content"] != "applied" {
		t.Fatalf("custom_tool_call_output did not become a tool message: %+v", output)
	}
}

func mustTranslate(t *testing.T, request string) []byte {
	t.Helper()
	converted, err := responsesToChat([]byte(request))
	if err != nil {
		t.Fatalf("translation failed: %v", err)
	}
	return converted
}

func TestResponsesChatTranslationSkipsOtherEndpoints(t *testing.T) {
	if path, body, translated, err := prepareChatCompletions(http.MethodGet, "/v1/responses", []byte(`{}`), "application/json", "/chat-completion", true); err != nil || translated || path != "/v1/responses" || body == nil {
		t.Fatalf("GET requests must pass through untouched")
	}
	if path, _, translated, err := prepareChatCompletions(http.MethodPost, "/v1/chat/completions", []byte(`{"model":"x","messages":[]}`), "application/json", "/chat-completion", true); err != nil || translated || path != "/v1/chat/completions" {
		t.Fatalf("chat completions requests must pass through untouched")
	}
	if _, _, _, err := prepareChatCompletions(http.MethodPost, "/v1/responses", []byte(`not json`), "application/json", "/chat-completion", true); err == nil {
		t.Fatal("invalid JSON must fail the translation")
	}
	if path, _, translated, err := prepareChatCompletions(http.MethodPost, "/v1/responses", []byte(`{"model":"x","input":"hi"}`), "application/json", "/chat-completion", true); err != nil || !translated || path != "/chat-completion" {
		t.Fatalf("responses request was not translated: path=%q translated=%v err=%v", path, translated, err)
	}
	if _, _, translated, _ := prepareChatCompletions(http.MethodPost, "/v1/responses", []byte(`{"model":"x","input":"hi"}`), "application/json", "/chat-completion", false); translated {
		t.Fatal("disabled chat compatibility must not translate")
	}
}

func TestChatStreamConvertsToResponsesLifecycle(t *testing.T) {
	converted, err := chatToResponses([]byte(chatStreamText), true)
	if err != nil {
		t.Fatalf("conversion failed: %v", err)
	}
	want := []string{
		"response.created",
		"response.output_item.added",
		"response.content_part.added",
		"response.output_text.delta",
		"response.output_text.done",
		"response.output_item.done",
		"response.completed",
	}
	got := sseEventTypes(converted)
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("event lifecycle mismatch:\n got: %v\nwant: %v", got, want)
	}
	for _, block := range strings.Split(strings.ReplaceAll(string(converted), "\r\n", "\n"), "\n\n") {
		data := sseData([]byte(block))
		if len(data) == 0 {
			continue
		}
		var payload map[string]any
		decoder := json.NewDecoder(strings.NewReader(string(data)))
		decoder.UseNumber()
		if decoder.Decode(&payload) != nil {
			t.Fatalf("converted block is not valid JSON: %s", data)
		}
		if payload["type"] != "response.completed" {
			continue
		}
		response := payload["response"].(map[string]any)
		output := response["output"].([]any)
		item := output[0].(map[string]any)
		if item["status"] != "completed" || item["type"] != "message" {
			t.Fatalf("completed item shape is wrong: %+v", item)
		}
		usage := response["usage"].(map[string]any)
		if usage["input_tokens"] != json.Number("10") || usage["output_tokens"] != json.Number("3") {
			t.Fatalf("usage was not mapped: %+v", usage)
		}
	}
}

func TestChatStreamConvertsToResponsesJSON(t *testing.T) {
	converted, err := chatToResponses([]byte(chatStreamText), false)
	if err != nil {
		t.Fatalf("conversion failed: %v", err)
	}
	var response map[string]any
	if err := json.Unmarshal(converted, &response); err != nil {
		t.Fatalf("JSON client output is not valid JSON: %v", err)
	}
	if response["object"] != "response" || response["status"] != "completed" {
		t.Fatalf("response envelope is wrong: %+v", response)
	}
	output := response["output"].([]any)
	item := output[0].(map[string]any)
	if item["type"] != "message" || item["status"] != "completed" {
		t.Fatalf("output item is wrong: %+v", item)
	}
	content := item["content"].([]any)
	part := content[0].(map[string]any)
	if part["type"] != "output_text" || part["text"] != "Hello world" {
		t.Fatalf("text part is wrong: %+v", part)
	}
}

func TestChatToolCallsBecomeResponsesFunctionCallItems(t *testing.T) {
	converted, err := chatToResponses([]byte(chatStreamToolCall), true)
	if err != nil {
		t.Fatalf("conversion failed: %v", err)
	}
	types := sseEventTypes(converted)
	for _, expected := range []string{
		"response.output_item.added", "response.function_call_arguments.delta",
		"response.function_call_arguments.done", "response.output_item.done", "response.completed",
	} {
		if !strings.Contains(strings.Join(types, " "), expected) {
			t.Fatalf("tool call lifecycle misses %q: %v", expected, types)
		}
	}
	completed := false
	for _, block := range strings.Split(strings.ReplaceAll(string(converted), "\r\n", "\n"), "\n\n") {
		data := sseData([]byte(block))
		var payload map[string]any
		if json.Unmarshal(data, &payload) != nil {
			continue
		}
		switch payload["type"] {
		case "response.completed":
			completed = true
			output := payload["response"].(map[string]any)["output"].([]any)
			call := output[0].(map[string]any)
			if call["type"] != "function_call" || call["name"] != "sh_cmd" || call["arguments"] != "{\"cmd\":\"ls\"}" {
				t.Fatalf("function call item is wrong: %+v", call)
			}
		case "response.function_call_arguments.done":
			if payload["arguments"] != "{\"cmd\":\"ls\"}" {
				t.Fatalf("arguments.done carries partial arguments: %+v", payload)
			}
		}
	}
	if !completed {
		t.Fatal("stream never completed")
	}
}

func TestBufferedCompletionSurvivesWhenTheGatewayIgnoresStreaming(t *testing.T) {
	// DeepSeek-style gateways answer "stream": true with a single JSON body. The
	// fields live under "message" instead of "delta" and the tool calls arrive in
	// order with no index, so both fallbacks have to hold or the reply is lost.
	completion := `{"id":"chatcmpl-11","object":"chat.completion","created":1700000002,"model":"gpt-test",
  "choices":[{"index":0,"finish_reason":"tool_calls","message":{"role":"assistant","content":"Working on it",
    "tool_calls":[
      {"id":"call_a","type":"function","function":{"name":"sh_cmd","arguments":"{\"cmd\":\"ls\"}"}},
      {"id":"call_b","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"go.mod\"}"}}
    ]}}],
  "usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`
	converted, err := chatToResponses([]byte(completion), false)
	if err != nil {
		t.Fatalf("conversion failed: %v", err)
	}
	response := translatedBody(t, converted)
	if response["status"] != "completed" {
		t.Fatalf("response envelope is wrong: %+v", response)
	}
	output, _ := response["output"].([]any)
	if len(output) != 3 {
		t.Fatalf("expected a message and two calls, got %d: %v", len(output), output)
	}
	message, _ := output[0].(map[string]any)
	content, _ := message["content"].([]any)
	part, _ := content[0].(map[string]any)
	if part["text"] != "Working on it" {
		t.Fatalf("buffered message text was dropped: %+v", message)
	}
	first, _ := output[1].(map[string]any)
	second, _ := output[2].(map[string]any)
	if first["call_id"] != "call_a" || first["name"] != "sh_cmd" || first["arguments"] != `{"cmd":"ls"}` {
		t.Fatalf("first indexless tool call is wrong: %+v", first)
	}
	if second["call_id"] != "call_b" || second["name"] != "read_file" || second["arguments"] != `{"path":"go.mod"}` {
		t.Fatalf("indexless tool calls collapsed onto one slot: %+v", second)
	}
	usage, _ := response["usage"].(map[string]any)
	if usage["total_tokens"] != json.Number("15") {
		t.Fatalf("usage was lost: %+v", usage)
	}
}

func TestEndpointMissing404Detection(t *testing.T) {
	if !endpointMissing404(http.StatusNotFound, []byte("<html><title>404 Not Found</title></html>")) {
		t.Fatal("HTML 404 page must count as endpoint missing")
	}
	if !endpointMissing404(http.StatusNotFound, []byte(`{"error":"Requested endpoint not found"}`)) {
		t.Fatal("JSON 404 must count as endpoint missing")
	}
	if !endpointMissing404(http.StatusNotFound, []byte(`{"error":{"message":"The requested route does not exist"}}`)) {
		t.Fatal("route message must count as endpoint missing")
	}
	if endpointMissing404(http.StatusNotFound, nil) {
		t.Fatal("empty body must not count as endpoint missing")
	}
	if endpointMissing404(http.StatusBadGateway, []byte("not found")) {
		t.Fatal("non-404 status must not count")
	}
}

// pathCounter records how many requests each upstream path received.
type pathCounter struct {
	mu     sync.Mutex
	hits   map[string]int
	bodies map[string][]byte
}

func (counter *pathCounter) hit(writer http.ResponseWriter, request *http.Request, body []byte) {
	counter.mu.Lock()
	defer counter.mu.Unlock()
	if counter.hits == nil {
		counter.hits = make(map[string]int)
		counter.bodies = make(map[string][]byte)
	}
	counter.hits[request.URL.Path]++
	counter.bodies[request.URL.Path] = append([]byte(nil), body...)
}

func (counter *pathCounter) count(path string) int {
	counter.mu.Lock()
	defer counter.mu.Unlock()
	return counter.hits[path]
}

func (counter *pathCounter) captured(path string) []byte {
	counter.mu.Lock()
	defer counter.mu.Unlock()
	return counter.bodies[path]
}

// chatOnlyUpstream serves /v1/responses with a plain 404 and the chat
// completions path with an OpenAI-style stream.
func chatOnlyUpstream(t *testing.T, chatPath string) (*Server, *pathCounter) {
	t.Helper()
	counter := &pathCounter{}
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, err := readRequestBody(request, absoluteMaxRequestBytes)
		if err != nil {
			t.Errorf("upstream could not read the request: %v", err)
			return
		}
		counter.hit(writer, request, body)
		if request.URL.Path == "/v1/responses" {
			writer.WriteHeader(http.StatusNotFound)
			_, _ = writer.Write([]byte("<html><head><title>404 Not Found</title></head><body>Not Found</body></html>"))
			return
		}
		writer.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		if chatPath == "/chat-completion" {
			_, _ = writer.Write([]byte(chatStreamToolCall))
		} else {
			_, _ = writer.Write([]byte(chatStreamText))
		}
	}))
	t.Cleanup(upstream.Close)
	parsed, _ := url.Parse(upstream.URL)
	server := NewServer("127.0.0.1:0", Dependencies{
		Routes: fixedRoute{route: relayapp.Route{
			ProviderID: "chat-only", BaseURL: parsed, AuthMode: "passthrough",
			Format: "auto", ChatPath: chatPath,
		}},
		Credentials: &credentialSource{values: []string{""}},
	})
	return server, counter
}

func responsesRequest(stream bool) *http.Request {
	request := httptest.NewRequest(http.MethodPost, "http://relay/v1/responses", strings.NewReader(`{"model":"gpt-test","input":"hi","stream":`+map[bool]string{true: "true", false: "false"}[stream]+`}`))
	request.Header.Set("Content-Type", "application/json")
	return request
}

func TestAutoFormatFallsBackToChatCompletionsOnEndpoint404(t *testing.T) {
	server, counter := chatOnlyUpstream(t, "/chat-completion")

	first := httptest.NewRecorder()
	server.ServeHTTP(first, responsesRequest(true))
	if first.Code != http.StatusOK || !strings.Contains(first.Header().Get("Content-Type"), "event-stream") {
		t.Fatalf("first request failed: status=%d type=%q body=%s", first.Code, first.Header().Get("Content-Type"), first.Body.String())
	}
	types := sseEventTypes(first.Body.Bytes())
	if !strings.Contains(strings.Join(types, " "), "response.completed") {
		t.Fatalf("first response never completed: %v", types)
	}
	blocks := strings.Split(strings.ReplaceAll(first.Body.String(), "\r\n", "\n"), "\n\n")
	completed := false
	for _, block := range blocks {
		var payload map[string]any
		if json.Unmarshal(sseData([]byte(block)), &payload) != nil || payload["type"] != "response.completed" {
			continue
		}
		completed = true
		output := payload["response"].(map[string]any)["output"].([]any)
		call := output[0].(map[string]any)
		if call["type"] != "function_call" || call["name"] != "sh_cmd" || call["arguments"] != "{\"cmd\":\"ls\"}" {
			t.Fatalf("tool call survived the round trip incorrectly: %+v", call)
		}
	}
	if !completed {
		t.Fatal("first response misses the terminal event")
	}
	if counter.count("/v1/responses") != 1 {
		t.Fatalf("expected exactly one responses probe, saw %d", counter.count("/v1/responses"))
	}
	if counter.count("/chat-completion") != 1 {
		t.Fatalf("expected one chat completion request, saw %d", counter.count("/chat-completion"))
	}
	chat := translatedBody(t, counter.captured("/chat-completion"))
	if chat["stream"] != true {
		t.Fatal("chat upstream must receive a streaming request")
	}

	second := httptest.NewRecorder()
	server.ServeHTTP(second, responsesRequest(true))
	if second.Code != http.StatusOK {
		t.Fatalf("memoized second request failed: status=%d body=%s", second.Code, second.Body.String())
	}
	if counter.count("/v1/responses") != 1 {
		t.Fatalf("memoized request probed again: %d responses hits", counter.count("/v1/responses"))
	}
	if counter.count("/chat-completion") != 2 {
		t.Fatalf("expected two chat completion requests, saw %d", counter.count("/chat-completion"))
	}
}

// The probe rewrites the upstream path mid-flight. The caller opened
// /v1/responses and its stream headers are already committed, so if that rewrite
// reaches the caller's own request the terminal frame is written in chat dialect:
// a Responses client gets data:{...} and [DONE] instead of response.failed, and
// hangs waiting for an event that never arrives.
func TestAFailureAfterTheChatProbeStillEndsInTheClientDialect(t *testing.T) {
	counter := &pathCounter{}
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, err := readRequestBody(request, absoluteMaxRequestBytes)
		if err != nil {
			t.Errorf("upstream could not read the request: %v", err)
			return
		}
		counter.hit(writer, request, body)
		if request.URL.Path == "/v1/responses" {
			writer.WriteHeader(http.StatusNotFound)
			_, _ = writer.Write([]byte("<html><head><title>404 Not Found</title></head><body>Not Found</body></html>"))
			return
		}
		// The translated chat request fails for good, after the client's stream was
		// already committed on the Responses endpoint. The message names nothing the
		// 400 ladder reacts to, so the attempt is terminal.
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusBadRequest)
		_, _ = writer.Write([]byte(`{"error":{"message":"the model refused this request"}}`))
	}))
	t.Cleanup(upstream.Close)
	parsed, _ := url.Parse(upstream.URL)
	server := NewServer("127.0.0.1:0", Dependencies{
		Routes: fixedRoute{route: relayapp.Route{
			ProviderID: "chat-only", BaseURL: parsed, AuthMode: "passthrough",
			Format: "auto", ChatPath: "/v1/chat/completions",
		}},
		Credentials: &credentialSource{values: []string{""}},
		Config:      Config{PermanentAttempts: 1},
	})

	response := httptest.NewRecorder()
	server.ServeHTTP(response, responsesRequest(true))
	body := response.Body.String()
	if !strings.Contains(body, "event: response.failed") {
		t.Fatalf("a Responses caller must be failed in its own dialect: %s", body)
	}
	if strings.Contains(body, "[DONE]") {
		t.Fatalf("the chat dialect terminal frame leaked to a Responses caller: %s", body)
	}
}

func TestJSONClientReceivesResponsesObject(t *testing.T) {
	server, counter := chatOnlyUpstream(t, "/chat-completion")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, responsesRequest(false))
	if response.Code != http.StatusOK {
		t.Fatalf("JSON request failed: status=%d body=%s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Header().Get("Content-Type"), "json") {
		t.Fatalf("JSON client must receive JSON, got %q", response.Header().Get("Content-Type"))
	}
	var payload map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("response is not valid JSON: %v", err)
	}
	if payload["status"] != "completed" {
		t.Fatalf("response status is wrong: %v", payload["status"])
	}
	output := payload["output"].([]any)
	call := output[0].(map[string]any)
	if call["type"] != "function_call" || call["arguments"] != "{\"cmd\":\"ls\"}" {
		t.Fatalf("function call output is wrong: %+v", call)
	}
	if counter.count("/v1/responses") != 1 {
		t.Fatalf("expected exactly one probe for the JSON client, saw %d", counter.count("/v1/responses"))
	}
}

func TestExplicitChatFormatNeverProbes(t *testing.T) {
	counter := &pathCounter{}
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, err := readRequestBody(request, absoluteMaxRequestBytes)
		if err != nil {
			t.Errorf("upstream could not read the request: %v", err)
			return
		}
		counter.hit(writer, request, body)
		writer.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		_, _ = writer.Write([]byte(chatStreamText))
	}))
	defer upstream.Close()
	parsed, _ := url.Parse(upstream.URL)
	server := NewServer("127.0.0.1:0", Dependencies{
		Routes: fixedRoute{route: relayapp.Route{
			ProviderID: "chat-only", BaseURL: parsed, AuthMode: "passthrough",
			Format: "chat", ChatPath: "/chat-completion",
		}},
		Credentials: &credentialSource{values: []string{""}},
	})
	response := httptest.NewRecorder()
	server.ServeHTTP(response, responsesRequest(true))
	if response.Code != http.StatusOK {
		t.Fatalf("explicit chat request failed: status=%d body=%s", response.Code, response.Body.String())
	}
	if counter.count("/v1/responses") != 0 {
		t.Fatalf("explicit chat format must not probe /v1/responses, saw %d hits", counter.count("/v1/responses"))
	}
	if counter.count("/chat-completion") != 1 {
		t.Fatalf("expected one chat completion request, saw %d", counter.count("/chat-completion"))
	}
	if !strings.Contains(strings.Join(sseEventTypes(response.Body.Bytes()), " "), "response.completed") {
		t.Fatalf("explicit chat stream never completed: %s", response.Body.String())
	}
}

func TestModelNotFound404DoesNotTriggerTranslation(t *testing.T) {
	counter := &pathCounter{}
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, err := readRequestBody(request, absoluteMaxRequestBytes)
		if err != nil {
			t.Errorf("upstream could not read the request: %v", err)
			return
		}
		counter.hit(writer, request, body)
		writer.WriteHeader(http.StatusNotFound)
		_, _ = writer.Write([]byte(`{"error":{"message":"The model gpt-test does not exist"}}`))
	}))
	defer upstream.Close()
	parsed, _ := url.Parse(upstream.URL)
	server := NewServer("127.0.0.1:0", Dependencies{
		Routes: fixedRoute{route: relayapp.Route{
			ProviderID: "model-404", BaseURL: parsed, AuthMode: "passthrough", Format: "auto",
		}},
		Credentials: &credentialSource{values: []string{""}},
	})
	response := httptest.NewRecorder()
	server.ServeHTTP(response, responsesRequest(false))
	if response.Code != http.StatusNotFound {
		t.Fatalf("model 404 must surface as 404, got %d", response.Code)
	}
	if counter.count("/chat-completion")+counter.count("/v1/chat/completions") != 0 {
		t.Fatalf("model 404 must not trigger chat translation")
	}
	if counter.count("/v1/responses") != 1 {
		t.Fatalf("a model the provider does not host must not be retried, saw %d hits", counter.count("/v1/responses"))
	}
}

func TestChatPath404ForgetsTheProbe(t *testing.T) {
	counter := &pathCounter{}
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, err := readRequestBody(request, absoluteMaxRequestBytes)
		if err != nil {
			t.Errorf("upstream could not read the request: %v", err)
			return
		}
		counter.hit(writer, request, body)
		writer.WriteHeader(http.StatusNotFound)
		_, _ = writer.Write([]byte("<html><title>404 Not Found</title></html>"))
	}))
	defer upstream.Close()
	parsed, _ := url.Parse(upstream.URL)
	server := NewServer("127.0.0.1:0", Dependencies{
		Routes: fixedRoute{route: relayapp.Route{
			ProviderID: "both-404", BaseURL: parsed, AuthMode: "passthrough",
			Format: "auto", ChatPath: "/chat-completion",
		}},
		Credentials: &credentialSource{values: []string{""}},
	})
	for round := 0; round < 2; round++ {
		response := httptest.NewRecorder()
		server.ServeHTTP(response, responsesRequest(false))
		if response.Code != http.StatusNotFound {
			t.Fatalf("round %d: expected 404, got %d", round, response.Code)
		}
	}
	if counter.count("/v1/responses") != 2 {
		t.Fatalf("a broken chat path must re-probe on the next request, saw %d probes", counter.count("/v1/responses"))
	}
	if counter.count("/chat-completion") != 2 {
		t.Fatalf("expected two chat attempts, saw %d", counter.count("/chat-completion"))
	}
}

// TestDefaultChatPathFallsBackToAievaStyle verifies that a 404 on the default
// /v1/chat/completions does not kill the translation: gateways of the
// aieva/nele.ai family serve chat at /chat-completion instead.
func TestDefaultChatPathFallsBackToAievaStyle(t *testing.T) {
	counter := &pathCounter{}
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, err := readRequestBody(request, absoluteMaxRequestBytes)
		if err != nil {
			t.Errorf("upstream could not read the request: %v", err)
			return
		}
		counter.hit(writer, request, body)
		if request.URL.Path != "/chat-completion" {
			writer.WriteHeader(http.StatusNotFound)
			_, _ = writer.Write([]byte("<html><head><title>404 Not Found</title></head><body>Not Found</body></html>"))
			return
		}
		writer.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		_, _ = writer.Write([]byte(chatStreamToolCall))
	}))
	defer upstream.Close()
	parsed, _ := url.Parse(upstream.URL)
	server := NewServer("127.0.0.1:0", Dependencies{
		Routes: fixedRoute{route: relayapp.Route{
			ProviderID: "aieva-style", BaseURL: parsed, AuthMode: "passthrough",
			Format: "auto", ChatPath: "",
		}},
		Credentials: &credentialSource{values: []string{""}},
	})
	response := httptest.NewRecorder()
	server.ServeHTTP(response, responsesRequest(true))
	if response.Code != http.StatusOK {
		t.Fatalf("aieva-style fallback failed: status=%d body=%s", response.Code, response.Body.String())
	}
	events := strings.Join(sseEventTypes(response.Body.Bytes()), " ")
	if !strings.Contains(events, "response.completed") {
		t.Fatalf("response never completed: %v", events)
	}
	if counter.count("/v1/responses") != 1 {
		t.Fatalf("expected one endpoint probe, saw %d", counter.count("/v1/responses"))
	}
	if counter.count("/v1/chat/completions") != 1 {
		t.Fatalf("expected one default chat attempt before the fallback, saw %d", counter.count("/v1/chat/completions"))
	}
	if counter.count("/chat-completion") != 1 {
		t.Fatalf("expected one aieva-style chat attempt, saw %d", counter.count("/chat-completion"))
	}
}

// TestExplicitChatPathIsNeverOverridden verifies that a configured custom chat
// path is trusted: its 404 is surfaced, not retried against /chat-completion.
func TestExplicitChatPathIsNeverOverridden(t *testing.T) {
	counter := &pathCounter{}
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, err := readRequestBody(request, absoluteMaxRequestBytes)
		if err != nil {
			t.Errorf("upstream could not read the request: %v", err)
			return
		}
		counter.hit(writer, request, body)
		writer.WriteHeader(http.StatusNotFound)
		_, _ = writer.Write([]byte("<html><title>404 Not Found</title></html>"))
	}))
	defer upstream.Close()
	parsed, _ := url.Parse(upstream.URL)
	server := NewServer("127.0.0.1:0", Dependencies{
		Routes: fixedRoute{route: relayapp.Route{
			ProviderID: "explicit-path", BaseURL: parsed, AuthMode: "passthrough",
			Format: "auto", ChatPath: "/custom-chat",
		}},
		Credentials: &credentialSource{values: []string{""}},
	})
	response := httptest.NewRecorder()
	server.ServeHTTP(response, responsesRequest(false))
	if response.Code != http.StatusNotFound {
		t.Fatalf("custom chat path 404 must surface as 404, got %d", response.Code)
	}
	if counter.count("/chat-completion") != 0 {
		t.Fatalf("an explicit chat path must not be overridden by the fallback probe")
	}
	if counter.count("/custom-chat") != 1 {
		t.Fatalf("expected one request to the custom chat path, saw %d", counter.count("/custom-chat"))
	}
}

func TestDispatchTranslatesPinnedChatFormat(t *testing.T) {
	counter := &pathCounter{}
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, err := readRequestBody(request, absoluteMaxRequestBytes)
		if err != nil {
			t.Errorf("upstream could not read the request: %v", err)
			return
		}
		counter.hit(writer, request, body)
		writer.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		_, _ = writer.Write([]byte(chatStreamText))
	}))
	defer upstream.Close()
	parsed, _ := url.Parse(upstream.URL)
	server := NewServer("127.0.0.1:0", Dependencies{
		Routes: fixedRoute{route: relayapp.Route{
			ProviderID: "chat-only", BaseURL: parsed, AuthMode: "passthrough",
			Format: "chat", ChatPath: "/v1/chat/completions",
		}},
		Credentials: &credentialSource{values: []string{""}},
	})
	result, err := server.Dispatch(context.Background(), relayapp.DispatchRequest{
		ProviderID: "chat-only", UpstreamModel: "gpt-test", PublicModel: "gpt-test",
		Method: http.MethodPost, Path: "/v1/responses",
		Headers: http.Header{"Content-Type": []string{"application/json"}},
		Body:    []byte(`{"model":"gpt-test","input":"hi","stream":true}`),
	})
	if err != nil {
		t.Fatalf("dispatch failed: %v", err)
	}
	if result.Status != http.StatusOK {
		t.Fatalf("dispatch status is wrong: %d", result.Status)
	}
	if !strings.Contains(strings.ToLower(result.Headers.Get("Content-Type")), "event-stream") {
		t.Fatalf("streaming client must get SSE, got %q", result.Headers.Get("Content-Type"))
	}
	if !strings.Contains(strings.Join(sseEventTypes(result.Body), " "), "response.completed") {
		t.Fatalf("dispatched stream never completed: %s", result.Body)
	}
	if counter.count("/v1/responses") != 0 {
		t.Fatalf("pinned chat format must not probe, saw %d", counter.count("/v1/responses"))
	}
	if counter.count("/v1/chat/completions") != 1 {
		t.Fatalf("expected one chat completion request, saw %d", counter.count("/v1/chat/completions"))
	}
}

// A chat provider says three different things in `finish_reason` and `refusal`,
// and the Responses envelope has a place for each. All three used to be dropped,
// so a client reading the translated answer could not tell a refusal, a filtered
// turn, or a truncated one from an ordinary completion — the first two arrived as
// `"status":"completed"` with nothing in `output`, which reads as a provider that
// answered with silence, and the third as a half sentence stamped complete.
func TestAChatProviderThatDidNotFinishSaysSoAfterTranslation(t *testing.T) {
	const usage = `,"usage":{"prompt_tokens":11,"completion_tokens":7,"total_tokens":18}`
	for _, testCase := range []struct {
		name       string
		delta      string
		reason     string
		status     string
		incomplete string
		text       string
		event      string
	}{
		{
			name: "a refusal is the answer", delta: `"refusal":"I will not do that."`,
			reason: "stop", status: "completed", text: "I will not do that.",
			event: "response.completed",
		},
		{
			name: "a filtered turn is not a completed one", delta: `"content":""`,
			reason: "content_filter", status: "incomplete", incomplete: "content_filter",
			event: "response.incomplete",
		},
		{
			name: "a truncated turn keeps its text and admits the cut", delta: `"content":"half an answ"`,
			reason: "length", status: "incomplete", incomplete: "max_output_tokens",
			text: "half an answ", event: "response.incomplete",
		},
		{
			name: "an ordinary answer is untouched", delta: `"content":"all done"`,
			reason: "stop", status: "completed", text: "all done", event: "response.completed",
		},
		{
			name: "a tool call is an ordinary answer too", delta: `"content":"calling"`,
			reason: "tool_calls", status: "completed", text: "calling", event: "response.completed",
		},
		{
			name: "an unfamiliar reason is not guessed at", delta: `"content":"who knows"`,
			reason: "something_new", status: "completed", text: "who knows", event: "response.completed",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			stream := `data: {"id":"chatcmpl-1","model":"m","choices":[{"index":0,"delta":{"role":"assistant",` +
				testCase.delta + `},"finish_reason":"` + testCase.reason + `"}]` + usage + "}\n\ndata: [DONE]\n\n"
			converted, err := chatToResponses([]byte(stream), false)
			if err != nil {
				t.Fatalf("conversion failed: %v", err)
			}
			response := translatedBody(t, converted)
			if response["status"] != testCase.status {
				t.Fatalf("status is %v, want %q: %s", response["status"], testCase.status, converted)
			}
			details, _ := response["incomplete_details"].(map[string]any)
			if testCase.incomplete == "" {
				if response["incomplete_details"] != nil {
					t.Fatalf("a finished turn claims it was cut short: %v", response["incomplete_details"])
				}
			} else if details["reason"] != testCase.incomplete {
				t.Fatalf("incomplete reason is %v, want %q", details["reason"], testCase.incomplete)
			}
			output, _ := response["output"].([]any)
			text := ""
			if len(output) > 0 {
				item, _ := output[0].(map[string]any)
				text = outputText(item)
			}
			if text != testCase.text {
				t.Fatalf("the client reads %q, want %q: %s", text, testCase.text, converted)
			}
			// The same conclusion has to be reachable from the event type alone: a
			// client that switches on it must not be told the turn completed.
			streamed, err := chatToResponses([]byte(stream), true)
			if err != nil {
				t.Fatalf("stream conversion failed: %v", err)
			}
			types := sseEventTypes(streamed)
			if len(types) == 0 || types[len(types)-1] != testCase.event {
				t.Fatalf("the stream closes with %v, want %q", types, testCase.event)
			}
		})
	}
}
