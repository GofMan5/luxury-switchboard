package relayhttp

import (
	"bytes"
	"encoding/json"
	"testing"
)

// A chat client that lands on a Responses-only provider must still be
// served: its Chat Completions request is translated, field by field, into
// the Responses dialect the upstream speaks. This is the request half of
// the bridge TestResponsesRequestTranslatesToChatCompletions covers from
// the other side — nothing a chat client can express is silently thrown
// away on the way up.
func TestAChatRequestBecomesAResponsesRequest(t *testing.T) {
	request := `{
		"model": "gpt-test",
		"messages": [
			{"role": "system", "content": "Be brief"},
			{"role": "user", "content": "run git status"},
			{"role": "assistant", "content": "", "tool_calls": [{"id": "call_1", "type": "function", "function": {"name": "sh_cmd", "arguments": "{\"cmd\":\"ls\"}"}}]},
			{"role": "tool", "tool_call_id": "call_1", "content": "files: none"}
		],
		"tools": [{"type": "function", "function": {"name": "sh_cmd", "description": "Run a shell command", "parameters": {"type": "object", "properties": {"cmd": {"type": "string"}}}}}],
		"tool_choice": "auto",
		"max_tokens": 64,
		"temperature": 0.5,
		"stream": true,
		"stream_options": {"include_usage": true}
	}`
	converted, err := chatRequestToResponses([]byte(request), false)
	if err != nil {
		t.Fatalf("translation failed: %v", err)
	}
	responses := translatedBody(t, converted)

	if responses["instructions"] != "Be brief" {
		t.Errorf("instructions = %v, want Be brief", responses["instructions"])
	}
	if responses["max_output_tokens"] != json.Number("64") {
		t.Errorf("max_output_tokens = %v, want 64", responses["max_output_tokens"])
	}
	if _, ok := responses["max_tokens"]; ok {
		t.Errorf("max_tokens survived translation: %v", converted)
	}
	if _, ok := responses["stream_options"]; ok {
		t.Errorf("stream_options survived translation: %v", converted)
	}
	if responses["temperature"] != json.Number("0.5") {
		t.Errorf("temperature = %v, want 0.5", responses["temperature"])
	}
	if responses["tool_choice"] != "auto" {
		t.Errorf("tool_choice = %v, want auto", responses["tool_choice"])
	}
	if responses["stream"] != true {
		t.Errorf("stream = %v, want true", responses["stream"])
	}

	input, ok := responses["input"].([]any)
	if !ok || len(input) != 3 {
		t.Fatalf("input = %v, want 3 items", responses["input"])
	}
	first, ok := input[0].(map[string]any)
	if !ok || first["type"] != "message" || first["role"] != "user" {
		t.Fatalf("first input item = %v, want a user message", input[0])
	}
	parts, ok := first["content"].([]any)
	if !ok || len(parts) != 1 {
		t.Fatalf("user message content = %v, want one part", first["content"])
	}
	part, ok := parts[0].(map[string]any)
	if !ok || part["type"] != "input_text" || part["text"] != "run git status" {
		t.Errorf("user content part = %v, want input_text with the prompt", parts[0])
	}

	second, ok := input[1].(map[string]any)
	if !ok || second["type"] != "function_call" {
		t.Fatalf("second input item = %v, want a function_call", input[1])
	}
	if second["call_id"] != "call_1" || second["name"] != "sh_cmd" || second["arguments"] != `{"cmd":"ls"}` {
		t.Errorf("function_call = %v, want the tool call replayed verbatim", second)
	}

	third, ok := input[2].(map[string]any)
	if !ok || third["type"] != "function_call_output" {
		t.Fatalf("third input item = %v, want a function_call_output", input[2])
	}
	if third["call_id"] != "call_1" || third["output"] != "files: none" {
		t.Errorf("function_call_output = %v, want the tool result", third)
	}

	tools, ok := responses["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("tools = %v, want one tool", responses["tools"])
	}
	tool, ok := tools[0].(map[string]any)
	if !ok || tool["type"] != "function" || tool["name"] != "sh_cmd" {
		t.Fatalf("translated tool = %v, want a flat function tool", tools[0])
	}
	if tool["description"] != "Run a shell command" {
		t.Errorf("tool description = %v, want carried over", tool["description"])
	}
	parameters, ok := tool["parameters"].(map[string]any)
	if !ok || parameters["type"] != "object" {
		t.Errorf("tool parameters = %v, want carried over", tool["parameters"])
	}
}

