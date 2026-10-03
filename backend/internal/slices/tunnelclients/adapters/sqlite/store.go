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

	"github.com/luxuryprivate/switchboard/backend/internal/platform/appdata"
	"github.com/luxuryprivate/switchboard/backend/internal/platform/batchqueue"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/tunnelclients/domain"
	_ "modernc.org/sqlite"
)

const (
	queueCapacity = 4096
	batchSize     = 128
)

type Store struct {
	db        *sql.DB
	queue     *batchqueue.Queue[domain.Event]
	closed    atomic.Bool
	closeMu   sync.Mutex
	dbClosed  bool
	retention atomic.Int64
}

func DefaultPath() (string, error) {
	root, err := appdata.Root()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "tunnel_history.v1.db"), nil
}

// SetRetentionHours moves the retention window live; the prune running right
// after applies it instead of waiting for the next launch.
func (store *Store) SetRetentionHours(hours int) {
	if hours < 1 {
		return
	}
	store.retention.Store(int64(time.Duration(hours) * time.Hour))
	_ = store.prune(context.Background())
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
	store := &Store{db: db}
	store.retention.Store(int64(time.Duration(retentionHours) * time.Hour))
	if err := store.prune(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	store.queue = batchqueue.New(queueCapacity, batchSize, 100*time.Millisecond, store.writeBatch, func() { _ = store.prune(context.Background()) })
	return store, nil
}

func (store *Store) Record(event domain.Event) bool {
	if store.closed.Load() || event.ID == "" || event.IP == "" || (event.State != "completed" && event.State != "error") {
		return false
	}
	return store.queue.Add(event)
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

// Profiles returns every stored owner decision. Profiles are governance state, so
// they are never pruned with the request history.
func (store *Store) Profiles(ctx context.Context) ([]domain.Profile, error) {
	rows, err := store.db.QueryContext(ctx, `SELECT client_ip, banned, note FROM tunnel_client_profiles ORDER BY client_ip`)
	if err != nil {
		return nil, errors.New("tunnel client profile query failed")
	}
	defer rows.Close()
	result := make([]domain.Profile, 0, 16)
	for rows.Next() {
		var profile domain.Profile
		var banned int
		if err := rows.Scan(&profile.IP, &banned, &profile.Note); err != nil {
			return nil, errors.New("tunnel client profile row is invalid")
		}
		profile.Banned = banned != 0
		result = append(result, profile)
	}
	return result, rows.Err()
}

func (store *Store) SaveProfile(ctx context.Context, profile domain.Profile) error {
	banned := 0
	if profile.Banned {
		banned = 1
	}
	_, err := store.db.ExecContext(ctx, `INSERT INTO tunnel_client_profiles (client_ip, banned, note, updated_ms) VALUES (?,?,?,?)
ON CONFLICT(client_ip) DO UPDATE SET banned=excluded.banned, note=excluded.note, updated_ms=excluded.updated_ms`,
		profile.IP, banned, profile.Note, time.Now().UTC().UnixMilli())
	if err != nil {
		return errors.New("tunnel client profile could not be saved")
	}
	return nil
}

func (store *Store) DeleteProfile(ctx context.Context, ip string) error {
	if _, err := store.db.ExecContext(ctx, `DELETE FROM tunnel_client_profiles WHERE client_ip=?`, ip); err != nil {
		return errors.New("tunnel client profile could not be removed")
	}
	return nil
}

func (store *Store) Close(ctx context.Context) error {
	store.closed.Store(true)
	closeErr := store.queue.Close(ctx)
	// The database is closed even when the final flush timed out: an unflushed
	// WAL checkpoint is already the worst outcome of that path, and skipping
	// db.Close() on top of it left the handle open for a process that is on its
	// way out anyway.
	store.closeMu.Lock()
	defer store.closeMu.Unlock()
	if store.dbClosed {
		return closeErr
	}
	dbErr := store.db.Close()
	store.dbClosed = true
	if closeErr != nil {
		return closeErr
	}
	return dbErr
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
	_, err := store.db.ExecContext(ctx, `DELETE FROM tunnel_events WHERE time_ms < ?`, time.Now().UTC().Add(-time.Duration(store.retention.Load())).UnixMilli())
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
CREATE INDEX IF NOT EXISTS tunnel_events_client_time ON tunnel_events(client_ip, time_ms DESC);
CREATE TABLE IF NOT EXISTS tunnel_client_profiles (
client_ip TEXT PRIMARY KEY, banned INTEGER NOT NULL CHECK(banned IN (0,1)),
note TEXT NOT NULL, updated_ms INTEGER NOT NULL);`)
	if err != nil {
		return errors.New("tunnel history migration failed")
	}
	return nil
}
