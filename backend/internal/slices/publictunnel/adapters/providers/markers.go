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

// SensitiveMarkers lists the provider identity that must not appear in a public
// answer. These are identifiers rather than secrets — credential values reach the
// sanitizer by their own path — and the gateway filters out the ones too short to
// redact before handing them over, because the sanitizer treats every marker it
// receives as a secret and refuses the whole answer when one survives. That width rule
// used to be stated here too, for the id and the name only, which is how the hostname
// came to be the one value exempt from it.
func (markers *Markers) SensitiveMarkers(providerID string) []string {
	provider, ok := markers.catalog.Get(providerID)
	if !ok {
		return nil
	}
	return []string{provider.BaseURL.String(), provider.BaseURL.Hostname(), provider.ID, provider.Name}
}
