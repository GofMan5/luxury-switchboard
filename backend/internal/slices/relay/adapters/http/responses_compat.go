package relayhttp

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// The signed-in ChatGPT backend speaks the Responses dialect and nothing
// else, so a chat-format client pointed at it is served by translating the
// request up and the answer back. This file owns the chat-to-responses
// direction; format_compat.go owns the mirror direction. The same contract
// applies both ways: a translation converts, it never drops — the model
// still sees the prompt, the tool calls and the tool results the client
// sent, in the dialect the upstream understands.

// freshSessionID mints the per-request session identity the stateless
// backend requires. Nothing is stored upstream to remember a session, so
// every relay call opens one of its own: a fresh UUID, never derived from
// client bytes — the client cannot correlate two relay calls by naming a
// session id in its request.
func freshSessionID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		// The identifier has to be unique, not strong: a time-seeded
		// fallback still separates concurrent requests, which is all
		// the accounting on the other side does with it.
		now := time.Now().UnixNano()
		for i := range raw {
			raw[i] = byte(now >> (uint(i%8) * 8))
		}
	}
	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80
	session := hex.EncodeToString(raw[:])
	return session[0:8] + "-" + session[8:12] + "-" + session[12:16] + "-" + session[16:20] + "-" + session[20:32]
}

// chatRequestToResponses translates a Chat Completions request body into
// the Responses dialect. When stateless is set the translated request also
// carries the codex wire contract (forced streaming, no server-side state)
// that the ChatGPT backend requires of every caller.
func chatRequestToResponses(body []byte, stateless bool) ([]byte, error) {
	translated, err := translateChatRequest(body)
	if err != nil {
		return nil, err
	}
	if !stateless {
		return translated, nil
	}
	return shapeStatelessResponsesRequest(translated)
}

// shapeStatelessResponsesRequest applies the codex wire contract to a
// Responses-format body: streaming is forced (the ChatGPT backend answers
// nothing else), nothing is stored server-side, instructions default to
// the empty string the backend expects, and stateful continuation fields
// are stripped — a direct relay call has no conversation to continue.
// Numbers keep their original spelling via json.Number.
func shapeStatelessResponsesRequest(body []byte) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var request map[string]any
	if err := decoder.Decode(&request); err != nil {
		return nil, fmt.Errorf("responses request is not valid JSON: %w", err)
	}
	request["stream"] = true
	request["store"] = false
	if _, ok := request["instructions"]; !ok {
		request["instructions"] = ""
	}
	for _, field := range []string{"previous_response_id", "prompt_cache_key", "prompt_cache_retention", "safety_identifier"} {
		delete(request, field)
	}
	return json.Marshal(request)
}

// translateChatRequest performs the pure dialect conversion: field names
// move, nesting moves, and nothing the client expressed is lost. Fields
// with no Responses counterpart (logprobs, n, logit_bias, stop) are a
// documented inherent loss — the Responses API has nowhere to put them.
func translateChatRequest(body []byte) ([]byte, error) {
	var request chatRequestShape
	if err := json.Unmarshal(body, &request); err != nil {
		return nil, fmt.Errorf("chat request is not valid JSON: %w", err)
	}
	instructions := request.systemInstructions()
	items, err := request.inputItems()
	if err != nil {
		return nil, err
	}
	out := map[string]any{
		"model":  request.Model,
		"input":  items,
		"stream": request.Stream,
	}
	if instructions != "" {
		out["instructions"] = instructions
	}
	if len(request.Tools) > 0 {
		out["tools"] = request.responseTools()
	}
	if len(request.ToolChoice) > 0 {
		out["tool_choice"] = responseToolChoice(request.ToolChoice)
	}
	if request.MaxTokens != nil {
		out["max_output_tokens"] = *request.MaxTokens
	} else if request.MaxCompletionTokens != nil {
		out["max_output_tokens"] = *request.MaxCompletionTokens
	}
	if request.ReasoningEffort != "" {
		out["reasoning"] = map[string]any{"effort": request.ReasoningEffort}
	}
	if len(request.ResponseFormat) > 0 {
		if format := responseTextFormat(request.ResponseFormat); format != nil {
			out["text"] = map[string]any{"format": format}
		}
	}
	if request.Temperature != nil {
		out["temperature"] = *request.Temperature
	}
	if request.TopP != nil {
		out["top_p"] = *request.TopP
	}
	if request.User != "" {
		out["user"] = request.User
	}
	if request.ParallelToolCalls != nil {
		out["parallel_tool_calls"] = *request.ParallelToolCalls
	}
	if len(request.Metadata) > 0 && !bytes.Equal(request.Metadata, []byte("null")) {
		var metadata any
		if json.Unmarshal(request.Metadata, &metadata) == nil {
			out["metadata"] = metadata
		}
	}
	return json.Marshal(out)
}

