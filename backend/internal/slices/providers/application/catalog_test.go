package application

import (
	"testing"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/providers/domain"
)

func provider(t *testing.T, id string, enabled bool) domain.Provider {
	t.Helper()
	value, err := domain.New(domain.Params{
		ID: id, Name: id, BaseURL: "http://127.0.0.1:8799",
		AuthMode: domain.AuthPassthrough, Enabled: enabled,
	})
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func TestActivatePublishesOnlyRealChanges(t *testing.T) {
	catalog, err := NewCatalog([]domain.Provider{provider(t, "local", true), provider(t, "echo", true)}, "local")
	if err != nil {
		t.Fatal(err)
	}
	changes := 0
	catalog.OnActivated(func(string) { changes++ })
	if _, err := catalog.Activate("local"); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.Activate("echo"); err != nil {
		t.Fatal(err)
	}
	if changes != 1 {
		t.Fatalf("expected one change, got %d", changes)
	}
}

func TestReplacePublishesOnlyActiveRelayChanges(t *testing.T) {
	local := provider(t, "local", true)
	echo := provider(t, "echo", true)
	catalog, _ := NewCatalog([]domain.Provider{local, echo}, "local")
	changes := 0
	catalog.OnActivated(func(string) { changes++ })

	updatedEcho, _ := domain.New(domain.Params{
		ID: "echo", Name: "Edited standby", BaseURL: "http://127.0.0.1:8799",
		AuthMode: domain.AuthPassthrough, RPM: 120, Enabled: true,
	})
	if err := catalog.Replace([]domain.Provider{local, updatedEcho}, "local"); err != nil {
		t.Fatal(err)
	}
	if changes != 0 {
		t.Fatalf("standby edit cancelled active requests: %d", changes)
	}

	updatedLocal, _ := domain.New(domain.Params{
		ID: "local", Name: "Local", BaseURL: "http://127.0.0.1:8800",
		AuthMode: domain.AuthPassthrough, Enabled: true,
	})
	if err := catalog.Replace([]domain.Provider{updatedLocal, updatedEcho}, "local"); err != nil {
		t.Fatal(err)
	}
	if changes != 1 {
		t.Fatalf("active relay edit did not cancel old requests: %d", changes)
	}
}
