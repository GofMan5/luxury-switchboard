package application

import (
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
