// Package store persists the Codex OAuth session as a single
// DPAPI-encrypted file, implementing the SessionStore port the codex
// application layer defines.
//
// The file is a magic prefix followed by a DPAPI-protected JSON
// document: token material exists in the clear only in memory, and the
// write goes through the shared atomic replace, so a crash leaves either
// the previous session or the new one — never a torn file. Errors from
// this package carry the "codex session store" prefix and never contain
// session material: the encrypted-file layer reports generic causes
// only, and the store adds nothing but its own prefix.
package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/luxuryprivate/switchboard/backend/internal/platform/appdata"
	"github.com/luxuryprivate/switchboard/backend/internal/platform/encryptedfile"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/codex/domain"
)

const (
	// fileVersion is the on-disk schema version. A file with any other
	// version is refused rather than best-effort decoded: a session is
	// only useful whole.
	fileVersion = 1

	// fileName is the store's file inside the per-user data directory.
	fileName = "codex.v2.dpapi"
)

// fileMagic prefixes the encrypted payload so the file is identifiable
// as the codex session store and distinguishable from every other
// DPAPI-protected file the application writes.
var fileMagic = []byte("SWCODEX2\n")

// Repository persists exactly one Codex session: the file at its path
// holds the DPAPI-encrypted JSON document, or does not exist when no
// session is stored. The path is fixed at construction and the type
// holds no mutable state, so a single instance can be shared for the
// lifetime of the process.
type Repository struct {
	path string
}

// document is the on-disk payload: a schema version plus the session
// itself, JSON-encoded inside the encrypted blob.
type document struct {
	Version int            `json:"version"`
	Session domain.Session `json:"session"`
}

// NewRepository returns a Repository persisting the Codex session at
// filePath. The parent directory is created on the first Save; the file
// itself appears only when a session is saved.
func NewRepository(filePath string) *Repository {
	return &Repository{path: filePath}
}

// DefaultPath returns the store's location in the per-user data
// directory, so the composition root does not hardcode it.
func DefaultPath() (string, error) {
	root, err := appdata.Root()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, fileName), nil
}

// Load returns the stored session. A missing file is not an error: it
// means no session exists and the caller must sign in. A file that
// cannot be read, decrypted, decoded or versioned is an error, and the
// error never carries session material.
func (repository *Repository) Load(ctx context.Context) (domain.Session, bool, error) {
	if err := ctx.Err(); err != nil {
		return domain.Session{}, false, fmt.Errorf("codex session store could not be loaded: %w", err)
	}
	var value document
	found, err := encryptedfile.Load(repository.path, fileMagic, encryptedfile.DefaultMaxPlaintext, &value)
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

// Save durably stores the session: it is JSON-encoded, DPAPI-encrypted
// and written through an atomic replace that also creates the parent
// directory, so a crash leaves either the previous session or the new
// one.
func (repository *Repository) Save(ctx context.Context, session domain.Session) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("codex session store could not be saved: %w", err)
	}
	value := document{Version: fileVersion, Session: session}
	if err := encryptedfile.Save(repository.path, fileMagic, encryptedfile.DefaultMaxPlaintext, value); err != nil {
		return fmt.Errorf("codex session store could not be saved: %w", err)
	}
	return nil
}

// Clear removes the stored session. A missing file is a success: after
// Clear there is no session either way.
func (repository *Repository) Clear(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("codex session store could not be cleared: %w", err)
	}
	if err := os.Remove(repository.path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("codex session store could not be cleared: %w", err)
	}
	return nil
}
