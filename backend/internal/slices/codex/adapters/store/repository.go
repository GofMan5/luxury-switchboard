// Package store persists Codex OAuth sessions as one DPAPI-encrypted
// file per account, implementing the AccountStore port the codex
// application layer defines.
//
// Each file is a magic prefix followed by a DPAPI-protected JSON
// document: token material exists in the clear only in memory, and the
// write goes through the shared atomic replace, so a crash leaves either
// the previous session or the new one — never a torn file. The file name
// is the digest of its exact AccountID, so no email reaches the
// directory listing and changing email/org overwrites the same file.
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
	"sync"
	"time"

	"github.com/luxuryprivate/switchboard/backend/internal/platform/appdata"
	"github.com/luxuryprivate/switchboard/backend/internal/platform/encryptedfile"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/codex/application"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/codex/domain"
)

const (
	// fileVersion is the on-disk schema version this package writes.
	// Version 1 carried only the session; version 2 added the login
	// method and the reset counter. Version 1 files are still readable
	// — their accounts simply report an unknown method and zero
	// resets — because a sign-in must not be lost to a schema widening.
	// Any other version is refused rather than best-effort decoded: a
	// session is only useful whole.
	fileVersion = 2

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
	// LoginMethod is the application LoginMethod value as a string;
	// version 1 files carry an empty one, which the application reads
	// as an unknown method.
	LoginMethod string `json:"loginMethod,omitempty"`
	Resets      int    `json:"resets"`
}

// storedAccount renders a decoded document as the application's
// StoredAccount. LoginMethod and Resets travel verbatim: the store does
// not interpret them, it only keeps them across restarts.
func (value document) storedAccount() application.StoredAccount {
	return application.StoredAccount{
		Session:     value.Session,
		LoginMethod: application.LoginMethod(value.LoginMethod),
		Resets:      value.Resets,
	}
}

// Repository persists one Codex account per file inside a directory.
// Operations on an instance are serialized so migration cannot race a
// save or resurrect a file concurrently cleared by that instance.
// ponytail: per-repository serialization; use per-ID locks only if
// measured account-file contention warrants coordinating migration too.
type Repository struct {
	mu         sync.Mutex
	dir        string
	legacyPath string
}

// NewRepository returns a Repository persisting Codex accounts as files
// in dir. legacyPath is the pre-accounts single-session file, migrated
// by Load and removed by ClearAll; parent directories are created on the
// first Save.
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

// accountPath keys persistence by the same exact AccountID as the
// application. Email and organization are mutable account metadata, not
// separate identities. The existing digest keeps identity out of listings.
func (repository *Repository) accountPath(session domain.Session) string {
	storageID := domain.StorageID("", session.Identity.AccountID, "")
	return filepath.Join(repository.dir, storageID+".dpapi")
}

// Load migrates email/org-keyed files to exact AccountID filenames and
// returns one whole record per ID, ordered by its stable filename. The
// newest supported record with tokens and a nonempty AccountID wins by
// file mtime, not token expiry; ties prefer stable file, then filename order.
// Unreadable files remain an error rather than silently losing a sign-in.
func (repository *Repository) Load(ctx context.Context) ([]application.StoredAccount, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
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
	files, err := repository.accountFiles(ctx, names)
	if err != nil {
		return nil, err
	}
	latest := make(map[string]accountFile)
	for _, file := range files {
		if !validStoredAccount(file.stored) {
			continue
		}
		path := repository.accountPath(file.stored.Session)
		previous, exists := latest[path]
		if !exists || file.modified.After(previous.modified) ||
			(file.modified.Equal(previous.modified) && file.name == filepath.Base(path)) {
			latest[path] = file
		}
	}
	// An invalid record is removable only when a usable record for the
	// same exact ID survives. Otherwise fail without deleting evidence.
	for _, file := range files {
		if _, exists := latest[repository.accountPath(file.stored.Session)]; !exists {
			return nil, errors.New("codex session store account is invalid")
		}
	}
	// A singleton is obsolete once plural storage exists. Leaving it
	// ignored would reactivate it after the last specific logout.
	if err := repository.removeFile(repository.legacyPath); err != nil {
		return nil, err
	}
	paths := make([]string, 0, len(latest))
	for path := range latest {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	accounts := make([]application.StoredAccount, 0, len(paths))
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("codex session store could not be migrated: %w", err)
		}
		file := latest[path]
		if file.name != filepath.Base(path) {
			// Commit the complete winning record before removing any copy.
			if err := encryptedfile.Save(path, fileMagic, encryptedfile.DefaultMaxPlaintext, documentFromStored(file.stored)); err != nil {
				return nil, fmt.Errorf("codex session store could not be migrated: %w", err)
			}
		}
		accounts = append(accounts, file.stored)
	}
	for _, file := range files {
		if file.name != filepath.Base(repository.accountPath(file.stored.Session)) {
			if err := repository.removeFile(filepath.Join(repository.dir, file.name)); err != nil {
				return nil, err
			}
		}
	}
	return accounts, nil
}

