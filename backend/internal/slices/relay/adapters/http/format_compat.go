package relayhttp

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	defaultChatCompletionsPath = "/v1/chat/completions"
	chatCompletionFallbackPath = "/chat-completion"
	responsesPath              = "/v1/responses"
	// Upstream picks the tool call slots, so the accumulator caps them instead of
	// growing a slice to whatever index arrives.
	maxChatToolCalls = 512
)

// chatPathFallback reports whether a 404 on the default chat path should be
// retried once against the aieva-style custom /chat-completion endpoint.
// A profile with an explicit chat path is trusted as-is and never probed.
func chatPathFallback(path, chatPath string) bool {
	return canonicalPath(path) == defaultChatCompletionsPath &&
		(chatPath == "" || chatPath == defaultChatCompletionsPath)
}

var errInvalidChatCompatibility = errors.New("invalid chat completions compatibility request")

// prepareChatCompletions rewrites a Responses API request into a Chat
// Completions request for providers that only speak the chat format
// (route.Format == "chat" or the automatic fallback already proved it).
// Chat providers stream by nature, so the translated request always asks for
// a stream; the relay buffers it and re-emits it in the shape the client asked
// for.
func prepareChatCompletions(method, path string, body []byte, contentType, chatPath string, enabled bool) (string, []byte, bool, error) {
	if !enabled || method != http.MethodPost || canonicalPath(path) != responsesPath || !strings.Contains(strings.ToLower(contentType), "json") {
		return path, body, false, nil
	}
	translated, err := responsesToChat(body)
	if err != nil {
		return path, body, true, errInvalidChatCompatibility
	}
	if chatPath == "" {
		chatPath = defaultChatCompletionsPath
	}
	return chatPath, translated, true, nil
}

func responsesToChat(body []byte) ([]byte, error) {
	var payload map[string]any
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if decoder.Decode(&payload) != nil {
		return nil, errInvalidChatCompatibility
	}
	messages := make([]any, 0, 8)
	if instructions, ok := payload["instructions"].(string); ok && instructions != "" {
		messages = append(messages, map[string]any{"role": "system", "content": instructions})
	}
	if input, ok := payload["input"]; ok && input != nil {
		switch value := input.(type) {
		case string:
			if value != "" {
				messages = append(messages, map[string]any{"role": "user", "content": value})
			}
		case []any:
			for _, item := range value {
				messages = append(messages, inputItemsToChat(item)...)
			}
		}
	}
	chat := map[string]any{"stream": true}
	for _, field := range []string{"model", "metadata", "temperature", "top_p", "user", "parallel_tool_calls"} {
		if value, exists := payload[field]; exists && value != nil {
			chat[field] = value
		}
	}
	if tools, ok := payload["tools"].([]any); ok && len(tools) > 0 {
		if converted := chatTools(tools); len(converted) > 0 {
			chat["tools"] = converted
		}
	}
	if choice, ok := payload["tool_choice"]; ok && choice != nil {
		chat["tool_choice"] = chatToolChoice(choice)
	}
	if tokens, ok := payload["max_output_tokens"]; ok && tokens != nil {
		chat["max_tokens"] = tokens
	}
	if reasoning, ok := payload["reasoning"].(map[string]any); ok {
		if effort, ok := reasoning["effort"].(string); ok && effort != "" {
			chat["reasoning_effort"] = effort
		}
	}
	chat["messages"] = messages
	return json.Marshal(chat)
}

