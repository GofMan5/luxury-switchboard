// Package application turns a finished provider answer into the pieces of text a
// guardrail can judge: the assistant's visible output, its reasoning, and the
// arguments of every tool call it asked the client to run.
package application

import (
	"bytes"
	"encoding/json"
	"strings"
)

// Bounds for one extraction. The response body is already capped by the relay's
// own buffer limit; these keep a body that is legitimately huge from turning into
// a second copy plus a scan of it.
//
// Prose and tool arguments get SEPARATE budgets on purpose. With one shared
// budget a provider could send half a megabyte of harmless explanation and every
// tool call after it would arrive with nothing left to inspect — padding would
// buy silence for the part that actually gets executed.
const (
	maxTextBytes  = 512 * 1024
	maxToolBytes  = 512 * 1024
	maxPieces     = 256
	maxToolNames  = 64
	maxSourceName = 64
)

// Piece is one span of text with a safe label saying where it came from.
type Piece struct {
	Text   string
	Source string
}

// Extraction is everything a guardrail needs from one answer.
type Extraction struct {
	Pieces []Piece
	// ToolNames lists the distinct tools the answer asked the client to run. It is
	// the signal for the unsolicited-tool anomaly and is metadata, not content.
	ToolNames []string
}

const (
	sourceAssistant = "assistant_text"
	sourceReasoning = "reasoning_text"
	sourceToolCall  = "tool_call:"
)

// Extract reads an answer in whichever dialect it ended up in.
//
// Dispatch is by SHAPE, not by request path: the relay rewrites Chat Completions
// into Responses and back, bridges images and repairs stream lifecycles, so the
// dialect of the final body does not follow from the URL the client called.
// Every payload is tried as each shape it could be.
//
// Deltas are accumulated before matching. A provider that splits "curl x | sh"
// across three events would otherwise pass every rule.
func Extract(body []byte, eventStream bool) Extraction {
	if len(body) == 0 {
		return Extraction{}
	}
	collector := &collector{}
	if eventStream {
		collector.readEventStream(body)
	} else {
		collector.readObject(body)
	}
	return collector.result()
}

type accumulator struct {
	source  string
	builder strings.Builder
}

type collector struct {
	order     []*accumulator
	byKey     map[string]*accumulator
	toolNames []string
	seenNames map[string]struct{}
	// itemTools remembers which tool an item belongs to. A streamed call announces
	// its name once and the argument fragments that follow carry only the item id.
	itemTools map[string]string
	textBytes int
	toolBytes int
}

func (state *collector) result() Extraction {
	pieces := make([]Piece, 0, len(state.order))
	for _, entry := range state.order {
		text := entry.builder.String()
		if strings.TrimSpace(text) == "" {
			continue
		}
		pieces = append(pieces, Piece{Text: text, Source: entry.source})
	}
	return Extraction{Pieces: pieces, ToolNames: state.toolNames}
}

// add appends text to the accumulator identified by key, creating it on first
// use. Pieces are keyed so deltas of the same item join into one string.
func (state *collector) add(key, source, text string) {
	if text == "" {
		return
	}
	// Which budget applies follows the source: an argument string is what the client
	// would run, so it is never charged against the prose allowance.
	spent, limit := &state.textBytes, maxTextBytes
	if strings.HasPrefix(source, sourceToolCall) {
		spent, limit = &state.toolBytes, maxToolBytes
	}
	if *spent >= limit {
		return
	}
	if remaining := limit - *spent; len(text) > remaining {
		text = text[:remaining]
	}
	if state.byKey == nil {
		state.byKey = make(map[string]*accumulator, 8)
	}
	entry := state.byKey[key]
	if entry == nil {
		if len(state.order) >= maxPieces {
			return
		}
		entry = &accumulator{source: source}
		state.byKey[key] = entry
		state.order = append(state.order, entry)
	}
	entry.builder.WriteString(text)
	*spent += len(text)
}

