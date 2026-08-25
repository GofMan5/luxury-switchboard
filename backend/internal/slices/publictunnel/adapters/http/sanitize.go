package tunnelhttp

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	safeBrand := brand
	if strings.TrimSpace(safeBrand) == "" {
		safeBrand = ""
	}
	if strings.Contains(contentType, "text/event-stream") {
		body, err := sanitizeSSE(response.Body, path, publicModel, redactor, safeBrand)
		return body, "text/event-stream; charset=utf-8", err
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(response.Body))
	decoder.UseNumber()
	if decoder.Decode(&value) != nil {
		return nil, "", errors.New("invalid upstream JSON")
	}
	clean, err := sanitizeJSONPath(value, publicModel, redactor, path, 0)
	if err != nil {
		return nil, "", err
	}
	if failedValue(clean) {
		return nil, "", errors.New("failed upstream response")
	}
	neutralizeResponseID(clean, path)
	if containsSensitiveJSON(clean, redactor, path) {
		return nil, "", errors.New("sensitive response marker remains")
	}
	prefixResponseBrand(clean, path, safeBrand)
	body, err := json.Marshal(clean)
	if err != nil {
		return nil, "", errors.New("response serialization failed")
	}
	return body, "application/json", nil
}

func sanitizeJSONPath(value any, publicModel string, redactor markerRedactor, path string, depth int) (any, error) {
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
			if opaqueImageField(path, key, value) {
				encoded, ok := item.(string)
				if !ok || !validImageBase64(encoded) {
					return nil, errors.New("invalid image data")
				}
				clean[key] = encoded
				continue
			}
			sanitized, err := sanitizeJSONPath(item, publicModel, redactor, path, depth+1)
			if err != nil {
				return nil, err
			}
			clean[key] = sanitized
		}
		return clean, nil
	case []any:
		clean := make([]any, len(value))
		for index, item := range value {
			sanitized, err := sanitizeJSONPath(item, publicModel, redactor, path, depth+1)
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

func opaqueImageField(path, key string, object map[string]any) bool {
	if key == "b64_json" && (path == "/v1/images/generations" || path == "/v1/images/edits") {
		return true
	}
	eventType, _ := object["type"].(string)
	if eventType != "image_generation_call" && !strings.Contains(eventType, "image_generation") {
		return false
	}
	return key == "result" || key == "b64_json" || key == "partial_image" || key == "partial_image_b64"
}

func validImageBase64(value string) bool {
	if value == "" || len(value)%4 != 0 {
		return false
	}
	decoder := base64.NewDecoder(base64.StdEncoding.Strict(), strings.NewReader(value))
	header := make([]byte, 12)
	count, err := io.ReadFull(decoder, header)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
		return false
	}
	if _, err = io.Copy(io.Discard, decoder); err != nil {
		return false
	}
	header = header[:count]
	return bytes.HasPrefix(header, []byte("\x89PNG\r\n\x1a\n")) ||
		bytes.HasPrefix(header, []byte("\xff\xd8\xff")) ||
		(len(header) >= 12 && bytes.Equal(header[:4], []byte("RIFF")) && bytes.Equal(header[8:12], []byte("WEBP")))
}

