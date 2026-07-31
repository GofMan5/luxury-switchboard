package tunnelstdio

import (
	"context"
	"encoding/json"
	platform "github.com/luxuryprivate/switchboard/backend/internal/platform/stdio"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/tunnel/application"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/tunnel/domain"
	"time"
)

func Register(server *platform.Server, service *application.Service) {
	server.Handle("tunnel.get", func(_ context.Context, _ json.RawMessage) (any, error) { return service.Snapshot(), nil })
	server.Handle("tunnel.configure", func(ctx context.Context, payload json.RawMessage) (any, error) {
		current := service.Config()
		var command struct {
			Port             int    `json:"port"`
			RPMPerIP         int    `json:"rpmPerIp"`
			ContextLimitKiB  int    `json:"contextLimitKiB"`
			BrandResponse    string `json:"brandResponse"`
			PublisherProfile string `json:"publisherProfile"`
		}
		if platform.DecodePayload(payload, &command) != nil {
			return nil, invalid()
		}
		current.Port = command.Port
		current.RPMPerIP = command.RPMPerIP
		current.ContextLimitKiB = command.ContextLimitKiB
		current.BrandResponse = command.BrandResponse
		current.PublisherProfile = command.PublisherProfile
		if err := service.Configure(ctx, current); err != nil {
			return nil, failed()
		}
		return service.Snapshot(), nil
	})
	server.Handle("tunnel.start", func(ctx context.Context, _ json.RawMessage) (any, error) {
		if err := service.Start(ctx); err != nil {
			return nil, failed()
		}
		return service.Snapshot(), nil
	})
	server.Handle("tunnel.stop", func(ctx context.Context, _ json.RawMessage) (any, error) {
		stopCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if err := service.Stop(stopCtx); err != nil {
			return nil, failed()
		}
		return service.Snapshot(), nil
	})
	server.Handle("tunnel.rotate", func(ctx context.Context, _ json.RawMessage) (any, error) {
		token, err := service.RotateToken(ctx)
		if err != nil {
			return nil, failed()
		}
		return map[string]string{"token": token}, nil
	})
	server.Handle("tunnel.reveal", func(_ context.Context, _ json.RawMessage) (any, error) {
		return map[string]string{"token": service.RevealToken()}, nil
	})
	service.OnChanged(func(snapshot domain.Snapshot) { _ = server.Emit("tunnel.changed", snapshot) })
}
func invalid() platform.MethodError {
	return platform.MethodError{Code: "invalid_payload", Message: "Invalid tunnel settings"}
}
func failed() platform.MethodError {
	return platform.MethodError{Code: "tunnel_operation_failed", Message: "Tunnel operation failed"}
}
