//go:build windows

package dpapi

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/appearance/domain"
)

func TestAppearanceRoundTripIsEncrypted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "appearance.dpapi")
	repository := New(path)
	if _, found, err := repository.Load(context.Background()); err != nil || found {
		t.Fatalf("a missing file is not an empty store: %v %v", found, err)
	}
	appearance := domain.Defaults()
	appearance.Theme = "fixture-theme"
	appearance.Tokens = map[string]string{"--bg": "#010203"}
	appearance.CustomCSS = ".fixture-private-selector { color: red; }"
	if err := repository.Save(context.Background(), appearance); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(raw, magic) || bytes.Contains(raw, []byte("fixture-private-selector")) {
		t.Fatal("appearance was stored as plaintext")
	}
	loaded, found, err := repository.Load(context.Background())
	if err != nil || !found || !reflect.DeepEqual(loaded, appearance) {
		t.Fatalf("unexpected restored appearance: %+v %v %v", loaded, found, err)
	}
}

func TestAnUnresolvedPathIsNotAnEmptyStore(t *testing.T) {
	repository := New("")
	if _, _, err := repository.Load(context.Background()); err == nil {
		t.Fatal("an unresolved path loaded as empty")
	}
	if repository.Save(context.Background(), domain.Defaults()) == nil {
		t.Fatal("an unresolved path accepted a save")
	}
}