type chatRequestShape struct {
	Model               string          `json:"model"`
	Messages            []chatMessage   `json:"messages"`
	Tools               []chatTool      `json:"tools"`
	ToolChoice          json.RawMessage `json:"tool_choice"`
	MaxTokens           *int            `json:"max_tokens"`
	MaxCompletionTokens *int            `json:"max_completion_tokens"`
	ReasoningEffort     string          `json:"reasoning_effort"`
	ResponseFormat      json.RawMessage `json:"response_format"`
	Temperature         *float64        `json:"temperature"`
	TopP                *float64        `json:"top_p"`
	User                string          `json:"user"`
	ParallelToolCalls   *bool           `json:"parallel_tool_calls"`
	Metadata            json.RawMessage `json:"metadata"`
	Stream              bool            `json:"stream"`
}

type chatMessage struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content"`
	ToolCalls  []chatToolCall  `json:"tool_calls"`
	ToolCallID string          `json:"tool_call_id"`
}

type chatToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type chatTool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Parameters  json.RawMessage `json:"parameters"`
		Strict      *bool           `json:"strict"`
	} `json:"function"`
}

type chatContentPart struct {
	Type     string `json:"type"`
	Text     string `json:"text"`
	ImageURL struct {
		URL    string `json:"url"`
		Detail string `json:"detail"`
	} `json:"image_url"`
}

// systemInstructions collects the system-side guidance a chat client sends
// as messages into the single instructions string Responses expects. Two
// system messages are joined, not ranked: the second one is not a
// replacement for the first.
func (request chatRequestShape) systemInstructions() string {
	parts := make([]string, 0, 2)
	for _, message := range request.Messages {
		if message.Role != "system" && message.Role != "developer" {
			continue
		}
		if text := contentText(message.Content); text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n\n")
}

// inputItems converts the message history into Responses input items. A
// tool call becomes a function_call item, its result a function_call_output,
// and plain messages keep their role with dialect-correct content part
// kinds (input_text on the way in, output_text on the way back).
func (request chatRequestShape) inputItems() ([]any, error) {
	items := make([]any, 0, len(request.Messages))
	for _, message := range request.Messages {
		switch message.Role {
		case "system", "developer":
			continue
		case "tool", "function":
			items = append(items, map[string]any{
				"type":    "function_call_output",
				"call_id": message.ToolCallID,
				"output":  contentText(message.Content),
			})
		default:
			parts, err := responseContentParts(message.Content, message.Role)
			if err != nil {
				return nil, err
			}
			if len(parts) > 0 {
				items = append(items, map[string]any{
					"type":    "message",
					"role":    message.Role,
					"content": parts,
				})
			}
			for _, call := range message.ToolCalls {
				items = append(items, map[string]any{
					"type":      "function_call",
					"call_id":   call.ID,
					"name":      call.Function.Name,
					"arguments": call.Function.Arguments,
				})
			}
		}
	}
	return items, nil
}

// responseContentParts converts one message's content — a bare string or a
// list of typed parts — into Responses content parts. User-side text
// becomes input_text, assistant-side text output_text; images travel as
// input_image with the URL lifted out of the chat envelope.
func responseContentParts(raw json.RawMessage, role string) ([]any, error) {
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil, nil
	}
	textKind := "input_text"
	if role == "assistant" {
		textKind = "output_text"
	}
	var asString string
	if err := json.Unmarshal(raw, &asString); err == nil {
		if asString == "" {
			return nil, nil
		}
		return []any{map[string]any{"type": textKind, "text": asString}}, nil
	}
	var parts []chatContentPart
	if err := json.Unmarshal(raw, &parts); err != nil {
		return nil, fmt.Errorf("message content is neither text nor a list of parts: %w", err)
	}
	converted := make([]any, 0, len(parts))
	for _, part := range parts {
		switch part.Type {
		case "text":
			converted = append(converted, map[string]any{"type": textKind, "text": part.Text})
		case "image_url":
			if part.ImageURL.URL == "" {
				continue
			}
			image := map[string]any{"type": "input_image", "image_url": part.ImageURL.URL}
			if part.ImageURL.Detail != "" {
				image["detail"] = part.ImageURL.Detail
			}
			converted = append(converted, image)
		}
	}
	return converted, nil
}