// The ChatGPT backend behind the codex login answers stateless, streaming
// calls only. A translated chat request must therefore arrive as
// stream:true, store:false, with default instructions and without the
// stateful continuation fields a chat client cannot send anyway but a
// responses-dialect client might.
func TestAStatelessCallCarriesTheCodexWireContract(t *testing.T) {
	request := `{
		"model": "gpt-test",
		"messages": [{"role": "user", "content": "hi"}],
		"max_tokens": 64,
		"stream": false
	}`
	converted, err := chatRequestToResponses([]byte(request), true)
	if err != nil {
		t.Fatalf("translation failed: %v", err)
	}
	responses := translatedBody(t, converted)

	if responses["stream"] != true {
		t.Errorf("stream = %v, want forced true for a stateless call", responses["stream"])
	}
	if responses["store"] != false {
		t.Errorf("store = %v, want forced false", responses["store"])
	}
	if _, ok := responses["instructions"]; !ok {
		t.Errorf("instructions missing, want the explicit empty default")
	} else if responses["instructions"] != "" {
		t.Errorf("instructions = %v, want empty default", responses["instructions"])
	}

	// The same contract applies to a responses-dialect client that skipped
	// translation entirely: the body is shaped in place, nothing else moves.
	passthrough := `{
		"model": "gpt-test",
		"instructions": "stay terse",
		"input": "hi",
		"stream": false,
		"store": true,
		"previous_response_id": "resp_123",
		"prompt_cache_key": "session-1",
		"prompt_cache_retention": "24h",
		"safety_identifier": "user-77",
		"temperature": 0.25
	}`
	shaped, err := shapeStatelessResponsesRequest([]byte(passthrough))
	if err != nil {
		t.Fatalf("shaping failed: %v", err)
	}
	shapedBody := translatedBody(t, shaped)

	if shapedBody["stream"] != true {
		t.Errorf("shaped stream = %v, want true", shapedBody["stream"])
	}
	if shapedBody["store"] != false {
		t.Errorf("shaped store = %v, want false", shapedBody["store"])
	}
	if shapedBody["instructions"] != "stay terse" {
		t.Errorf("shaped instructions = %v, want the client's own value untouched", shapedBody["instructions"])
	}
	for _, field := range []string{"previous_response_id", "prompt_cache_key", "prompt_cache_retention", "safety_identifier"} {
		if _, ok := shapedBody[field]; ok {
			t.Errorf("%s survived shaping: %v", field, shaped)
		}
	}
	if shapedBody["temperature"] != json.Number("0.25") {
		t.Errorf("temperature = %v, want carried over with its own spelling", shapedBody["temperature"])
	}
}

