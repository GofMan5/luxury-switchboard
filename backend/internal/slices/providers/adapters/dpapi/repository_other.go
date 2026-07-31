//go:build !windows

package dpapi

import (
	"context"
	"errors"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/providers/application"
)

type Repository struct{}

func New(string) *Repository       { return &Repository{} }
func DefaultPath() (string, error) { return "", errors.New("secure provider storage is unavailable") }
func (*Repository) Load(context.Context) (application.SavedState, error) {
	return application.SavedState{}, errors.New("secure provider storage is unavailable")
}
func (*Repository) Save(context.Context, application.SavedState) error {
	return errors.New("secure provider storage is unavailable")
}
