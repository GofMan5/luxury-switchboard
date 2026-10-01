package jsonfile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/luxuryprivate/switchboard/backend/internal/platform/appdata"
	"github.com/luxuryprivate/switchboard/backend/internal/platform/atomicfile"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/analytics/domain"
)

// Store keeps the price catalog as a plain JSON file. Prices are market
// rates, not secrets: they carry no credential, no endpoint, no identity —
// the same reason the backup slice writes them in the clear. The write is
// atomic and durable (temp, fsync, rename), so neither a crash nor a power
// loss leaves half a catalog.
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
		return domain.Catalog{}, fmt.Errorf("%w: read failed", domain.ErrCatalogUnreadable)
	}
	var doc document
	if err := json.Unmarshal(raw, &doc); err != nil || doc.Version != 1 {
		return domain.Catalog{}, fmt.Errorf("%w: invalid document", domain.ErrCatalogUnreadable)
	}
	for _, price := range doc.Prices {
		if price.Validate() != nil {
			return domain.Catalog{}, fmt.Errorf("%w: invalid entry", domain.ErrCatalogUnreadable)
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
		return fmt.Errorf("%w: encode failed", domain.ErrCatalogUnreadable)
	}
	// The platform's atomic writer (temp + fsync + rename, MOVEFILE_WRITE_THROUGH
	// on Windows): the price catalog is human-diffed and tool-compared, and a
	// power-loss mid-write must not cost the last edit — the hand-rolled
	// temp+rename here was atomic against crashes but not against power.
	if err := atomicfile.Replace(store.path, encoded, 0o600); err != nil {
		return fmt.Errorf("%w: write failed", domain.ErrCatalogUnreadable)
	}
	return nil
}
