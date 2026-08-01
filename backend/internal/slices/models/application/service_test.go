package application

import (
	"context"
	"fmt"
	"testing"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/models/domain"
)

type testProviders struct{}

func (testProviders) Get(string) (domain.Provider, bool) {
	return domain.Provider{ID: "provider"}, true
}

type testGateway struct{}

func (testGateway) Discover(context.Context, domain.Provider) ([]string, error) { return nil, nil }
func (testGateway) Test(_ context.Context, provider domain.Provider, model string) domain.TestResult {
	return domain.TestResult{ProviderID: provider.ID, Model: model, State: "available"}
}

func TestModelTestBatchIsBoundedAtTheApplicationBoundary(t *testing.T) {
	service, _ := NewService(testProviders{}, testGateway{})
	models := make([]string, maxTestModels+1)
	for index := range models {
		models[index] = fmt.Sprintf("model-%d", index)
	}
	if _, err := service.Test(context.Background(), "provider", models); err == nil {
		t.Fatal("oversized model test batch was accepted")
	}
}
