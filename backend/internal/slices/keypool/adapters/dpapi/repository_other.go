//go:build !windows

package dpapi

import (
	"context"
	"errors"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/keypool/domain"
)

type Repository struct{}

func New(string) *Repository       { return &Repository{} }
func DefaultPath() (string, error) { return "", errors.New("secure key storage is unavailable") }
func (*Repository) Load(context.Context) ([]domain.Key, error) {
	return nil, errors.New("secure key storage is unavailable")
}
func (*Repository) Save(context.Context, []domain.Key) error {
	return errors.New("secure key storage is unavailable")
}