// Validity does not depend on token expiry: a renewable expired session
// is still a whole credential record. No upstream/network check is made.
func validStoredAccount(stored application.StoredAccount) bool {
	return stored.Session.Identity.AccountID != "" && !stored.Session.Empty()
}

// accountFile retains filesystem freshness only during an operation.
// Legacy documents have no save timestamp, so mtime is the available
// durable ordering; access expiry cannot order reauthentication/reset.
type accountFile struct {
	name     string
	stored   application.StoredAccount
	modified time.Time
}

func (repository *Repository) accountFiles(ctx context.Context, names []string) ([]accountFile, error) {
	files := make([]accountFile, 0, len(names))
	for _, name := range names {
		path := filepath.Join(repository.dir, name)
		info, err := os.Stat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, errors.New("codex session store could not be read")
		}
		stored, found, err := repository.loadFile(ctx, path)
		if err != nil {
			return nil, err
		}
		if found {
			files = append(files, accountFile{name: name, stored: stored, modified: info.ModTime()})
		}
	}
	return files, nil
}

// removeFile never includes a path (which may be legacy identity data)
// in diagnostics. An unset optional legacy path is not a file.
func (repository *Repository) removeFile(path string) error {
	if path == "" {
		return nil
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return errors.New("codex session store file could not be removed")
	}
	return nil
}

