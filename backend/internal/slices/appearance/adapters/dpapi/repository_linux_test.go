//go:build linux

package dpapi

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/appearance/domain"
	"github.com/zalando/go-keyring"
)

func TestAppearanceRoundTripEncryptedOnLinux(t *testing.T) {
	keyring.MockInit()
	path := filepath.Join(t.TempDir(), "appearance.dpapi")
	repository := New(path)
	appearance := domain.Defaults()
	appearance.Tokens = map[string]string{"--bg": "#010203"}
	appearance.CustomCSS = ".fixture-private-selector { color: red; }"
	if err := repository.Save(context.Background(), appearance); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	if bytes.Contains(raw, []byte("fixture-private-selector")) {
		t.Fatal("appearance stored as plaintext")
	}
	loaded, found, err := repository.Load(context.Background())
	if err != nil || !found || !reflect.DeepEqual(loaded, appearance) {
		t.Fatalf("unexpected appearance: %+v %v %v", loaded, found, err)
	}
}
