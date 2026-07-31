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
