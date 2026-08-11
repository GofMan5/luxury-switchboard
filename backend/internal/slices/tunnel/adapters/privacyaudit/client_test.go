package privacyaudit

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/tunnel/domain"
)

const testToken = "0123456789abcdef0123456789abcdef0123456789abcdef"

func TestAuditReportsTheExternalSurfaceWithoutReturningTheToken(t *testing.T) {
	var requestValid atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/models" || request.Header.Get("Authorization") != "Bearer "+testToken {
			http.Error(writer, "rejected", http.StatusBadRequest)
			return
		}
		requestValid.Store(true)
		writer.Header().Set("Content-Type", "application/json")
		writer.Header().Set("X-Provider-Trace", "visible-provider-marker")
		writer.Header().Set("X-Credential-Echo", "Bearer "+testToken)
		_, _ = writer.Write([]byte(`{"object":"list","credential":"` + testToken + `","data":[{"id":"public-one","object":"model","created":0,"owned_by":"visible-owner"}]}`))
	}))
	defer server.Close()

	report, err := NewClient().Audit(context.Background(), server.URL+"/v1", testToken)
	if err != nil || report.Status != http.StatusOK || len(report.Models) != 1 {
		t.Fatalf("unexpected privacy report: report=%+v err=%v", report, err)
	}
	if !requestValid.Load() {
		t.Fatal("privacy request did not use the expected path and credentials")
	}
	if report.Models[0].ID != "public-one" || !contains(report.Models[0].Fields, "owned_by") || !strings.Contains(report.RawBody, "visible-owner") {
		t.Fatalf("privacy report hid externally visible data: %+v", report)
	}
	if !hasHeader(report.Headers, "X-Provider-Trace", "visible-provider-marker") {
		t.Fatalf("privacy report omitted a response header: %+v", report.Headers)
	}
	if !report.CredentialReflected || !strings.Contains(report.RawBody, "[redacted access key]") || !hasHeader(report.Headers, "X-Credential-Echo", "Bearer [redacted access key]") {
		t.Fatalf("reflected credentials were not reported and redacted: %+v", report)
	}
	encoded, _ := json.Marshal(report)
	if strings.Contains(string(encoded), testToken) {
		t.Fatal("privacy report returned the access key")
	}
}

func TestAuditDoesNotForwardTheTokenAcrossRedirects(t *testing.T) {
	var redirected atomic.Bool
	destination := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { redirected.Store(true) }))
	defer destination.Close()
	source := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Location", destination.URL)
		writer.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer source.Close()

	report, err := NewClient().Audit(context.Background(), source.URL+"/v1", testToken)
	if err != nil || report.Status != http.StatusTemporaryRedirect {
		t.Fatalf("redirect was not reported safely: report=%+v err=%v", report, err)
	}
	if redirected.Load() {
		t.Fatal("privacy audit followed a redirect with owner credentials")
	}
}

func TestAuditRejectsAnUntrustedCredentialTarget(t *testing.T) {
	if _, err := NewClient().Audit(context.Background(), "http://example.com/v1", testToken); err == nil {
		t.Fatal("privacy audit accepted an untrusted target")
	}
}

func TestModelsURLAcceptsOnlyTheGeneratedPublisherBoundary(t *testing.T) {
	address := "https://luxuryprivate.duckdns.org/model-tunnel/0123456789abcdef0123456789abcdef0123456789abcdef/v1"
	target, err := modelsURL(address)
	if err != nil || target.String() != address+"/models" {
		t.Fatalf("generated publisher URL was rejected: target=%v err=%v", target, err)
	}
	if _, err := modelsURL("https://luxuryprivate.duckdns.org/other/v1"); err == nil {
		t.Fatal("unexpected publisher path was accepted")
	}
}

func contains(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func hasHeader(headers []domain.PrivacyHeader, name, value string) bool {
	for _, header := range headers {
		if header.Name == name && contains(header.Values, value) {
			return true
		}
	}
	return false
}
