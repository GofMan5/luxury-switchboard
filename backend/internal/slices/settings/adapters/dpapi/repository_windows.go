//go:build windows

package dpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/luxuryprivate/switchboard/backend/internal/platform/atomicfile"
	"github.com/luxuryprivate/switchboard/backend/internal/platform/secretstore"
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
	raw, err := os.ReadFile(repository.path)
	if errors.Is(err, os.ErrNotExist) {
		return domain.Settings{}, false, nil
	}
	if err != nil || len(raw) <= len(magic) || string(raw[:len(magic)]) != string(magic) {
		return domain.Settings{}, false, errors.New("encrypted settings could not be read")
	}
	plaintext, err := secretstore.Unprotect(raw[len(magic):])
	if err != nil {
		return domain.Settings{}, false, err
	}
	defer clear(plaintext)
	var value document
	decoder := json.NewDecoder(bytes.NewReader(plaintext))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil || decoder.Decode(&struct{}{}) != io.EOF || value.Version != 1 {
		return domain.Settings{}, false, errors.New("encrypted settings are invalid")
	}
	return value.Settings, true, nil
}

func (repository *Repository) Save(ctx context.Context, settings domain.Settings) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	plaintext, err := json.Marshal(document{Version: 1, Settings: settings})
	if err != nil {
		return errors.New("settings could not be encoded")
	}
	protected, err := secretstore.Protect(plaintext)
	clear(plaintext)
	if err != nil {
		return err
	}
	payload := append(append([]byte(nil), magic...), protected...)
	defer clear(payload)
	return atomicfile.Replace(repository.path, payload, 0o600)
}
