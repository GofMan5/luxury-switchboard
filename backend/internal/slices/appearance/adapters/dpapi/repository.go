package dpapi

import (
	"context"
	"errors"
	"path/filepath"

	"github.com/luxuryprivate/switchboard/backend/internal/platform/appdata"
	"github.com/luxuryprivate/switchboard/backend/internal/platform/encryptedfile"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/appearance/domain"
)

const fileVersion = 1

// maxPlaintext comfortably holds the largest valid document: 32 KiB of custom
// CSS and 96 tokens, each escaped at most sixfold by JSON.
const maxPlaintext = 512 * 1024

var magic = []byte("SWAPP1\n")

type Repository struct{ path string }

type document struct {
	Version    int               `json:"version"`
	Appearance domain.Appearance `json:"appearance"`
}

func New(path string) *Repository { return &Repository{path: path} }

func DefaultPath() (string, error) {
	root, err := appdata.Root()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "appearance.v1.dpapi"), nil
}

func (repository *Repository) Load(ctx context.Context) (domain.Appearance, bool, error) {
	if err := ctx.Err(); err != nil {
		return domain.Appearance{}, false, err
	}
	if repository.path == "" {
		// An unresolved location is not an empty store.
		return domain.Appearance{}, false, errors.New("appearance storage location is unavailable")
	}
	var value document
	found, err := encryptedfile.Load(repository.path, magic, maxPlaintext, &value)
	if err != nil || !found {
		return domain.Appearance{}, false, err
	}
	if value.Version != fileVersion {
		return domain.Appearance{}, false, errors.New("encrypted appearance is invalid")
	}
	return value.Appearance, true, nil
}

func (repository *Repository) Save(ctx context.Context, appearance domain.Appearance) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if repository.path == "" {
		return errors.New("appearance storage location is unavailable")
	}
	return encryptedfile.Save(repository.path, magic, maxPlaintext, document{Version: fileVersion, Appearance: appearance})
}
