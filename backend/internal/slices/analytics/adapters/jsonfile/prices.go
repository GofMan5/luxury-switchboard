package jsonfile

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/luxuryprivate/switchboard/backend/internal/platform/appdata"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/analytics/domain"
)

// Store keeps the price catalog as a plain JSON file. Prices are market
// rates, not secrets: they carry no credential, no endpoint, no identity —
// the same reason the backup slice writes them in the clear. The write is
// atomic (temp file, rename), so a crash never leaves half a catalog.
type Store struct{ path string }

type document struct {
	Version int            `json:"version"`
	Prices  []domain.Price `json:"prices"`
}

func New(path string) *Store { return &Store{path: path} }

func DefaultPath() (string, error) {
	root, err := appdata.Root()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "analytics-prices.json"), nil
}

func (store *Store) Load(_ context.Context) (domain.Catalog, error) {
	catalog := domain.NewCatalog()
	raw, err := os.ReadFile(store.path)
	if errors.Is(err, os.ErrNotExist) {
		return catalog, nil
	}
	if err != nil {
		return domain.Catalog{}, errors.New("price catalog is unreadable")
	}
	var doc document
	if err := json.Unmarshal(raw, &doc); err != nil || doc.Version != 1 {
		return domain.Catalog{}, errors.New("price catalog is invalid")
	}
	for _, price := range doc.Prices {
		if price.Validate() != nil {
			return domain.Catalog{}, errors.New("price catalog contains invalid data")
		}
		catalog.Prices[price.Model] = price
	}
	return catalog, nil
}

func (store *Store) Save(_ context.Context, catalog domain.Catalog) error {
	doc := document{Version: 1, Prices: make([]domain.Price, 0, len(catalog.Prices))}
	for _, price := range catalog.Prices {
		doc.Prices = append(doc.Prices, price)
	}
	// Stable order: the file is diffed by humans and compared by tools.
	for i := 1; i < len(doc.Prices); i++ {
		for j := i; j > 0 && doc.Prices[j].Model < doc.Prices[j-1].Model; j-- {
			doc.Prices[j], doc.Prices[j-1] = doc.Prices[j-1], doc.Prices[j]
		}
	}
	encoded, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return errors.New("price catalog could not be encoded")
	}
	if err := os.MkdirAll(filepath.Dir(store.path), 0o700); err != nil {
		return errors.New("price catalog directory could not be created")
	}
	temp := store.path + ".tmp"
	if err := os.WriteFile(temp, encoded, 0o600); err != nil {
		return errors.New("price catalog could not be written")
	}
	if err := os.Rename(temp, store.path); err != nil {
		os.Remove(temp)
		return errors.New("price catalog could not be replaced")
	}
	return nil
}
