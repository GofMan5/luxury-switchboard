package application

import (
	"bytes"
	"encoding/json"
)

// RequestDeclaresTools reports whether the client offered the model any tool at
// all. A tool call in an answer to a request that declared none is not a
// borderline judgement — the client has nothing to run it with, so the provider
// put it there.
//
// Every dialect is checked, because the relay rewrites requests between them:
// Responses tools[], the additional_tools input items recent Codex builds send
// instead, Chat Completions tools[]/functions[], and Anthropic tools[]. A turn
// that continues a tool exchange counts too: clients that already ran a tool
// sometimes drop the declaration on the follow-up.
//
// Anything unreadable answers true. Guessing false would accuse the provider of
// an unsolicited call on a request this code simply failed to understand, and
// that misfire costs the caller a legitimate answer. The pattern rules still
// inspect such a response; only this one anomaly is withheld.
func RequestDeclaresTools(body []byte) bool {
	if len(body) == 0 {
		return false
	}
	// Fields are decoded one at a time. A single mismatch — Responses input is a
	// bare string as often as an array — must not take the others down with it.
	var fields map[string]json.RawMessage
	if json.Unmarshal(body, &fields) != nil {
		return true
	}
	if nonEmptyArray(fields["tools"]) || nonEmptyArray(fields["functions"]) {
		return true
	}
	return declaresThroughItems(fields["input"]) || declaresThroughItems(fields["messages"])
}

// declaresThroughItems looks inside a conversation for evidence that tools are
// in play: the nested declaration recent Codex builds send, and the calls and
// results a continued exchange carries.
func declaresThroughItems(raw json.RawMessage) bool {
	var items []json.RawMessage
	if !isArray(raw) || json.Unmarshal(raw, &items) != nil {
		return false
	}
	for _, raw := range items {
		var item struct {
			Type      string            `json:"type"`
			Tools     json.RawMessage   `json:"tools"`
			ToolCalls json.RawMessage   `json:"tool_calls"`
			Content   []json.RawMessage `json:"content"`
		}
		if json.Unmarshal(raw, &item) != nil {
			continue
		}
		if item.Type == "additional_tools" && nonEmptyArray(item.Tools) {
			return true
		}
		if nonEmptyArray(item.ToolCalls) || toolExchangeType(item.Type) {
			return true
		}
		// Anthropic carries tool_use and tool_result as content blocks.
		for _, block := range item.Content {
			var typed struct {
				Type string `json:"type"`
			}
			if json.Unmarshal(block, &typed) == nil && toolExchangeType(typed.Type) {
				return true
			}
		}
	}
	return false
}

func toolExchangeType(value string) bool {
	switch value {
	case "function_call", "function_call_output", "custom_tool_call", "custom_tool_call_output",
		"tool_use", "tool_result", "tool":
		return true
	default:
		return false
	}
}

func nonEmptyArray(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return false
	}
	// A shape this code does not recognise counts as a declaration for the same
	// reason an unreadable body does: only the false direction accuses anyone.
	if trimmed[0] != '[' {
		return true
	}
	var entries []json.RawMessage
	if json.Unmarshal(trimmed, &entries) != nil {
		return true
	}
	return len(entries) > 0
}

func isArray(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && trimmed[0] == '['
}
