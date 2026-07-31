package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/activity/application"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/activity/domain"
	_ "modernc.org/sqlite"
)

const (
	queueCapacity = 4_096
	batchSize     = 128
)

type Store struct {
	db            *sql.DB
	queue         chan domain.Request
	cancel        context.CancelFunc
	workers       sync.WaitGroup
	closed        atomic.Bool
	retentionDays int
}

func DefaultPath() (string, error) {
	root := os.Getenv("LOCALAPPDATA")
	if root == "" {
		return "", errors.New("LOCALAPPDATA is unavailable")
	}
	return filepath.Join(root, "ProviderSwitchboard", "history.v2.db"), nil
}

func Open(path string, retentionDays int) (*Store, error) {
	if path == "" || retentionDays < 1 {
		return nil, errors.New("invalid history settings")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, errors.New("history directory could not be created")
	}
	dsn := "file:" + filepath.ToSlash(path) + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(ON)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, errors.New("history database could not open")
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, errors.New("history database is unavailable")
	}
	if err := migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	store := &Store{db: db, queue: make(chan domain.Request, queueCapacity), cancel: cancel, retentionDays: retentionDays}
	if err := store.prune(context.Background()); err != nil {
		db.Close()
		cancel()
		return nil, err
	}
	store.workers.Add(1)
	go store.run(ctx)
	return store, nil
}

func (store *Store) Record(request domain.Request) bool {
	if store.closed.Load() || !terminal(request.State) {
		return false
	}
	select {
	case store.queue <- request:
		return true
	default:
		return false
	}
}

func (store *Store) Recent(ctx context.Context, period application.Period, limit int) ([]domain.Request, error) {
	limit = min(max(limit, 1), 500)
	cutoff, err := periodCutoff(period, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	query := `SELECT request_id, started_at_ms, updated_at_ms, state, model_id, provider_id,
method, path, status_code, queue_ms, latency_ms, retries, bytes_in, bytes_out, error_code,
input_tokens, output_tokens, cached_tokens, reasoning_tokens, total_tokens, context_tokens,
generation_ms, tokens_per_second
FROM requests WHERE (? = 0 OR started_at_ms >= ?) ORDER BY started_at_ms DESC LIMIT ?`
	rows, err := store.db.QueryContext(ctx, query, cutoff, cutoff, limit)
	if err != nil {
		return nil, errors.New("history query failed")
	}
	defer rows.Close()
	result := make([]domain.Request, 0, limit)
	for rows.Next() {
		var request domain.Request
		var started, updated int64
		if err := rows.Scan(
			&request.ID, &started, &updated, &request.State, &request.Model, &request.ProviderID,
			&request.Method, &request.Path, &request.Status, &request.QueueMS, &request.LatencyMS,
			&request.Retries, &request.BytesIn, &request.BytesOut, &request.ErrorCode,
			&request.InputTokens, &request.OutputTokens, &request.CachedTokens,
			&request.ReasoningTokens, &request.TotalTokens, &request.ContextTokens,
			&request.GenerationMS, &request.TokensPerSecond,
		); err != nil {
			return nil, errors.New("history row is invalid")
		}
		request.StartedAt = time.UnixMilli(started).UTC()
		request.UpdatedAt = time.UnixMilli(updated).UTC()
		result = append(result, request)
	}
	return result, rows.Err()
}

func (store *Store) Stats(ctx context.Context, period application.Period) (application.HistoryStats, error) {
	cutoff, err := periodCutoff(period, time.Now().UTC())
	if err != nil {
		return application.HistoryStats{}, err
	}
	var stats application.HistoryStats
	query := `SELECT COUNT(*),
COALESCE(SUM(state='completed'),0), COALESCE(SUM(state='failed'),0), COALESCE(SUM(state='cancelled'),0),
COALESCE(SUM(retries),0), COALESCE(SUM(input_tokens),0), COALESCE(SUM(output_tokens),0),
COALESCE(SUM(cached_tokens),0), COALESCE(SUM(reasoning_tokens),0),
COALESCE(SUM(input_tokens + output_tokens),0),
COALESCE(SUM(MAX(input_tokens - cached_tokens, 0) + output_tokens),0),
CASE WHEN SUM(generation_ms) > 0 THEN SUM(output_tokens) * 1000.0 / SUM(generation_ms) ELSE 0 END
FROM requests WHERE (? = 0 OR started_at_ms >= ?)`
	if err := store.db.QueryRowContext(ctx, query, cutoff, cutoff).Scan(
		&stats.Requests, &stats.Completed, &stats.Failed, &stats.Cancelled,
		&stats.Retries, &stats.InputTokens, &stats.OutputTokens, &stats.CachedTokens,
		&stats.ReasoningTokens, &stats.ProcessedTokens, &stats.NonCachedTokens,
		&stats.TokensPerSecond,
	); err != nil {
		return application.HistoryStats{}, errors.New("history statistics query failed")
	}
	if stats.Completed > 0 {
		offset := max(int64(float64(stats.Completed)*0.95+0.999999)-1, 0)
		if err := store.db.QueryRowContext(ctx,
			`SELECT latency_ms FROM requests WHERE state='completed' AND (? = 0 OR started_at_ms >= ?) ORDER BY latency_ms LIMIT 1 OFFSET ?`,
			cutoff, cutoff, offset,
		).Scan(&stats.P95MS); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return application.HistoryStats{}, errors.New("history percentile query failed")
		}
	}
	return stats, nil
}

