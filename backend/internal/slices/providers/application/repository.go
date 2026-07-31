package application

import (
	"context"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/providers/domain"
)

type SavedState struct {
	Providers []domain.Provider
	ActiveID  string
}

type Repository interface {
	Load(context.Context) (SavedState, error)
	Save(context.Context, SavedState) error
}
