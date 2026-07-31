package application

import (
	"context"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/tunnelclients/domain"
)

type History interface {
	Record(domain.Event) bool
	Recent(context.Context, string, int) ([]domain.Event, error)
	Close(context.Context) error
}
