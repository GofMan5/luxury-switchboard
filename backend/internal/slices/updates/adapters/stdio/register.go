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
	// The download reports itself as it goes: the operator started a
	// tens-of-megabytes transfer and a percent is the difference between
	// progress and a frozen window. The answer is the verified path, which
	// only the shell's own run-installer command will execute.
	server.Handle("updates.install", func(ctx context.Context, _ json.RawMessage) (any, error) {
		progress := func(report updatesapp.InstallProgress) {
			_ = server.Emit("updates.installProgress", report)
		}
		return service.Install(ctx, progress)
	})
}
