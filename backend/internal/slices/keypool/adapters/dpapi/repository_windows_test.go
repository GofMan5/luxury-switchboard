//go:build windows

package dpapi

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/keypool/domain"
)

func TestRepositoryRoundTripIsEncryptedAndAtomic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys.dpapi")
	repository := New(path)
	key, err := domain.NewKey(domain.Params{
		ProviderID: "echo", Label: "Primary", Secret: "private-fixture-secret",
		RPM: 120, ProxyURL: "http://proxy.invalid:8080",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.Save(context.Background(), []domain.Key{key}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range [][]byte{[]byte("private-fixture-secret"), []byte("proxy.invalid")} {
		if bytes.Contains(raw, forbidden) {
			t.Fatal("encrypted repository contains plaintext")
		}
	}
	loaded, err := repository.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 1 || loaded[0].Credential.Reveal() != "private-fixture-secret" || loaded[0].RPM != 120 {
		t.Fatalf("unexpected restored key: %+v", loaded)
	}

	key.RPM = 90
	if err := repository.Save(context.Background(), []domain.Key{key}); err != nil {
		t.Fatal(err)
	}
	loaded, err = repository.Load(context.Background())
	if err != nil || loaded[0].RPM != 90 {
		t.Fatalf("atomic replacement did not persist update: %+v %v", loaded, err)
	}
}
