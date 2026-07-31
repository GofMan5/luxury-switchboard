package application

import (
	"context"
	"errors"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/settings/domain"
	"testing"
)

type memoryRepository struct {
	settings domain.Settings
	found    bool
	fail     bool
}

func (repository *memoryRepository) Load(context.Context) (domain.Settings, bool, error) {
	return repository.settings, repository.found, nil
}
func (repository *memoryRepository) Save(_ context.Context, settings domain.Settings) error {
	if repository.fail {
		return errors.New("injected")
	}
	repository.settings = settings
	repository.found = true
	return nil
}

func TestServicePersistsBeforePublishing(t *testing.T) {
	repository := &memoryRepository{}
	service, _ := NewService(repository)
	next := domain.Defaults()
	next.ListenerPort = 9000
	result, err := service.Update(context.Background(), next)
	if err != nil || !result.RestartRequired || service.Snapshot().ListenerPort != 9000 {
		t.Fatalf("unexpected update: %+v %v", result, err)
	}
	repository.fail = true
	failed := next
	failed.ListenerPort = 9001
	if _, err := service.Update(context.Background(), failed); err == nil {
		t.Fatal("save failure was ignored")
	}
	if service.Snapshot().ListenerPort != 9000 {
		t.Fatal("failed save mutated settings")
	}
}
