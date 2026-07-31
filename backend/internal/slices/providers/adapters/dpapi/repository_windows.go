//go:build windows

package dpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/luxuryprivate/switchboard/backend/internal/platform/atomicfile"
	"github.com/luxuryprivate/switchboard/backend/internal/platform/secretstore"
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
	RPM             int    `json:"rpm"`
	CacheTTLSeconds int64  `json:"cacheTtlSeconds"`
	Enabled         bool   `json:"enabled"`
	Builtin         bool   `json:"builtin"`
}

func New(path string) *Repository { return &Repository{path: path} }

func DefaultPath() (string, error) {
	root := os.Getenv("LOCALAPPDATA")
	if root == "" {
		return "", errors.New("LOCALAPPDATA is unavailable")
	}
	return filepath.Join(root, "ProviderSwitchboard", "providers.v2.dpapi"), nil
}

func (repository *Repository) Load(ctx context.Context) (application.SavedState, error) {
	if err := ctx.Err(); err != nil {
		return application.SavedState{}, err
	}
	raw, err := os.ReadFile(repository.path)
	if errors.Is(err, os.ErrNotExist) {
		return application.SavedState{}, nil
	}
	if err != nil || len(raw) <= len(fileMagic) || string(raw[:len(fileMagic)]) != string(fileMagic) {
		return application.SavedState{}, errors.New("encrypted provider settings could not be read")
	}
	plaintext, err := secretstore.Unprotect(raw[len(fileMagic):])
	if err != nil {
		return application.SavedState{}, err
	}
	defer clear(plaintext)
	var value document
	decoder := json.NewDecoder(bytes.NewReader(plaintext))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil || decoder.Decode(&struct{}{}) != io.EOF || value.Version != fileVersion {
		return application.SavedState{}, errors.New("encrypted provider settings are invalid")
	}
	providers := make([]domain.Provider, 0, len(value.Providers))
	for _, saved := range value.Providers {
		provider, err := domain.New(domain.Params{
			ID: saved.ID, Name: saved.Name, BaseURL: saved.BaseURL,
			AuthMode: domain.AuthMode(saved.AuthMode), AuthHeader: saved.AuthHeader,
			Dialect: domain.Dialect(saved.Dialect), ModelsPath: saved.ModelsPath,
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
		value.Providers = append(value.Providers, storedProvider{
			ID: provider.ID, Name: provider.Name, BaseURL: provider.BaseURL.String(),
			AuthMode: string(provider.AuthMode), RPM: provider.RPM,
			AuthHeader: provider.AuthHeader, Dialect: string(provider.Dialect),
			ModelsPath:      provider.ModelsPath,
			CacheTTLSeconds: int64(provider.CacheTTL / time.Second),
			Enabled:         provider.Enabled, Builtin: provider.Builtin,
		})
	}
	plaintext, err := json.Marshal(value)
	if err != nil {
		return errors.New("provider settings could not be encoded")
	}
	protected, err := secretstore.Protect(plaintext)
	clear(plaintext)
	if err != nil {
		return err
	}
	payload := append(append([]byte(nil), fileMagic...), protected...)
	defer clear(payload)
	return atomicfile.Replace(repository.path, payload, 0o600)
}
