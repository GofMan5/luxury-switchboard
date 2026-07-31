package relaystdio

import (
	"context"
	"encoding/json"
	"time"

	platform "github.com/luxuryprivate/switchboard/backend/internal/platform/stdio"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/relay/application"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/relay/domain"
)

func Register(server *platform.Server, service *application.Service) {
	server.Handle("relay.status", func(_ context.Context, _ json.RawMessage) (any, error) {
		return service.Snapshot(), nil
	})
	server.Handle("relay.start", func(_ context.Context, _ json.RawMessage) (any, error) {
		snapshot, err := service.Start()
		if err != nil {
			return nil, platform.MethodError{Code: "relay_start_failed", Message: "Relay could not start"}
		}
		return snapshot, nil
	})
	server.Handle("relay.stop", func(ctx context.Context, _ json.RawMessage) (any, error) {
		stopCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if err := service.Stop(stopCtx); err != nil {
			return nil, platform.MethodError{Code: "relay_stop_failed", Message: "Relay could not stop cleanly"}
		}
		return service.Snapshot(), nil
	})
	_ = service.OnChanged(func(snapshot domain.Snapshot) {
		_ = server.Emit("relay.changed", snapshot)
	})
}
