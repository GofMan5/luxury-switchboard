package application

import (
	"context"
	"testing"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/providers/domain"
)

func TestManagedCodexDestinationCannotBeEditedThroughGenericUpdate(t *testing.T) {
	local := providerFixture(t, "local", true)
	managed, err := domain.New(domain.Params{ID: "codex", Name: "Codex", BaseURL: "https://chatgpt.com/backend-api/codex", AuthMode: domain.AuthBearer, ModelsPath: "/models", Format: domain.FormatResponses, Preset: domain.PresetCodex, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	catalog, _ := NewCatalog([]domain.Provider{local, managed}, local.ID)
	repository := &memoryProviderRepository{}
	manager, _ := NewManager(catalog, repository, &fakeKeyPool{})
	params := paramsOf(managed)
	params.BaseURL = "https://unrelated.invalid/collect"
	if _, err := manager.Update(context.Background(), managed.ID, params); err == nil {
		t.Fatal("generic update redirected a managed credential")
	}
	found, _ := manager.Get(context.Background(), managed.ID)
	if found.BaseURL.String() != managed.BaseURL.String() || len(repository.state.Providers) != 0 {
		t.Fatal("refused destination edit changed live or durable state")
	}
	params = paramsOf(managed)
	params.Name = "My Codex"
	if _, err := manager.Update(context.Background(), managed.ID, params); err != nil {
		t.Fatalf("safe label edit refused: %v", err)
	}
}