// inputItemsToChat maps Responses input items onto chat messages. Unsupported
// item kinds (reasoning transcripts, file uploads) are dropped; the chat format
// has no place for them.
//
// Freeform call history maps onto the same chat shapes as a documented call: the
// tool it names was translated into a documented function, so replaying its turn
// as one keeps the exchange consistent with the definition the model was given.
func inputItemsToChat(item any) []any {
	object, ok := item.(map[string]any)
	if !ok {
		return nil
	}
	switch kind, _ := object["type"].(string); kind {
	case "message":
		content := contentPartsToChat(object["content"])
		if len(content) == 0 {
			return nil
		}
		role, _ := object["role"].(string)
		switch role {
		case "assistant", "system", "tool", "user":
		case "developer":
			role = "system"
		default:
			role = "user"
		}
		message := map[string]any{"role": role, "content": content}
		if name, ok := object["name"].(string); ok && name != "" {
			message["name"] = name
		}
		return []any{message}
	case "function_call", "custom_tool_call":
		callID, _ := object["call_id"].(string)
		name, _ := object["name"].(string)
		if callID == "" || name == "" {
			return nil
		}
		arguments := stringifyArgument(object["arguments"])
		if kind == "custom_tool_call" {
			text, _ := object["input"].(string)
			encoded, err := json.Marshal(map[string]any{"input": text})
			if err != nil {
				return nil
			}
			arguments = string(encoded)
		}
		return []any{map[string]any{
			"role": "assistant", "content": nil,
			"tool_calls": []any{map[string]any{
				"id": callID, "type": "function",
				"function": map[string]any{"name": name, "arguments": arguments},
			}},
		}}
	case "function_call_output", "custom_tool_call_output":
		callID, _ := object["call_id"].(string)
		if callID == "" {
			return nil
		}
		return []any{map[string]any{
			"role": "tool", "tool_call_id": callID, "content": stringifyArgument(object["output"]),
		}}
	default:
		return nil
	}
}

func contentPartsToChat(parts any) []any {
	items, ok := parts.([]any)
	if !ok {
		return nil
	}
	converted := make([]any, 0, len(items))
	for _, part := range items {
		object, ok := part.(map[string]any)
		if !ok {
			continue
		}
		switch kind, _ := object["type"].(string); kind {
		case "input_text", "output_text":
			text, _ := object["text"].(string)
			if text != "" {
				converted = append(converted, map[string]any{"type": "text", "text": text})
			}
		case "input_image":
			imageURL, _ := object["image_url"].(string)
			if imageURL == "" {
				continue
			}
			urlValue := map[string]any{"url": imageURL}
			if detail, ok := object["detail"].(string); ok && detail != "" {
				urlValue["detail"] = detail
			}
			converted = append(converted, map[string]any{"type": "image_url", "image_url": urlValue})
		}
	}
	return converted
}

func stringifyArgument(value any) string {
	if value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return text
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return string(encoded)
}

