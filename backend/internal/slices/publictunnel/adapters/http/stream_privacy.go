package tunnelhttp

import (
	"errors"
	"fmt"
	"strings"
)

// The public answer is already buffered. Sweep the strings clients concatenate
// once, before branding or writing anything; event-local redaction cannot hide a
// marker spanning two deltas. Such answers are refused rather than rewriting
// tool arguments or moving text between events.
type streamStrings struct {
	values map[string]*strings.Builder
	items  map[string]string
}

func (streams *streamStrings) collect(value any, path string) error {
	object, ok := value.(map[string]any)
	if !ok {
		return errors.New("invalid SSE object")
	}
	if streams.values == nil {
		streams.values = make(map[string]*strings.Builder)
		streams.items = make(map[string]string)
	}
	switch path {
	case "/v1/chat/completions", "/v1/completions":
		choices, ok := object["choices"].([]any)
		if object["choices"] != nil && !ok {
			return errors.New("invalid SSE choices")
		}
		for position, raw := range choices {
			choice, ok := raw.(map[string]any)
			if !ok {
				return errors.New("invalid SSE choice")
			}
			index, err := streamIndex(choice, "index", position)
			if err != nil {
				return err
			}
			key := fmt.Sprintf("choice/%d", index)
			if path == "/v1/completions" {
				streams.add(key+"/text", choice["text"])
			} else if choice["delta"] != nil {
				delta, ok := choice["delta"].(map[string]any)
				if !ok {
					return errors.New("invalid SSE choice delta")
				}
				if err := streams.walk(delta, key); err != nil {
					return err
				}
			}
		}
	case "/v1/responses":
		event, _ := object["type"].(string)
		if event == "response.output_item.added" {
			identity := map[string]any{}
			if index, exists := object["output_index"]; exists {
				identity["output_index"] = index
			}
			if item, ok := object["item"].(map[string]any); ok {
				if id, exists := item["id"]; exists {
					identity["item_id"] = id
				}
			}
			key, err := streams.responseItem(identity)
			if err != nil {
				return err
			}
			item, ok := object["item"].(map[string]any)
			if !ok {
				return errors.New("invalid SSE item")
			}
			streams.addResponsePart(key, "response.function_call_arguments.delta", 0, item["arguments"])
			streams.addResponsePart(key, "response.custom_tool_call_input.delta", 0, item["input"])
			if content, ok := item["content"].([]any); ok {
				for index, part := range content {
					if err := streams.responsePart(key, index, part, false); err != nil {
						return err
					}
				}
			}
			if summary, ok := item["summary"].([]any); ok {
				for index, part := range summary {
					if err := streams.responsePart(key, index, part, true); err != nil {
						return err
					}
				}
			}
			return nil
		}
		if event == "response.content_part.added" || event == "response.reasoning_summary_part.added" {
			item, err := streams.responseItem(object)
			if err != nil {
				return err
			}
			summary := event == "response.reasoning_summary_part.added"
			field := "content_index"
			if summary {
				field = "summary_index"
			}
			index, err := streamIndex(object, field, 0)
			if err != nil {
				return err
			}
			return streams.responsePart(item, index, object["part"], summary)
		}
		if !strings.HasSuffix(event, ".delta") {
			return nil
		}
		delta, ok := object["delta"].(string)
		if !ok {
			// Image and other structured deltas have no concatenated string here.
			if object["delta"] != nil {
				return errors.New("invalid SSE delta")
			}
			return nil
		}
		item, err := streams.responseItem(object)
		if err != nil {
			return err
		}
		content, err := streamIndex(object, "content_index", 0)
		if err != nil {
			return err
		}
		summary, err := streamIndex(object, "summary_index", 0)
		if err != nil {
			return err
		}
		streams.add(fmt.Sprintf("%s/%s/%d/%d", item, event, content, summary), delta)
	case "/v1/messages":
		event, _ := object["type"].(string)
		if event != "content_block_start" && event != "content_block_delta" {
			return nil
		}
		index, err := streamIndex(object, "index", 0)
		if err != nil {
			return err
		}
		block := object["delta"]
		if event == "content_block_start" {
			block = object["content_block"]
		}
		return streams.walk(block, fmt.Sprintf("block/%d", index))
	}
	return nil
}

