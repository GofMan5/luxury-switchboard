//go:build windows || linux

package dpapi

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/plugins/domain"
)

func TestJournalRoundTripEncrypted(t *testing.T) {
	useTestKeyring()
	ctx := context.Background()
	path := DefaultJournalPath(filepath.Join(t.TempDir(), "plugins.v1.dpapi"))
	store := NewJournal(path)
	if entries, err := store.Load(ctx); err != nil || entries != nil {
		t.Fatalf("a missing file: %v %v", entries, err)
	}
	entries := []domain.JournalEntry{{Seq: 1, At: time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC), Kind: domain.JournalInstalled, PluginID: "acme.fixture", Name: "Fixture-private-name", Version: "1.0.0", Granted: []string{domain.PermissionStorage}, Origin: domain.OriginFile}}
	if err := store.Save(ctx, entries); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(raw, journalMagic) || bytes.Contains(raw, []byte("Fixture-private")) {
		t.Fatal("the journal was stored as plaintext")
	}
	loaded, err := store.Load(ctx)
	if err != nil || !reflect.DeepEqual(loaded, entries) {
		t.Fatalf("restored %v %v", loaded, err)
	}
	if err := store.Delete(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the file outlived its delete")
	}
	if err := store.Delete(ctx); err != nil {
		t.Fatalf("deleting a missing file: %v", err)
	}
}

func TestJournalRefusesAnUnknownLocation(t *testing.T) {
	ctx := context.Background()
	if DefaultJournalPath("") != "" {
		t.Fatal("an unknown plugins file named a journal")
	}
	store := NewJournal("")
	if _, err := store.Load(ctx); err == nil {
		t.Fatal("loaded from nowhere")
	}
	if err := store.Save(ctx, nil); err == nil {
		t.Fatal("saved to nowhere")
	}
	if err := store.Delete(ctx); err == nil {
		t.Fatal("deleted nowhere")
	}
}