// contentText flattens message content into a plain string for the places
// Responses demands one (instructions, tool results). A list of text parts
// joins with newlines; anything exotic is carried as its own JSON text —
// the model still gets to see it.
func contentText(raw json.RawMessage) string {
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return ""
	}
	var asString string
	if err := json.Unmarshal(raw, &asString); err == nil {
		return asString
	}
	var parts []chatContentPart
	if err := json.Unmarshal(raw, &parts); err == nil {
		texts := make([]string, 0, len(parts))
		for _, part := range parts {
			if part.Type == "text" && part.Text != "" {
				texts = append(texts, part.Text)
			}
		}
		if len(texts) > 0 {
			return strings.Join(texts, "\n")
		}
	}
	compact := &bytes.Buffer{}
	if err := json.Compact(compact, raw); err != nil {
		return string(raw)
	}
	return compact.String()
}

// responseTools flattens the chat tool envelope: Responses carries the
// function fields at the top level of the tool object.
func (request chatRequestShape) responseTools() []any {
	tools := make([]any, 0, len(request.Tools))
	for _, tool := range request.Tools {
		if tool.Type != "function" || tool.Function.Name == "" {
			continue
		}
		flat := map[string]any{"type": "function", "name": tool.Function.Name}
		if tool.Function.Description != "" {
			flat["description"] = tool.Function.Description
		}
		if len(tool.Function.Parameters) > 0 && !bytes.Equal(tool.Function.Parameters, []byte("null")) {
			var parameters any
			if json.Unmarshal(tool.Function.Parameters, &parameters) == nil {
				flat["parameters"] = parameters
			}
		}
		if tool.Function.Strict != nil {
			flat["strict"] = *tool.Function.Strict
		}
		tools = append(tools, flat)
	}
	return tools
}

// responseToolChoice carries the three string spellings unchanged and lifts
// the object spelling out of its chat envelope.
func responseToolChoice(raw json.RawMessage) any {
	var name string
	if err := json.Unmarshal(raw, &name); err == nil {
		return name
	}
	var shaped struct {
		Type     string `json:"type"`
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	}
	if err := json.Unmarshal(raw, &shaped); err == nil && shaped.Type == "function" && shaped.Function.Name != "" {
		return map[string]any{"type": "function", "name": shaped.Function.Name}
	}
	return "auto"
}

// responseTextFormat converts response_format into the Responses text
// envelope. The json_object and text spellings move as they are; json_schema
// lifts name/schema/strict out of the nested chat envelope.
func responseTextFormat(raw json.RawMessage) any {
	var shaped struct {
		Type       string          `json:"type"`
		JSONSchema json.RawMessage `json:"json_schema"`
	}
	if err := json.Unmarshal(raw, &shaped); err != nil || shaped.Type == "" {
		return nil
	}
	format := map[string]any{"type": shaped.Type}
	if len(shaped.JSONSchema) > 0 && !bytes.Equal(shaped.JSONSchema, []byte("null")) {
		var schema map[string]any
		if json.Unmarshal(shaped.JSONSchema, &schema) == nil {
			for key, value := range schema {
				format[key] = value
			}
		}
	}
	return format
}

// responsesAnswerToChat converts a buffered Responses answer — SSE events
// or one JSON object that never streamed — into the Chat Completions
// dialect the client speaks. It is the mirror of chatToResponses: the same
// block and data-line rules, an undecodable non-streamed body is a
// compatibility failure rather than a silently empty answer, and the way a
// turn ended is carried over instead of being stamped "completed".
func responsesAnswerToChat(body []byte, wantsStream bool) ([]byte, error) {
	normalized := bytes.ReplaceAll(body, []byte("\r\n"), []byte("\n"))
	state := &responsesAnswerState{}
	streamed := false
	for _, block := range bytes.Split(normalized, []byte("\n\n")) {
		data := sseData(block)
		if len(data) == 0 {
			continue
		}
		streamed = true
		if bytes.Equal(bytes.TrimSpace(data), []byte("[DONE]")) {
			break
		}
		var event map[string]any
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.UseNumber()
		if decoder.Decode(&event) != nil {
			continue
		}
		state.consume(event)
	}
	if !streamed {
		var response map[string]any
		decoder := json.NewDecoder(bytes.NewReader(normalized))
		decoder.UseNumber()
		if decoder.Decode(&response) != nil {
			return nil, errInvalidChatCompatibility
		}
		state.consumeResponse(response)
	}
	return state.emit(wantsStream)
}

