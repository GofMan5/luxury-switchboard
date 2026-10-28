package dpapi

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/luxuryprivate/switchboard/backend/internal/platform/encryptedfile"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/plugins/domain"
)

const journalVersion = 1

// maxJournalPlaintext caps the journal file: MaxJournalEntries entries of at
// most a few kilobytes each, the longest being a full permission list.
const maxJournalPlaintext = 2 * 1024 * 1024

var journalMagic = []byte("SWPJRNL1\n")

// Journal keeps the plugin activity journal encrypted in one file.
type Journal struct{ path string }

type journalDocument struct {
	Version int                   `json:"version"`
	Entries []domain.JournalEntry `json:"entries"`
}

func NewJournal(path string) *Journal { return &Journal{path: path} }

// DefaultJournalPath is the journal file beside the plugins file. An unknown
// plugins file leaves it empty, which the store refuses.
func DefaultJournalPath(pluginsPath string) string {
	if pluginsPath == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(pluginsPath), "plugin-journal.v1.dpapi")
}

func (store *Journal) Load(ctx context.Context) ([]domain.JournalEntry, error) {
	if err := store.ready(ctx); err != nil {
		return nil, err
	}
	var value journalDocument
	found, err := encryptedfile.Load(store.path, journalMagic, maxJournalPlaintext, &value)
	if err != nil || !found {
		return nil, err
	}
	if value.Version != journalVersion {
		return nil, errors.New("encrypted plugin journal is invalid")
	}
	return value.Entries, nil
}

func (store *Journal) Save(ctx context.Context, entries []domain.JournalEntry) error {
	if err := store.ready(ctx); err != nil {
		return err
	}
	return encryptedfile.Save(store.path, journalMagic, maxJournalPlaintext, journalDocument{Version: journalVersion, Entries: entries})
}

func (store *Journal) Delete(ctx context.Context) error {
	if err := store.ready(ctx); err != nil {
		return err
	}
	if err := os.Remove(store.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return errors.New("plugin journal could not be deleted")
	}
	return nil
}

func (store *Journal) ready(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if store.path == "" {
		return errors.New("plugin journal location is unavailable")
	}
	return nil
}
