package tunnelhttp

import (
	"bytes"
	"net/http"
	"testing"

	relayapp "github.com/luxuryprivate/switchboard/backend/internal/slices/relay/application"
)

func FuzzSanitizeResponseNeverLeaksMarkerOnSuccess(f *testing.F) {
	f.Add([]byte(`{"type":"response.completed","response":{"status":"completed","model":"private","error":null,"incomplete_details":null}}`), false)
	f.Add([]byte("data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"model\":\"private\",\"error\":null,\"incomplete_details\":null}}\n\n"), true)
	f.Add([]byte(`{"SecretProvider_metadata":true}`), false)
	f.Fuzz(func(t *testing.T, body []byte, stream bool) {
		contentType := "application/json"
		if stream {
			contentType = "text/event-stream"
		}
		clean, _, err := sanitizeResponse(relayapp.DispatchResponse{Status: http.StatusOK, Headers: http.Header{"Content-Type": []string{contentType}}, Body: body}, "/v1/responses", "public", []string{"SecretProvider", "private"}, "Luxury Private")
		if err == nil && (bytes.Contains(bytes.ToLower(clean), []byte("secretprovider")) || bytes.Contains(bytes.ToLower(clean), []byte("private"))) {
			t.Fatalf("sanitizer committed a configured marker: %q", clean)
		}
	})
}

// Everything the sanitizer receives is treated as a secret: a marker that survives
// redaction refuses the whole answer. Right for a credential, wrong for a short
// identifier — measured, a `https://ai/v1` provider and an `o3` upstream model each
// refused every ordinary answer containing "detail", "again" or "foo3", which reads
// exactly like an unavailable provider. Both directions are asserted: the short
// identifiers stop rejecting, and a credential of the same length still does.
func TestShortIdentifiersAreDroppedButShortSecretsStillRefuse(t *testing.T) {
	kept := identifyingMarkers("https://ai/v1", "ai", "o3", "gw", "GW", "  x ", "gpt-4o-mini")
	for _, marker := range kept {
		if len(marker) < relayapp.MinRedactableMarkerBytes {
			t.Fatalf("an unredactable identifier survived and will refuse every answer: %q in %q", marker, kept)
		}
	}
	if len(kept) != 2 || kept[0] != "https://ai/v1" || kept[1] != "gpt-4o-mini" {
		t.Fatalf("the identity was dropped along with the short values: %q", kept)
	}

	headers := http.Header{}
	headers.Set("Content-Type", "application/json")
	answer := []byte(`{"id":"chatcmpl-1","model":"o3","choices":[{"index":0,"message":{"role":"assistant","content":"Rename foo3, check the detail, mail me again."},"finish_reason":"stop"}]}`)
	body, _, err := sanitizeResponse(
		relayapp.DispatchResponse{Status: http.StatusOK, Headers: headers, Body: answer},
		"/v1/chat/completions", "public-model", identifyingMarkers("https://ai/v1", "ai", "o3"), brand,
	)
	if err != nil {
		t.Fatalf("an ordinary answer was refused over a two-character identifier: %v", err)
	}
	if !bytes.Contains(body, []byte("foo3")) || !bytes.Contains(body, []byte("detail")) {
		t.Fatalf("ordinary prose was mangled: %s", body)
	}
	if bytes.Contains(body, []byte(`"model":"o3"`)) || bytes.Contains(body, []byte("ai/v1")) {
		t.Fatalf("the upstream identity survived: %s", body)
	}

	// The same length, as a credential rather than an identifier: those never pass
	// through identifyingMarkers, and refusing is the only safe answer.
	if _, _, err = sanitizeResponse(
		relayapp.DispatchResponse{Status: http.StatusOK, Headers: headers, Body: []byte(`{"output":"o3"}`)},
		"/v1/responses", "public-model", []string{"o3"}, brand,
	); err == nil {
		t.Fatal("a short credential marker reached public output")
	}
}
