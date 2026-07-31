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

	"github.com/luxuryprivate/switchboard/backend/internal/platform/atomicfile"
	"github.com/luxuryprivate/switchboard/backend/internal/platform/secretstore"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/keypool/domain"
)

const (
	fileVersion   = 1
	fileName      = "keys.v2.dpapi"
	maxConfigSize = 4 * 1024 * 1024
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
	root := os.Getenv("LOCALAPPDATA")
	if root == "" {
		return "", errors.New("LOCALAPPDATA is unavailable")
	}
	return filepath.Join(root, "ProviderSwitchboard", fileName), nil
}

func (repository *Repository) Load(ctx context.Context) ([]domain.Key, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(repository.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil || len(raw) <= len(fileMagic) || len(raw) > maxConfigSize*2 {
		return nil, errors.New("encrypted key settings could not be read")
	}
	if string(raw[:len(fileMagic)]) != string(fileMagic) {
		return nil, errors.New("encrypted key settings have an invalid header")
	}
	plaintext, err := secretstore.Unprotect(raw[len(fileMagic):])
	if err != nil {
		return nil, err
	}
	defer clear(plaintext)
	var value document
	decoder := json.NewDecoder(bytes.NewReader(plaintext))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil || decoder.Decode(&struct{}{}) != io.EOF || value.Version != fileVersion {
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
	plaintext, err := json.Marshal(value)
	if err != nil || len(plaintext) > maxConfigSize {
		return errors.New("key settings could not be encoded")
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
