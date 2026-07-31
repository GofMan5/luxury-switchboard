//go:build !windows

package dpapi

import (
	"context"
	"errors"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/settings/domain"
)

type Repository struct{}

func New(string) *Repository       { return &Repository{} }
func DefaultPath() (string, error) { return "", errors.New("secure settings storage is unavailable") }
func (*Repository) Load(context.Context) (domain.Settings, bool, error) {
	return domain.Settings{}, false, errors.New("secure settings storage is unavailable")
}
func (*Repository) Save(context.Context, domain.Settings) error {
	return errors.New("secure settings storage is unavailable")
}
