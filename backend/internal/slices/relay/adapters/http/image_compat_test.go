package relayhttp

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestCodexImageRequestUsesConfiguredResponsesModel(t *testing.T) {
	path, body, matched, err := prepareImageRequest("POST", "/v1/images/generations", []byte(`{"model":"gpt-image-2","prompt":" blue robot ","background":"opaque","quality":"high","size":"1536x864","output_format":"webp","output_compression":42}`), "application/json", "custom-image-model", true)
	if err != nil || !matched || path != "/v1/responses" {
		t.Fatalf("request was not adapted: path=%s matched=%v err=%v", path, matched, err)
	}
	var payload map[string]any
	if json.Unmarshal(body, &payload) != nil || payload["model"] != "custom-image-model" || payload["input"] != "blue robot" || payload["stream"] != true {
		t.Fatalf("unexpected image request: %s", body)
	}
	tools, _ := payload["tools"].([]any)
	tool, _ := tools[0].(map[string]any)
	if tool["background"] != "opaque" || tool["quality"] != "high" || tool["size"] != "1536x864" || tool["output_format"] != "webp" || tool["output_compression"] != float64(42) {
		t.Fatalf("GPT Image 2 options were lost: %#v", tool)
	}
}

func TestNativeImageProviderKeepsGenerationsEndpoint(t *testing.T) {
	original := []byte(`{"model":"gpt-image-2","prompt":"blue robot"}`)
	path, body, matched, err := prepareImageRequest("POST", "/v1/images/generations", original, "application/json", "gpt-image-2", false)
	if err != nil || matched || path != "/v1/images/generations" || string(body) != string(original) {
		t.Fatalf("native image request was rewritten: path=%s matched=%v body=%s err=%v", path, matched, body, err)
	}
}

func TestGPTImageSnapshotUsesResponsesModel(t *testing.T) {
	path, body, matched, err := prepareImageRequest("POST", "/v1/images/generations", []byte(`{"model":"gpt-image-2-2026-04-21","prompt":"blue robot"}`), "application/json", "gpt-image-2-2026-04-21", true)
	if err != nil || !matched || path != "/v1/responses" || !bytes.Contains(body, []byte(`"model":"`+defaultImageUpstream+`"`)) {
		t.Fatalf("GPT Image snapshot was sent as a mainline model: path=%s body=%s err=%v", path, body, err)
	}
}

func TestImageCompatibilitySupportsPublicAliases(t *testing.T) {
	path, body, matched, err := prepareImageRequest("POST", "/v1/images/generations", []byte(`{"model":"public-image","prompt":"blue robot"}`), "application/json", "private-responses-model", true)
	if err != nil || !matched || path != "/v1/responses" || !bytes.Contains(body, []byte(`"model":"private-responses-model"`)) {
		t.Fatalf("public image alias was not bridged: path=%s matched=%v body=%s err=%v", path, matched, body, err)
	}
}

func TestImagesResponseAcceptsOneCompletedImage(t *testing.T) {
	sse := []byte("data: {\"type\":\"response.created\",\"response\":{\"created_at\":1780000000}}\n\ndata: {\"type\":\"response.image_generation_call.partial_image\",\"partial_image_b64\":\"YWJjZA==\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"error\":null,\"output\":[{\"result\":\"YQ==\"}]}}\n\n")
	body, err := imagesResponse(sse)
	if err != nil || string(body) != `{"created":1780000000,"data":[{"b64_json":"YQ=="}]}` {
		t.Fatalf("unexpected image response: %s err=%v", body, err)
	}
	if _, err := imagesResponse(append(sse, sse...)); err == nil {
		t.Fatal("duplicate terminal image response was accepted")
	}
}
