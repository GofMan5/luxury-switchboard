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

// A client that asked for three pictures is answered with three: n travels to
// the tool, and every image the provider produced comes back as its own data
// entry, in the order the answer carried them.
func TestSeveralRequestedImagesTravelAndComeBack(t *testing.T) {
	_, body, matched, err := prepareImageRequest("POST", "/v1/images/generations", []byte(`{"model":"gpt-image-2","prompt":"blue robot","n":3}`), "application/json", "custom-image-model", true)
	if err != nil || !matched {
		t.Fatalf("request was not adapted: matched=%v err=%v", matched, err)
	}
	var payload map[string]any
	if json.Unmarshal(body, &payload) != nil {
		t.Fatalf("unreadable image request: %s", body)
	}
	tool := payload["tools"].([]any)[0].(map[string]any)
	if tool["n"] != float64(3) {
		t.Fatalf("the requested image count was dropped from the tool: %v", tool)
	}

	sse := []byte("data: {\"type\":\"response.created\",\"response\":{\"created_at\":1780000000}}\n\n" +
		"data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"image_generation_call\",\"result\":\"YQ==\"}}\n\n" +
		"data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"image_generation_call\",\"result\":\"Yg==\"}}\n\n" +
		"data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"image_generation_call\",\"result\":\"Yw==\"}}\n\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[]}}\n\n")
	converted, err := imagesResponse(sse)
	if err != nil {
		t.Fatalf("a three-image answer was refused: %v", err)
	}
	var answer struct {
		Data []map[string]string `json:"data"`
	}
	if json.Unmarshal(converted, &answer) != nil || len(answer.Data) != 3 {
		t.Fatalf("expected three images, got: %s", converted)
	}
	for index, want := range []string{"YQ==", "Yg==", "Yw=="} {
		if answer.Data[index]["b64_json"] != want {
			t.Fatalf("image %d was %q, want %q: %s", index, answer.Data[index]["b64_json"], want, converted)
		}
	}

	// The buffered JSON form carries the same three images in one output
	// array, and the order the provider chose is the order the client reads.
	buffered := []byte(`{"created_at":1780000000,"status":"completed","output":[` +
		`{"type":"image_generation_call","result":"YQ=="},{"type":"image_generation_call","result":"Yg=="},{"type":"image_generation_call","result":"Yw=="}]}`)
	converted, err = imagesResponse(buffered)
	if err != nil {
		t.Fatalf("a three-image buffered answer was refused: %v", err)
	}
	if json.Unmarshal(converted, &answer) != nil || len(answer.Data) != 3 || answer.Data[0]["b64_json"] != "YQ==" || answer.Data[2]["b64_json"] != "Yw==" {
		t.Fatalf("the buffered images lost their order: %s", converted)
	}
}
