package sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/tunnelclients/domain"
)

func TestTunnelHistoryPersistsSanitizedEvent(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "tunnel.db"), 72)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close(context.Background())
	event := domain.Event{ID: "tun_1", IP: "203.0.113.10", Time: time.Now().UTC(), State: "completed", Method: "POST", Path: "/v1/responses", Model: "public-model", Status: 200, LatencyMS: 12, BytesIn: 3, BytesOut: 4}
	if !store.Record(event) {
		t.Fatal("event was not queued")
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		events, queryErr := store.Recent(context.Background(), event.IP, 10)
		if queryErr == nil && len(events) == 1 {
			if events[0].Model != "public-model" || events[0].Status != 200 {
				t.Fatalf("unexpected persisted event: %+v", events[0])
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("event was not flushed")
}

func TestClientProfilesPersistAndSurviveRetentionPruning(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tunnel.db")
	store, err := Open(path, 1)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	profile := domain.Profile{IP: "203.0.113.10", Banned: true, Note: "abuse"}
	if err := store.SaveProfile(ctx, profile); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveProfile(ctx, domain.Profile{IP: "203.0.113.10", Banned: false, Note: "watched"}); err != nil {
		t.Fatal(err)
	}
	if err := store.prune(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(ctx); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(path, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close(ctx)
	stored, err := reopened.Profiles(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != 1 || stored[0].Banned || stored[0].Note != "watched" {
		t.Fatalf("profile was not updated in place: %+v", stored)
	}
	if err := reopened.DeleteProfile(ctx, "203.0.113.10"); err != nil {
		t.Fatal(err)
	}
	if stored, err = reopened.Profiles(ctx); err != nil || len(stored) != 0 {
		t.Fatalf("profile was not removed: %+v %v", stored, err)
	}
}
