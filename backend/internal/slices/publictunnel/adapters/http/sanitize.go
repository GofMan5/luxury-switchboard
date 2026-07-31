package tunnelhttp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	relayapp "github.com/luxuryprivate/switchboard/backend/internal/slices/relay/application"
)

var safeEventType = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,128}$`)

func sanitizeResponse(response relayapp.DispatchResponse, path, publicModel string, markers []string, brand string) ([]byte, string, error) {
	if response.Status < 200 || response.Status >= 300 {
		return nil, "", errors.New("upstream status rejected")
	}
	contentType := strings.ToLower(response.Headers.Get("Content-Type"))
	if strings.Contains(contentType, "text/event-stream") {
		body, err := sanitizeSSE(response.Body, path, publicModel, markers, brand)
		return body, "text/event-stream; charset=utf-8", err
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(response.Body))
	decoder.UseNumber()
	if decoder.Decode(&value) != nil {
		return nil, "", errors.New("invalid upstream JSON")
	}
	clean, err := sanitizeJSON(value, publicModel, markers, brand, 0)
	if err != nil {
		return nil, "", err
	}
	if failedValue(clean) {
		return nil, "", errors.New("failed upstream response")
	}
	body, err := json.Marshal(clean)
	if err != nil {
		return nil, "", errors.New("response serialization failed")
	}
	return body, "application/json", nil
}

func sanitizeJSON(value any, publicModel string, markers []string, brand string, depth int) (any, error) {
	if depth > 64 {
		return nil, errors.New("response nesting rejected")
	}
	switch value := value.(type) {
	case map[string]any:
		clean := make(map[string]any, len(value))
		for key, item := range value {
			if privateField(key) {
				continue
			}
			if strings.EqualFold(key, "model") {
				clean[key] = publicModel
				continue
			}
			sanitized, err := sanitizeJSON(item, publicModel, markers, brand, depth+1)
			if err != nil {
				return nil, err
			}
			clean[key] = sanitized
		}
		return clean, nil
	case []any:
		clean := make([]any, len(value))
		for index, item := range value {
			sanitized, err := sanitizeJSON(item, publicModel, markers, brand, depth+1)
			if err != nil {
				return nil, err
			}
			clean[index] = sanitized
		}
		return clean, nil
	case string:
		return redactMarkers(value, markers, brand), nil
	default:
		return value, nil
	}
}

func privateField(key string) bool {
	normalized := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(key, "-", "_"), " ", "_"))
	switch normalized {
	case "owned_by", "provider", "provider_id", "provider_name", "upstream", "upstream_url", "backend", "backend_id", "vendor", "api_key", "authorization", "proxy", "proxy_url", "routing_hint", "internal", "debug",
		"system_fingerprint", "service_tier", "endpoint", "host", "region", "deployment", "cluster", "node", "account", "organization", "trace", "trace_id", "server":
		return true
	}
	return strings.HasPrefix(normalized, "provider_") || strings.HasPrefix(normalized, "upstream_")
}

func failedValue(value any) bool {
	object, ok := value.(map[string]any)
	if !ok {
		return false
	}
	eventType, _ := object["type"].(string)
	if eventType == "error" || eventType == "response.failed" || eventType == "response.incomplete" || eventType == "response.cancelled" {
		return true
	}
	for _, candidate := range []map[string]any{object, mapValue(object["response"])} {
		status, _ := candidate["status"].(string)
		if status == "failed" || status == "incomplete" || status == "cancelled" || candidate["error"] != nil || candidate["incomplete_details"] != nil {
			return true
		}
	}
	return false
}
func mapValue(value any) map[string]any {
	result, _ := value.(map[string]any)
	if result == nil {
		return map[string]any{}
	}
	return result
}

func sanitizeSSE(body []byte, path, publicModel string, markers []string, brand string) ([]byte, error) {
	blocks := splitSSE(body)
	output := bytes.Buffer{}
	terminal := false
	for _, block := range blocks {
		data := eventData(block)
		if len(data) == 0 {
			continue
		}
		if bytes.Equal(data, []byte("[DONE]")) {
			output.WriteString("data: [DONE]\n\n")
			if path == "/v1/chat/completions" || path == "/v1/completions" {
				terminal = true
			}
			continue
		}
		var value any
		if json.Unmarshal(data, &value) != nil {
			return nil, errors.New("invalid SSE event")
		}
		clean, err := sanitizeJSON(value, publicModel, markers, brand, 0)
		if err != nil || failedValue(clean) {
			return nil, errors.New("unsafe SSE event")
		}
		object, _ := clean.(map[string]any)
		eventType, _ := object["type"].(string)
		if eventType == "response.completed" && path == "/v1/responses" {
			terminal = true
		}
		if eventType == "message_stop" && path == "/v1/messages" {
			terminal = true
		}
		encoded, _ := json.Marshal(clean)
		if safeEventType.MatchString(eventType) {
			fmt.Fprintf(&output, "event: %s\n", eventType)
		}
		output.WriteString("data: ")
		output.Write(encoded)
		output.WriteString("\n\n")
	}
	if !terminal {
		return nil, errors.New("SSE terminal event missing")
	}
	return output.Bytes(), nil
}

func splitSSE(body []byte) [][]byte {
	normalized := bytes.ReplaceAll(body, []byte("\r\n"), []byte("\n"))
	return bytes.Split(normalized, []byte("\n\n"))
}
func eventData(block []byte) []byte {
	lines := bytes.Split(block, []byte("\n"))
	parts := make([][]byte, 0)
	for _, line := range lines {
		if bytes.HasPrefix(line, []byte("data:")) {
			value := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
			parts = append(parts, value)
		}
	}
	return bytes.Join(parts, []byte("\n"))
}

func redactMarkers(value string, markers []string, brand string) string {
	if brand == "" {
		brand = "Luxury Private"
	}
	for _, marker := range markers {
		marker = strings.TrimSpace(marker)
		if len(marker) < 4 {
			continue
		}
		for {
			lower := strings.ToLower(value)
			index := strings.Index(lower, strings.ToLower(marker))
			if index < 0 {
				break
			}
			value = value[:index] + brand + value[index+len(marker):]
		}
	}
	return value
}
