package relayhttp

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
)

const (
	defaultImageUpstream = "gpt-5.6-sol"
	maxImageResponse     = 16 * 1024 * 1024
)

var (
	errInvalidImageRequest  = errors.New("invalid image generation request")
	errInvalidImageResponse = errors.New("invalid image generation response")
)

func prepareImageRequest(method, path string, body []byte, contentType, upstreamModel string, enabled bool) (string, []byte, bool, error) {
	if !enabled || method != http.MethodPost || !imageDialectPath(canonicalPath(path)) || !strings.Contains(strings.ToLower(contentType), "json") {
		return path, body, false, nil
	}
	var payload map[string]any
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if decoder.Decode(&payload) != nil {
		return path, body, false, nil
	}
	requestedModel, _ := payload["model"].(string)
	requestedModel = strings.TrimSpace(requestedModel)
	prompt, ok := payload["prompt"].(string)
	prompt = strings.TrimSpace(prompt)
	if requestedModel == "" || !ok || prompt == "" {
		return path, body, true, errInvalidImageRequest
	}
	if upstreamModel == "" || strings.HasPrefix(upstreamModel, "gpt-image-") {
		upstreamModel = defaultImageUpstream
	}
	tool := map[string]any{"type": "image_generation", "action": "generate"}
	for _, field := range []string{"background", "quality", "size", "output_format", "output_compression"} {
		if value, exists := payload[field]; exists && value != nil {
			tool[field] = value
		}
	}
	// n travels: a client asking for three pictures must not be answered with
	// one and a shrug. The answer conversion returns every image the provider
	// produced, so the count the provider honors is the count the client gets.
	// response_format does not: the bridge always answers b64_json, because it
	// has no storage to publish URLs from — the one field the translation
	// fixes in shape rather than carries.
	if count, ok := payload["n"]; ok && count != nil {
		tool["n"] = count
	}
	encoded, err := json.Marshal(map[string]any{
		"model": upstreamModel, "input": prompt, "tools": []any{tool},
		"stream": true, "store": false,
	})
	if err != nil {
		return path, body, true, errInvalidImageRequest
	}
	return "/v1/responses", encoded, true, nil
}

func imagesResponse(body []byte) ([]byte, error) {
	if len(body) == 0 || len(body) > maxImageResponse {
		return nil, errInvalidImageResponse
	}
	images := []string{}
	partial := ""
	created := int64(0)
	var value any
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if decoder.Decode(&value) == nil {
		if responseFailed(value) {
			return nil, errInvalidImageResponse
		}
		images, partial = imageCollection(value)
		created = createdAt(value)
	} else {
		var err error
		images, partial, created, err = sseImages(body)
		if err != nil {
			return nil, err
		}
	}
	if len(images) == 0 && partial == "" {
		return nil, errInvalidImageResponse
	}
	// A partial image stands in for a final one that never arrived; a final
	// answer with several images is several entries, because the client asked
	// for n and paid for n.
	if len(images) == 0 {
		images = []string{partial}
	}
	if created == 0 {
		created = time.Now().Unix()
	}
	data := make([]any, 0, len(images))
	for _, image := range images {
		data = append(data, map[string]string{"b64_json": image})
	}
	encoded, err := json.Marshal(map[string]any{"created": created, "data": data})
	if err != nil {
		return nil, errInvalidImageResponse
	}
	return encoded, nil
}

