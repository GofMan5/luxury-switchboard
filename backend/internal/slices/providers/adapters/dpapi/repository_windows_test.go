//go:build windows

package dpapi

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/providers/application"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/providers/domain"
)

func TestProviderRepositoryRoundTripIsEncrypted(t *testing.T) {
	provider, err := domain.New(domain.Params{
		ID: "custom", Name: "Private provider", BaseURL: "https://private-provider.invalid/v1",
		AuthMode: domain.AuthBearer, RPM: 120, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "providers.dpapi")
	repository := New(path)
	if err := repository.Save(context.Background(), application.SavedState{
		Providers: []domain.Provider{provider}, ActiveID: provider.ID,
	}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("private-provider.invalid")) || bytes.Contains(raw, []byte("Private provider")) {
		t.Fatal("provider metadata was stored as plaintext")
	}
	loaded, err := repository.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Providers) != 1 || loaded.ActiveID != "custom" || loaded.Providers[0].RPM != 120 {
		t.Fatalf("unexpected provider state: %+v", loaded)
	}
}