func (state *collector) noteTool(name string) {
	name = strings.TrimSpace(name)
	if name == "" || len(state.toolNames) >= maxToolNames {
		return
	}
	if state.seenNames == nil {
		state.seenNames = make(map[string]struct{}, 8)
	}
	if _, seen := state.seenNames[name]; seen {
		return
	}
	state.seenNames[name] = struct{}{}
	state.toolNames = append(state.toolNames, truncate(name, maxSourceName))
}

// readEventStream walks SSE blocks and feeds every data payload through each
// dialect. Framing, comments and unknown events are ignored.
//
// One event may carry several data lines, and the payload is their values joined
// by newline - that is the framing, not one payload per line. Decoding each line
// on its own means neither half of a split JSON object parses, so a provider that
// wraps its output at any column would hand the guardrails nothing to judge.
// Blocks that pack independent objects instead are not legal SSE, but their
// output still has to be inspected, so they are read line by line as a fallback.
func (state *collector) readEventStream(body []byte) {
	for _, block := range bytes.Split(bytes.ReplaceAll(body, []byte("\r\n"), []byte("\n")), []byte("\n\n")) {
		values := make([][]byte, 0, 4)
		for _, line := range bytes.Split(block, []byte("\n")) {
			if bytes.HasPrefix(line, []byte("data:")) {
				values = append(values, bytes.TrimPrefix(line, []byte("data:")))
			}
		}
		if len(values) == 0 {
			continue
		}
		if joined := bytes.TrimSpace(bytes.Join(values, []byte("\n"))); json.Valid(joined) {
			state.readObject(joined)
			continue
		}
		for _, value := range values {
			if payload := bytes.TrimSpace(value); len(payload) > 0 {
				state.readObject(payload)
			}
		}
	}
}

// readObject tries one JSON object as every shape it might be. A body is only
// ever one of them, but which one is not knowable from the outside, and trying
// all three costs one decode of an already-parsed map.
func (state *collector) readObject(payload []byte) {
	var object map[string]json.RawMessage
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	if decoder.Decode(&object) != nil {
		return
	}
	state.readResponsesShape(payload)
	state.readChatShape(object)
	state.readAnthropicShape(object, payload)
}

// --- Responses API -------------------------------------------------------

type responsesItem struct {
	ID        string          `json:"id"`
	Type      string          `json:"type"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
	Input     json.RawMessage `json:"input"`
	Content   []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Summary []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"summary"`
}

func (state *collector) readResponsesShape(payload []byte) {
	var event struct {
		Type        string          `json:"type"`
		ItemID      string          `json:"item_id"`
		OutputIndex json.Number     `json:"output_index"`
		Delta       json.RawMessage `json:"delta"`
		Arguments   json.RawMessage `json:"arguments"`
		Text        json.RawMessage `json:"text"`
		Item        *responsesItem  `json:"item"`
		Output      []responsesItem `json:"output"`
		Response    *struct {
			Output []responsesItem `json:"output"`
		} `json:"response"`
	}
	if json.Unmarshal(payload, &event) != nil {
		return
	}
	// A streamed item announces its tool name once and then sends argument
	// fragments that carry only the item id, so the name has to be remembered.
	if event.Item != nil {
		state.readResponsesItem(*event.Item)
	}
	if event.Response != nil {
		for _, item := range event.Response.Output {
			state.readResponsesItem(item)
		}
	}
	for _, item := range event.Output {
		state.readResponsesItem(item)
	}
	if !strings.HasPrefix(event.Type, "response.") {
		return
	}
	key := event.ItemID
	if key == "" {
		key = "output_" + event.OutputIndex.String()
	}
	switch {
	case strings.HasPrefix(event.Type, "response.output_text"),
		strings.HasPrefix(event.Type, "response.refusal"):
		state.add("responses_text_"+key, sourceAssistant, jsonString(event.Delta)+jsonString(event.Text))
	case strings.HasPrefix(event.Type, "response.reasoning"):
		state.add("responses_reasoning_"+key, sourceReasoning, jsonString(event.Delta)+jsonString(event.Text))
	case strings.HasPrefix(event.Type, "response.function_call_arguments"),
		strings.HasPrefix(event.Type, "response.custom_tool_call_input"):
		state.add("responses_args_"+key, state.toolSource(key), jsonString(event.Delta)+jsonString(event.Arguments))
	}
}

