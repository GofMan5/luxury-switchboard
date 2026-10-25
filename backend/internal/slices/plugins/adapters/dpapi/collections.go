package dpapi

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/luxuryprivate/switchboard/backend/internal/platform/encryptedfile"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/plugins/application"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/plugins/domain"
)

const collectionsVersion = 1

// maxCollectionsPlaintext is the absolute cap on one plugin's collections
// file. The application keeps the counted size under 2 MiB, and every
// document's counted overhead covers its JSON punctuation, so this leaves
// room for the collection names and the envelope.
const maxCollectionsPlaintext = 3 * 1024 * 1024

var collectionsMagic = []byte("SWPDATA1\n")

// Collections keeps each plugin's documents encrypted in
// <directory>/<plugin id>.dpapi.
type Collections struct{ directory string }

type collectionsDocument struct {
	Version     int                        `json:"version"`
	Collections application.CollectionData `json:"collections"`
}

func NewCollections(directory string) *Collections { return &Collections{directory: directory} }

// DefaultCollectionsPath is the plugin-data folder beside the plugins file.
// An unknown plugins file leaves it empty, which the store refuses.
func DefaultCollectionsPath(pluginsPath string) string {
	if pluginsPath == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(pluginsPath), "plugin-data")
}

func (store *Collections) Load(ctx context.Context, id string) (application.CollectionData, error) {
	path, err := store.path(ctx, id)
	if err != nil {
		return nil, err
	}
	var value collectionsDocument
	found, err := encryptedfile.Load(path, collectionsMagic, maxCollectionsPlaintext, &value)
	if err != nil || !found {
		return nil, err
	}
	if value.Version != collectionsVersion {
		return nil, errors.New("encrypted plugin data is invalid")
	}
	return value.Collections, nil
}

func (store *Collections) Save(ctx context.Context, id string, data application.CollectionData) error {
	path, err := store.path(ctx, id)
	if err != nil {
		return err
	}
	return encryptedfile.Save(path, collectionsMagic, maxCollectionsPlaintext, collectionsDocument{Version: collectionsVersion, Collections: data})
}

func (store *Collections) Delete(ctx context.Context, id string) error {
	path, err := store.path(ctx, id)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return errors.New("plugin data could not be deleted")
	}
	return nil
}

// path names the plugin's file. A plugin id is a lowercase DNS-like name
// without separators, so it cannot leave the directory; it is checked again
// here because the id is the only thing between a caller and the path.
func (store *Collections) path(ctx context.Context, id string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if store.directory == "" {
		return "", errors.New("plugin data location is unavailable")
	}
	if !domain.ValidID(id) {
		return "", errors.New("plugin id is invalid")
	}
	return filepath.Join(store.directory, id+".dpapi"), nil
}
