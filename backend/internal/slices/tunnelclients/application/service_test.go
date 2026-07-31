package application

import "testing"

func TestQueueStateIsVisibleAndReleased(t *testing.T) {
	service := NewService()
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
