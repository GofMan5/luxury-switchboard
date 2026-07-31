package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/tunnelclients/domain"
	_ "modernc.org/sqlite"
)

const (
	queueCapacity = 4096
	batchSize     = 128
)

type Store struct {
	db        *sql.DB
	queue     chan domain.Event
	cancel    context.CancelFunc
	workers   sync.WaitGroup
	closed    atomic.Bool
	retention time.Duration
}

func DefaultPath() (string, error) {
	root := os.Getenv("LOCALAPPDATA")
	if root == "" {
		return "", errors.New("LOCALAPPDATA is unavailable")
	}
	return filepath.Join(root, "ProviderSwitchboard", "tunnel_history.v1.db"), nil
}

func Open(path string, retentionHours int) (*Store, error) {
	if path == "" || retentionHours < 1 {
		return nil, errors.New("invalid tunnel history settings")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, errors.New("tunnel history directory could not be created")
	}
	dsn := "file:" + filepath.ToSlash(path) + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, errors.New("tunnel history database could not open")
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, errors.New("tunnel history database is unavailable")
	}
	if err := migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	store := &Store{db: db, queue: make(chan domain.Event, queueCapacity), cancel: cancel, retention: time.Duration(retentionHours) * time.Hour}
	if err := store.prune(context.Background()); err != nil {
		cancel()
		db.Close()
		return nil, err
	}
	store.workers.Add(1)
	go store.run(ctx)
	return store, nil
}

func (store *Store) Record(event domain.Event) bool {
	if store.closed.Load() || event.ID == "" || event.IP == "" || (event.State != "completed" && event.State != "error") {
		return false
	}
	select {
	case store.queue <- event:
		return true
	default:
		return false
	}
}

func (store *Store) Recent(ctx context.Context, ip string, limit int) ([]domain.Event, error) {
	if ip == "" {
		return nil, errors.New("client IP is required")
	}
	limit = min(max(limit, 1), 500)
	rows, err := store.db.QueryContext(ctx, `SELECT event_id, client_ip, time_ms, state, method, path, model_id, status_code, latency_ms, bytes_in, bytes_out, error_code FROM tunnel_events WHERE client_ip=? ORDER BY time_ms DESC LIMIT ?`, ip, limit)
	if err != nil {
		return nil, errors.New("tunnel history query failed")
	}
	defer rows.Close()
	result := make([]domain.Event, 0, limit)
	for rows.Next() {
		var event domain.Event
		var timeMS int64
		if err := rows.Scan(&event.ID, &event.IP, &timeMS, &event.State, &event.Method, &event.Path, &event.Model, &event.Status, &event.LatencyMS, &event.BytesIn, &event.BytesOut, &event.ErrorCode); err != nil {
			return nil, errors.New("tunnel history row is invalid")
		}
		event.Time = time.UnixMilli(timeMS).UTC()
		result = append(result, event)
	}
	return result, rows.Err()
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
	batch := make([]domain.Event, 0, batchSize)
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
		case event := <-store.queue:
			batch = append(batch, event)
			if len(batch) >= batchSize {
				flush()
			}
		case <-ticker.C:
			flush()
		case <-ctx.Done():
			for {
				select {
				case event := <-store.queue:
					batch = append(batch, event)
				default:
					flush()
					return
				}
			}
		}
	}
}

func (store *Store) writeBatch(batch []domain.Event) error {
	tx, err := store.db.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	statement, err := tx.Prepare(`INSERT OR REPLACE INTO tunnel_events (event_id, client_ip, time_ms, state, method, path, model_id, status_code, latency_ms, bytes_in, bytes_out, error_code) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		tx.Rollback()
		return err
	}
	defer statement.Close()
	for _, event := range batch {
		if _, err := statement.Exec(event.ID, event.IP, event.Time.UnixMilli(), event.State, event.Method, event.Path, event.Model, event.Status, event.LatencyMS, event.BytesIn, event.BytesOut, event.ErrorCode); err != nil {
			tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

func (store *Store) prune(ctx context.Context) error {
	_, err := store.db.ExecContext(ctx, `DELETE FROM tunnel_events WHERE time_ms < ?`, time.Now().UTC().Add(-store.retention).UnixMilli())
	if err != nil {
		return errors.New("tunnel history retention failed")
	}
	return nil
}

func migrate(db *sql.DB) error {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS tunnel_events (
event_id TEXT PRIMARY KEY, client_ip TEXT NOT NULL, time_ms INTEGER NOT NULL,
state TEXT NOT NULL CHECK(state IN ('completed','error')), method TEXT NOT NULL,
path TEXT NOT NULL, model_id TEXT NOT NULL, status_code INTEGER NOT NULL,
latency_ms REAL NOT NULL, bytes_in INTEGER NOT NULL, bytes_out INTEGER NOT NULL,
error_code TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS tunnel_events_client_time ON tunnel_events(client_ip, time_ms DESC);`)
	if err != nil {
		return errors.New("tunnel history migration failed")
	}
	return nil
}
