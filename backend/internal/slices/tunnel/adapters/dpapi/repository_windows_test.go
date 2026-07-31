//go:build windows

package dpapi

import (
	"bytes"
	"context"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/tunnel/domain"
	"os"
	"path/filepath"
	"testing"
)

func TestTunnelConfigEncryptedRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tunnel.dpapi")
	repo := New(path)
	config := domain.Config{Port: 18888, Token: "fixture-token-000000000000000000000000000000", RPMPerIP: 30, BrandResponse: "private brand"}
	if err := repo.Save(context.Background(), config); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	if bytes.Contains(raw, []byte("fixture-token")) || bytes.Contains(raw, []byte("private brand")) {
		t.Fatal("tunnel config stored plaintext")
	}
	loaded, found, err := repo.Load(context.Background())
	if err != nil || !found || loaded != config {
		t.Fatalf("unexpected config: %+v %v %v", loaded, found, err)
	}
}
