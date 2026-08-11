package dpapi

import (
	"context"
	"errors"
	"path/filepath"
	"time"

	"github.com/luxuryprivate/switchboard/backend/internal/platform/appdata"
	"github.com/luxuryprivate/switchboard/backend/internal/platform/encryptedfile"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/providers/application"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/providers/domain"
)

const fileVersion = 1

var fileMagic = []byte("SWPROV2\n")

type Repository struct{ path string }

type document struct {
	Version   int              `json:"version"`
	ActiveID  string           `json:"activeId"`
	Providers []storedProvider `json:"providers"`
}

type storedProvider struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	BaseURL         string `json:"baseUrl"`
	AuthMode        string `json:"authMode"`
	AuthHeader      string `json:"authHeader,omitempty"`
	Dialect         string `json:"dialect"`
	ModelsPath      string `json:"modelsPath"`
	ImageCompat     *bool  `json:"imageCompat,omitempty"`
	RPM             int    `json:"rpm"`
	CacheTTLSeconds int64  `json:"cacheTtlSeconds"`
	Enabled         bool   `json:"enabled"`
	Builtin         bool   `json:"builtin"`
}

func New(path string) *Repository { return &Repository{path: path} }

func DefaultPath() (string, error) {
	root, err := appdata.Root()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "providers.v2.dpapi"), nil
}

func (repository *Repository) Load(ctx context.Context) (application.SavedState, error) {
	if err := ctx.Err(); err != nil {
		return application.SavedState{}, err
	}
	var value document
	found, err := encryptedfile.Load(repository.path, fileMagic, encryptedfile.DefaultMaxPlaintext, &value)
	if err != nil {
		return application.SavedState{}, err
	}
	if !found {
		return application.SavedState{}, nil
	}
	if value.Version != fileVersion {
		return application.SavedState{}, errors.New("encrypted provider settings are invalid")
	}
	providers := make([]domain.Provider, 0, len(value.Providers))
	for _, saved := range value.Providers {
		imageCompat := saved.ImageCompat != nil && *saved.ImageCompat
		if saved.ImageCompat == nil && saved.ID == "echo" {
			imageCompat = true
		}
		provider, err := domain.New(domain.Params{
			ID: saved.ID, Name: saved.Name, BaseURL: saved.BaseURL,
			AuthMode: domain.AuthMode(saved.AuthMode), AuthHeader: saved.AuthHeader,
			Dialect: domain.Dialect(saved.Dialect), ModelsPath: saved.ModelsPath, ImageCompat: imageCompat,
			RPM:      saved.RPM,
			CacheTTL: time.Duration(saved.CacheTTLSeconds) * time.Second,
			Enabled:  saved.Enabled, Builtin: saved.Builtin,
		})
		if err != nil {
			return application.SavedState{}, errors.New("encrypted provider settings contain an invalid provider")
		}
		providers = append(providers, provider)
	}
	return application.SavedState{Providers: providers, ActiveID: value.ActiveID}, nil
}

func (repository *Repository) Save(ctx context.Context, state application.SavedState) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	value := document{Version: fileVersion, ActiveID: state.ActiveID, Providers: make([]storedProvider, 0, len(state.Providers))}
	for _, provider := range state.Providers {
		imageCompat := provider.ImageCompat
		value.Providers = append(value.Providers, storedProvider{
			ID: provider.ID, Name: provider.Name, BaseURL: provider.BaseURL.String(),
			AuthMode: string(provider.AuthMode), RPM: provider.RPM,
			AuthHeader: provider.AuthHeader, Dialect: string(provider.Dialect),
			ModelsPath:      provider.ModelsPath,
			ImageCompat:     &imageCompat,
			CacheTTLSeconds: int64(provider.CacheTTL / time.Second),
			Enabled:         provider.Enabled, Builtin: provider.Builtin,
		})
	}
	return encryptedfile.Save(repository.path, fileMagic, encryptedfile.DefaultMaxPlaintext, value)
}