// responsesAnswerState accumulates a Responses answer into the pieces a
// chat answer is built from. Deltas are the stream's own words; a terminal
// event's output is the complete statement and replaces them — but only
// when it actually carries items, because Responses sends the same output
// both in output_item.done and in the terminal, and an empty terminal
// output is a proxy's shorthand, not an instruction to drop what streamed.
type responsesAnswerState struct {
	id      string
	model   string
	created json.Number

	text       string
	sawText    bool
	refusal    string
	incomplete string
	failure    string
	failed     bool
	calls      []any
	usage      map[string]any
}

// consume folds one event into the state. Delta events append the
// provider's live words; item and terminal events carry whole output.
func (state *responsesAnswerState) consume(event map[string]any) {
	switch eventType, _ := event["type"].(string); eventType {
	case "response.created", "response.in_progress", "response.completed", "response.incomplete", "response.failed":
		state.consumeResponse(responseOf(event))
	case "response.output_text.delta":
		if delta, ok := event["delta"].(string); ok {
			state.text += delta
			state.sawText = true
		}
	case "response.output_text.done":
		if text, ok := event["text"].(string); ok && !state.sawText {
			state.text += text
		}
	case "response.refusal.delta":
		if delta, ok := event["delta"].(string); ok {
			state.refusal += delta
		}
	case "response.refusal.done":
		if refusal, ok := event["refusal"].(string); ok && state.refusal == "" {
			state.refusal += refusal
		}
	case "response.output_item.done":
		state.consumeItem(event["item"])
	}
}

func responseOf(event map[string]any) map[string]any {
	response, _ := event["response"].(map[string]any)
	return response
}

// consumeResponse reads a whole response object. The last terminal wins:
// status, failure and incomplete reason are reset before they are set, and
// a non-empty output replaces the streamed preview of the same words.
func (state *responsesAnswerState) consumeResponse(response map[string]any) {
	if response == nil {
		return
	}
	if id, ok := response["id"].(string); ok && id != "" {
		state.id = id
	}
	if model, ok := response["model"].(string); ok && model != "" {
		state.model = model
	}
	if created, ok := response["created_at"].(json.Number); ok {
		state.created = created
	}
	status, _ := response["status"].(string)
	state.failed = status == "failed"
	state.incomplete = ""
	state.failure = ""
	if details, ok := response["incomplete_details"].(map[string]any); ok {
		if reason, ok := details["reason"].(string); ok {
			state.incomplete = reason
		}
	}
	if failure, ok := response["error"].(map[string]any); ok {
		if message, ok := failure["message"].(string); ok {
			state.failure = message
		}
	}
	if usage, ok := response["usage"].(map[string]any); ok && len(usage) > 0 {
		state.usage = usage
	}
	if output, ok := response["output"].([]any); ok && len(output) > 0 {
		state.text = ""
		state.refusal = ""
		state.calls = nil
		for _, item := range output {
			state.consumeItem(item)
		}
	}
}

// consumeItem folds one output item into the answer. A message fills the
// text and the refusal (only where empty — deltas already carry the same
// words); a function or custom tool call becomes a chat tool call, the
// only chat-dialect home a call has.
func (state *responsesAnswerState) consumeItem(value any) {
	item, ok := value.(map[string]any)
	if !ok {
		return
	}
	switch itemType, _ := item["type"].(string); itemType {
	case "message":
		parts, _ := item["content"].([]any)
		for _, part := range parts {
			partObject, ok := part.(map[string]any)
			if !ok {
				continue
			}
			switch partType, _ := partObject["type"].(string); partType {
			case "output_text":
				if text, ok := partObject["text"].(string); ok && state.text == "" {
					state.text += text
					state.sawText = true
				}
			case "refusal":
				if refusal, ok := partObject["refusal"].(string); ok && state.refusal == "" {
					state.refusal += refusal
				}
			}
		}
	case "function_call", "custom_tool_call":
		callID, _ := item["call_id"].(string)
		name, _ := item["name"].(string)
		arguments, _ := item["arguments"].(string)
		if arguments == "" {
			arguments, _ = item["input"].(string)
		}
		state.calls = append(state.calls, map[string]any{
			"id":   callID,
			"type": "function",
			"function": map[string]any{
				"name":      name,
				"arguments": arguments,
			},
		})
	}
}

