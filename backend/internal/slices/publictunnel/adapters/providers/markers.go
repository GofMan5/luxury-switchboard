package providers

import (
	"github.com/luxuryprivate/switchboard/backend/internal/slices/providers/application"
)

type Keys interface {
	Count(string) int
}
type Markers struct {
	catalog *application.Catalog
	keys    Keys
}

func NewMarkers(catalog *application.Catalog, keys Keys) *Markers {
	return &Markers{catalog: catalog, keys: keys}
}
func (markers *Markers) Publishable(providerID string) bool {
	provider, ok := markers.catalog.Get(providerID)
	return ok && provider.Enabled && provider.AuthMode != "passthrough" && markers.keys != nil && markers.keys.Count(providerID) > 0
}
func (markers *Markers) SensitiveMarkers(providerID string) []string {
	provider, ok := markers.catalog.Get(providerID)
	if !ok {
		return nil
	}
	values := []string{provider.BaseURL.String(), provider.BaseURL.Hostname()}
	if len(provider.ID) >= 4 {
		values = append(values, provider.ID)
	}
	if len(provider.Name) >= 4 {
		values = append(values, provider.Name)
	}
	return values
}
