//go:build !windows

package dpapi

import (
	"context"
	"errors"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/routes/domain"
)

type Repository struct{}

func New(string) *Repository       { return &Repository{} }
func DefaultPath() (string, error) { return "", errors.New("secure routes storage is unavailable") }
func (*Repository) Load(context.Context) ([]domain.Assignment, error) {
	return nil, errors.New("secure routes storage is unavailable")
}
func (*Repository) Save(context.Context, []domain.Assignment) error {
	return errors.New("secure routes storage is unavailable")
}
