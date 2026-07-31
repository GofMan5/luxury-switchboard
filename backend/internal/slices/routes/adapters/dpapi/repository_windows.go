//go:build windows

package dpapi

import (
	"context"
	"errors"
	"github.com/luxuryprivate/switchboard/backend/internal/platform/encryptedfile"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/routes/domain"
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
	var value document
	found, err := encryptedfile.Load(repository.path, magic, encryptedfile.DefaultMaxPlaintext, &value)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, nil
	}
	if value.Version != 1 {
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
	return encryptedfile.Save(repository.path, magic, encryptedfile.DefaultMaxPlaintext, document{Version: 1, Assignments: assignments})
}