// A Responses provider answering a chat client has to speak chat on the way
// back down. The Responses envelope says three things a chat client reads
// elsewhere: an ordinary answer is output_text, a refusal is text on a
// refusal part, and a turn that did not finish says so in status and
// incomplete_details. Each has a chat-dialect home — message content,
// message refusal, finish_reason — and the translation has to carry all of
// them in both the buffered and the streaming form.
func TestAResponsesProviderThatDidNotFinishSaysSoInChatDialect(t *testing.T) {
	const usage = `,"usage":{"input_tokens":11,"output_tokens":7,"total_tokens":18}`
	for _, testCase := range []struct {
		name    string
		body    string
		finish  string
		content string
		refusal string
		tool    bool
	}{
		{
			name: "an ordinary answer is untouched",
			body: "data: {\"type\":\"response.output_text.delta\",\"delta\":\"all done\"}\n\n" +
				"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"created_at\":1710000000,\"model\":\"m\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"all done\"}]}]" + usage + "}}\n\n",
			finish:  "stop",
			content: "all done",
		},
		{
			name:    "a refusal is the answer",
			body:    "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_2\",\"created_at\":1710000000,\"model\":\"m\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"refusal\",\"refusal\":\"I will not do that.\"}]}]" + usage + "}}\n\n",
			finish:  "stop",
			content: "I will not do that.",
			refusal: "I will not do that.",
		},
		{
			name:   "a filtered turn is not a completed one",
			body:   "data: {\"type\":\"response.incomplete\",\"response\":{\"id\":\"resp_3\",\"created_at\":1710000000,\"model\":\"m\",\"status\":\"incomplete\",\"incomplete_details\":{\"reason\":\"content_filter\"},\"output\":[]" + usage + "}}\n\n",
			finish: "content_filter",
		},
		{
			name: "a truncated turn keeps its text and admits the cut",
			body: "data: {\"type\":\"response.output_text.delta\",\"delta\":\"half an answ\"}\n\n" +
				"data: {\"type\":\"response.incomplete\",\"response\":{\"id\":\"resp_4\",\"created_at\":1710000000,\"model\":\"m\",\"status\":\"incomplete\",\"incomplete_details\":{\"reason\":\"max_output_tokens\"},\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"half an answ\"}]}]" + usage + "}}\n\n",
			finish:  "length",
			content: "half an answ",
		},
		{
			name: "a tool call is an ordinary answer too",
			body: "data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"function_call\",\"call_id\":\"call_1\",\"name\":\"sh_cmd\",\"arguments\":\"{\\\"cmd\\\":\\\"ls\\\"}\"}}\n\n" +
				"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_5\",\"created_at\":1710000000,\"model\":\"m\",\"status\":\"completed\",\"output\":[{\"type\":\"function_call\",\"call_id\":\"call_1\",\"name\":\"sh_cmd\",\"arguments\":\"{\\\"cmd\\\":\\\"ls\\\"}\"}]" + usage + "}}\n\n",
			finish: "tool_calls",
			tool:   true,
		},
		{
			name: "an unfamiliar reason is not guessed at",
			body: "data: {\"type\":\"response.output_text.delta\",\"delta\":\"who knows\"}\n\n" +
				"data: {\"type\":\"response.incomplete\",\"response\":{\"id\":\"resp_6\",\"created_at\":1710000000,\"model\":\"m\",\"status\":\"incomplete\",\"incomplete_details\":{\"reason\":\"something_new\"},\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"who knows\"}]}]" + usage + "}}\n\n",
			finish:  "stop",
			content: "who knows",
		},
		{
			name:    "an answer that never streamed is still an answer",
			body:    "{\"id\":\"resp_7\",\"object\":\"response\",\"created_at\":1710000000,\"model\":\"m\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"plain answer\"}]}]" + usage + "}",
			finish:  "stop",
			content: "plain answer",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			converted, err := responsesAnswerToChat([]byte(testCase.body), false)
			if err != nil {
				t.Fatalf("conversion failed: %v", err)
			}
			completion := translatedBody(t, converted)
			if completion["object"] != "chat.completion" {
				t.Fatalf("object = %v, want chat.completion: %s", completion["object"], converted)
			}
			if completion["created"] != json.Number("1710000000") {
				t.Fatalf("created = %v, want the provider's own clock: %s", completion["created"], converted)
			}
			if usageMap, ok := completion["usage"].(map[string]any); !ok {
				t.Fatalf("usage is missing: %s", converted)
			} else if usageMap["prompt_tokens"] != json.Number("11") || usageMap["completion_tokens"] != json.Number("7") || usageMap["total_tokens"] != json.Number("18") {
				t.Fatalf("usage = %v, want the meter mapped onto chat: %s", completion["usage"], converted)
			}
			choices, _ := completion["choices"].([]any)
			if len(choices) != 1 {
				t.Fatalf("choices = %v, want one: %s", choices, converted)
			}
			choice, _ := choices[0].(map[string]any)
			if choice["finish_reason"] != testCase.finish {
				t.Fatalf("finish_reason = %v, want %q: %s", choice["finish_reason"], testCase.finish, converted)
			}
			message, _ := choice["message"].(map[string]any)
			if message["role"] != "assistant" {
				t.Fatalf("role = %v, want assistant: %s", message["role"], converted)
			}
			if message["content"] != testCase.content {
				t.Fatalf("content = %v, want %q: %s", message["content"], testCase.content, converted)
			}
			if testCase.refusal == "" {
				if _, present := message["refusal"]; present {
					t.Fatalf("refusal invented: %s", converted)
				}
			} else if message["refusal"] != testCase.refusal {
				t.Fatalf("refusal = %v, want %q: %s", message["refusal"], testCase.refusal, converted)
			}
			if testCase.tool {
				calls, _ := message["tool_calls"].([]any)
				if len(calls) != 1 {
					t.Fatalf("tool_calls = %v, want the call: %s", message["tool_calls"], converted)
				}
				call, _ := calls[0].(map[string]any)
				if call["id"] != "call_1" || call["type"] != "function" {
					t.Fatalf("call = %v, want the provider's own call: %s", call, converted)
				}
				function, _ := call["function"].(map[string]any)
				if function["name"] != "sh_cmd" || function["arguments"] != `{"cmd":"ls"}` {
					t.Fatalf("function = %v, want the call verbatim: %s", function, converted)
				}
			}

			streamed, err := responsesAnswerToChat([]byte(testCase.body), true)
			if err != nil {
				t.Fatalf("stream conversion failed: %v", err)
			}
			var content, refusal, toolArguments, finish string
			sawUsage := false
			for _, chunk := range chatStreamChunks(t, streamed) {
				if chunk["object"] != "chat.completion.chunk" {
					t.Fatalf("chunk object = %v, want chat.completion.chunk: %s", chunk["object"], streamed)
				}
				if usage, ok := chunk["usage"].(map[string]any); ok {
					sawUsage = true
					if usage["prompt_tokens"] != json.Number("11") || usage["completion_tokens"] != json.Number("7") {
						t.Fatalf("usage = %v, want the meter mapped onto chat: %s", chunk["usage"], streamed)
					}
				}
				choices, _ := chunk["choices"].([]any)
				if len(choices) == 0 {
					continue
				}
				choice, _ := choices[0].(map[string]any)
				if reason, _ := choice["finish_reason"].(string); reason != "" {
					finish = reason
				}
				delta, _ := choice["delta"].(map[string]any)
				if text, _ := delta["content"].(string); text != "" {
					content += text
				}
				if text, _ := delta["refusal"].(string); text != "" {
					refusal += text
				}
				if calls, _ := delta["tool_calls"].([]any); len(calls) > 0 {
					call, _ := calls[0].(map[string]any)
					function, _ := call["function"].(map[string]any)
					toolArguments, _ = function["arguments"].(string)
				}
			}
			if finish != testCase.finish {
				t.Fatalf("stream finish_reason = %q, want %q: %s", finish, testCase.finish, streamed)
			}
			if content != testCase.content {
				t.Fatalf("stream content = %q, want %q: %s", content, testCase.content, streamed)
			}
			if refusal != testCase.refusal {
				t.Fatalf("stream refusal = %q, want %q: %s", refusal, testCase.refusal, streamed)
			}
			if testCase.tool && toolArguments != `{"cmd":"ls"}` {
				t.Fatalf("stream tool arguments = %q, want the provider's call: %s", toolArguments, streamed)
			}
			if !sawUsage {
				t.Fatalf("the stream carries no usage chunk: %s", streamed)
			}
		})
	}
}

