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

// Profiles persists the owner-only ban and note decisions for client addresses.
type Profiles interface {
	Profiles(context.Context) ([]domain.Profile, error)
	SaveProfile(context.Context, domain.Profile) error
	DeleteProfile(context.Context, string) error
}
