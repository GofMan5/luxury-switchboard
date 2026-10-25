//go:build windows || linux

package dpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/plugins/application"
)

func TestCollectionsRoundTripEncryptedPerPlugin(t *testing.T) {
	useTestKeyring()
	ctx := context.Background()
	directory := filepath.Join(t.TempDir(), "plugin-data")
	store := NewCollections(directory)
	if data, err := store.Load(ctx, "acme.notes"); err != nil || data != nil {
		t.Fatalf("a missing file: %v %v", data, err)
	}
	data := application.CollectionData{"notes": {"a": json.RawMessage(`{"text":"fixture-private-note"}`)}}
	if err := store.Save(ctx, "acme.notes", data); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "acme.notes.dpapi")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(raw, collectionsMagic) || bytes.Contains(raw, []byte("fixture-private")) {
		t.Fatal("collections were stored as plaintext")
	}
	loaded, err := store.Load(ctx, "acme.notes")
	if err != nil || !reflect.DeepEqual(loaded, data) {
		t.Fatalf("restored %v %v", loaded, err)
	}
	if other, err := store.Load(ctx, "acme.other"); err != nil || other != nil {
		t.Fatalf("another plugin saw the data: %v %v", other, err)
	}
	if err := store.Delete(ctx, "acme.notes"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the file outlived its delete")
	}
	if err := store.Delete(ctx, "acme.notes"); err != nil {
		t.Fatalf("deleting a missing file: %v", err)
	}
}

func TestCollectionsRefuseWhatCannotBeAFile(t *testing.T) {
	ctx := context.Background()
	if DefaultCollectionsPath("") != "" {
		t.Fatal("an unknown plugins file named a data folder")
	}
	if _, err := NewCollections("").Load(ctx, "acme.notes"); err == nil {
		t.Fatal("an unresolved folder loaded as empty")
	}
	store := NewCollections(t.TempDir())
	for _, id := range []string{"", "..", "acme/../x", `acme\x`, "Acme.notes"} {
		if _, err := store.Load(ctx, id); err == nil {
			t.Fatalf("id %q named a file", id)
		}
		if store.Delete(ctx, id) == nil {
			t.Fatalf("id %q deleted a file", id)
		}
	}
}
