//go:build !windows

package dpapi

import (
	"context"
	"errors"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/tunnel/domain"
)

type Repository struct{}

func New(string) *Repository       { return &Repository{} }
func DefaultPath() (string, error) { return "", errors.New("secure tunnel storage unavailable") }
func (*Repository) Load(context.Context) (domain.Config, bool, error) {
	return domain.Config{}, false, errors.New("secure tunnel storage unavailable")
}
func (*Repository) Save(context.Context, domain.Config) error {
	return errors.New("secure tunnel storage unavailable")
}
