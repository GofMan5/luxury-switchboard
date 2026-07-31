package application

import (
	"context"
	"errors"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/tunnel/domain"
	"testing"
)

type memoryRepo struct {
	config domain.Config
	found  bool
	fail   bool
}

func (repo *memoryRepo) Load(context.Context) (domain.Config, bool, error) {
	return repo.config, repo.found, nil
}
func (repo *memoryRepo) Save(_ context.Context, config domain.Config) error {
	if repo.fail {
		return errors.New("injected")
	}
	repo.config = config
	repo.found = true
	return nil
}

type fakeRuntime struct{ running bool }

func (runtime *fakeRuntime) Start(config domain.Config) (string, error) {
	runtime.running = true
	return "http://127.0.0.1:8797/v1", nil
}
func (runtime *fakeRuntime) Stop(context.Context) error { runtime.running = false; return nil }

type routeCount int

func (count routeCount) Count() int { return int(count) }
func TestTunnelRequiresRoutesAndHidesTokenFromSnapshot(t *testing.T) {
	service, _ := NewService(&memoryRepo{}, &fakeRuntime{}, routeCount(0))
	if err := service.Start(); !errors.Is(err, ErrNoRoutes) {
		t.Fatalf("missing routes not rejected: %v", err)
	}
	if service.Snapshot().TokenConfigured != true {
		t.Fatal("token configuration missing")
	}
	if service.Snapshot().Address != "" {
		t.Fatal("stopped tunnel exposed address")
	}
}
func TestTunnelPersistsConfigAndLifecycle(t *testing.T) {
	repo := &memoryRepo{}
	runtime := &fakeRuntime{}
	service, _ := NewService(repo, runtime, routeCount(1))
	config := service.Config()
	config.Port = 18888
	config.RPMPerIP = 45
	if err := service.Configure(context.Background(), config); err != nil {
		t.Fatal(err)
	}
	if !repo.found || repo.config.Port != 18888 {
		t.Fatal("config not persisted")
	}
	if err := service.Start(); err != nil {
		t.Fatal(err)
	}
	if service.Snapshot().State != domain.StateOnline || !runtime.running {
		t.Fatal("tunnel not online")
	}
	if _, err := service.RotateToken(context.Background()); !errors.Is(err, ErrRunning) {
		t.Fatal("online token rotation accepted")
	}
	if err := service.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	old := service.RevealToken()
	next, err := service.RotateToken(context.Background())
	if err != nil || old == next {
		t.Fatal("token not rotated")
	}
}
