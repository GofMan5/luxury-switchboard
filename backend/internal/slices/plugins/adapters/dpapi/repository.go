package dpapi

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"time"

	"github.com/luxuryprivate/switchboard/backend/internal/platform/appdata"
	"github.com/luxuryprivate/switchboard/backend/internal/platform/encryptedfile"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/plugins/application"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/plugins/domain"
)

const fileVersion = 1

// maxPlaintext is the absolute cap on the plugins document. Every plugin's
// package and storage each fit one stdio payload, so 24 of them could in
// principle outgrow it; Save reports that as application.ErrCapacity before
// anything is written.
const maxPlaintext = 8 * 1024 * 1024

var magic = []byte("SWPLUG1\n")

type Repository struct{ path string }

type document struct {
	Version int            `json:"version"`
	Plugins []storedPlugin `json:"plugins"`
}

type storedPlugin struct {
	Package     domain.Package    `json:"package"`
	Enabled     bool              `json:"enabled"`
	Granted     []string          `json:"granted"`
	InstalledAt time.Time         `json:"installedAt"`
	UpdatedAt   time.Time         `json:"updatedAt"`
	Storage     map[string]string `json:"storage"`
	// Secrets are the owner's secret setting values. They live only here,
	// encrypted with the plugin, and are never part of a backup.
	Secrets map[string]string `json:"secrets,omitempty"`
	Dev     *domain.DevLink   `json:"dev,omitempty"`
}

func New(path string) *Repository { return &Repository{path: path} }

func DefaultPath() (string, error) {
	root, err := appdata.Root()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "plugins.v1.dpapi"), nil
}

// Load returns the saved plugins as they were written; the application
// validates each one before trusting it.
func (repository *Repository) Load(ctx context.Context) ([]domain.Plugin, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if repository.path == "" {
		// An unresolved location is not an empty store.
		return nil, errors.New("plugin storage location is unavailable")
	}
	var value document
	found, err := encryptedfile.Load(repository.path, magic, maxPlaintext, &value)
	if err != nil || !found {
		return nil, err
	}
	if value.Version != fileVersion {
		return nil, errors.New("encrypted plugins are invalid")
	}
	plugins := make([]domain.Plugin, 0, len(value.Plugins))
	for _, saved := range value.Plugins {
		plugins = append(plugins, domain.Plugin{
			Package:     saved.Package,
			Enabled:     saved.Enabled,
			Granted:     saved.Granted,
			InstalledAt: saved.InstalledAt,
			UpdatedAt:   saved.UpdatedAt,
			Storage:     saved.Storage,
			Secrets:     saved.Secrets,
			Dev:         saved.Dev,
		})
	}
	return plugins, nil
}

func (repository *Repository) Save(ctx context.Context, plugins []domain.Plugin) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if repository.path == "" {
		return errors.New("plugin storage location is unavailable")
	}
	value := document{Version: fileVersion, Plugins: make([]storedPlugin, 0, len(plugins))}
	for _, plugin := range plugins {
		value.Plugins = append(value.Plugins, storedPlugin{
			Package:     plugin.Package,
			Enabled:     plugin.Enabled,
			Granted:     plugin.Granted,
			InstalledAt: plugin.InstalledAt.UTC(),
			UpdatedAt:   plugin.UpdatedAt.UTC(),
			Storage:     plugin.Storage,
			Secrets:     plugin.Secrets,
			Dev:         plugin.Dev,
		})
	}
	// encryptedfile refuses an oversized document without saying why; measure
	// first so a full file is reported as full rather than as a failed save.
	encoded, err := json.Marshal(value)
	size := len(encoded)
	clear(encoded)
	if err != nil {
		return errors.New("plugins could not be encoded")
	}
	if size > maxPlaintext {
		return application.ErrCapacity
	}
	return encryptedfile.Save(repository.path, magic, maxPlaintext, value)
}
