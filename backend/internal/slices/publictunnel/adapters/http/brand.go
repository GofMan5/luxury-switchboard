package tunnelhttp

import (
	"encoding/json"
	"strings"
)

var providerProbePhrases = []string{
	"what provider", "which provider", "who is the provider", "what upstream", "which upstream",
	"what backend", "which backend", "what vendor", "which vendor", "what is your source",
	"where do you get", "api endpoint", "who hosts", "what host", "what origin",
	"какой провайдер", "что за провайдер", "кто провайдер", "какой апстрим", "какой бэкенд",
	"какой источник", "откуда модели", "чей api", "чей апи", "где хостится",
}

func providerProbe(payload map[string]any, path string) bool {
	if path != "/v1/responses" && path != "/v1/chat/completions" && path != "/v1/completions" && path != "/v1/messages" {
		return false
	}
	stack := []any{payload["input"], payload["messages"], payload["prompt"]}
	for visited := 0; len(stack) > 0 && visited < 2048; visited++ {
		last := len(stack) - 1
		value := stack[last]
		stack = stack[:last]
		switch value := value.(type) {
		case string:
			text := strings.ToLower(value)
			for _, phrase := range providerProbePhrases {
				if strings.Contains(text, phrase) {
					return true
				}
			}
		case []any:
			stack = append(stack, value...)
		case map[string]any:
			for key, child := range value {
				if key == "content" || key == "text" || key == "input_text" {
					stack = append(stack, child)
				}
			}
		}
	}
	return false
}

func localBrandResponse(path, model string, payload map[string]any, brand string) ([]byte, string) {
	if brand == "" {
		brand = "Luxury Private"
	}
	stream, _ := payload["stream"].(bool)
	if stream {
		return streamBrandResponse(path, model, brand), "text/event-stream; charset=utf-8"
	}
	var value any
	switch path {
	case "/v1/responses":
		value = map[string]any{"id": "luxury-private", "object": "response", "status": "completed", "model": model, "output": []any{map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]string{"type": "output_text", "text": brand}}}}, "error": nil, "incomplete_details": nil}
	case "/v1/messages":
		value = map[string]any{"id": "luxury-private", "type": "message", "role": "assistant", "model": model, "content": []any{map[string]string{"type": "text", "text": brand}}, "stop_reason": "end_turn", "stop_sequence": nil, "usage": map[string]int{"input_tokens": 0, "output_tokens": 0}}
	case "/v1/completions":
		value = map[string]any{"id": "luxury-private", "object": "text_completion", "model": model, "choices": []any{map[string]any{"index": 0, "text": brand, "finish_reason": "stop"}}}
	default:
		value = map[string]any{"id": "luxury-private", "object": "chat.completion", "model": model, "choices": []any{map[string]any{"index": 0, "message": map[string]string{"role": "assistant", "content": brand}, "finish_reason": "stop"}}}
	}
	body, _ := json.Marshal(value)
	return body, "application/json"
}

func streamBrandResponse(path, model, brand string) []byte {
	var events []any
	namedEvents := false
	switch path {
	case "/v1/responses":
		events = []any{
			map[string]any{"type": "response.output_text.delta", "delta": brand},
			map[string]any{
				"type": "response.completed",
				"response": map[string]any{
					"id": "luxury-private", "status": "completed", "model": model,
					"output": []any{map[string]any{
						"type": "message", "role": "assistant",
						"content": []any{map[string]string{"type": "output_text", "text": brand}},
					}},
					"error": nil, "incomplete_details": nil,
				},
			},
		}
	case "/v1/messages":
		namedEvents = true
		events = []any{
			map[string]any{"type": "message_start", "message": map[string]any{"id": "luxury-private", "type": "message", "role": "assistant", "model": model, "content": []any{}, "stop_reason": nil, "stop_sequence": nil, "usage": map[string]int{"input_tokens": 0, "output_tokens": 0}}},
			map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]string{"type": "text", "text": ""}},
			map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]string{"type": "text_delta", "text": brand}},
			map[string]any{"type": "content_block_stop", "index": 0},
			map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": "end_turn", "stop_sequence": nil}, "usage": map[string]int{"output_tokens": 0}},
			map[string]any{"type": "message_stop"},
		}
	case "/v1/completions":
		events = []any{map[string]any{"id": "luxury-private", "object": "text_completion", "model": model, "choices": []any{map[string]any{"index": 0, "text": brand, "finish_reason": "stop"}}}}
	default:
		events = []any{map[string]any{"id": "luxury-private", "object": "chat.completion.chunk", "model": model, "choices": []any{map[string]any{"index": 0, "delta": map[string]string{"content": brand}, "finish_reason": "stop"}}}}
	}
	var output strings.Builder
	for _, event := range events {
		encoded, _ := json.Marshal(event)
		if namedEvents {
			if object, ok := event.(map[string]any); ok {
				if eventType, ok := object["type"].(string); ok {
					output.WriteString("event: ")
					output.WriteString(eventType)
					output.WriteByte('\n')
				}
			}
		}
		output.WriteString("data: ")
		output.Write(encoded)
		output.WriteString("\n\n")
	}
	if path != "/v1/responses" && path != "/v1/messages" {
		output.WriteString("data: [DONE]\n\n")
	}
	return []byte(output.String())
}
