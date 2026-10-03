package domain

import "testing"

func TestTheCurrencyIsAThreeLetterCode(t *testing.T) {
	catalog := NewCatalog()
	next, err := catalog.SetCurrency("cny")
	if err != nil || next.Currency != "CNY" {
		t.Fatalf("a lowercase ISO code was not normalized: %v %+v", err, next)
	}
	for _, bad := range []string{"", "US", "USDD", "U1D", "us$"} {
		if _, err := catalog.SetCurrency(bad); err == nil {
			t.Fatalf("%q was accepted as a currency", bad)
		}
	}
}

func TestSetAndRemoveKeepTheCurrency(t *testing.T) {
	catalog, err := NewCatalog().SetCurrency("RUB")
	if err != nil {
		t.Fatal(err)
	}
	catalog = catalog.Set(Price{Model: "m", Output: 2})
	catalog = catalog.Remove("ghost")
	if catalog.Currency != "RUB" {
		t.Fatalf("a price edit dropped the currency: %+v", catalog)
	}
}

func TestAnOldCatalogReadsAsUSD(t *testing.T) {
	if NewCatalog().Normalized().Currency != DefaultCurrency {
		t.Fatal("an empty catalog did not read as the default currency")
	}
	if (Catalog{Prices: map[string]Price{}}).Normalized().Currency != "USD" {
		t.Fatal("a pre-currency file did not normalize to USD")
	}
}
