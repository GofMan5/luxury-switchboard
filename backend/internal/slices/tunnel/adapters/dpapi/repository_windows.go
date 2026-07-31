//go:build windows

package dpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/luxuryprivate/switchboard/backend/internal/platform/atomicfile"
	"github.com/luxuryprivate/switchboard/backend/internal/platform/secretstore"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/tunnel/domain"
	"io"
	"os"
	"path/filepath"
)

var magic = []byte("SWTUN2\n")

type Repository struct{ path string }
type document struct {
	Version int          `json:"version"`
	Config  storedConfig `json:"config"`
}
type storedConfig struct {
	Port             int    `json:"port"`
	Token            string `json:"token"`
	RPMPerIP         int    `json:"rpmPerIp"`
	ContextLimitKiB  int    `json:"contextLimitKiB"`
	BrandResponse    string `json:"brandResponse"`
	PublisherProfile string `json:"publisherProfile,omitempty"`
}

func New(path string) *Repository { return &Repository{path: path} }
func DefaultPath() (string, error) {
	root := os.Getenv("LOCALAPPDATA")
	if root == "" {
		return "", errors.New("LOCALAPPDATA unavailable")
	}
	return filepath.Join(root, "ProviderSwitchboard", "tunnel.v2.dpapi"), nil
}
func (repository *Repository) Load(ctx context.Context) (domain.Config, bool, error) {
	if err := ctx.Err(); err != nil {
		return domain.Config{}, false, err
	}
	raw, err := os.ReadFile(repository.path)
	if errors.Is(err, os.ErrNotExist) {
		return domain.Config{}, false, nil
	}
	if err != nil || len(raw) <= len(magic) || string(raw[:len(magic)]) != string(magic) {
		return domain.Config{}, false, errors.New("tunnel settings unreadable")
	}
	plain, err := secretstore.Unprotect(raw[len(magic):])
	if err != nil {
		return domain.Config{}, false, err
	}
	defer clear(plain)
	var value document
	decoder := json.NewDecoder(bytes.NewReader(plain))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil || decoder.Decode(&struct{}{}) != io.EOF || (value.Version != 1 && value.Version != 2) {
		return domain.Config{}, false, errors.New("tunnel settings invalid")
	}
	config := domain.Config{Port: value.Config.Port, Token: value.Config.Token, RPMPerIP: value.Config.RPMPerIP, ContextLimitKiB: value.Config.ContextLimitKiB, BrandResponse: value.Config.BrandResponse, PublisherProfile: value.Config.PublisherProfile}
	if config.Validate() != nil {
		return domain.Config{}, false, errors.New("tunnel settings invalid")
	}
	return config, true, nil
}
func (repository *Repository) Save(ctx context.Context, config domain.Config) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	stored := storedConfig{Port: config.Port, Token: config.Token, RPMPerIP: config.RPMPerIP, ContextLimitKiB: config.ContextLimitKiB, BrandResponse: config.BrandResponse, PublisherProfile: config.PublisherProfile}
	plain, err := json.Marshal(document{Version: 2, Config: stored})
	if err != nil {
		return errors.New("tunnel settings encode failed")
	}
	protected, err := secretstore.Protect(plain)
	clear(plain)
	if err != nil {
		return err
	}
	payload := append(append([]byte(nil), magic...), protected...)
	defer clear(payload)
	return atomicfile.Replace(repository.path, payload, 0o600)
}
