package dpapi

import (
	"context"
	"errors"
	"path/filepath"

	"github.com/luxuryprivate/switchboard/backend/internal/platform/appdata"
	"github.com/luxuryprivate/switchboard/backend/internal/platform/encryptedfile"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/rules/domain"
)

const fileVersion = 1

// maxPlaintext holds the largest set the service will save (192 KiB encoded)
// with room for the document envelope.
const maxPlaintext = 512 * 1024

var magic = []byte("SWRUL1\n")

type Repository struct{ path string }

type document struct {
	Version int        `json:"version"`
	Rules   domain.Set `json:"rules"`
}

func New(path string) *Repository { return &Repository{path: path} }

func DefaultPath() (string, error) {
	root, err := appdata.Root()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "rules.v1.dpapi"), nil
}

func (repository *Repository) Load(ctx context.Context) (domain.Set, bool, error) {
	if err := ctx.Err(); err != nil {
		return domain.Set{}, false, err
	}
	if repository.path == "" {
		// An unresolved location is not an empty store.
		return domain.Set{}, false, errors.New("rules storage location is unavailable")
	}
	var value document
	found, err := encryptedfile.Load(repository.path, magic, maxPlaintext, &value)
	if err != nil || !found {
		return domain.Set{}, false, err
	}
	if value.Version != fileVersion {
		return domain.Set{}, false, errors.New("encrypted rules are invalid")
	}
	return value.Rules, true, nil
}

func (repository *Repository) Save(ctx context.Context, rules domain.Set) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if repository.path == "" {
		return errors.New("rules storage location is unavailable")
	}
	return encryptedfile.Save(repository.path, magic, maxPlaintext, document{Version: fileVersion, Rules: rules})
}