func (state *collector) readResponsesItem(item responsesItem) {
	key := item.ID
	if key == "" {
		key = item.Name
	}
	// Item-level content is kept apart from the delta stream of the same item. A
	// completed item repeats everything its deltas already carried, and splicing
	// the two together could invent a match at the seam.
	switch item.Type {
	case "function_call", "custom_tool_call", "tool_call":
		state.noteTool(item.Name)
		state.rememberToolName(key, item.Name)
		state.add("responses_item_args_"+key, sourceToolCall+truncate(item.Name, maxSourceName),
			jsonString(item.Arguments)+jsonString(item.Input))
	case "reasoning":
		for index, part := range item.Summary {
			state.add("responses_item_reasoning_"+key+"_"+itoa(index), sourceReasoning, part.Text)
		}
	default:
		for index, part := range item.Content {
			if part.Text == "" {
				continue
			}
			source := sourceAssistant
			if part.Type == "thinking" {
				source = sourceReasoning
			}
			state.add("responses_item_text_"+key+"_"+itoa(index), source, part.Text)
		}
	}
}

func (state *collector) rememberToolName(key, name string) {
	if state.itemTools == nil {
		state.itemTools = make(map[string]string, 4)
	}
	// Every other collector is bounded, and this one is keyed by an item id the
	// provider chooses, so an answer full of one-fragment items would otherwise
	// grow it for as long as the body lasts.
	if name != "" && len(state.itemTools) < maxPieces {
		state.itemTools[key] = name
	}
}

func (state *collector) toolSource(key string) string {
	if name := state.itemTools[key]; name != "" {
		return sourceToolCall + truncate(name, maxSourceName)
	}
	return sourceToolCall + "unknown"
}

// --- Chat Completions ----------------------------------------------------

func (state *collector) readChatShape(object map[string]json.RawMessage) {
	raw, ok := object["choices"]
	if !ok {
		return
	}
	var choices []struct {
		Index json.Number `json:"index"`
		Delta *struct {
			Content   json.RawMessage `json:"content"`
			Reasoning json.RawMessage `json:"reasoning_content"`
			ToolCalls []chatToolCall  `json:"tool_calls"`
		} `json:"delta"`
		Message *struct {
			Content   json.RawMessage `json:"content"`
			Reasoning json.RawMessage `json:"reasoning_content"`
			ToolCalls []chatToolCall  `json:"tool_calls"`
		} `json:"message"`
	}
	if json.Unmarshal(raw, &choices) != nil {
		return
	}
	for position, choice := range choices {
		key := choice.Index.String()
		if key == "" {
			key = itoa(position)
		}
		if choice.Delta != nil {
			state.add("chat_text_"+key, sourceAssistant, jsonString(choice.Delta.Content))
			state.add("chat_reasoning_"+key, sourceReasoning, jsonString(choice.Delta.Reasoning))
			state.readChatToolCalls(key, choice.Delta.ToolCalls)
		}
		if choice.Message != nil {
			state.add("chat_text_"+key, sourceAssistant, jsonString(choice.Message.Content))
			state.add("chat_reasoning_"+key, sourceReasoning, jsonString(choice.Message.Reasoning))
			state.readChatToolCalls(key, choice.Message.ToolCalls)
		}
	}
}