// emit renders the answer in the dialect the client asked for. A refusal
// rides as the message text — Responses has no chat_refusal field on the
// wire, an empty answer tells the client nothing, and this is the text the
// guardrail reads — with the chat refusal field set alongside, and the
// finish reason says what the Responses status said.
func (state *responsesAnswerState) emit(wantsStream bool) ([]byte, error) {
	if state.failed {
		return state.emitFailure(wantsStream)
	}
	finish := "stop"
	if reason := chatFinishReason(state.incomplete); reason != "" {
		finish = reason
	} else if len(state.calls) > 0 {
		finish = "tool_calls"
	}
	message := map[string]any{"role": "assistant", "content": state.answer()}
	if state.refusal != "" {
		message["refusal"] = state.refusal
	}
	if len(state.calls) > 0 {
		message["tool_calls"] = state.calls
	}
	if !wantsStream {
		completion := map[string]any{
			"id":      state.chatID(),
			"object":  "chat.completion",
			"created": state.createdAt(),
			"model":   state.model,
			"choices": []any{map[string]any{
				"index":         0,
				"message":       message,
				"finish_reason": finish,
			}},
		}
		if usage := state.chatUsage(); usage != nil {
			completion["usage"] = usage
		}
		return json.Marshal(completion)
	}
	blocks := make([]string, 0, 6)
	chunk := func(delta map[string]any, finishReason any) {
		payload := map[string]any{
			"id":      state.chatID(),
			"object":  "chat.completion.chunk",
			"created": state.createdAt(),
			"model":   state.model,
			"choices": []any{map[string]any{
				"index":         0,
				"delta":         delta,
				"finish_reason": finishReason,
			}},
		}
		if encoded, err := json.Marshal(payload); err == nil {
			blocks = append(blocks, "data: "+string(encoded))
		}
	}
	chunk(map[string]any{"role": "assistant"}, nil)
	if content := state.answer(); content != "" {
		chunk(map[string]any{"content": content}, nil)
	}
	if state.refusal != "" {
		chunk(map[string]any{"refusal": state.refusal}, nil)
	}
	for index, call := range state.calls {
		streamedCall, _ := call.(map[string]any)
		if streamedCall == nil {
			continue
		}
		streamedCall["index"] = index
		blocks = append(blocks, "data: "+mustEncodeChunk(map[string]any{
			"id":      state.chatID(),
			"object":  "chat.completion.chunk",
			"created": state.createdAt(),
			"model":   state.model,
			"choices": []any{map[string]any{
				"index": 0,
				"delta": map[string]any{"tool_calls": []any{streamedCall}},
			}},
		}))
	}
	chunk(map[string]any{}, finish)
	if usage := state.chatUsage(); usage != nil {
		blocks = append(blocks, "data: "+mustEncodeChunk(map[string]any{
			"id":      state.chatID(),
			"object":  "chat.completion.chunk",
			"created": state.createdAt(),
			"model":   state.model,
			"choices": []any{},
			"usage":   usage,
		}))
	}
	blocks = append(blocks, "data: [DONE]")
	return []byte(strings.Join(blocks, "\n\n") + "\n\n"), nil
}

// emitFailure renders a failed response. The words are the provider's own;
// a failure without words is still a failure, and neither becomes a
// completed answer. The half-delivered text is kept — the failure is
// appended to the stream, not substituted for it. Normally the retry
// ladder routes failures before the conversion sees them; it can still
// hand one over with HTTP 200, and this is the branch that meets it.
func (state *responsesAnswerState) emitFailure(wantsStream bool) ([]byte, error) {
	failure := map[string]any{}
	if state.failure != "" {
		failure["message"] = state.failure
	}
	if !wantsStream {
		return json.Marshal(map[string]any{"error": failure})
	}
	blocks := make([]string, 0, 3)
	if content := state.answer(); content != "" {
		blocks = append(blocks, "data: "+mustEncodeChunk(map[string]any{
			"id":      state.chatID(),
			"object":  "chat.completion.chunk",
			"created": state.createdAt(),
			"model":   state.model,
			"choices": []any{map[string]any{
				"index": 0,
				"delta": map[string]any{"content": content},
			}},
		}))
	}
	encoded, err := json.Marshal(map[string]any{"error": failure})
	if err != nil {
		return nil, err
	}
	blocks = append(blocks, "data: "+string(encoded), "data: [DONE]")
	return []byte(strings.Join(blocks, "\n\n") + "\n\n"), nil
}

// mustEncodeChunk renders a chat stream chunk. The payloads are built from
// maps of strings and numbers, so the marshal cannot fail; a chunk that
// somehow did would be skipped rather than corrupt the stream.
func mustEncodeChunk(payload map[string]any) string {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	return string(encoded)
}

