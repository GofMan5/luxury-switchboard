package providers

import (
	"testing"

	providerapp "github.com/luxuryprivate/switchboard/backend/internal/slices/providers/application"
	providerdomain "github.com/luxuryprivate/switchboard/backend/internal/slices/providers/domain"
)

type keys struct{ count int }

func (value keys) Count(string) int                { return value.count }
func (value keys) SensitiveValues(string) []string { return nil }

func TestOnlyAuthenticatedKeyedProviderIsPublishable(t *testing.T) {
	local, _ := providerdomain.New(providerdomain.Params{ID: "local", Name: "Local", BaseURL: "http://127.0.0.1:8799", AuthMode: providerdomain.AuthPassthrough, Enabled: true})
	remote, _ := providerdomain.New(providerdomain.Params{ID: "remote", Name: "Remote", BaseURL: "https://provider.invalid/v1", AuthMode: providerdomain.AuthBearer, Enabled: true})
	catalog, _ := providerapp.NewCatalog([]providerdomain.Provider{local, remote}, "local")
	if NewMarkers(catalog, keys{count: 1}).Publishable("local") {
		t.Fatal("passthrough provider was publishable")
	}
	if NewMarkers(catalog, keys{}).Publishable("remote") {
		t.Fatal("provider without a key was publishable")
	}
	if !NewMarkers(catalog, keys{count: 1}).Publishable("remote") {
		t.Fatal("safe provider was not publishable")
	}
}