// chatTools moves a Responses tool definition, which carries the name and the
// parameters at the top level, into the nested shape Chat Completions expects.
//
// A freeform ("custom") tool has no chat equivalent: the dialect knows only
// documented functions. Dropping it would leave the model with no definition for
// the tool it is about to be asked to use, and it would answer by narrating the
// call in its prose - the exact failure this relay exists to prevent - so it is
// re-expressed as the documented function the downgrade path already builds.
// Hosted tools such as web search have no equivalent at all and are dropped
// instead of being rejected upstream.
func chatTools(tools []any) []any {
	converted := make([]any, 0, len(tools))
	for _, entry := range tools {
		definition, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		if _, nested := definition["function"]; nested {
			converted = append(converted, definition)
			continue
		}
		if kind, _ := definition["type"].(string); kind == "custom" {
			definition = documentedFreeformTool(definition)
		} else if kind != "function" && kind != "" {
			continue
		}
		function := make(map[string]any, len(definition))
		for key, value := range definition {
			if key != "type" {
				function[key] = value
			}
		}
		if name, _ := function["name"].(string); name == "" {
			continue
		}
		if parameters, ok := function["parameters"]; !ok || parameters == nil {
			function["parameters"] = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		converted = append(converted, map[string]any{"type": "function", "function": function})
	}
	return converted
}

func chatToolChoice(value any) any {
	object, ok := value.(map[string]any)
	if !ok || object["type"] != "function" {
		return value
	}
	name, _ := object["name"].(string)
	if name == "" {
		return value
	}
	return map[string]any{"type": "function", "function": map[string]any{"name": name}}
}

// chatToResponses converts a buffered Chat Completions SSE stream back into the
// Responses format: an event stream when the client asked for one, or a single
// JSON response object otherwise. The whole stream is available because the
// retry layer already buffered it.
func chatToResponses(body []byte, wantsStream bool) ([]byte, error) {
	normalized := bytes.ReplaceAll(body, []byte("\r\n"), []byte("\n"))
	blocks := bytes.Split(normalized, []byte("\n\n"))
	state := &chatAccumulator{}
	streamed := false
	for _, block := range blocks {
		data := sseData(block)
		if len(data) == 0 {
			continue
		}
		streamed = true
		if bytes.Equal(bytes.TrimSpace(data), []byte("[DONE]")) {
			break
		}
		var chunk map[string]any
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.UseNumber()
		if decoder.Decode(&chunk) != nil {
			continue
		}
		state.consume(chunk)
	}
	// Gateways that ignore "stream": true answer with a single completion object
	// instead of an event stream; without this the client would get an empty
	// response that claims to be complete.
	if !streamed {
		var completion map[string]any
		decoder := json.NewDecoder(bytes.NewReader(normalized))
		decoder.UseNumber()
		if decoder.Decode(&completion) != nil {
			return nil, errInvalidChatCompatibility
		}
		state.consume(completion)
	}
	return state.emit(wantsStream)
}

type chatCall struct {
	id        string
	name      string
	arguments string
}

type chatAccumulator struct {
	id      string
	model   string
	created json.Number
	text    string
	refusal string
	reason  string
	calls   []chatCall
	usage   map[string]any
}

func (state *chatAccumulator) consume(chunk map[string]any) {
	if id, ok := chunk["id"].(string); ok && id != "" && state.id == "" {
		state.id = id
	}
	if model, ok := chunk["model"].(string); ok && model != "" {
		state.model = model
	}
	if created, ok := chunk["created"].(json.Number); ok && state.created == "" {
		state.created = created
	}
	if usage, ok := chunk["usage"].(map[string]any); ok && usage != nil {
		state.usage = usage
	}
	choices, _ := chunk["choices"].([]any)
	if len(choices) == 0 {
		return
	}
	choice, _ := choices[0].(map[string]any)
	if choice == nil {
		return
	}
	if usage, ok := choice["usage"].(map[string]any); ok && usage != nil {
		state.usage = usage
	}
	// Why the reason is kept: it is the difference between an answer and the
	// absence of one. A chat provider says "I stopped because I ran out of
	// budget" or "the filter took it" in this field and nowhere else, and the
	// Responses envelope has its own place to say the same thing. Dropping it
	// used to hand the client a truncated answer stamped `completed`.
	if reason, ok := choice["finish_reason"].(string); ok && reason != "" {
		state.reason = reason
	}
	delta, ok := choice["delta"].(map[string]any)
	if !ok {
		// A buffered completion carries the same fields under "message".
		delta, ok = choice["message"].(map[string]any)
	}
	if !ok {
		return
	}
	if content, ok := delta["content"].(string); ok {
		state.text += content
	}
	// A refusal is the whole answer when it arrives: the model declined, and
	// `content` stays empty. Chat keeps it in its own field, Responses has no
	// such field on a text part, so it is carried as the message text rather
	// than dropped - an empty answer stamped `completed` tells the client
	// nothing, and it is text a guardrail can still read.
	if refusal, ok := delta["refusal"].(string); ok {
		state.refusal += refusal
	}
	calls, _ := delta["tool_calls"].([]any)
	for position, call := range calls {
		callItem, ok := call.(map[string]any)
		if !ok {
			continue
		}
		// Streamed chunks address their slot by index; a buffered message lists
		// the calls in order without one.
		index := position
		if rawIndex, ok := callItem["index"].(json.Number); ok {
			if parsed, err := rawIndex.Int64(); err == nil && parsed >= 0 {
				index = int(parsed)
			}
		}
		if index >= maxChatToolCalls {
			continue
		}
		for len(state.calls) <= index {
			state.calls = append(state.calls, chatCall{})
		}
		target := &state.calls[index]
		if id, ok := callItem["id"].(string); ok && id != "" {
			target.id = id
		}
		if function, ok := callItem["function"].(map[string]any); ok {
			if name, ok := function["name"].(string); ok && name != "" {
				target.name = name
			}
			if arguments, ok := function["arguments"].(string); ok {
				target.arguments += arguments
			}
		}
	}
}

func (state *chatAccumulator) emit(wantsStream bool) ([]byte, error) {
	responseID := "resp_" + strings.TrimPrefix(state.id, "chatcmpl-")
	createdAt := state.createdAt()
	output := state.outputItems()
	if wantsStream {
		response := state.responseObject(responseID, createdAt, output)
		blocks := make([][]byte, 0, 2+4*len(output))
		blocks = append(blocks, streamEvent(map[string]any{
			"type": "response.created",
			"response": map[string]any{
				"id": responseID, "object": "response", "created_at": createdAt,
				"status": "in_progress", "model": state.model, "output": []any{},
			},
		}))
		for index, item := range output {
			switch kind, _ := item["type"].(string); kind {
			case "message":
				itemID, _ := item["id"].(string)
				text := outputText(item)
				blocks = append(blocks, streamEvent(map[string]any{
					"type": "response.output_item.added", "output_index": index,
					"item": map[string]any{
						"id": itemID, "type": "message", "status": "in_progress",
						"role": "assistant", "content": []any{},
					},
				}))
				blocks = append(blocks, streamEvent(map[string]any{
					"type": "response.content_part.added", "item_id": itemID,
					"output_index": index, "content_index": 0,
					"part": map[string]any{"type": "output_text", "text": "", "annotations": []any{}},
				}))
				if text != "" {
					blocks = append(blocks, streamEvent(map[string]any{
						"type": "response.output_text.delta", "item_id": itemID,
						"output_index": index, "content_index": 0, "delta": text,
					}))
				}
				blocks = append(blocks, streamEvent(map[string]any{
					"type": "response.output_text.done", "item_id": itemID,
					"output_index": index, "content_index": 0, "text": text,
				}))
				blocks = append(blocks, streamEvent(map[string]any{
					"type": "response.output_item.done", "output_index": index, "item": item,
				}))
			case "function_call":
				itemID, _ := item["id"].(string)
				callID, _ := item["call_id"].(string)
				name, _ := item["name"].(string)
				arguments, _ := item["arguments"].(string)
				blocks = append(blocks, streamEvent(map[string]any{
					"type": "response.output_item.added", "output_index": index,
					"item": map[string]any{
						"id": itemID, "type": "function_call", "status": "in_progress",
						"call_id": callID, "name": name, "arguments": "",
					},
				}))
				if arguments != "" {
					blocks = append(blocks, streamEvent(map[string]any{
						"type": "response.function_call_arguments.delta", "item_id": itemID,
						"output_index": index, "delta": arguments,
					}))
				}
				blocks = append(blocks, streamEvent(map[string]any{
					"type": "response.function_call_arguments.done", "item_id": itemID,
					"output_index": index, "arguments": arguments,
				}))
				blocks = append(blocks, streamEvent(map[string]any{
					"type": "response.output_item.done", "output_index": index, "item": item,
				}))
			}
		}
		blocks = append(blocks, streamEvent(map[string]any{
			"type": terminalEventType(response), "response": response,
		}))
		return bytes.Join(blocks, []byte("\n\n")), nil
	}
	return json.Marshal(state.responseObject(responseID, createdAt, output))
}

// terminalEventType names the event that closes the stream. A turn the provider
// cut short closes with `response.incomplete`, so a client that reads the event
// type reaches the same conclusion as one that reads the envelope.
func terminalEventType(response map[string]any) string {
	if response["status"] == "incomplete" {
		return "response.incomplete"
	}
	return "response.completed"
}

// outputItems returns the completed Responses output items the accumulated
// stream produced: one message item for the text and one function_call item
// per tool call.
func (state *chatAccumulator) outputItems() []map[string]any {
	items := make([]map[string]any, 0, 1+len(state.calls))
	if text := state.answer(); text != "" {
		items = append(items, map[string]any{
			"type": "message", "id": "msg_0", "status": "completed", "role": "assistant",
			"content": []any{map[string]any{"type": "output_text", "text": text, "annotations": []any{}}},
		})
	}
	for index, call := range state.calls {
		if call.name == "" {
			continue
		}
		callID := call.id
		if callID == "" {
			callID = "fnc_" + strconv.Itoa(index)
		}
		items = append(items, map[string]any{
			"type": "function_call", "id": callID, "call_id": callID,
			"name": call.name, "arguments": call.arguments, "status": "completed",
		})
	}
	return items
}

// outputText returns the accumulated text of a message output item.
func outputText(item map[string]any) string {
	content, _ := item["content"].([]any)
	if len(content) == 0 {
		return ""
	}
	part, _ := content[0].(map[string]any)
	text, _ := part["text"].(string)
	return text
}

// answer is what the assistant said, which is the refusal when it refused. A
// provider that sends both is taken at its word and both are kept.
func (state *chatAccumulator) answer() string {
	if state.refusal == "" {
		return state.text
	}
	if state.text == "" {
		return state.refusal
	}
	return state.text + "\n\n" + state.refusal
}

// incompleteReason maps a chat finish reason onto the Responses field that says
// the turn did not finish. Only the reasons that mean "the answer is missing or
// cut short" map to one: `stop` and `tool_calls` are ordinary completions, and an
// unfamiliar reason is left alone rather than guessed at.
func incompleteReason(reason string) string {
	switch reason {
	case "length", "max_tokens":
		return "max_output_tokens"
	case "content_filter":
		return "content_filter"
	default:
		return ""
	}
}

func (state *chatAccumulator) responseObject(responseID string, createdAt int64, output []map[string]any) map[string]any {
	items := make([]any, 0, len(output))
	for _, item := range output {
		items = append(items, item)
	}
	status := "completed"
	var incomplete any
	if reason := incompleteReason(state.reason); reason != "" {
		status = "incomplete"
		incomplete = map[string]any{"reason": reason}
	}
	return map[string]any{
		"id": responseID, "object": "response", "created_at": createdAt, "status": status,
		"error": nil, "incomplete_details": incomplete, "instructions": nil,
		"max_output_tokens": nil, "model": state.model, "output": items,
		"parallel_tool_calls": true, "previous_response_id": nil,
		"reasoning": map[string]any{"effort": nil, "summary": nil},
		"store":     true, "temperature": 1,
		"text":        map[string]any{"format": map[string]any{"type": "text"}},
		"tool_choice": "auto", "tools": []any{}, "top_p": 1,
		"truncation": "disabled",
		"usage":      chatUsageToResponses(state.usage),
	}
}

func (state *chatAccumulator) createdAt() int64 {
	if state.created != "" {
		if value, err := state.created.Int64(); err == nil && value > 0 {
			return value
		}
	}
	return time.Now().Unix()
}

func chatUsageToResponses(usage map[string]any) map[string]any {
	input := int64(0)
	output := int64(0)
	total := int64(0)
	if usage != nil {
		input = intValue(usage["prompt_tokens"])
		output = intValue(usage["completion_tokens"])
		total = intValue(usage["total_tokens"])
	}
	if total == 0 {
		total = input + output
	}
	return map[string]any{
		"input_tokens": input,
		"input_tokens_details": map[string]any{
			"cached_tokens": int64(0),
		},
		"output_tokens": output,
		"output_tokens_details": map[string]any{
			"reasoning_tokens": int64(0),
		},
		"total_tokens": total,
	}
}

func intValue(value any) int64 {
	switch number := value.(type) {
	case json.Number:
		if parsed, err := number.Int64(); err == nil && parsed >= 0 {
			return parsed
		}
	case float64:
		if number >= 0 {
			return int64(number)
		}
	}
	return 0
}

// endpointMissing404 reports a 404 that says the endpoint itself is missing,
// as opposed to a model-level error. Chat-only providers commonly answer
// /v1/responses with a plain "Not Found" page; those are the ones the relay
// retries through the chat completions translation. Markers stay specific:
// a false positive would route a working provider through the translation.
func endpointMissing404(status int, body []byte) bool {
	if status != http.StatusNotFound || len(body) == 0 {
		return false
	}
	text := strings.ToLower(string(body))
	for _, marker := range []string{
		"not found", "does not exist", "doesn't exist", "not exist",
		"not supported", "not implemented", "unknown endpoint", "no route",
		"unknown path", "invalid url",
	} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}
