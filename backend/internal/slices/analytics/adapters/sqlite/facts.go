package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/analytics/domain"
	_ "modernc.org/sqlite"
)

// Facts reads the history database the activity slice owns. It opens its own
// read-only connection: WAL lets one writer and many readers coexist, and
// query_only keeps this side from ever writing even by accident. The
// composition root hands both slices the same path — that is the one place
// that knows they share a file.
type Facts struct{ db *sql.DB }

func Open(path string) (*Facts, error) {
	if path == "" {
		return nil, errors.New("analytics path is empty")
	}
	if _, err := os.Stat(path); err != nil {
		return nil, errors.New("history database is not there yet")
	}
	dsn := "file:" + filepath.ToSlash(path) + "?_pragma=busy_timeout(5000)&_pragma=query_only(true)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, errors.New("analytics connection could not open")
	}
	db.SetMaxOpenConns(2)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, errors.New("history database is unavailable")
	}
	return &Facts{db: db}, nil
}

func (facts *Facts) Close() error {
	if facts == nil || facts.db == nil {
		return nil
	}
	return facts.db.Close()
}

// maxGroupRows is a safety ceiling on each GROUP BY. Retention and period
// bounds keep real results far below it; a result set past this means
// something upstream is wrong, and cutting it off is safer than shipping it.
const maxGroupRows = 20_000

func (facts *Facts) Grouped(ctx context.Context, since int64) ([]domain.GroupedRow, error) {
	query := `SELECT provider_id, MAX(provider_name), model_id,
COUNT(*), COALESCE(SUM(state='completed'),0), COALESCE(SUM(state='failed'),0), COALESCE(SUM(state='cancelled'),0),
COALESCE(SUM(retries),0), COALESCE(SUM(input_tokens),0), COALESCE(SUM(output_tokens),0),
COALESCE(SUM(cached_tokens),0), COALESCE(SUM(reasoning_tokens),0), COALESCE(SUM(total_tokens),0),
COALESCE(SUM(generation_ms),0)
FROM requests WHERE (? = 0 OR started_at_ms >= ?)
GROUP BY provider_id, model_id LIMIT ?`
	rows, err := facts.db.QueryContext(ctx, query, since, since, maxGroupRows)
	if err != nil {
		return nil, errors.New("analytics grouping query failed")
	}
	defer rows.Close()
	result := make([]domain.GroupedRow, 0)
	for rows.Next() {
		var row domain.GroupedRow
		if err := rows.Scan(&row.ProviderID, &row.ProviderName, &row.Model,
			&row.Requests, &row.Completed, &row.Failed, &row.Cancelled, &row.Retries,
			&row.InputTokens, &row.OutputTokens, &row.CachedTokens, &row.ReasoningToken,
			&row.TotalTokens, &row.GenerationMS); err != nil {
			return nil, errors.New("analytics grouping row is invalid")
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func (facts *Facts) Latencies(ctx context.Context, since int64) ([]domain.LatencySample, error) {
	rows, err := facts.db.QueryContext(ctx,
		`SELECT provider_id, model_id, latency_ms FROM requests
WHERE state='completed' AND (? = 0 OR started_at_ms >= ?) LIMIT 100000`,
		since, since)
	if err != nil {
		return nil, errors.New("analytics latency query failed")
	}
	defer rows.Close()
	result := make([]domain.LatencySample, 0)
	for rows.Next() {
		var sample domain.LatencySample
		if err := rows.Scan(&sample.ProviderID, &sample.Model, &sample.LatencyMS); err != nil {
			return nil, errors.New("analytics latency row is invalid")
		}
		result = append(result, sample)
	}
	return result, rows.Err()
}

// Daily groups by local day. The relay's operator reads their own clock, and
// a UTC split would call an evening burst two days; the machine's local zone
// is the zone the numbers are lived in.
func (facts *Facts) Daily(ctx context.Context, since int64) ([]domain.DailyRow, error) {
	query := `SELECT date(started_at_ms / 1000, 'unixepoch', 'localtime'), model_id,
COUNT(*), COALESCE(SUM(state='completed'),0), COALESCE(SUM(state='failed'),0), COALESCE(SUM(state='cancelled'),0),
COALESCE(SUM(retries),0), COALESCE(SUM(input_tokens),0), COALESCE(SUM(output_tokens),0),
COALESCE(SUM(cached_tokens),0), COALESCE(SUM(reasoning_tokens),0), COALESCE(SUM(total_tokens),0),
COALESCE(SUM(generation_ms),0)
FROM requests WHERE (? = 0 OR started_at_ms >= ?)
GROUP BY 1, model_id LIMIT ?`
	rows, err := facts.db.QueryContext(ctx, query, since, since, maxGroupRows)
	if err != nil {
		return nil, errors.New("analytics daily query failed")
	}
	defer rows.Close()
	result := make([]domain.DailyRow, 0)
	for rows.Next() {
		var row domain.DailyRow
		if err := rows.Scan(&row.Date, &row.Model,
			&row.Requests, &row.Completed, &row.Failed, &row.Cancelled, &row.Retries,
			&row.InputTokens, &row.OutputTokens, &row.CachedTokens, &row.ReasoningToken,
			&row.TotalTokens, &row.GenerationMS); err != nil {
			return nil, errors.New("analytics daily row is invalid")
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

// Errors groups by the relay's own error codes only: the provider's prose is
// diagnostics, not a taxonomy, and counting it would sort by wording.
func (facts *Facts) Errors(ctx context.Context, since int64) ([]domain.ErrorRow, error) {
	rows, err := facts.db.QueryContext(ctx,
		`SELECT provider_id, model_id, error_code, COUNT(*) FROM requests
WHERE state='failed' AND error_code != '' AND (? = 0 OR started_at_ms >= ?)
GROUP BY provider_id, model_id, error_code LIMIT ?`,
		since, since, maxGroupRows)
	if err != nil {
		return nil, errors.New("analytics error query failed")
	}
	defer rows.Close()
	result := make([]domain.ErrorRow, 0)
	for rows.Next() {
		var row domain.ErrorRow
		if err := rows.Scan(&row.ProviderID, &row.Model, &row.ErrorCode, &row.Requests); err != nil {
			return nil, errors.New("analytics error row is invalid")
		}
		result = append(result, row)
	}
	return result, rows.Err()
}