// A failed response reaches the conversion with HTTP 200 — the retry ladder
// delivers it, the terminal says failed — and a chat client must hear the
// failure in the provider's own words, never an empty completed answer.
func TestAResponseThatFailedIsAnErrorInChatDialect(t *testing.T) {
	body := "data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n" +
		"data: {\"type\":\"response.failed\",\"response\":{\"id\":\"resp_8\",\"created_at\":1710000000,\"model\":\"m\",\"status\":\"failed\",\"error\":{\"message\":\"the model overloaded\"},\"output\":[]}}\n\n"

	converted, err := responsesAnswerToChat([]byte(body), false)
	if err != nil {
		t.Fatalf("conversion failed: %v", err)
	}
	failure := translatedBody(t, converted)
	if _, present := failure["choices"]; present {
		t.Fatalf("a failure is not a completed answer: %s", converted)
	}
	failureObject, _ := failure["error"].(map[string]any)
	if failureObject["message"] != "the model overloaded" {
		t.Fatalf("error message = %v, want the provider's words: %s", failureObject["message"], converted)
	}

	streamed, err := responsesAnswerToChat([]byte(body), true)
	if err != nil {
		t.Fatalf("stream conversion failed: %v", err)
	}
	var content string
	sawError := false
	for _, chunk := range chatStreamChunks(t, streamed) {
		if _, present := chunk["error"]; present {
			sawError = true
			continue
		}
		choices, _ := chunk["choices"].([]any)
		if len(choices) == 0 {
			continue
		}
		choice, _ := choices[0].(map[string]any)
		delta, _ := choice["delta"].(map[string]any)
		if text, _ := delta["content"].(string); text != "" {
			content += text
		}
	}
	if !sawError {
		t.Fatalf("the stream carries no error: %s", streamed)
	}
	if content != "partial" {
		t.Fatalf("the half-delivered text was dropped: %q: %s", content, streamed)
	}
}