// loadLegacyOrEmpty migrates the legacy single-session file when the
// account directory is empty, and answers no accounts otherwise. The
// migrated record is rewritten at the current version so the legacy
// file never survives its own migration.
func (repository *Repository) loadLegacyOrEmpty() ([]application.StoredAccount, error) {
	var value document
	found, err := encryptedfile.Load(repository.legacyPath, fileMagic, encryptedfile.DefaultMaxPlaintext, &value)
	if err != nil || !found {
		if err != nil {
			return nil, fmt.Errorf("codex session store could not be migrated: %w", err)
		}
		return nil, nil
	}
	if !supportedVersion(value.Version) {
		return nil, fmt.Errorf("codex session store could not be migrated: version %d is not supported", value.Version)
	}
	stored := value.storedAccount()
	if !validStoredAccount(stored) {
		return nil, errors.New("codex session store account is invalid")
	}
	if err := encryptedfile.Save(repository.accountPath(stored.Session), fileMagic, encryptedfile.DefaultMaxPlaintext, documentFromStored(stored)); err != nil {
		return nil, fmt.Errorf("codex session store could not be migrated: %w", err)
	}
	if err := repository.removeFile(repository.legacyPath); err != nil {
		return nil, err
	}
	return []application.StoredAccount{stored}, nil
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

// supportedVersion reports whether a stored document version can be
// read. Version 2 is what this package writes; version 1 is the
// session-only shape from before login methods and reset counters
// existed, readable so a schema widening never costs a sign-in.
func supportedVersion(version int) bool {
	return version == 1 || version == 2
}

// loadFile decodes one account file. A missing file is reported as
// not found rather than an error, so a file removed between the
// directory listing and the read costs its account nothing.
func (repository *Repository) loadFile(ctx context.Context, path string) (application.StoredAccount, bool, error) {
	if err := ctx.Err(); err != nil {
		return application.StoredAccount{}, false, fmt.Errorf("codex session store could not be loaded: %w", err)
	}
	var value document
	found, err := encryptedfile.Load(path, fileMagic, encryptedfile.DefaultMaxPlaintext, &value)
	if err != nil {
		return application.StoredAccount{}, false, fmt.Errorf("codex session store could not be loaded: %w", err)
	}
	if !found {
		return application.StoredAccount{}, false, nil
	}
	if !supportedVersion(value.Version) {
		return application.StoredAccount{}, false, fmt.Errorf("codex session store version %d is not supported", value.Version)
	}
	return value.storedAccount(), true, nil
}

// documentFromStored renders the record exactly as Save persists it —
// one place, so the migration writer and the save writer cannot drift.
func documentFromStored(stored application.StoredAccount) document {
	return document{
		Version:     fileVersion,
		Session:     stored.Session,
		LoginMethod: string(stored.LoginMethod),
		Resets:      stored.Resets,
	}
}

// Save durably stores one account: it is JSON-encoded, DPAPI-encrypted
// and written through an atomic replace that also creates the parent
// directory, so a crash leaves either the previous session or the new
// one, and re-signing an identity replaces exactly that account's
// file. The login method and reset counter are part of the record: a
// caller that reconstructs only the session would erase them here.
func (repository *Repository) Save(ctx context.Context, stored application.StoredAccount) error {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if repository.dir == "" {
		return errUnconfigured
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("codex session store could not be saved: %w", err)
	}
	if !validStoredAccount(stored) {
		return errors.New("codex session store account is invalid")
	}
	names, err := repository.accountFileNames()
	if err != nil {
		return err
	}
	files, err := repository.accountFiles(ctx, names)
	if err != nil {
		return err
	}
	path := repository.accountPath(stored.Session)
	if err := encryptedfile.Save(path, fileMagic, encryptedfile.DefaultMaxPlaintext, documentFromStored(stored)); err != nil {
		return fmt.Errorf("codex session store could not be saved: %w", err)
	}
	for _, file := range files {
		if file.stored.Session.Identity.AccountID == stored.Session.Identity.AccountID && file.name != filepath.Base(path) {
			if err := repository.removeFile(filepath.Join(repository.dir, file.name)); err != nil {
				return err
			}
		}
	}
	return repository.removeFile(repository.legacyPath)
}

// Clear removes every file for an exact AccountID, including unmigrated
// email/org copies. It also retires an ignored singleton before deleting
// plural files, so emptying the directory cannot resurrect that singleton.
// With only a singleton present, a legitimate different ID is preserved.
func (repository *Repository) Clear(ctx context.Context, session domain.Session) error {
	repository.mu.Lock()
	defer repository.mu.Unlock()
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
	files, err := repository.accountFiles(ctx, names)
	if err != nil {
		return err
	}
	if len(names) != 0 {
		if err := repository.removeFile(repository.legacyPath); err != nil {
			return err
		}
	} else if repository.legacyPath != "" {
		legacy, found, err := repository.loadFile(ctx, repository.legacyPath)
		if err != nil {
			return err
		}
		if found && legacy.Session.Identity.AccountID == session.Identity.AccountID {
			if err := repository.removeFile(repository.legacyPath); err != nil {
				return err
			}
		}
	}
	path := repository.accountPath(session)
	for _, file := range files {
		if file.stored.Session.Identity.AccountID == session.Identity.AccountID && file.name != filepath.Base(path) {
			if err := repository.removeFile(filepath.Join(repository.dir, file.name)); err != nil {
				return err
			}
		}
	}
	// Remove the stable record last: a failed legacy cleanup must not
	// leave only an older credential available on the next load.
	return repository.removeFile(path)
}

// ClearAll removes every account file plus any leftover legacy file.
// Files it does not own — anything that is not a .dpapi account file
// or the legacy path — are left in place. A missing directory or file
// is a success: after ClearAll no account is stored either way.
func (repository *Repository) ClearAll(ctx context.Context) error {
	repository.mu.Lock()
	defer repository.mu.Unlock()
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
	if err := repository.removeFile(repository.legacyPath); err != nil {
		return err
	}
	for _, name := range names {
		if err := repository.removeFile(filepath.Join(repository.dir, name)); err != nil {
			return err
		}
	}
	return nil
}
