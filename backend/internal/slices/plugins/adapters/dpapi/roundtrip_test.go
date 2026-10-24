//go:build windows || linux

package dpapi

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/plugins/domain"
)

func TestPluginsRoundTripEncrypted(t *testing.T) {
	useTestKeyring()
	path := filepath.Join(t.TempDir(), "plugins.dpapi")
	repository := New(path)
	if plugins, err := repository.Load(context.Background()); err != nil || len(plugins) != 0 {
		t.Fatalf("a missing file is not an empty store: %v %v", plugins, err)
	}
	installed := time.Date(2026, 2, 3, 4, 5, 6, 0, time.FixedZone("fixture", 3*3600))
	plugins := []domain.Plugin{{
		Package: domain.Package{
			Schema: 1, ID: "acme.dusk", Name: "Dusk", Version: "1.0.0", Author: "Fixture", Description: "Fixture plugin",
			Permissions: []string{domain.PermissionTheme, domain.PermissionStorage},
			Theme:       &domain.Theme{Name: "Dusk", Scheme: "dark", Tokens: map[string]string{"--bg": "#101010"}},
			Script:      "export default 'fixture-private-script'",
		},
		Enabled:     true,
		Granted:     []string{domain.PermissionStorage},
		InstalledAt: installed,
		UpdatedAt:   installed.Add(time.Hour),
		Storage:     map[string]string{"prefs:a": "fixture-private-value"},
	}}
	if err := repository.Save(context.Background(), plugins); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(raw, magic) || bytes.Contains(raw, []byte("fixture-private")) {
		t.Fatal("plugins were stored as plaintext")
	}
	loaded, err := repository.Load(context.Background())
	if err != nil || len(loaded) != 1 {
		t.Fatalf("unexpected restored plugins: %+v %v", loaded, err)
	}
	if !loaded[0].InstalledAt.Equal(installed) || loaded[0].InstalledAt.Location() != time.UTC {
		t.Fatalf("times were not stored as UTC: %v", loaded[0].InstalledAt)
	}
	want := plugins[0]
	want.InstalledAt, want.UpdatedAt = want.InstalledAt.UTC(), want.UpdatedAt.UTC()
	if !reflect.DeepEqual(loaded[0], want) {
		t.Fatalf("restored plugin differs:\n%+v\n%+v", loaded[0], want)
	}
	if err := loaded[0].Validate(); err != nil {
		t.Fatalf("the restored plugin is invalid: %v", err)
	}
}
