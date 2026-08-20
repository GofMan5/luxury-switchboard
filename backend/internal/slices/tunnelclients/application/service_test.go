package application

import (
	"context"
	"testing"
	"time"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/tunnelclients/domain"
)

func TestQueueStateIsVisibleAndReleased(t *testing.T) {
	service := NewService(nil)
	service.Queue("203.0.113.10", 1)
	clients := service.List()
	if len(clients) != 1 || clients[0].Queued != 1 || clients[0].State != "queued" {
		t.Fatalf("queue was not visible: %+v", clients)
	}
	service.Queue("203.0.113.10", -1)
	clients = service.List()
	if clients[0].Queued != 0 || clients[0].State != "idle" {
		t.Fatalf("queue was not released: %+v", clients[0])
	}
}

func TestQueuedStateSurvivesAnActiveRequestFinishing(t *testing.T) {
	service := NewService(nil)
	service.Queue("203.0.113.11", 1)
	id := service.Begin(domain.Start{IP: "203.0.113.11"})
	service.Finish(id, domain.Finish{Status: 200})
	client := service.List()[0]
	if client.Active != 0 || client.Queued != 1 || client.State != "queued" {
		t.Fatalf("finishing an active request hid the remaining queue: %+v", client)
	}
}

func TestIdleClientIsEvictedWithoutDroppingActiveClient(t *testing.T) {
	service := NewService(nil)
	now := time.Unix(1_000, 0).UTC()
	service.now = func() time.Time { return now }
	service.Queue("idle", 1)
	service.Queue("idle", -1)
	service.Queue("active", 1)
	now = now.Add(clientIdleTTL + time.Second)
	clients := service.List()
	if len(clients) != 1 || clients[0].IP != "active" || clients[0].Queued != 1 {
		t.Fatalf("idle eviction removed live state or kept stale state: %+v", clients)
	}
}

func TestTunnelEventIDsDoNotCollideAcrossServiceRestarts(t *testing.T) {
	first := NewService(nil).Begin(domain.Start{IP: "203.0.113.1"})
	second := NewService(nil).Begin(domain.Start{IP: "203.0.113.1"})
	if first == second {
		t.Fatalf("tunnel event id was reused across service instances: %s", first)
	}
}

// The stored error code is an allowlist, not a filter list. Every internal label
// the gateway or relay might invent — a guardrail refusal above all — has to fall
// through to empty, because this field is shown in the tunnel client history and
// tells a story about why one particular answer failed.
func TestOnlyNeutralErrorCodesAreStored(t *testing.T) {
	allowed := []string{"upstream_rejected", "unsafe_response", "context_limit", "client_disconnected"}
	for _, code := range allowed {
		if safeError(code) != code {
			t.Fatalf("expected %q to be storable, got %q", code, safeError(code))
		}
	}
	internal := []string{
		"guardrail_blocked", "guardrail_alert", "proto-tooluse-unsolicited",
		"dl-curl-pipe-sh", "upstream_status", "chat_compatibility", "transport",
		"stream_incomplete", "image_generation", "request_rejected", "cancelled",
		"", "UPSTREAM_REJECTED", "upstream_rejected ",
	}
	for _, code := range internal {
		if safeError(code) != "" {
			t.Fatalf("internal label %q reached the client history as %q", code, safeError(code))
		}
	}
}

// A refused request must still be visible to the owner as a completed request,
// without the history naming why it failed.
func TestARefusalIsRecordedWithoutNamingItself(t *testing.T) {
	service := NewService(nil)
	id := service.Begin(domain.Start{IP: "203.0.113.9"})
	service.Finish(id, domain.Finish{Status: 502, ErrorCode: "guardrail_blocked"})
	events := service.Events(context.Background(), "203.0.113.9")
	if len(events) == 0 {
		t.Fatal("a refusal left no trace for the owner")
	}
	for _, event := range events {
		if event.ErrorCode != "" {
			t.Fatalf("the refusal named itself in the history: %+v", event)
		}
		if event.Status != 502 {
			t.Fatalf("the recorded status does not match what the caller saw: %+v", event)
		}
	}
}
