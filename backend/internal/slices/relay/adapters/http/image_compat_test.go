package relayhttp

import (
	"encoding/json"
	"testing"
)

func TestCodexImageRequestUsesConfiguredResponsesModel(t *testing.T) {
	path, body, matched, err := prepareImageRequest("POST", "/v1/images/generations", []byte(`{"model":"gpt-image-2","prompt":" blue robot ","quality":"high"}`), "application/json", "custom-image-model")
	if err != nil || !matched || path != "/v1/responses" {
		t.Fatalf("request was not adapted: path=%s matched=%v err=%v", path, matched, err)
	}
	var payload map[string]any
	if json.Unmarshal(body, &payload) != nil || payload["model"] != "custom-image-model" || payload["input"] != "blue robot" || payload["stream"] != true {
		t.Fatalf("unexpected image request: %s", body)
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
