package tunnelhttp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode"

	relayapp "github.com/luxuryprivate/switchboard/backend/internal/slices/relay/application"
)

var safeEventType = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,128}$`)

func sanitizeResponse(response relayapp.DispatchResponse, path, publicModel string, markers []string, brand string) ([]byte, string, error) {
	if response.Status < 200 || response.Status >= 300 {
		return nil, "", errors.New("upstream status rejected")
	}
	contentType := strings.ToLower(response.Headers.Get("Content-Type"))
	redactor := newMarkerRedactor(markers, brand)
	if strings.Contains(contentType, "text/event-stream") {
		body, err := sanitizeSSE(response.Body, path, publicModel, redactor)
		return body, "text/event-stream; charset=utf-8", err
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(response.Body))
	decoder.UseNumber()
	if decoder.Decode(&value) != nil {
		return nil, "", errors.New("invalid upstream JSON")
	}
	clean, err := sanitizeJSON(value, publicModel, redactor, 0)
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
	if redactor.contains(body) {
		return nil, "", errors.New("sensitive response marker remains")
	}
	return body, "application/json", nil
}

func sanitizeJSON(value any, publicModel string, redactor markerRedactor, depth int) (any, error) {
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
			if modelField(key) {
				clean[key] = publicModel
				continue
			}
			sanitized, err := sanitizeJSON(item, publicModel, redactor, depth+1)
			if err != nil {
				return nil, err
			}
			clean[key] = sanitized
		}
		return clean, nil
	case []any:
		clean := make([]any, len(value))
		for index, item := range value {
			sanitized, err := sanitizeJSON(item, publicModel, redactor, depth+1)
			if err != nil {
				return nil, err
			}
			clean[index] = sanitized
		}
		return clean, nil
	case string:
		return redactor.replace(value), nil
	default:
		return value, nil
	}
}

func privateField(key string) bool {
	normalized := normalizeField(key)
	switch normalized {
	case "ownedby", "provider", "providerid", "providername", "upstream", "upstreamurl", "backend", "backendid", "vendor", "apikey", "authorization", "proxy", "proxyurl", "routinghint", "internal", "debug",
		"systemfingerprint", "servicetier", "endpoint", "host", "region", "deployment", "cluster", "node", "account", "organization", "trace", "traceid", "server":
		return true
	}
	return strings.HasPrefix(normalized, "provider") || strings.HasPrefix(normalized, "upstream") || strings.HasPrefix(normalized, "internal") || strings.HasPrefix(normalized, "debug")
}

func modelField(key string) bool {
	normalized := normalizeField(key)
	return normalized == "model" || normalized == "modelid" || normalized == "modelname"
}

func normalizeField(key string) string {
	return strings.Map(func(character rune) rune {
		if unicode.IsLetter(character) || unicode.IsDigit(character) {
			return unicode.ToLower(character)
		}
		return -1
	}, key)
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

func sanitizeSSE(body []byte, path, publicModel string, redactor markerRedactor) ([]byte, error) {
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
		clean, err := sanitizeJSON(value, publicModel, redactor, 0)
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
	if redactor.contains(output.Bytes()) {
		return nil, errors.New("sensitive SSE marker remains")
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
	return newMarkerRedactor(markers, brand).replace(value)
}

type markerReplacement struct {
	pattern     *regexp.Regexp
	replacement string
	replace     bool
}

type markerRedactor []markerReplacement

func newMarkerRedactor(markers []string, brand string) markerRedactor {
	if brand == "" {
		brand = "Luxury Private"
	}
	redactor := make(markerRedactor, 0, len(markers))
	for _, marker := range markers {
		marker = strings.TrimSpace(marker)
		if marker == "" {
			continue
		}
		replacement := brand
		if strings.Contains(strings.ToLower(replacement), strings.ToLower(marker)) {
			replacement = "[hidden]"
			if strings.Contains(strings.ToLower(replacement), strings.ToLower(marker)) {
				replacement = ""
			}
		}
		pattern, err := regexp.Compile("(?i:" + regexp.QuoteMeta(marker) + ")")
		if err == nil {
			redactor = append(redactor, markerReplacement{pattern: pattern, replacement: replacement, replace: len(marker) >= 4})
		}
	}
	return redactor
}

func (redactor markerRedactor) replace(value string) string {
	for _, marker := range redactor {
		if !marker.replace {
			continue
		}
		value = marker.pattern.ReplaceAllStringFunc(value, func(string) string { return marker.replacement })
	}
	return value
}

func (redactor markerRedactor) contains(body []byte) bool {
	for _, marker := range redactor {
		if marker.pattern.Match(body) {
			return true
		}
	}
	return false
}