// chatStreamChunks reads a chat-dialect SSE answer the way the client does:
// blocks split on blank lines, data: lines joined per event, [DONE] closes.
func chatStreamChunks(t *testing.T, body []byte) []map[string]any {
	t.Helper()
	normalized := bytes.ReplaceAll(body, []byte("\r\n"), []byte("\n"))
	var chunks []map[string]any
	sawDone := false
	for _, block := range bytes.Split(normalized, []byte("\n\n")) {
		data := sseData(block)
		if len(data) == 0 {
			continue
		}
		if string(bytes.TrimSpace(data)) == "[DONE]" {
			sawDone = true
			continue
		}
		var chunk map[string]any
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.UseNumber()
		if decoder.Decode(&chunk) != nil {
			continue
		}
		chunks = append(chunks, chunk)
	}
	if !sawDone {
		t.Fatalf("the stream does not close with [DONE]: %s", body)
	}
	return chunks
}

// A stateless responses client asks for a non-streaming answer, and the
// relay serves it by forcing the stream upstream and folding the buffered
// events back into the one JSON answer that client asked for. The folding
// is the terminal event's own response object — Responses sends the whole
// answer there — and a failed terminal is still one JSON answer, not a
// stream that never ended.
func TestAStreamedResponsesAnswerFoldsIntoOneJSONAnswer(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		body   string
		status string
		id     string
	}{
		{
			name: "an ordinary completed answer",
			body: "data: {\"type\":\"response.output_text.delta\",\"delta\":\"all \"}\n\n" +
				"data: {\"type\":\"response.output_text.delta\",\"delta\":\"done\"}\n\n" +
				"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_9\",\"object\":\"response\",\"created_at\":1710000000,\"model\":\"m\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"all done\"}]}],\"usage\":{\"input_tokens\":11,\"output_tokens\":7,\"total_tokens\":18}}}\n\n" +
				"data: [DONE]\n\n",
			status: "completed",
			id:     "resp_9",
		},
		{
			name: "a failed turn",
			body: "data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n" +
				"data: {\"type\":\"response.failed\",\"response\":{\"id\":\"resp_10\",\"object\":\"response\",\"created_at\":1710000000,\"model\":\"m\",\"status\":\"failed\",\"error\":{\"message\":\"the model overloaded\"},\"output\":[]}}\n\n" +
				"data: [DONE]\n\n",
			status: "failed",
			id:     "resp_10",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			folded, err := responsesSSEToJSON([]byte(testCase.body))
			if err != nil {
				t.Fatalf("folding failed: %v", err)
			}
			answer := translatedBody(t, folded)
			if answer["id"] != testCase.id {
				t.Fatalf("id = %v, want %q: %s", answer["id"], testCase.id, folded)
			}
			if answer["status"] != testCase.status {
				t.Fatalf("status = %v, want %q: %s", answer["status"], testCase.status, folded)
			}
			if testCase.status == "completed" {
				output, _ := answer["output"].([]any)
				item, _ := output[0].(map[string]any)
				parts, _ := item["content"].([]any)
				part, _ := parts[0].(map[string]any)
				if part["text"] != "all done" {
					t.Fatalf("text = %v, want the folded answer: %s", part["text"], folded)
				}
				usageMap, _ := answer["usage"].(map[string]any)
				if usageMap["input_tokens"] != json.Number("11") || usageMap["output_tokens"] != json.Number("7") {
					t.Fatalf("usage = %v, want the meter as it came: %s", answer["usage"], folded)
				}
			} else {
				failureObject, _ := answer["error"].(map[string]any)
				if failureObject["message"] != "the model overloaded" {
					t.Fatalf("error message = %v, want the provider's words: %s", failureObject["message"], folded)
				}
			}
		})
	}

	t.Run("an answer that never streamed is passed through", func(t *testing.T) {
		body := []byte("{\"id\":\"resp_11\",\"object\":\"response\",\"created_at\":1710000000,\"model\":\"m\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":3,\"output_tokens\":1,\"total_tokens\":4}}")
		folded, err := responsesSSEToJSON(body)
		if err != nil {
			t.Fatalf("folding failed: %v", err)
		}
		if !bytes.Equal(folded, body) {
			t.Fatalf("a whole-JSON answer should leave as it arrived: %s", folded)
		}
	})

	t.Run("a stream without a terminal is a failure, not silence", func(t *testing.T) {
		_, err := responsesSSEToJSON([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"dangling\"}\n\n"))
		if err == nil {
			t.Fatalf("a dangling stream folded into an answer")
		}
	})
}

