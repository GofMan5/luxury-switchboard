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

// The repository must implement the application layer's AccountStore
// port exactly; the service binds to the interface, not this package.
var _ application.AccountStore = (*Repository)(nil)

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

// storePaths returns a repository rooted in a fresh directory with its
// legacy path alongside, so every test migrates the same shape the
// composition root wires.
func storePaths(t *testing.T) *Repository {
	t.Helper()
	base := t.TempDir()
	return NewRepository(filepath.Join(base, accountsDirName), filepath.Join(base, legacyFileName))
}

// prepareDir creates the account directory for tests that hand-write
// files into it; Save would create it itself, but a hand-written
// fixture must not depend on that.
func prepareDir(t *testing.T, repository *Repository) {
	t.Helper()
	if err := os.MkdirAll(repository.dir, 0o700); err != nil {
		t.Fatal(err)
	}
}

func TestLoadOfEmptyDirectoryReportsNoAccounts(t *testing.T) {
	sessions, err := storePaths(t).Load(context.Background())
	if err != nil {
		t.Fatalf("load of an empty directory must not fail: %v", err)
	}
	if len(sessions) != 0 {
		t.Fatalf("empty directory reported accounts: %d", len(sessions))
	}
}

func TestLoadIgnoresFilesThatAreNotAccountFiles(t *testing.T) {
	repository := storePaths(t)
	prepareDir(t, repository)
	if err := os.WriteFile(filepath.Join(repository.dir, "readme.txt"), []byte("notes"), 0o600); err != nil {
		t.Fatal(err)
	}
	sessions, err := repository.Load(context.Background())
	if err != nil {
		t.Fatalf("load must skip non-account files: %v", err)
	}
	if len(sessions) != 0 {
		t.Fatalf("non-account file reported an account: %d", len(sessions))
	}
}

func TestClearOfMissingFileSucceeds(t *testing.T) {
	if err := storePaths(t).Clear(context.Background(), domain.Session{}); err != nil {
		t.Fatalf("clear of a missing file must succeed: %v", err)
	}
}

func TestAnUnconfiguredRepositoryRefusesInsteadOfTouchingTheWorkdir(t *testing.T) {
	// The composition root hands the repository empty paths when the
	// user-data location cannot be resolved, and the app keeps booting.
	// Joining an empty directory with a file name resolves relative to
	// the process working directory, so an unconfigured store would
	// drop a session file into whatever directory the app happened to
	// start from — a secret in an unowned location. It must refuse
	// every operation and touch nothing.
	repository := NewRepository("", "")
	session := domain.Session{Identity: domain.Identity{
		Email:          "dev@example.com",
		AccountID:      "acc_1",
		OrganizationID: "org_1",
	}}
	requireStoreError(t, repository.Save(context.Background(), session))
	requireStoreError(t, repository.Clear(context.Background(), session))
	requireStoreError(t, repository.ClearAll(context.Background()))
	requireStoreError(t, func() error {
		_, err := repository.Load(context.Background())
		return err
	}())
	if _, err := os.Stat(repository.accountPath(session)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the working directory was touched: %v", err)
	}
}

func TestLoadRejectsFileWithForeignMagic(t *testing.T) {
	// keypool's magic: a file another slice wrote must not decode as a
	// codex session. The magic check fails before any decryption, so
	// this case needs no platform secret store.
	repository := storePaths(t)
	prepareDir(t, repository)
	if err := os.WriteFile(filepath.Join(repository.dir, "codex_foreign.dpapi"), []byte("SWKEYS2\npayload"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := repository.Load(context.Background())
	requireStoreError(t, err)
}

func TestLoadRejectsGarbageFile(t *testing.T) {
	repository := storePaths(t)
	prepareDir(t, repository)
	if err := os.WriteFile(filepath.Join(repository.dir, "codex_garbage.dpapi"), []byte("not a codex session store"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := repository.Load(context.Background())
	requireStoreError(t, err)
}

func TestLoadRejectsTruncatedFile(t *testing.T) {
	// The magic alone carries no encrypted payload to decrypt.
	repository := storePaths(t)
	prepareDir(t, repository)
	if err := os.WriteFile(filepath.Join(repository.dir, "codex_truncated.dpapi"), fileMagic, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := repository.Load(context.Background())
	requireStoreError(t, err)
}

func TestClearAllRemovesAccountFilesAndTheLegacyFileOnly(t *testing.T) {
	repository := storePaths(t)
	prepareDir(t, repository)
	keeper := filepath.Join(repository.dir, "readme.txt")
	for _, name := range []string{"codex_first.dpapi", "codex_second.dpapi"} {
		if err := os.WriteFile(filepath.Join(repository.dir, name), fileMagic, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(repository.legacyPath, fileMagic, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keeper, []byte("notes"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := repository.ClearAll(context.Background()); err != nil {
		t.Fatalf("clear all failed: %v", err)
	}
	for _, gone := range []string{repository.legacyPath, filepath.Join(repository.dir, "codex_first.dpapi"), filepath.Join(repository.dir, "codex_second.dpapi")} {
		if _, err := os.Stat(gone); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("clear all left %q behind: %v", filepath.Base(gone), err)
		}
	}
	if _, err := os.Stat(keeper); err != nil {
		t.Fatalf("clear all removed a file that is not its own: %v", err)
	}
}

func TestCanceledContextIsRefusedBeforeDiskAccess(t *testing.T) {
	repository := storePaths(t)
	prepareDir(t, repository)
	// Files that exist prove the context check precedes any file
	// access.
	if err := os.WriteFile(filepath.Join(repository.dir, "codex_placeholder.dpapi"), []byte("placeholder"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(repository.legacyPath, []byte("placeholder"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := repository.Load(ctx); err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("load ignored cancellation: %v", err)
	} else {
		requireStoreError(t, err)
	}
	if err := repository.Save(ctx, domain.Session{}); err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("save ignored cancellation: %v", err)
	} else {
		requireStoreError(t, err)
	}
	if err := repository.Clear(ctx, domain.Session{}); err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("clear ignored cancellation: %v", err)
	} else {
		requireStoreError(t, err)
	}
	if err := repository.ClearAll(ctx); err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("clear all ignored cancellation: %v", err)
	} else {
		requireStoreError(t, err)
	}
}

func TestDefaultPathsLiveInTheUserDataDirectory(t *testing.T) {
	base := t.TempDir()
	var wantDir, wantLegacy string
	switch runtime.GOOS {
	case "windows":
		t.Setenv("LOCALAPPDATA", base)
		wantDir = filepath.Join(base, "ProviderSwitchboard", accountsDirName)
		wantLegacy = filepath.Join(base, "ProviderSwitchboard", legacyFileName)
	case "darwin":
		t.Setenv("HOME", base)
		wantDir = filepath.Join(base, "Library", "Application Support", "provider-switchboard", accountsDirName)
		wantLegacy = filepath.Join(base, "Library", "Application Support", "provider-switchboard", legacyFileName)
	default:
		t.Setenv("XDG_CONFIG_HOME", base)
		wantDir = filepath.Join(base, "provider-switchboard", accountsDirName)
		wantLegacy = filepath.Join(base, "provider-switchboard", legacyFileName)
	}
	dir, legacy, err := DefaultPaths()
	if err != nil {
		t.Fatalf("default paths are unavailable: %v", err)
	}
	if dir != wantDir || legacy != wantLegacy {
		t.Fatalf("unexpected default paths: dir=%q legacy=%q want dir=%q legacy=%q", dir, legacy, wantDir, wantLegacy)
	}
}
