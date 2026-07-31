//go:build windows

package dpapi

import (
	"bytes"
	"context"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/settings/domain"
	"os"
	"path/filepath"
	"testing"
)

func TestSettingsRepositoryRoundTripIsEncrypted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.dpapi")
	repository := New(path)
	settings := domain.Defaults()
	settings.ListenerPort = 19000
	if err := repository.Save(context.Background(), settings); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(`"listenerPort":19000`)) {
		t.Fatal("settings were stored as plaintext")
	}
	loaded, found, err := repository.Load(context.Background())
	if err != nil || !found || loaded != settings {
		t.Fatalf("unexpected restored settings: %+v %v %v", loaded, found, err)
	}
}
