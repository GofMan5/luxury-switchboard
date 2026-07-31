//go:build windows

package dpapi

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/luxuryprivate/switchboard/backend/internal/platform/encryptedfile"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/settings/domain"
)

var magic = []byte("SWSET2\n")

type Repository struct{ path string }

type document struct {
	Version  int             `json:"version"`
	Settings domain.Settings `json:"settings"`
}

func New(path string) *Repository { return &Repository{path: path} }

func DefaultPath() (string, error) {
	root := os.Getenv("LOCALAPPDATA")
	if root == "" {
		return "", errors.New("LOCALAPPDATA is unavailable")
	}
	return filepath.Join(root, "ProviderSwitchboard", "settings.v2.dpapi"), nil
}

func (repository *Repository) Load(ctx context.Context) (domain.Settings, bool, error) {
	if err := ctx.Err(); err != nil {
		return domain.Settings{}, false, err
	}
	var value document
	found, err := encryptedfile.Load(repository.path, magic, encryptedfile.DefaultMaxPlaintext, &value)
	if err != nil {
		return domain.Settings{}, false, err
	}
	if !found {
		return domain.Settings{}, false, nil
	}
	if value.Version != 1 {
		return domain.Settings{}, false, errors.New("encrypted settings are invalid")
	}
	return value.Settings, true, nil
}

func (repository *Repository) Save(ctx context.Context, settings domain.Settings) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return encryptedfile.Save(repository.path, magic, encryptedfile.DefaultMaxPlaintext, document{Version: 1, Settings: settings})
}
