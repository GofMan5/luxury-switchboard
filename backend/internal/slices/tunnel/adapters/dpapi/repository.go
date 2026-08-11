package dpapi

import (
	"context"
	"errors"
	"path/filepath"

	"github.com/luxuryprivate/switchboard/backend/internal/platform/appdata"
	"github.com/luxuryprivate/switchboard/backend/internal/platform/encryptedfile"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/tunnel/domain"
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
	root, err := appdata.Root()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "tunnel.v2.dpapi"), nil
}
func (repository *Repository) Load(ctx context.Context) (domain.Config, bool, error) {
	if err := ctx.Err(); err != nil {
		return domain.Config{}, false, err
	}
	var value document
	found, err := encryptedfile.Load(repository.path, magic, encryptedfile.DefaultMaxPlaintext, &value)
	if err != nil {
		return domain.Config{}, false, err
	}
	if !found {
		return domain.Config{}, false, nil
	}
	if value.Version != 1 && value.Version != 2 {
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
	return encryptedfile.Save(repository.path, magic, encryptedfile.DefaultMaxPlaintext, document{Version: 2, Config: stored})
}
