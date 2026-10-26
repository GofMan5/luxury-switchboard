//go:build windows || linux

package dpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/luxuryprivate/switchboard/backend/internal/platform/encryptedfile"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/plugins/domain"
)

func TestPluginsRoundTripEncrypted(t *testing.T) {
	useTestKeyring()
	path := filepath.Join(t.TempDir(), "plugins.dpapi")
	devFolder := filepath.Join(t.TempDir(), "dusk")
	repository := New(path)
	if plugins, err := repository.Load(context.Background()); err != nil || len(plugins) != 0 {
		t.Fatalf("a missing file is not an empty store: %v %v", plugins, err)
	}
	installed := time.Date(2026, 2, 3, 4, 5, 6, 789_000_000, time.FixedZone("fixture", 3*3600))
	limit := 10.0
	plugins := []domain.Plugin{{
		Package: domain.Package{
			Schema: 1, ID: "acme.dusk", Name: "Dusk", Version: "1.0.0", Author: "Fixture", Description: "Fixture plugin",
			Homepage: "https://example.test/dusk", MinAppVersion: "1.0.0",
			Permissions: []string{domain.PermissionTheme, domain.PermissionStorage},
			Theme:       &domain.Theme{Name: "Dusk", Scheme: "dark", Tokens: map[string]string{"--bg": "#101010"}},
			Script:      "export default 'fixture-private-script'",
			Settings: []domain.Setting{
				{Key: "greeting", Label: "Greeting", Type: domain.SettingText, Default: json.RawMessage(`"<fixture-private-default>"`)},
				{Key: "limit", Label: "Limit", Type: domain.SettingNumber, Default: json.RawMessage(`2.5`), Max: &limit},
				{Key: "token", Label: "Token", Type: domain.SettingSecret},
			},
		},
		Enabled:     true,
		Granted:     []string{domain.PermissionStorage},
		InstalledAt: installed,
		UpdatedAt:   installed.Add(time.Hour),
		Storage:     map[string]string{"prefs:a": "fixture-private-value"},
		Secrets:     map[string]string{"token": "fixture-private-secret"},
		Dev:         &domain.DevLink{Path: devFolder, LoadedAt: installed.UTC(), Problem: "fixture-private-problem"},
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
	// The file encoder escapes HTML inside setting defaults; normalizing, as
	// the service does on load, gives back the installed bytes.
	loaded[0].Package = loaded[0].Package.Normalized()
	want := plugins[0]
	want.Package = want.Package.Normalized()
	want.InstalledAt, want.UpdatedAt = want.InstalledAt.UTC(), want.UpdatedAt.UTC()
	if !reflect.DeepEqual(loaded[0], want) {
		t.Fatalf("restored plugin differs:\n%+v\n%+v", loaded[0], want)
	}
	if err := loaded[0].Validate(); err != nil {
		t.Fatalf("the restored plugin is invalid: %v", err)
	}
}

func TestTheInstallLinkRoundTripsAndOlderRecordsHaveNone(t *testing.T) {
	useTestKeyring()
	path := filepath.Join(t.TempDir(), "plugins.dpapi")
	repository := New(path)
	now := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)
	linked := domain.Plugin{
		Package: domain.Package{Schema: 1, ID: "acme.linked", Name: "Linked", Version: "1.0.0", Permissions: []string{}},
		Enabled: true, Granted: []string{}, InstalledAt: now, UpdatedAt: now, Storage: map[string]string{},
		Source: "https://example.test/acme/linked.json",
	}
	if err := repository.Save(context.Background(), []domain.Plugin{linked}); err != nil {
		t.Fatal(err)
	}
	loaded, err := repository.Load(context.Background())
	if err != nil || len(loaded) != 1 || loaded[0].Source != linked.Source || loaded[0].Validate() != nil {
		t.Fatalf("the install link did not round trip: %+v %v", loaded, err)
	}

	// A record written before the link was kept has no "source" key.
	older := map[string]any{"version": fileVersion, "plugins": []map[string]any{{
		"package": linked.Package, "enabled": true, "granted": []string{}, "installedAt": now, "updatedAt": now, "storage": map[string]string{},
	}}}
	if err := encryptedfile.Save(path, magic, maxPlaintext, older); err != nil {
		t.Fatal(err)
	}
	loaded, err = repository.Load(context.Background())
	if err != nil || len(loaded) != 1 || loaded[0].Source != "" || loaded[0].Validate() != nil {
		t.Fatalf("an older record did not load without a link: %+v %v", loaded, err)
	}
}
