// Package store persists Codex OAuth sessions as one DPAPI-encrypted
// file per account, implementing the AccountStore port the codex
// application layer defines.
//
// Each file is a magic prefix followed by a DPAPI-protected JSON
// document: token material exists in the clear only in memory, and the
// write goes through the shared atomic replace, so a crash leaves either
// the previous session or the new one — never a torn file. The file name
// is the digest of the account's identity, so no email reaches the
// directory listing and re-saving an identity overwrites its own file.
//
// Errors from this package carry the "codex session store" prefix and
// never contain session material: the encrypted-file layer reports
// generic causes only, and the store adds nothing but its own prefix.
package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/luxuryprivate/switchboard/backend/internal/platform/appdata"
	"github.com/luxuryprivate/switchboard/backend/internal/platform/encryptedfile"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/codex/domain"
)

const (
	// fileVersion is the on-disk schema version. A file with any other
	// version is refused rather than best-effort decoded: a session is
	// only useful whole.
	fileVersion = 1

	// legacyFileName is the single-session file this package wrote
	// before accounts became plural. Load migrates it once; ClearAll
	// removes whatever is left of it.
	legacyFileName = "codex.v2.dpapi"

	// accountsDirName is the directory holding one file per account
	// inside the per-user data directory.
	accountsDirName = "codex-accounts"
)

// fileMagic prefixes the encrypted payload so a file written by another
// subsystem (the keyring writes a sibling magic) fails before any
// decryption is attempted, without a platform secret store call.
var fileMagic = []byte("SWCODEX2\n")

// document is the JSON-encoded plaintext inside the DPAPI envelope.
type document struct {
	Version int            `json:"version"`
	Session domain.Session `json:"session"`
}

// Repository persists one Codex account per file inside a directory.
// The directory and the legacy single-session path are fixed at
// construction and the type holds no mutable state, so a single
// instance can be shared for the lifetime of the process.
type Repository struct {
	dir        string
	legacyPath string
}

// NewRepository returns a Repository persisting Codex accounts as files
// in dir. legacyPath is the pre-accounts single-session file, migrated
// by Load and removed by ClearAll; parent directories are created on
// the first Save.
func NewRepository(dir, legacyPath string) *Repository {
	return &Repository{dir: dir, legacyPath: legacyPath}
}

// errUnconfigured refuses every operation on a repository whose location
// was never resolved: an empty directory would resolve account file
// names against the process working directory, dropping a session file
// into whatever directory the app happened to start from. The
// composition root keeps booting when the user-data location cannot be
// resolved — sign-ins simply stop persisting — so this refusal is the
// honest form of that degradation.
var errUnconfigured = errors.New("codex session store location is not configured")

// DefaultPaths returns the account directory and the legacy
// single-session path inside the per-user data directory, so the
// composition root does not hardcode either location.
func DefaultPaths() (dir string, legacyPath string, err error) {
	root, err := appdata.Root()
	if err != nil {
		return "", "", err
	}
	return filepath.Join(root, accountsDirName), filepath.Join(root, legacyFileName), nil
}

// accountPath returns the file that stores an account's session: the
// identity digest, stable per account and carrying no readable
// identity in the directory listing.
func (repository *Repository) accountPath(session domain.Session) string {
	storageID := domain.StorageID(
		session.Identity.Email,
		session.Identity.AccountID,
		session.Identity.OrganizationID,
	)
	return filepath.Join(repository.dir, storageID+".dpapi")
}

// Load returns every stored account, in file-name order. A missing
// directory is not an error: it means no account exists and the caller
// must sign in. A file that cannot be read, decrypted, decoded or
// versioned is an error, and the error never carries session material.
//
// The single-session file this package used to write is migrated on the
// first load: when the directory holds no account and the legacy file
// exists, its session becomes the first account file and the legacy
// file is removed. A legacy file that exists but cannot be read is an
// error — silently dropping the user's sign-in would be worse than
// refusing to start. Once the directory holds accounts, any leftover
// legacy file is ignored.
func (repository *Repository) Load(ctx context.Context) ([]domain.Session, error) {
	if repository.dir == "" {
		return nil, errUnconfigured
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("codex session store could not be loaded: %w", err)
	}
	names, err := repository.accountFileNames()
	if err != nil {
		return nil, err
	}
	if len(names) == 0 {
		return repository.loadLegacyOrEmpty()
	}
	sessions := make([]domain.Session, 0, len(names))
	for _, name := range names {
		session, found, err := repository.loadFile(ctx, filepath.Join(repository.dir, name))
		if err != nil {
			return nil, err
		}
		if !found {
			// The file vanished between the listing and the read;
			// the account simply does not exist.
			continue
		}
		sessions = append(sessions, session)
	}
	return sessions, nil
}

