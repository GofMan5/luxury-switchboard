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
	settings.NotificationsEnabled = false
	// The stream timers ride the same document; their tags must round-trip so
	// a saved heartbeat survives the next launch instead of resetting.
	settings.HeartbeatSeconds = 20
	settings.StreamProbationMilliseconds = 300
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
	if err != nil || !found || !loaded.Equal(settings) {
		t.Fatalf("unexpected restored settings: %+v %v %v", loaded, found, err)
	}
}

// A settings file written before the switches existed must load with them on:
// an absent bool is the previous build's silence, not the user's choice.
func TestPreSwitchSettingsLoadWithSwitchesOn(t *testing.T) {
	absent := storedSettings{GuardrailMode: domain.DefaultGuardrailMode}
	loaded := absent.restore()
	if !loaded.NotificationsEnabled || !loaded.ProviderHealthEnabled || !loaded.AnimationsEnabled {
		t.Fatalf("an older settings file lost the switch defaults: %+v", loaded)
	}
	off := false
	explicit := storedSettings{GuardrailMode: domain.DefaultGuardrailMode, NotificationsEnabled: &off, ProviderHealthEnabled: &off, AnimationsEnabled: &off}
	loaded = explicit.restore()
	if loaded.NotificationsEnabled || loaded.ProviderHealthEnabled || loaded.AnimationsEnabled {
		t.Fatalf("the user's explicit off was overridden: %+v", loaded)
	}
}
