package updatesstdio

import (
	"context"
	"encoding/json"

	platform "github.com/luxuryprivate/switchboard/backend/internal/platform/stdio"
	updatesapp "github.com/luxuryprivate/switchboard/backend/internal/slices/updates/application"
)

func Register(server *platform.Server, service *updatesapp.Service) {
	server.Handle("updates.check", func(ctx context.Context, _ json.RawMessage) (any, error) {
		return service.Check(ctx), nil
	})
}