// A forced stream is bookkept as a stream, so an upstream that answered
// with one whole JSON body has to be recognised as the finished answer it
// is — with its own terminal vocabulary and its own usage — instead of
// being retried to death as a stream that never closed.
func TestAWholeJSONResponseIsACompletedAnswerToTheStreamBookkeeping(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		body     string
		terminal string
		usage    json.Number
	}{
		{
			name:     "a completed answer",
			body:     "{\"id\":\"resp_12\",\"object\":\"response\",\"created_at\":1710000000,\"model\":\"m\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":11,\"output_tokens\":7,\"total_tokens\":18}}",
			terminal: "response.completed",
			usage:    json.Number("11"),
		},
		{
			name:     "an incomplete answer",
			body:     "{\"id\":\"resp_13\",\"object\":\"response\",\"created_at\":1710000000,\"model\":\"m\",\"status\":\"incomplete\",\"incomplete_details\":{\"reason\":\"max_output_tokens\"},\"output\":[],\"usage\":{\"input_tokens\":11,\"output_tokens\":7,\"total_tokens\":18}}",
			terminal: "response.incomplete",
			usage:    json.Number("11"),
		},
		{
			name:     "a failed answer with no meter",
			body:     "{\"id\":\"resp_14\",\"object\":\"response\",\"created_at\":1710000000,\"model\":\"m\",\"status\":\"failed\",\"error\":{\"message\":\"overloaded\"},\"output\":[]}",
			terminal: "response.failed",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			terminal, usage, complete := responsesCompletionBody([]byte(testCase.body))
			if !complete {
				t.Fatalf("a whole-JSON answer read as an unfinished stream: %s", testCase.body)
			}
			if terminal != testCase.terminal {
				t.Fatalf("terminal = %q, want %q", terminal, testCase.terminal)
			}
			if testCase.usage == "" {
				if usage != nil {
					t.Fatalf("usage = %v, want none", usage)
				}
			} else if usage["input_tokens"] != testCase.usage {
				t.Fatalf("input_tokens = %v, want %v", usage["input_tokens"], testCase.usage)
			}
		})
	}

	for _, testCase := range []struct {
		name string
		body string
	}{
		{name: "an event stream is not a JSON answer", body: "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_15\",\"status\":\"completed\"}}\n\n"},
		{name: "an object without a status", body: "{\"id\":\"resp_16\",\"object\":\"response\",\"model\":\"m\"}"},
		{name: "an object without an identity", body: "{\"status\":\"completed\",\"output\":[]}"},
		{name: "not JSON at all", body: "the model overloaded"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			_, _, complete := responsesCompletionBody([]byte(testCase.body))
			if complete {
				t.Fatalf("body read as a whole-JSON answer: %s", testCase.body)
			}
		})
	}
}
