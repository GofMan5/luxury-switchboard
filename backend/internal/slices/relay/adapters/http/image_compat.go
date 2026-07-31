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
	codexImageModel      = "gpt-image-2"
	defaultImageUpstream = "gpt-5.6-sol"
	maxImageResponse     = 16 * 1024 * 1024
)

var (
	errInvalidImageRequest  = errors.New("invalid image generation request")
	errInvalidImageResponse = errors.New("invalid image generation response")
)

func prepareImageRequest(method, path string, body []byte, contentType, upstreamModel string) (string, []byte, bool, error) {
	if method != http.MethodPost || strings.TrimRight(path, "/") != "/v1/images/generations" || !strings.Contains(strings.ToLower(contentType), "json") {
		return path, body, false, nil
	}
	var payload map[string]any
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if decoder.Decode(&payload) != nil || payload["model"] != codexImageModel {
		return path, body, false, nil
	}
	prompt, ok := payload["prompt"].(string)
	prompt = strings.TrimSpace(prompt)
	if !ok || prompt == "" {
		return path, body, true, errInvalidImageRequest
	}
	if upstreamModel == "" || upstreamModel == codexImageModel {
		upstreamModel = defaultImageUpstream
	}
	tool := map[string]any{"type": "image_generation", "action": "generate"}
	for _, field := range []string{"background", "quality", "size"} {
		if value, exists := payload[field]; exists && value != nil {
			tool[field] = value
		}
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
	image := ""
	created := int64(0)
	var value any
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if decoder.Decode(&value) == nil {
		if responseFailed(value) {
			return nil, errInvalidImageResponse
		}
		image, _ = imageCandidates(value)
		created = createdAt(value)
	} else {
		var err error
		image, created, err = sseImage(body)
		if err != nil {
			return nil, err
		}
	}
	if image == "" {
		return nil, errInvalidImageResponse
	}
	if created == 0 {
		created = time.Now().Unix()
	}
	encoded, err := json.Marshal(map[string]any{"created": created, "data": []any{map[string]string{"b64_json": image}}})
	if err != nil {
		return nil, errInvalidImageResponse
	}
	return encoded, nil
}

func sseImage(body []byte) (string, int64, error) {
	blocks := bytes.Split(bytes.ReplaceAll(body, []byte("\r\n"), []byte("\n")), []byte("\n\n"))
	parsed, terminals := false, 0
	final, partial := "", ""
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
			return "", 0, errInvalidImageResponse
		}
		if object, ok := value.(map[string]any); ok && object["type"] == "response.completed" {
			terminals++
		}
		candidate, candidatePartial := imageCandidates(value)
		if len(candidate) > len(final) {
			final = candidate
		}
		if candidatePartial != "" {
			partial = candidatePartial
		}
		if created == 0 {
			created = createdAt(value)
		}
	}
	if !parsed || terminals != 1 || (final == "" && partial == "") {
		return "", 0, errInvalidImageResponse
	}
	if final != "" {
		return final, created, nil
	}
	return partial, created, nil
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

func imageCandidates(value any) (string, string) {
	stack := []any{value}
	final, partial := "", ""
	for visited := 0; len(stack) > 0 && visited < 100000; visited++ {
		last := len(stack) - 1
		current := stack[last]
		stack = stack[:last]
		switch current := current.(type) {
		case map[string]any:
			for key, child := range current {
				if text, ok := child.(string); ok {
					if (key == "result" || key == "b64_json") && len(text) > len(final) && validBase64(text) {
						final = strings.TrimSpace(text)
					}
					if (key == "partial_image" || key == "partial_image_b64") && validBase64(text) {
						partial = strings.TrimSpace(text)
					}
				}
				stack = append(stack, child)
			}
		case []any:
			stack = append(stack, current...)
		}
	}
	return final, partial
}

func validBase64(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	decoded, err := base64.StdEncoding.Strict().DecodeString(value)
	return err == nil && len(decoded) > 0
}