func sseImages(body []byte) ([]string, string, int64, error) {
	blocks := bytes.Split(bytes.ReplaceAll(body, []byte("\r\n"), []byte("\n")), []byte("\n\n"))
	parsed, terminals := false, 0
	images := []string{}
	partial := ""
	created := int64(0)
	for _, block := range blocks {
		data := sseData(block)
		if len(data) == 0 || bytes.Equal(data, []byte("[DONE]")) {
			continue
		}
		var value any
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.UseNumber()
		if decoder.Decode(&value) != nil {
			continue
		}
		parsed = true
		if responseFailed(value) {
			return nil, "", 0, errInvalidImageResponse
		}
		if object, ok := value.(map[string]any); ok && object["type"] == "response.completed" {
			terminals++
		}
		candidates, candidatePartial := imageCollection(value)
		images = append(images, candidates...)
		if candidatePartial != "" {
			partial = candidatePartial
		}
		if created == 0 {
			created = createdAt(value)
		}
	}
	if !parsed || terminals != 1 || (len(images) == 0 && partial == "") {
		return nil, "", 0, errInvalidImageResponse
	}
	return images, partial, created, nil
}

func sseData(block []byte) []byte {
	parts := make([][]byte, 0, 1)
	for _, line := range bytes.Split(block, []byte("\n")) {
		if bytes.HasPrefix(line, []byte("data:")) {
			parts = append(parts, bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:"))))
		}
	}
	return bytes.Join(parts, []byte("\n"))
}

func responseFailed(value any) bool {
	object, ok := value.(map[string]any)
	if !ok {
		return false
	}
	response, _ := object["response"].(map[string]any)
	eventType, _ := object["type"].(string)
	status, _ := object["status"].(string)
	if status == "" {
		status, _ = response["status"].(string)
	}
	errorValue := object["error"]
	if errorValue == nil {
		errorValue = response["error"]
	}
	return eventType == "response.failed" || eventType == "response.incomplete" || eventType == "response.cancelled" || eventType == "error" || status == "failed" || status == "incomplete" || status == "cancelled" || errorValue != nil
}

func createdAt(value any) int64 {
	object, ok := value.(map[string]any)
	if !ok {
		return 0
	}
	response, _ := object["response"].(map[string]any)
	for _, candidate := range []any{object["created_at"], object["created"], response["created_at"], response["created"]} {
		if number, ok := candidate.(json.Number); ok {
			if value, err := number.Int64(); err == nil && value >= 0 {
				return value
			}
		}
	}
	return 0
}

// imageCollection walks one decoded value (a buffered answer or one SSE event)
// and collects every final image it carries, first-seen order, duplicates
// collapsed: an answer that produced n pictures has n distinct results, and
// the client asked for exactly that many. The partial preview is reported
// alongside for the answer that never finished.
func imageCollection(value any) ([]string, string) {
	stack := []any{value}
	finals := make([]string, 0, 1)
	seen := make(map[string]struct{})
	partial := ""
	for visited := 0; len(stack) > 0 && visited < 100000; visited++ {
		last := len(stack) - 1
		current := stack[last]
		stack = stack[:last]
		switch current := current.(type) {
		case map[string]any:
			for key, child := range current {
				if text, ok := child.(string); ok {
					trimmed := strings.TrimSpace(text)
					if (key == "result" || key == "b64_json") && validBase64(trimmed) {
						if _, duplicate := seen[trimmed]; !duplicate {
							seen[trimmed] = struct{}{}
							finals = append(finals, trimmed)
						}
					}
					if (key == "partial_image" || key == "partial_image_b64") && validBase64(trimmed) {
						partial = trimmed
					}
				}
				stack = append(stack, child)
			}
		case []any:
			// Prepending the items reversed, so the stack pops them in the
			// order the provider wrote them: several pictures in one answer
			// come back the way they were sent, not shuffled by a depth-first
			// walk. The new stack is built in one allocation: appending onto
			// items with its own capacity would reallocate per array on a
			// hostile nested body, and the walk is quadratic before either
			// budget cap notices.
			merged := make([]any, 0, len(current)+len(stack))
			for index := len(current) - 1; index >= 0; index-- {
				merged = append(merged, current[index])
			}
			merged = append(merged, stack...)
			stack = merged
		}
	}
	return finals, partial
}

func validBase64(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	decoded, err := base64.StdEncoding.Strict().DecodeString(value)
	return err == nil && len(decoded) > 0
}
