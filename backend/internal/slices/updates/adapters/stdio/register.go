package updatesstdio

import (
	"context"
	"encoding/json"
	"errors"

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
		result, err := service.Install(ctx, progress)
		if err != nil {
			return nil, installError(err)
		}
		return result, nil
	})
}

// installError keeps the failure distinguishable without carrying anything
// upstream said: every message is this package's own sentence, and the codes
// are stable for the interface.
func installError(err error) platform.MethodError {
	switch {
	case errors.Is(err, updatesapp.ErrAlreadyCurrent):
		return platform.MethodError{Code: "already_current", Message: "This build is already the newest release."}
	case errors.Is(err, updatesapp.ErrNoSelfUpdate):
		return platform.MethodError{Code: "no_self_update", Message: "The release ships no self-update for this platform; the release page is the path."}
	default:
		return platform.MethodError{Code: "update_failed", Message: err.Error()}
	}
}
