package dpapi

import (
	"context"
	"errors"
	"path/filepath"

	"github.com/luxuryprivate/switchboard/backend/internal/platform/appdata"
	"github.com/luxuryprivate/switchboard/backend/internal/platform/encryptedfile"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/keypool/domain"
)

const (
	fileVersion = 1
	fileName    = "keys.v2.dpapi"
)

var fileMagic = []byte("SWKEYS2\n")

type Repository struct {
	path string
}

type document struct {
	Version int         `json:"version"`
	Keys    []storedKey `json:"keys"`
}

type storedKey struct {
	ProviderID string `json:"providerId"`
	Label      string `json:"label"`
	Secret     string `json:"secret"`
	Priority   int    `json:"priority"`
	RPM        int    `json:"rpm"`
	ProxyURL   string `json:"proxyUrl,omitempty"`
	Pinned     bool   `json:"pinned"`
}

func New(path string) *Repository {
	return &Repository{path: path}
}

func DefaultPath() (string, error) {
	root, err := appdata.Root()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, fileName), nil
}

func (repository *Repository) Load(ctx context.Context) ([]domain.Key, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var value document
	found, err := encryptedfile.Load(repository.path, fileMagic, encryptedfile.DefaultMaxPlaintext, &value)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, nil
	}
	if value.Version != fileVersion {
		return nil, errors.New("encrypted key settings are invalid")
	}
	keys := make([]domain.Key, 0, len(value.Keys))
	for _, saved := range value.Keys {
		key, err := domain.NewKey(domain.Params{
			ProviderID: saved.ProviderID, Label: saved.Label, Secret: saved.Secret,
			Priority: saved.Priority, RPM: saved.RPM, ProxyURL: saved.ProxyURL,
			Pinned: saved.Pinned,
		})
		if err != nil {
			return nil, errors.New("encrypted key settings contain an invalid key")
		}
		keys = append(keys, key)
	}
	return keys, nil
}

func (repository *Repository) Save(ctx context.Context, keys []domain.Key) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	value := document{Version: fileVersion, Keys: make([]storedKey, 0, len(keys))}
	for _, key := range keys {
		value.Keys = append(value.Keys, storedKey{
			ProviderID: key.ProviderID, Label: key.Label,
			Secret: key.Credential.Reveal(), Priority: key.Priority,
			RPM: key.RPM, ProxyURL: key.ProxyURL, Pinned: key.Pinned,
		})
	}
	return encryptedfile.Save(repository.path, fileMagic, encryptedfile.DefaultMaxPlaintext, value)
}