func containsSensitiveJSON(value any, redactor markerRedactor, path string) bool {
	switch value := value.(type) {
	case map[string]any:
		for key, item := range value {
			if redactor.contains([]byte(key)) {
				return true
			}
			if opaqueImageField(path, key, value) {
				continue
			}
			if containsSensitiveJSON(item, redactor, path) {
				return true
			}
		}
	case []any:
		for _, item := range value {
			if containsSensitiveJSON(item, redactor, path) {
				return true
			}
		}
	case string:
		return redactor.contains([]byte(value))
	}
	return false
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

func sanitizeSSE(body []byte, path, publicModel string, redactor markerRedactor, brand string) ([]byte, error) {
	blocks := splitSSE(body)
	output := bytes.Buffer{}
	terminal := false
	finished := false
	brandState := streamBrandState{choices: make(map[int]bool)}
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
		clean, err := sanitizeJSONPath(value, publicModel, redactor, path, 0)
		if err != nil || failedValue(clean) {
			return nil, errors.New("unsafe SSE event")
		}
		neutralizeResponseID(clean, path)
		if containsSensitiveJSON(clean, redactor, path) {
			return nil, errors.New("sensitive SSE marker remains")
		}
		prefixStreamBrand(clean, path, brand, &brandState)
		object, _ := clean.(map[string]any)
		eventType, _ := object["type"].(string)
		if eventType == "response.completed" && path == "/v1/responses" {
			terminal = true
		}
		if eventType == "message_stop" && path == "/v1/messages" {
			terminal = true
		}
		if (path == "/v1/chat/completions" || path == "/v1/completions") && chatFinished(object) {
			finished = true
		}
		encoded, _ := json.Marshal(clean)
		if safeEventType.MatchString(eventType) {
			fmt.Fprintf(&output, "event: %s\n", eventType)
		}
		output.WriteString("data: ")
		output.Write(encoded)
		output.WriteString("\n\n")
	}
	if !terminal && finished {
		output.WriteString("data: [DONE]\n\n")
		terminal = true
	}
	if !terminal {
		return nil, errors.New("SSE terminal event missing")
	}
	return output.Bytes(), nil
}

func prefixResponseBrand(value any, path, brand string) {
	if brand == "" {
		return
	}
	object, _ := value.(map[string]any)
	if object == nil {
		return
	}
	switch path {
	case "/v1/chat/completions":
		choices, _ := object["choices"].([]any)
		for _, rawChoice := range choices {
			choice, _ := rawChoice.(map[string]any)
			message, _ := choice["message"].(map[string]any)
			prefixTextField(message, "content", brand)
		}
	case "/v1/completions":
		choices, _ := object["choices"].([]any)
		for _, rawChoice := range choices {
			choice, _ := rawChoice.(map[string]any)
			prefixTextField(choice, "text", brand)
		}
	case "/v1/responses":
		prefixResponsesBrand(object, brand)
	case "/v1/messages":
		prefixTextField(object, "content", brand)
	}
}

func prefixResponsesBrand(response map[string]any, brand string) {
	output, _ := response["output"].([]any)
	for _, rawItem := range output {
		item, _ := rawItem.(map[string]any)
		if prefixTextField(item, "content", brand) {
			break
		}
	}
	prefixTextField(response, "output_text", brand)
}

func prefixTextField(target map[string]any, field, brand string) bool {
	if target == nil {
		return false
	}
	switch value := target[field].(type) {
	case string:
		if value == "" {
			return false
		}
		target[field] = prefixedText(value, brand)
		return true
	case []any:
		for index, rawPart := range value {
			switch part := rawPart.(type) {
			case string:
				if part != "" {
					value[index] = prefixedText(part, brand)
					return true
				}
			case map[string]any:
				if text, ok := part["text"].(string); ok && text != "" {
					part["text"] = prefixedText(text, brand)
					return true
				}
			}
		}
	}
	return false
}

func prefixedText(text, brand string) string {
	if brand == "" || strings.HasPrefix(text, brand) {
		return text
	}
	return brand + "\n\n" + text
}

type streamBrandState struct {
	text    bool
	choices map[int]bool
}

