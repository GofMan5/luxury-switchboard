package jsonfile_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/analytics/adapters/jsonfile"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/analytics/application"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/analytics/domain"
)

func TestLegacyCatalogDefaultsToUSDAndInvalidCurrencyIsNotSilentlyDefaulted(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "prices.json")
	for _, test := range []struct {
		name  string
		field string
		valid bool
	}{
		{name: "legacy", valid: true},
		{name: "empty", field: `,"currency":""`},
		{name: "null", field: `,"currency":null`},
		{name: "malformed", field: `,"currency":"U$D"`},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := os.WriteFile(path, []byte(`{"version":1`+test.field+`,"prices":[{"model":"model","input":2}]}`), 0o600); err != nil {
				t.Fatal(err)
			}
			catalog, err := jsonfile.New(path).Load(ctx)
			if !test.valid {
				if !errors.Is(err, domain.ErrCatalogUnreadable) {
					t.Fatalf("invalid stored currency was accepted: %+v, %v", catalog, err)
				}
				return
			}
			if err != nil || catalog.Currency != "USD" || catalog.Prices["model"].Input != 2 {
				t.Fatalf("legacy catalog changed meaning: %+v, %v", catalog, err)
			}
			if err := jsonfile.New(path).Save(ctx, catalog); err != nil {
				t.Fatal(err)
			}
		})
	}
	if err := jsonfile.New(path).Save(ctx, domain.Catalog{Currency: "R1B"}); !errors.Is(err, domain.ErrCatalogUnreadable) {
		t.Fatalf("invalid currency was persisted: %v", err)
	}
}

func TestCurrencySurvivesImmediateReadsAndRestartWithoutChangingRates(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "prices.json")
	service := application.NewService(nil, jsonfile.New(path), nil)
	initial, err := service.SetPrice(ctx, domain.Price{Model: "model", Input: 1, CachedInput: 0.2, Output: 3, Reasoning: 4})
	if err != nil {
		t.Fatal(err)
	}
	for _, currency := range []string{"RUB", "USD"} {
		if _, err := service.SetCurrency(ctx, currency); err != nil {
			t.Fatal(err)
		}
		for _, reader := range []*application.Service{service, application.NewService(nil, jsonfile.New(path), nil)} {
			got, err := reader.Prices(ctx)
			if err != nil {
				t.Fatal(err)
			}
			price, exists := got.Get("model")
			want := initial.Prices["model"]
			if got.Currency != currency || len(got.Prices) != 1 || !exists ||
				price.Input != want.Input || price.CachedInput != want.CachedInput ||
				price.Output != want.Output || price.Reasoning != want.Reasoning || !price.UpdatedAt.Equal(want.UpdatedAt) {
				t.Fatalf("currency/rates did not roundtrip: got %+v, want %s with %+v", got, currency, initial.Prices)
			}
		}
	}
}
