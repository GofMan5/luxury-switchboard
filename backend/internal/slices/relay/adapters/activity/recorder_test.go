package activity

import (
	activityapp "github.com/luxuryprivate/switchboard/backend/internal/slices/activity/application"
	relayapp "github.com/luxuryprivate/switchboard/backend/internal/slices/relay/application"
	"testing"
)

func TestFinalServingProviderOwnsActivityUsage(t *testing.T) {
	service := activityapp.NewService(10)
	recorder := NewRecorder(service)
	id := recorder.Begin(relayapp.ActivityStart{ProviderID: "primary", ProviderName: "Primary", Model: "public", Method: "POST", Path: "/v1/responses"})
	recorder.Finish(id, relayapp.ActivityFinish{ProviderID: "fallback", ProviderName: "Fallback", Status: 200, Usage: relayapp.TokenUsage{InputTokens: 10, OutputTokens: 5}})
	rows := service.List(10)
	if len(rows) != 1 || rows[0].ProviderID != "fallback" || rows[0].ProviderName != "Fallback" || rows[0].InputTokens != 10 {
		t.Fatalf("usage still belongs to primary: %+v", rows)
	}
	// Callers that have no serving override preserve their original provider.
	id = recorder.Begin(relayapp.ActivityStart{ProviderID: "primary", ProviderName: "Primary", Model: "public"})
	recorder.Finish(id, relayapp.ActivityFinish{Status: 200})
	if service.List(10)[0].ProviderID != "primary" {
		t.Fatal("unspecified identity erased provider")
	}
}