type chatToolCall struct {
	Index    *json.Number `json:"index"`
	ID       string       `json:"id"`
	Function *struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// readChatToolCalls accumulates argument fragments. Gateways that ignore the
// documented shape send calls in order with no index, so position is the
// fallback key — matching how the relay's own chat accumulator reads them.
func (state *collector) readChatToolCalls(choiceKey string, calls []chatToolCall) {
	for position, call := range calls {
		key := choiceKey + "_"
		switch {
		case call.Index != nil:
			key += call.Index.String()
		case call.ID != "":
			key += call.ID
		default:
			key += itoa(position)
		}
		if call.Function == nil {
			continue
		}
		if call.Function.Name != "" {
			state.noteTool(call.Function.Name)
			state.rememberToolName("chat_"+key, call.Function.Name)
		}
		state.add("chat_args_"+key, state.toolSource("chat_"+key), call.Function.Arguments)
	}
}

// --- Anthropic messages --------------------------------------------------

func (state *collector) readAnthropicShape(object map[string]json.RawMessage, payload []byte) {
	if _, streamed := object["content_block"]; !streamed {
		if _, delta := object["delta"]; !delta {
			if _, blocks := object["content"]; !blocks {
				return
			}
		}
	}
	var event struct {
		Type         string      `json:"type"`
		Index        json.Number `json:"index"`
		ContentBlock *struct {
			Type  string          `json:"type"`
			Name  string          `json:"name"`
			Text  string          `json:"text"`
			Input json.RawMessage `json:"input"`
		} `json:"content_block"`
		Delta *struct {
			Type        string `json:"type"`
			Text        string `json:"text"`
			Thinking    string `json:"thinking"`
			PartialJSON string `json:"partial_json"`
		} `json:"delta"`
		Content []struct {
			Type     string          `json:"type"`
			Name     string          `json:"name"`
			Text     string          `json:"text"`
			Thinking string          `json:"thinking"`
			Input    json.RawMessage `json:"input"`
		} `json:"content"`
	}
	if json.Unmarshal(payload, &event) != nil {
		return
	}
	key := event.Index.String()
	if block := event.ContentBlock; block != nil {
		switch block.Type {
		case "tool_use":
			state.noteTool(block.Name)
			state.rememberToolName("anthropic_"+key, block.Name)
			state.add("anthropic_args_"+key, sourceToolCall+truncate(block.Name, maxSourceName), jsonString(block.Input))
		case "thinking", "redacted_thinking":
			state.add("anthropic_reasoning_"+key, sourceReasoning, block.Text)
		default:
			state.add("anthropic_text_"+key, sourceAssistant, block.Text)
		}
	}
	if delta := event.Delta; delta != nil {
		switch delta.Type {
		case "input_json_delta":
			state.add("anthropic_args_"+key, state.toolSource("anthropic_"+key), delta.PartialJSON)
		case "thinking_delta":
			state.add("anthropic_reasoning_"+key, sourceReasoning, delta.Thinking)
		case "text_delta":
			state.add("anthropic_text_"+key, sourceAssistant, delta.Text)
		}
	}
	for index, block := range event.Content {
		position := itoa(index)
		switch block.Type {
		case "tool_use":
			state.noteTool(block.Name)
			state.add("anthropic_block_args_"+position, sourceToolCall+truncate(block.Name, maxSourceName), jsonString(block.Input))
		case "thinking", "redacted_thinking":
			state.add("anthropic_block_reasoning_"+position, sourceReasoning, block.Text+block.Thinking)
		case "text":
			state.add("anthropic_block_text_"+position, sourceAssistant, block.Text)
		}
	}
}

// --- helpers -------------------------------------------------------------

// jsonString renders a value that may be a string, a structured argument object
// or absent. Tool arguments arrive both as an encoded string and as a real
// object depending on the dialect, and both have to be scanned.
func jsonString(raw json.RawMessage) string {
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return ""
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	return string(raw)
}

func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	cut := limit
	for cut > 0 && value[cut]&0xC0 == 0x80 {
		cut--
	}
	return value[:cut]
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	digits := [20]byte{}
	position := len(digits)
	for value > 0 {
		position--
		digits[position] = byte('0' + value%10)
		value /= 10
	}
	return string(digits[position:])
}
