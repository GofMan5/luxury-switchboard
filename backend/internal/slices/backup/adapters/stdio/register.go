package backupstdio

import (
	"context"
	"encoding/json"
	"path/filepath"

	"github.com/luxuryprivate/switchboard/backend/internal/platform/appdata"
	platform "github.com/luxuryprivate/switchboard/backend/internal/platform/stdio"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/backup/application"
)

func Register(server *platform.Server, service *application.Service) {
	server.Handle("backup.export", func(_ context.Context, _ json.RawMessage) (any, error) {
		root, err := appdata.Root()
		if err != nil {
			return nil, platform.MethodError{Code: "backup_failed", Message: "The backup folder is unavailable"}
		}
		path, err := service.Export(filepath.Join(root, "backups"))
		if err != nil {
			return nil, platform.MethodError{Code: "backup_failed", Message: "The backup could not be written"}
		}
		return map[string]string{"path": path}, nil
	})
	server.Handle("backup.import", func(ctx context.Context, payload json.RawMessage) (any, error) {
		var command struct {
			Content string `json:"content"`
		}
		if platform.DecodePayload(payload, &command) != nil || command.Content == "" {
			return nil, platform.MethodError{Code: "invalid_payload", Message: "Invalid backup content"}
		}
		report, err := service.Import(command.Content)
		if err != nil {
			return nil, platform.MethodError{Code: "backup_invalid", Message: err.Error()}
		}
		return report, nil
	})
}
