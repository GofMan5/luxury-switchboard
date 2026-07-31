package application

import (
	"testing"
	"time"
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
