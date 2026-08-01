package jsonfile

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLockWaitHonorsContext(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.lock")
	first, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if err := lockFile(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	defer unlockFile(first) //nolint:errcheck
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := lockFile(ctx, second); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("lock ignored its context: %v", err)
	}
}
