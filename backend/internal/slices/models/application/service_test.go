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
	if _, err := service.Test(context.Background(), "provider", "run-1", models); err == nil {
		t.Fatal("oversized model test batch was accepted")
	}
}

func TestModelTestResultCarriesItsRunID(t *testing.T) {
	service, _ := NewService(testProviders{}, testGateway{})
	published := make(chan domain.TestResult, 1)
	service.OnTested(func(result domain.TestResult) { published <- result })
	if _, err := service.Test(context.Background(), "provider", "run-42", []string{"model"}); err != nil {
		t.Fatal(err)
	}
	if result := <-published; result.RunID != "run-42" {
		t.Fatalf("model result lost its run id: %+v", result)
	}
}

type cancelledGateway struct{ started chan struct{} }

func (gateway cancelledGateway) Discover(context.Context, domain.Provider) ([]string, error) {
	return nil, nil
}

func (gateway cancelledGateway) Test(ctx context.Context, provider domain.Provider, model string) domain.TestResult {
	close(gateway.started)
	<-ctx.Done()
	return domain.TestResult{ProviderID: provider.ID, Model: model, State: "unavailable"}
}

func TestCancelledModelTestDoesNotPublishAStaleResult(t *testing.T) {
	started := make(chan struct{})
	service, _ := NewService(testProviders{}, cancelledGateway{started: started})
	published := 0
	service.OnTested(func(domain.TestResult) { published++ })
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := service.Test(ctx, "provider", "run-1", []string{"model"})
		done <- err
	}()
	<-started
	cancel()
	if err := <-done; err == nil {
		t.Fatal("cancelled model test reported success")
	}
	if published != 0 {
		t.Fatal("cancelled model test published a stale result")
	}
}
