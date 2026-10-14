package providers

import (
	"context"
	providerapp "github.com/luxuryprivate/switchboard/backend/internal/slices/providers/application"
	providerdomain "github.com/luxuryprivate/switchboard/backend/internal/slices/providers/domain"
	relayapp "github.com/luxuryprivate/switchboard/backend/internal/slices/relay/application"
	"net/url"
	"testing"
)

func TestCodexCredentialIdentityCannotResolveAnUnboundDestination(t *testing.T) {
	active, _ := providerdomain.New(providerdomain.Params{ID: "local", Name: "Local", BaseURL: "http://127.0.0.1:8799", AuthMode: providerdomain.AuthPassthrough, Enabled: true})
	for _, destination := range []string{"https://unrelated.invalid/v1", "https://chatgpt.com.evil.invalid/backend-api/codex", "https://chatgpt.com/backend-api/other", "https://chatgpt.com/backend-api/codex?next=unrelated", "https://chatgpt.com:443/backend-api/codex"} {
		t.Run(destination, func(t *testing.T) {
			codex := codexProvider(t)
			codex.BaseURL, _ = url.Parse(destination)
			catalog, _ := providerapp.NewCatalog([]providerdomain.Provider{active, codex}, codex.ID)
			source := NewSource(catalog, nil, &fixedChain{routes: []relayapp.ModelRoute{{ProviderID: codex.ID, UpstreamModel: "gpt"}}})
			if _, err := source.Current(context.Background(), "gpt"); err == nil {
				t.Fatal("active credential identity accepted wrong destination")
			}
			if _, err := source.Pinned(context.Background(), codex.ID, "gpt"); err == nil {
				t.Fatal("pinned credential identity accepted wrong destination")
			}
			if _, ok, err := source.Next(context.Background(), active.ID, "gpt"); ok || err == nil {
				t.Fatal("failover credential identity accepted wrong destination")
			}
		})
	}
	t.Run("resolved destination is captured", func(t *testing.T) {
		codex := codexProvider(t)
		catalog, _ := providerapp.NewCatalog([]providerdomain.Provider{codex}, codex.ID)
		route, err := NewSource(catalog, nil, nil).Pinned(context.Background(), codex.ID, "gpt")
		if err != nil {
			t.Fatal(err)
		}
		codex.BaseURL.Host = "unrelated.invalid"
		if route.BaseURL.String() != providerdomain.CodexBaseURL {
			t.Fatal("resolved credential destination followed catalog mutation")
		}
	})
	custom, _ := providerdomain.New(providerdomain.Params{ID: "codex", Name: "Custom", BaseURL: "https://custom.invalid/v1", AuthMode: providerdomain.AuthBearer, Enabled: true})
	catalog, _ := providerapp.NewCatalog([]providerdomain.Provider{custom}, custom.ID)
	if _, err := NewSource(catalog, nil, nil).Pinned(context.Background(), custom.ID, "gpt"); err == nil {
		t.Fatal("custom slot can borrow managed credential identity")
	}
}