func (streams *streamStrings) responsePart(item string, index int, raw any, summary bool) error {
	part, ok := raw.(map[string]any)
	if !ok {
		return errors.New("invalid SSE content part")
	}
	if summary {
		streams.add(fmt.Sprintf("%s/response.reasoning_summary_text.delta/0/%d", item, index), part["text"])
		return nil
	}
	streams.addResponsePart(item, "response.output_text.delta", index, part["text"])
	streams.addResponsePart(item, "response.refusal.delta", index, part["refusal"])
	return nil
}

func (streams *streamStrings) addResponsePart(item, event string, content int, value any) {
	streams.add(fmt.Sprintf("%s/%s/%d/0", item, event, content), value)
}

func streamIndex(object map[string]any, field string, fallback int) (int, error) {
	value, exists := object[field]
	if !exists {
		return fallback, nil
	}
	number, ok := value.(float64)
	if !ok || number < 0 || number > 1<<53-1 || number != float64(int64(number)) {
		return 0, errors.New("invalid SSE index")
	}
	return int(number), nil
}

// Resolve both wire identities to one item; a late bridge between independently
// accumulated items is ambiguous and fails closed instead of losing ordering.
func (streams *streamStrings) responseItem(object map[string]any) (string, error) {
	aliases := []string{}
	if _, exists := object["output_index"]; exists {
		index, err := streamIndex(object, "output_index", 0)
		if err != nil {
			return "", err
		}
		aliases = append(aliases, fmt.Sprintf("index/%d", index))
	}
	if raw, exists := object["item_id"]; exists {
		id, ok := raw.(string)
		if !ok || id == "" {
			return "", errors.New("invalid SSE item")
		}
		aliases = append(aliases, "id/"+id)
	}
	if len(aliases) == 0 {
		aliases = append(aliases, "index/0")
	}
	key := ""
	for _, alias := range aliases {
		if previous := streams.items[alias]; previous != "" {
			if key != "" && key != previous {
				return "", errors.New("ambiguous SSE item")
			}
			key = previous
		}
	}
	if key == "" {
		key = fmt.Sprintf("item/%d", len(streams.items))
	}
	for _, alias := range aliases {
		streams.items[alias] = key
	}
	return key, nil
}

func (streams *streamStrings) walk(value any, key string) error {
	switch value := value.(type) {
	case map[string]any:
		for field, child := range value {
			if field == "type" || field == "role" || field == "index" {
				continue
			}
			// Only wire fields whose strings are accumulated by clients need
			// shape validation; annotations may legitimately carry numbers.
			switch field {
			case "text", "refusal", "thinking", "partial_json", "arguments", "name", "id":
				if child != nil {
					if _, ok := child.(string); !ok {
						return errors.New("invalid SSE string delta")
					}
				}
			case "tool_calls":
				if _, ok := child.([]any); child != nil && !ok {
					return errors.New("invalid SSE tool calls")
				}
			case "function", "function_call":
				if _, ok := child.(map[string]any); child != nil && !ok {
					return errors.New("invalid SSE function")
				}
			}
			if err := streams.walk(child, key+"/"+field); err != nil {
				return err
			}
		}
	case []any:
		for position, child := range value {
			if strings.HasSuffix(key, "/tool_calls") {
				if _, ok := child.(map[string]any); !ok {
					return errors.New("invalid SSE tool call")
				}
			}
			index := position
			if object, ok := child.(map[string]any); ok {
				var err error
				index, err = streamIndex(object, "index", position)
				if err != nil {
					return err
				}
			}
			if err := streams.walk(child, fmt.Sprintf("%s/%d", key, index)); err != nil {
				return err
			}
		}
	case string:
		streams.add(key, value)
	}
	return nil
}

func (streams *streamStrings) add(key string, value any) {
	text, ok := value.(string)
	if !ok || text == "" {
		return
	}
	buffer := streams.values[key]
	if buffer == nil {
		buffer = &strings.Builder{}
		streams.values[key] = buffer
	}
	buffer.WriteString(text)
}

func (streams *streamStrings) sensitive(redactor markerRedactor) bool {
	for _, value := range streams.values {
		if redactor.contains([]byte(value.String())) {
			return true
		}
	}
	return false
}
