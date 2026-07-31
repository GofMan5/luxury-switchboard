package sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/activity/application"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/activity/domain"
)

func TestHistoryPersistsSanitizedRequestsAndNonOverlappingTokens(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.db")
	store, err := Open(path, 30)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	request := domain.Request{
		ID: "req_test", StartedAt: now.Add(-time.Second), UpdatedAt: now,
		State: domain.StateCompleted, Model: "gpt-test", ProviderID: "echo",
		Method: "POST", Path: "/v1/responses", Status: 200,
		LatencyMS: 1000, Retries: 1, BytesIn: 100, BytesOut: 200,
		InputTokens: 100, OutputTokens: 40, CachedTokens: 30,
		ReasoningTokens: 10, TotalTokens: 140, ContextTokens: 100,
		GenerationMS: 1000, TokensPerSecond: 40,
	}
	if !store.Record(request) {
		t.Fatal("terminal request was not queued")
	}
	if err := store.Close(context.Background()); err != nil {
		t.Fatal(err)
	}

	store, err = Open(path, 30)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close(context.Background()) //nolint:errcheck
	recent, err := store.Recent(context.Background(), application.Period24H, 10)
	if err != nil || len(recent) != 1 || recent[0].Path != "/v1/responses" {
		t.Fatalf("unexpected recent history: %+v %v", recent, err)
	}
	stats, err := store.Stats(context.Background(), application.Period24H)
	if err != nil {
		t.Fatal(err)
	}
	if stats.ProcessedTokens != 140 || stats.NonCachedTokens != 110 || stats.ReasoningTokens != 10 || stats.TokensPerSecond != 40 {
		t.Fatalf("token subsets were double counted: %+v", stats)
	}
}

func TestHistoryRejectsUnknownPeriod(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "history.db"), 30)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close(context.Background()) //nolint:errcheck
	if _, err := store.Stats(context.Background(), application.Period("week")); err == nil {
		t.Fatal("unknown period was accepted")
	}
}
