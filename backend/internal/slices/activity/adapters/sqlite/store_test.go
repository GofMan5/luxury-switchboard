package sqlite

import (
	"context"
	"database/sql"
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
		State: domain.StateCompleted, Model: "gpt-test", ProviderID: "echo", ProviderName: "Echo",
		Method: "POST", Path: "/v1/responses", Status: 200,
		LatencyMS: 1000, Retries: 1, BytesIn: 100, BytesOut: 200,
		InputTokens: 100, OutputTokens: 40, CachedTokens: 30,
		ReasoningTokens: 10, TotalTokens: 140, ContextTokens: 100,
		GenerationMS: 1000, TokensPerSecond: 40,
		ErrorDetail: "provider returned HTTP 502",
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
	if recent[0].ErrorDetail != request.ErrorDetail {
		t.Fatalf("error detail did not survive history: %q", recent[0].ErrorDetail)
	}
	if recent[0].ProviderName != "Echo" {
		t.Fatalf("provider name did not survive history: %q", recent[0].ProviderName)
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

func TestHistoryMigratesDatabaseWithoutNewColumns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.db")
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	const legacySchema = `CREATE TABLE requests (
request_id TEXT PRIMARY KEY, started_at_ms INTEGER NOT NULL, updated_at_ms INTEGER NOT NULL,
state TEXT NOT NULL CHECK(state IN ('completed','failed','cancelled')), model_id TEXT NOT NULL,
provider_id TEXT NOT NULL, method TEXT NOT NULL, path TEXT NOT NULL, status_code INTEGER NOT NULL,
queue_ms REAL NOT NULL, latency_ms REAL NOT NULL, retries INTEGER NOT NULL,
bytes_in INTEGER NOT NULL, bytes_out INTEGER NOT NULL, error_code TEXT NOT NULL,
input_tokens INTEGER NOT NULL, output_tokens INTEGER NOT NULL, cached_tokens INTEGER NOT NULL,
reasoning_tokens INTEGER NOT NULL, total_tokens INTEGER NOT NULL, context_tokens INTEGER NOT NULL,
generation_ms REAL NOT NULL, tokens_per_second REAL NOT NULL)`
	if _, err := db.Exec(legacySchema); err != nil {
		db.Close()
		t.Fatal(err)
	}
	now := time.Now().UTC().UnixMilli()
	if _, err := db.Exec(`INSERT INTO requests (
request_id, started_at_ms, updated_at_ms, state, model_id, provider_id, method, path,
status_code, queue_ms, latency_ms, retries, bytes_in, bytes_out, error_code,
input_tokens, output_tokens, cached_tokens, reasoning_tokens, total_tokens, context_tokens,
generation_ms, tokens_per_second) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		"req_legacy", now, now, "completed", "gpt-test", "echo", "POST", "/v1/responses", 200,
		0.0, 100.0, 0, 10, 20, "", 5, 5, 0, 0, 10, 5, 100.0, 50.0); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := Open(path, 30)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close(context.Background()) //nolint:errcheck
	recent, err := store.Recent(context.Background(), application.Period24H, 10)
	if err != nil || len(recent) != 1 {
		t.Fatalf("legacy row was lost by migration: %+v %v", recent, err)
	}
	if recent[0].ProviderName != "" || recent[0].ErrorDetail != "" {
		t.Fatalf("migrated columns were not empty: %+v", recent[0])
	}
	if _, err := store.Stats(context.Background(), application.Period24H); err != nil {
		t.Fatal(err)
	}
}