func prefixStreamBrand(value any, path, brand string, state *streamBrandState) {
	if brand == "" || state == nil {
		return
	}
	object, _ := value.(map[string]any)
	if object == nil {
		return
	}
	switch path {
	case "/v1/chat/completions":
		prefixStreamChoices(object, "content", brand, state)
	case "/v1/completions":
		prefixStreamChoices(object, "text", brand, state)
	case "/v1/responses":
		eventType, _ := object["type"].(string)
		if eventType == "response.output_text.delta" && !state.text {
			if delta, ok := object["delta"].(string); ok && delta != "" {
				object["delta"] = prefixedText(delta, brand)
				state.text = true
			}
		}
		if eventType == "response.output_text.done" {
			prefixTextField(object, "text", brand)
		}
		if eventType == "response.completed" {
			response, _ := object["response"].(map[string]any)
			prefixResponsesBrand(response, brand)
		}
	case "/v1/messages":
		eventType, _ := object["type"].(string)
		if eventType == "content_block_start" && !state.text {
			block, _ := object["content_block"].(map[string]any)
			state.text = prefixTextField(block, "text", brand)
		}
		if eventType == "content_block_delta" && !state.text {
			delta, _ := object["delta"].(map[string]any)
			state.text = prefixTextField(delta, "text", brand)
		}
	}
}

func prefixStreamChoices(object map[string]any, field, brand string, state *streamBrandState) {
	choices, _ := object["choices"].([]any)
	for position, rawChoice := range choices {
		if state.choices[position] {
			continue
		}
		choice, _ := rawChoice.(map[string]any)
		target := choice
		if field == "content" {
			target, _ = choice["delta"].(map[string]any)
		}
		if prefixTextField(target, field, brand) {
			state.choices[position] = true
		}
	}
}

func neutralizeResponseID(value any, path string) {
	object, _ := value.(map[string]any)
	if object == nil {
		return
	}
	neutralizeIDField(object)
	if path == "/v1/responses" {
		response, _ := object["response"].(map[string]any)
		neutralizeIDField(response)
	}
	if path == "/v1/messages" {
		message, _ := object["message"].(map[string]any)
		neutralizeIDField(message)
	}
}

func neutralizeIDField(object map[string]any) {
	if object == nil {
		return
	}
	id, _ := object["id"].(string)
	if id == "" || strings.HasPrefix(id, "luxury_") {
		return
	}
	digest := sha256.Sum256([]byte(id))
	object["id"] = "luxury_" + hex.EncodeToString(digest[:12])
}

func chatFinished(payload map[string]any) bool {
	choices, _ := payload["choices"].([]any)
	for _, choice := range choices {
		item, _ := choice.(map[string]any)
		if reason, ok := item["finish_reason"].(string); ok && reason != "" {
			return true
		}
	}
	return false
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

// identifyingMarkers keeps only the values long enough to be redacted out of an
// answer. Everything reaching the sanitizer is treated as a secret — a marker that
// survives redaction refuses the whole answer — and that is right for a credential at
// any length and wrong for an identifier of two or three characters, which cannot be
// replaced without mangling ordinary prose and so can only ever reject. Measured:
// provider validation accepts `https://ai/v1`, `https://llm:8080/v1` and
// `http://[::1]:8080/v1` (a single-label internal alias and two shapes a Docker
// service name takes), and `o3` is a real upstream model id — the hostname or model
// markers those produce refused every answer containing "detail", "email", "again" or
// "foo3", indistinguishably from an unavailable provider and with nothing on screen
// saying why. The identity is not lost: the base URL marker still carries it.
//
// So the rule lives here, at the one place identifiers are handed to the sanitizer,
// rather than in each of the producers. It was stated in a producer once and the
// hostname was the value that slipped past it.
func identifyingMarkers(values ...string) []string {
	kept := make([]string, 0, len(values))
	for _, value := range values {
		if len(strings.TrimSpace(value)) >= relayapp.MinRedactableMarkerBytes {
			kept = append(kept, value)
		}
	}
	return kept
}

func redactMarkers(value string, markers []string, brand string) string {
	return newMarkerRedactor(markers, brand).replace(value)
}

// A marker is either a secret or an identity, and this list cannot tell them apart.
// A credential of any length must fail closed rather than reach a public answer
// (TestShortCredentialMarkerFailsClosed), while an identifier too short to redact
// would refuse every answer instead — so the callers filter the identifiers through
// identifyingMarkers before handing them over, and everything that arrives here is
// treated as a secret.
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
