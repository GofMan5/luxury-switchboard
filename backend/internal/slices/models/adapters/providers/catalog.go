package providers

import (
	modeldomain "github.com/luxuryprivate/switchboard/backend/internal/slices/models/domain"
	providerapp "github.com/luxuryprivate/switchboard/backend/internal/slices/providers/application"
)

type Catalog struct{ source *providerapp.Catalog }

func NewCatalog(source *providerapp.Catalog) *Catalog { return &Catalog{source: source} }

func (catalog *Catalog) Get(id string) (modeldomain.Provider, bool) {
	provider, exists := catalog.source.Get(id)
	if !exists || !provider.Enabled {
		return modeldomain.Provider{}, false
	}
	return modeldomain.Provider{ID: provider.ID, ModelsPath: provider.ModelsPath, Dialect: string(provider.Dialect)}, true
}
