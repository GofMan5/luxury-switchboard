package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/codex/application"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/codex/domain"
)

// The repository must implement the application layer's SessionStore
// port exactly; the service binds to the interface, not this package.
var _ application.SessionStore = (*Repository)(nil)

// errPrefix is the exact prefix every error from this package carries.
const errPrefix = "codex session store"

// requireStoreError fails unless err is a store error carrying the
// package prefix. The prefix is what callers key diagnostics on; the
// rest of the text must stay free of session material.
func requireStoreError(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected a store error, got nil")
	}
	if !strings.HasPrefix(err.Error(), errPrefix) {
		t.Fatalf("error %q does not carry the %q prefix", err.Error(), errPrefix)
	}
}

func TestLoadOfMissingFileReportsNoSession(t *testing.T) {
	repository := NewRepository(filepath.Join(t.TempDir(), fileName))
	session, ok, err := repository.Load(context.Background())
	if err != nil {
		t.Fatalf("load of a missing file must not fail: %v", err)
	}
	if ok || session != (domain.Session{}) {
		t.Fatalf("missing file reported a session: ok=%v summary=%s", ok, session.RedactedSummary())
	}
}

func TestClearOfMissingFileSucceeds(t *testing.T) {
	repository := NewRepository(filepath.Join(t.TempDir(), fileName))
	if err := repository.Clear(context.Background()); err != nil {
		t.Fatalf("clear of a missing file must succeed: %v", err)
	}
}

func TestLoadRejectsFileWithForeignMagic(t *testing.T) {
	// keypool's magic: a file another slice wrote must not decode as a
	// codex session. The magic check fails before any decryption, so
	// this case needs no platform secret store.
	path := filepath.Join(t.TempDir(), fileName)
	if err := os.WriteFile(path, []byte("SWKEYS2\npayload"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err := NewRepository(path).Load(context.Background())
	requireStoreError(t, err)
}

func TestLoadRejectsGarbageFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), fileName)
	if err := os.WriteFile(path, []byte("not a codex session store"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err := NewRepository(path).Load(context.Background())
	requireStoreError(t, err)
}

func TestLoadRejectsTruncatedFile(t *testing.T) {
	// The magic alone carries no encrypted payload to decrypt.
	path := filepath.Join(t.TempDir(), fileName)
	if err := os.WriteFile(path, fileMagic, 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err := NewRepository(path).Load(context.Background())
	requireStoreError(t, err)
}

func TestCanceledContextIsRefusedBeforeDiskAccess(t *testing.T) {
	path := filepath.Join(t.TempDir(), fileName)
	// A file that exists proves the context check precedes any file
	// access.
	if err := os.WriteFile(path, []byte("placeholder"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	repository := NewRepository(path)
	if _, _, err := repository.Load(ctx); err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("load ignored cancellation: %v", err)
	} else {
		requireStoreError(t, err)
	}
	if err := repository.Save(ctx, domain.Session{}); err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("save ignored cancellation: %v", err)
	} else {
		requireStoreError(t, err)
	}
	if err := repository.Clear(ctx); err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("clear ignored cancellation: %v", err)
	} else {
		requireStoreError(t, err)
	}
}

func TestDefaultPathLivesInTheUserDataDirectory(t *testing.T) {
	base := t.TempDir()
	var want string
	switch runtime.GOOS {
	case "windows":
		t.Setenv("LOCALAPPDATA", base)
		want = filepath.Join(base, "ProviderSwitchboard", fileName)
	case "darwin":
		t.Setenv("HOME", base)
		want = filepath.Join(base, "Library", "Application Support", "provider-switchboard", fileName)
	default:
		t.Setenv("XDG_CONFIG_HOME", base)
		want = filepath.Join(base, "provider-switchboard", fileName)
	}
	path, err := DefaultPath()
	if err != nil {
		t.Fatalf("default path is unavailable: %v", err)
	}
	if path != want {
		t.Fatalf("unexpected default path: path=%q want=%q", path, want)
	}
}
