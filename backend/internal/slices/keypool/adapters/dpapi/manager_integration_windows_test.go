//go:build windows

package dpapi_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/keypool/adapters/dpapi"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/keypool/application"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/keypool/domain"
)

func TestManagerPersistsThroughDPAPIRepository(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys.dpapi")
	scheduler := application.NewScheduler(10)
	builtin, err := domain.NewKey(domain.Params{
		ProviderID: "echo", Label: "Environment key", Secret: "fixture-environment-secret",
		RPM: 30, Pinned: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	manager, err := application.NewManager(
		scheduler,
		dpapi.New(path),
		map[string]int{"echo": 120},
		[]domain.Key{builtin},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Add(context.Background(), domain.Params{
		ProviderID: "echo", Label: "Fixture", Secret: "fixture-secret", RPM: 30,
		ProxyURL: "http://fixture-proxy.invalid:8080",
	}); err != nil {
		t.Fatal(err)
	}
	restarted, err := application.NewManager(
		application.NewScheduler(10),
		dpapi.New(path),
		map[string]int{"echo": 120},
		[]domain.Key{builtin},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	if keys := restarted.List("echo"); len(keys) != 2 || keys[0].RPM != 30 || !keys[0].Pinned {
		t.Fatalf("unexpected restored keys: %+v", keys)
	}
}
