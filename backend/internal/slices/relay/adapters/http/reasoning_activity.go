package relayhttp

import (
	"encoding/json"
	"strings"
)

// maxThinkingSpans bounds how many times one stream may report entering its
// reasoning phase. An honest provider reasons once, or a handful of times
// when thinking interleaves with tool calls; a stream alternating every
// event must not turn into one activity frame per event. Leaving the phase
// is always reported, so a capped stream never stays marked as thinking.
const maxThinkingSpans = 16

// requestReasoning reads the reasoning level a client asked for: the effort
// name, and the thinking-token budget where the dialect speaks in budgets.
// Only these parameters are read — never content — and only the documented
// spellings: Responses `reasoning.effort` / `reasoning.max_tokens`, chat
// `reasoning_effort`, Anthropic `thinking` / `output_config.effort`, and
// the `enable_thinking` / `thinking_budget` pair of Qwen-style providers.
// The activity slice allowlists the name again; this only finds it.
func requestReasoning(body []byte, contentType string) (string, int64) {
	if !strings.Contains(strings.ToLower(contentType), "json") {
		return "", 0
	}
	// RawMessage fields: a field of an unexpected type leaves that one
	// field empty instead of failing the whole read, and the fields this
	// does not name — the prompt — are skipped, not copied.
	var payload struct {
		Reasoning       json.RawMessage `json:"reasoning"`
		ReasoningEffort json.RawMessage `json:"reasoning_effort"`
		OutputConfig    json.RawMessage `json:"output_config"`
		Thinking        json.RawMessage `json:"thinking"`
		EnableThinking  json.RawMessage `json:"enable_thinking"`
		ThinkingBudget  json.RawMessage `json:"thinking_budget"`
	}
	if json.Unmarshal(body, &payload) != nil {
		return "", 0
	}
	var reasoning struct {
		Effort    json.RawMessage `json:"effort"`
		MaxTokens json.RawMessage `json:"max_tokens"`
		Enabled   json.RawMessage `json:"enabled"`
	}
	var outputConfig struct {
		Effort json.RawMessage `json:"effort"`
	}
	var thinking struct {
		Type         json.RawMessage `json:"type"`
		BudgetTokens json.RawMessage `json:"budget_tokens"`
	}
	// Absent or non-object values leave the zero struct: nothing to read.
	_ = json.Unmarshal(payload.Reasoning, &reasoning)
	_ = json.Unmarshal(payload.OutputConfig, &outputConfig)
	_ = json.Unmarshal(payload.Thinking, &thinking)

	effort := ""
	for _, candidate := range []json.RawMessage{reasoning.Effort, payload.ReasoningEffort, outputConfig.Effort} {
		if name := reasoningEffortName(jsonString(candidate)); name != "" {
			effort = name
			break
		}
	}
	budget := int64(0)
	for _, candidate := range []json.RawMessage{reasoning.MaxTokens, thinking.BudgetTokens, payload.ThinkingBudget} {
		if tokens := jsonTokens(candidate); tokens > 0 {
			budget = tokens
			break
		}
	}
	if effort == "" {
		switch strings.ToLower(jsonString(thinking.Type)) {
		case "enabled":
			effort = "on"
		case "adaptive", "auto":
			effort = "auto"
		case "disabled":
			effort = "none"
		}
	}
	for _, toggle := range []json.RawMessage{payload.EnableThinking, reasoning.Enabled} {
		if effort != "" {
			break
		}
		if enabled, ok := jsonBool(toggle); ok {
			effort = "none"
			if enabled {
				effort = "on"
			}
		}
	}
	if effort == "" && budget > 0 {
		effort = "on"
	}
	if effort == "none" {
		// A budget on a request that turned reasoning off is not a budget
		// anything will spend.
		budget = 0
	}
	return effort, budget
}

func reasoningEffortName(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case "none", "minimal", "low", "medium", "high", "xhigh", "max":
		return value
	default:
		return ""
	}
}

func jsonString(raw json.RawMessage) string {
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return ""
	}
	return value
}

func jsonBool(raw json.RawMessage) (bool, bool) {
	var value bool
	if json.Unmarshal(raw, &value) != nil {
		return false, false
	}
	return value, true
}

func jsonTokens(raw json.RawMessage) int64 {
	var value int64
	if json.Unmarshal(raw, &value) != nil || value < 0 {
		return 0
	}
	return value
}

// reasoningPhase classifies one stream event: whether it shows the provider
// reasoning (true) or past its reasoning — answering, calling a tool, or
// closing the reasoning item (false). known is false for events that say
// nothing about the phase: lifecycle frames, usage, keep-alives.
func reasoningPhase(path, eventType string, payload map[string]any) (thinking bool, known bool) {
	switch {
	case responsesDialectPath(path):
		switch eventType {
		case "response.output_item.added":
			kind := nestedType(payload, "item")
			return kind == "reasoning", kind != ""
		case "response.output_item.done":
			// The reasoning item closing ends the phase; any other item
			// closing says nothing the item's own deltas did not.
			if nestedType(payload, "item") == "reasoning" {
				return false, true
			}
			return false, false
		}
		if strings.HasPrefix(eventType, "response.reasoning") {
			return true, true
		}
		if strings.HasSuffix(eventType, ".delta") {
			return false, true
		}
		return false, false
	case messagesDialectPath(path):
		switch eventType {
		case "content_block_start":
			kind := nestedType(payload, "content_block")
			return kind == "thinking" || kind == "redacted_thinking", kind != ""
		case "content_block_delta":
			switch kind := nestedType(payload, "delta"); kind {
			case "thinking_delta", "signature_delta":
				return true, true
			case "":
				return false, false
			}
			return false, true
		}
		return false, false
	case chatDialectPath(path):
		choices, _ := payload["choices"].([]any)
		reasoning := false
		for _, choice := range choices {
			item, _ := choice.(map[string]any)
			delta, _ := item["delta"].(map[string]any)
			if delta == nil {
				continue
			}
			// The answer starting wins over reasoning in the same chunk:
			// content arriving is the end of the thinking phase.
			if nonEmptyString(delta["content"]) || nonEmptyList(delta["tool_calls"]) {
				return false, true
			}
			if nonEmptyString(delta["reasoning_content"]) || nonEmptyString(delta["reasoning"]) || nonEmptyList(delta["reasoning_details"]) {
				reasoning = true
			}
		}
		return reasoning, reasoning
	}
	return false, false
}

func nestedType(payload map[string]any, field string) string {
	nested, _ := payload[field].(map[string]any)
	kind, _ := nested["type"].(string)
	return kind
}

func nonEmptyString(value any) bool {
	text, _ := value.(string)
	return text != ""
}

func nonEmptyList(value any) bool {
	list, _ := value.([]any)
	return len(list) > 0
}

// notePhase reports the stream's reasoning phase on its transitions only.
func (inspector *sseInspector) notePhase(eventType string, payload map[string]any) {
	if inspector.onThinking == nil {
		return
	}
	thinking, known := reasoningPhase(inspector.path, eventType, payload)
	if !known || thinking == inspector.thinking {
		return
	}
	if thinking {
		if inspector.thinkingSpans >= maxThinkingSpans {
			return
		}
		inspector.thinkingSpans++
	}
	inspector.thinking = thinking
	inspector.onThinking(thinking)
}