func (store *Store) Close(ctx context.Context) error {
	if !store.closed.CompareAndSwap(false, true) {
		return nil
	}
	store.cancel()
	done := make(chan struct{})
	go func() { store.workers.Wait(); close(done) }()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
		return store.db.Close()
	}
}

func (store *Store) run(ctx context.Context) {
	defer store.workers.Done()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	batch := make([]domain.Request, 0, batchSize)
	lastPrune := time.Now()
	flush := func() {
		if len(batch) == 0 {
			return
		}
		_ = store.writeBatch(batch)
		batch = batch[:0]
		if time.Since(lastPrune) >= time.Hour {
			_ = store.prune(context.Background())
			lastPrune = time.Now()
		}
	}
	for {
		select {
		case request := <-store.queue:
			batch = append(batch, request)
			if len(batch) >= batchSize {
				flush()
			}
		case <-ticker.C:
			flush()
		case <-ctx.Done():
			for {
				select {
				case request := <-store.queue:
					batch = append(batch, request)
				default:
					flush()
					return
				}
			}
		}
	}
}

func (store *Store) writeBatch(batch []domain.Request) error {
	tx, err := store.db.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	statement, err := tx.Prepare(`INSERT OR REPLACE INTO requests (
request_id, started_at_ms, updated_at_ms, state, model_id, provider_id, method, path,
status_code, queue_ms, latency_ms, retries, bytes_in, bytes_out, error_code,
input_tokens, output_tokens, cached_tokens, reasoning_tokens, total_tokens, context_tokens,
generation_ms, tokens_per_second) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		tx.Rollback()
		return err
	}
	defer statement.Close()
	for _, request := range batch {
		if _, err := statement.Exec(
			request.ID, request.StartedAt.UnixMilli(), request.UpdatedAt.UnixMilli(), request.State,
			request.Model, request.ProviderID, request.Method, request.Path, request.Status,
			request.QueueMS, request.LatencyMS, request.Retries, request.BytesIn, request.BytesOut,
			request.ErrorCode, request.InputTokens, request.OutputTokens, request.CachedTokens,
			request.ReasoningTokens, request.TotalTokens, request.ContextTokens,
			request.GenerationMS, request.TokensPerSecond,
		); err != nil {
			tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

func (store *Store) prune(ctx context.Context) error {
	cutoff := time.Now().UTC().AddDate(0, 0, -store.retentionDays).UnixMilli()
	_, err := store.db.ExecContext(ctx, `DELETE FROM requests WHERE started_at_ms < ?`, cutoff)
	if err != nil {
		return errors.New("history retention failed")
	}
	return nil
}

func migrate(db *sql.DB) error {
	const schema = `CREATE TABLE IF NOT EXISTS requests (
request_id TEXT PRIMARY KEY, started_at_ms INTEGER NOT NULL, updated_at_ms INTEGER NOT NULL,
state TEXT NOT NULL CHECK(state IN ('completed','failed','cancelled')), model_id TEXT NOT NULL,
provider_id TEXT NOT NULL, method TEXT NOT NULL, path TEXT NOT NULL, status_code INTEGER NOT NULL,
queue_ms REAL NOT NULL, latency_ms REAL NOT NULL, retries INTEGER NOT NULL,
bytes_in INTEGER NOT NULL, bytes_out INTEGER NOT NULL, error_code TEXT NOT NULL,
input_tokens INTEGER NOT NULL, output_tokens INTEGER NOT NULL, cached_tokens INTEGER NOT NULL,
reasoning_tokens INTEGER NOT NULL, total_tokens INTEGER NOT NULL, context_tokens INTEGER NOT NULL,
generation_ms REAL NOT NULL, tokens_per_second REAL NOT NULL,
CHECK(cached_tokens <= input_tokens), CHECK(reasoning_tokens <= output_tokens));
CREATE INDEX IF NOT EXISTS requests_started ON requests(started_at_ms DESC);`
	if _, err := db.Exec(schema); err != nil {
		return fmt.Errorf("history migration failed: %w", err)
	}
	return nil
}

func periodCutoff(period application.Period, now time.Time) (int64, error) {
	var duration time.Duration
	switch period {
	case application.Period24H:
		duration = 24 * time.Hour
	case application.Period48H:
		duration = 48 * time.Hour
	case application.Period72H:
		duration = 72 * time.Hour
	case application.PeriodAll:
		return 0, nil
	default:
		return 0, errors.New("invalid history period")
	}
	return now.Add(-duration).UnixMilli(), nil
}

func terminal(state domain.State) bool {
	return state == domain.StateCompleted || state == domain.StateFailed || state == domain.StateCancelled
}
