package providers

import providerapp "github.com/luxuryprivate/switchboard/backend/internal/slices/providers/application"

type Catalog struct{ catalog *providerapp.Catalog }

func NewCatalog(catalog *providerapp.Catalog) *Catalog { return &Catalog{catalog: catalog} }
func (adapter *Catalog) Exists(id string) bool         { _, exists := adapter.catalog.Get(id); return exists }