// loadLegacyOrEmpty migrates the legacy single-session file when the
// account directory is empty, and answers no accounts otherwise.
func (repository *Repository) loadLegacyOrEmpty() ([]domain.Session, error) {
	var value document
	found, err := encryptedfile.Load(repository.legacyPath, fileMagic, encryptedfile.DefaultMaxPlaintext, &value)
	if err != nil || !found {
		if err != nil {
			return nil, fmt.Errorf("codex session store could not be migrated: %w", err)
		}
		return nil, nil
	}
	if value.Version != fileVersion {
		return nil, fmt.Errorf("codex session store could not be migrated: version %d is not supported", value.Version)
	}
	if err := encryptedfile.Save(repository.accountPath(value.Session), fileMagic, encryptedfile.DefaultMaxPlaintext, document{Version: fileVersion, Session: value.Session}); err != nil {
		return nil, fmt.Errorf("codex session store could not be migrated: %w", err)
	}
	if err := os.Remove(repository.legacyPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("codex session store could not be migrated: %w", err)
	}
	return []domain.Session{value.Session}, nil
}

// accountFileNames lists the account files in the directory, sorted by
// name for a deterministic load order. A missing directory is an empty
// list, not an error.
func (repository *Repository) accountFileNames() ([]string, error) {
	entries, err := os.ReadDir(repository.dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("codex session store could not be loaded: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".dpapi" {
			continue
		}
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	return names, nil
}

// loadFile decodes one account file. A missing file is reported as
// not found rather than an error, so a file removed between the
// directory listing and the read costs its account nothing.
func (repository *Repository) loadFile(ctx context.Context, path string) (domain.Session, bool, error) {
	if err := ctx.Err(); err != nil {
		return domain.Session{}, false, fmt.Errorf("codex session store could not be loaded: %w", err)
	}
	var value document
	found, err := encryptedfile.Load(path, fileMagic, encryptedfile.DefaultMaxPlaintext, &value)
	if err != nil {
		return domain.Session{}, false, fmt.Errorf("codex session store could not be loaded: %w", err)
	}
	if !found {
		return domain.Session{}, false, nil
	}
	if value.Version != fileVersion {
		return domain.Session{}, false, fmt.Errorf("codex session store version %d is not supported", value.Version)
	}
	return value.Session, true, nil
}

// Save durably stores one account: it is JSON-encoded, DPAPI-encrypted
// and written through an atomic replace that also creates the parent
// directory, so a crash leaves either the previous session or the new
// one, and re-signing an identity replaces exactly that account's
// file.
func (repository *Repository) Save(ctx context.Context, session domain.Session) error {
	if repository.dir == "" {
		return errUnconfigured
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("codex session store could not be saved: %w", err)
	}
	value := document{Version: fileVersion, Session: session}
	if err := encryptedfile.Save(repository.accountPath(session), fileMagic, encryptedfile.DefaultMaxPlaintext, value); err != nil {
		return fmt.Errorf("codex session store could not be saved: %w", err)
	}
	return nil
}

// Clear removes one account's file. A missing file is a success: after
// Clear that account is gone either way.
func (repository *Repository) Clear(ctx context.Context, session domain.Session) error {
	if repository.dir == "" {
		return errUnconfigured
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("codex session store could not be cleared: %w", err)
	}
	if err := os.Remove(repository.accountPath(session)); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("codex session store could not be cleared: %w", err)
	}
	return nil
}

// ClearAll removes every account file plus any leftover legacy file.
// Files it does not own — anything that is not a .dpapi account file
// or the legacy path — are left in place. A missing directory or file
// is a success: after ClearAll no account is stored either way.
func (repository *Repository) ClearAll(ctx context.Context) error {
	if repository.dir == "" {
		return errUnconfigured
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("codex session store could not be cleared: %w", err)
	}
	names, err := repository.accountFileNames()
	if err != nil {
		return err
	}
	for _, name := range names {
		if err := os.Remove(filepath.Join(repository.dir, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("codex session store could not be cleared: %w", err)
		}
	}
	if err := os.Remove(repository.legacyPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("codex session store could not be cleared: %w", err)
	}
	return nil
}
