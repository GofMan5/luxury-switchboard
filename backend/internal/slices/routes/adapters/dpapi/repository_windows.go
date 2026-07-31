//go:build windows

package dpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/luxuryprivate/switchboard/backend/internal/platform/atomicfile"
	"github.com/luxuryprivate/switchboard/backend/internal/platform/secretstore"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/routes/domain"
	"io"
	"os"
	"path/filepath"
)

var magic = []byte("SWROUTE2\n")

type Repository struct{ path string }
type document struct {
	Version     int                 `json:"version"`
	Assignments []domain.Assignment `json:"assignments"`
}

func New(path string) *Repository { return &Repository{path: path} }
func DefaultPath() (string, error) {
	root := os.Getenv("LOCALAPPDATA")
	if root == "" {
		return "", errors.New("LOCALAPPDATA is unavailable")
	}
	return filepath.Join(root, "ProviderSwitchboard", "routes.v2.dpapi"), nil
}
func (repository *Repository) Load(ctx context.Context) ([]domain.Assignment, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(repository.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil || len(raw) <= len(magic) || string(raw[:len(magic)]) != string(magic) {
		return nil, errors.New("encrypted routes could not be read")
	}
	plaintext, err := secretstore.Unprotect(raw[len(magic):])
	if err != nil {
		return nil, err
	}
	defer clear(plaintext)
	var value document
	decoder := json.NewDecoder(bytes.NewReader(plaintext))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil || decoder.Decode(&struct{}{}) != io.EOF || value.Version != 1 {
		return nil, errors.New("encrypted routes are invalid")
	}
	for _, assignment := range value.Assignments {
		if assignment.Validate() != nil {
			return nil, errors.New("encrypted routes contain invalid data")
		}
	}
	return value.Assignments, nil
}
func (repository *Repository) Save(ctx context.Context, assignments []domain.Assignment) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	plaintext, err := json.Marshal(document{Version: 1, Assignments: assignments})
	if err != nil {
		return errors.New("routes could not be encoded")
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
