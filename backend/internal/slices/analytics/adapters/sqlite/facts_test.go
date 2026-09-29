package sqlite_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	activitysqlite "github.com/luxuryprivate/switchboard/backend/internal/slices/activity/adapters/sqlite"
	activitydomain "github.com/luxuryprivate/switchboard/backend/internal/slices/activity/domain"
	analyticssqlite "github.com/luxuryprivate/switchboard/backend/internal/slices/analytics/adapters/sqlite"
)

// The facts adapter reads the very database the activity slice writes, so
// the test drives both through their real persistence path: one store
// records, the other aggregates, and the numbers must agree.
func TestFactsAggregateWhatTheHistoryStoreRecords(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.db")
	writer, err := activitysqlite.Open(path, 30)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	rows := []activitydomain.Request{
		{
			ID: "a", StartedAt: base, UpdatedAt: base, State: activitydomain.StateCompleted,
			ProviderID: "p1", ProviderName: "Alpha", Model: "m1", Status: 200,
			LatencyMS: 100, InputTokens: 1_000, OutputTokens: 500, CachedTokens: 200, ReasoningTokens: 100,
			TotalTokens: 1_500, GenerationMS: 1_000,
		},
		{
			ID: "b", StartedAt: base.Add(time.Hour), UpdatedAt: base, State: activitydomain.StateFailed,
			ProviderID: "p1", Model: "m1", Status: 402, LatencyMS: 50, ErrorCode: "balance_exhausted",
		},
		{
			ID: "c", StartedAt: base.Add(25 * time.Hour), UpdatedAt: base, State: activitydomain.StateCompleted,
			ProviderID: "p2", Model: "m2", Status: 200,
			LatencyMS: 300, InputTokens: 2_000, OutputTokens: 100, TotalTokens: 2_100, GenerationMS: 2_000,
		},
	}
	for _, row := range rows {
		if !writer.Record(row) {
			t.Fatalf("history store refused a terminal row: %s", row.ID)
		}
	}
	if err := writer.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	facts, err := analyticssqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer facts.Close()

	since := base.Add(-time.Hour).UnixMilli()
	grouped, err := facts.Grouped(context.Background(), since)
	if err != nil {
		t.Fatal(err)
	}
	if len(grouped) != 2 {
		t.Fatalf("grouping did not fold rows: %+v", grouped)
	}
	for _, row := range grouped {
		if row.ProviderID == "p1" && row.Model == "m1" {
			if row.Requests != 2 || row.Completed != 1 || row.Failed != 1 {
				t.Fatalf("p1/m1 counts are wrong: %+v", row.TokenVolume)
			}
			if row.InputTokens != 1_000 || row.CachedTokens != 200 || row.ReasoningToken != 100 || row.GenerationMS != 1_000 {
				t.Fatalf("p1/m1 sums are wrong: %+v", row.TokenVolume)
			}
			if row.ProviderName != "Alpha" {
				t.Fatalf("provider name did not travel: %q", row.ProviderName)
			}
		}
	}
	latencies, err := facts.Latencies(context.Background(), since)
	if err != nil {
		t.Fatal(err)
	}
	// The failed row contributes no latency sample: failed latencies are
	// diagnostics, not measurements.
	if len(latencies) != 2 {
		t.Fatalf("latency samples are wrong: %+v", latencies)
	}
	errorRows, err := facts.Errors(context.Background(), since)
	if err != nil {
		t.Fatal(err)
	}
	if len(errorRows) != 1 || errorRows[0].ErrorCode != "balance_exhausted" || errorRows[0].Requests != 1 {
		t.Fatalf("error grouping is wrong: %+v", errorRows)
	}
	daily, err := facts.Daily(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	// Two local days: the 25-hour spread cannot fold into one.
	if len(daily) != 2 {
		t.Fatalf("daily grouping is wrong: %+v", daily)
	}
}

func TestFactsRefuseAMissingDatabase(t *testing.T) {
	if _, err := analyticssqlite.Open(filepath.Join(t.TempDir(), "absent.db")); err == nil {
		t.Fatal("a missing database opened")
	}
	if _, err := analyticssqlite.Open(""); err == nil {
		t.Fatal("an empty path opened")
	}
}

func TestFactsOnlyRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.db")
	writer, err := activitysqlite.Open(path, 30)
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	facts, err := analyticssqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer facts.Close()
	if _, err := facts.Grouped(context.Background(), 0); err != nil {
		t.Fatal(err)
	}
	// query_only is the adapter's own safety net: an accidental write fails
	// loudly here instead of corrupting the writer's database. The probe is
	// a CREATE TABLE on a fresh name: it has no NOT NULL or CHECK constraint
	// to fail on its own, so the only thing that can refuse it is the
	// read-only pragma itself. (An INSERT into requests was tried first and
	// was vacuous: the schema's NOT NULL columns refused the write with or
	// without the pragma — caught by a mutation probe, not by reading.)
	if _, err := facts.DB().Exec("CREATE TABLE probe_write_refusal (x INTEGER)"); err == nil {
		t.Fatal("a read-only connection accepted a write")
	}
}