// answer is the message text. A refusal is the answer's text, because
// Responses carries refusal words nowhere else and an empty answer says
// nothing; when both arrived the text is the answer.
func (state *responsesAnswerState) answer() string {
	if state.text != "" {
		return state.text
	}
	return state.refusal
}

func (state *responsesAnswerState) chatID() string {
	return "chatcmpl-" + strings.TrimPrefix(state.id, "resp_")
}

func (state *responsesAnswerState) createdAt() int64 {
	if value, err := state.created.Int64(); err == nil && value > 0 {
		return value
	}
	return time.Now().Unix()
}

// chatUsage maps the Responses meter onto the chat one: input and output
// tokens become prompt and completion, the total keeps its name and is
// derived when the provider left it out.
func (state *responsesAnswerState) chatUsage() map[string]any {
	if state.usage == nil {
		return nil
	}
	prompt := intValue(state.usage["input_tokens"])
	completion := intValue(state.usage["output_tokens"])
	total := intValue(state.usage["total_tokens"])
	if total == 0 {
		total = prompt + completion
	}
	if prompt == 0 && completion == 0 && total == 0 {
		return nil
	}
	usage := map[string]any{}
	if prompt > 0 {
		usage["prompt_tokens"] = prompt
	}
	if completion > 0 {
		usage["completion_tokens"] = completion
	}
	if total > 0 {
		usage["total_tokens"] = total
	}
	return usage
}

// chatFinishReason maps an incomplete reason onto the chat vocabulary.
// Only the reasons with a chat-dialect home are mapped; an unfamiliar one
// is not guessed at — the turn still says it stopped.
func chatFinishReason(reason string) string {
	switch reason {
	case "max_output_tokens":
		return "length"
	case "content_filter":
		return "content_filter"
	}
	return ""
}

var errInvalidResponsesCompatibility = errors.New("invalid responses compatibility answer")

// responsesSSEToJSON folds a buffered Responses stream into the single JSON
// answer a non-streaming client asked for. The terminal event carries the
// whole response object in the client's own dialect, so the folded answer
// is that object, not a reconstruction; a stream that never reached a
// terminal is a failed fold, not an empty answer, and a body that never
// streamed is already the answer and passes through untouched.
func responsesSSEToJSON(body []byte) ([]byte, error) {
	normalized := bytes.ReplaceAll(body, []byte("\r\n"), []byte("\n"))
	var terminal map[string]any
	streamed := false
	for _, block := range bytes.Split(normalized, []byte("\n\n")) {
		data := sseData(block)
		if len(data) == 0 {
			continue
		}
		streamed = true
		if bytes.Equal(bytes.TrimSpace(data), []byte("[DONE]")) {
			break
		}
		var event map[string]any
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.UseNumber()
		if decoder.Decode(&event) != nil {
			continue
		}
		switch eventType, _ := event["type"].(string); eventType {
		case "response.completed", "response.incomplete", "response.failed":
			terminal = responseOf(event)
		}
	}
	if terminal != nil {
		return json.Marshal(terminal)
	}
	if !streamed {
		var answer map[string]any
		decoder := json.NewDecoder(bytes.NewReader(normalized))
		decoder.UseNumber()
		if decoder.Decode(&answer) == nil && len(answer) > 0 {
			return body, nil
		}
	}
	return nil, errInvalidResponsesCompatibility
}

// responsesCompletionBody reports whether a buffered answer is a whole
// Responses JSON object — an upstream that answered a forced stream with
// one body — and if so, hands back the terminal vocabulary and the usage
// it carries, so the stream bookkeeping can close on it instead of
// treating the answer as an unfinished stream.
func responsesCompletionBody(body []byte) (terminal string, usage map[string]any, complete bool) {
	var answer map[string]any
	decoder := json.NewDecoder(bytes.NewReader(bytes.TrimSpace(body)))
	decoder.UseNumber()
	if decoder.Decode(&answer) != nil || len(answer) == 0 {
		return "", nil, false
	}
	status, _ := answer["status"].(string)
	switch status {
	case "completed", "incomplete", "failed":
	default:
		return "", nil, false
	}
	if id, _ := answer["id"].(string); id == "" {
		return "", nil, false
	}
	if responseUsage, ok := answer["usage"].(map[string]any); ok && len(responseUsage) > 0 {
		usage = responseUsage
	}
	return "response." + status, usage, true
}
