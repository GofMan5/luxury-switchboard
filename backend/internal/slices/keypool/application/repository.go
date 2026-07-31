package application

import (
	"context"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/keypool/domain"
)

type Repository interface {
	Load(context.Context) ([]domain.Key, error)
	Save(context.Context, []domain.Key) error
}
