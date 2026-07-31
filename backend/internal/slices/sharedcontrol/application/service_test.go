package application

import (
	"context"
	"errors"
	"testing"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/sharedcontrol/domain"
)

type fakeClient struct {
	responses []domain.Snapshot
	errors    []error
	calls     [][]string
}

func (client *fakeClient) Request(_ context.Context, args ...string) (domain.Snapshot, error) {
	client.calls = append(client.calls, args)
	index := len(client.calls) - 1
	if index < len(client.errors) && client.errors[index] != nil {
		return domain.Snapshot{}, client.errors[index]
	}
	return client.responses[index], nil
}

func TestAmbiguousMutationReconcilesWithoutReplayingAction(t *testing.T) {
	current := domain.Snapshot{Available: true, Revision: 4, Tunnels: []domain.Tunnel{{Position: 0, Name: SelfName, State: "paused"}}}
	client := &fakeClient{responses: []domain.Snapshot{{}, current}, errors: []error{errors.New("response lost"), nil}}
	service, _ := NewService(client)
	result, err := service.Control(context.Background(), 0, 3, "pause")
	if err != nil || result.Tunnels[0].State != "paused" || len(client.calls) != 2 || client.calls[1][0] != "list" {
		t.Fatalf("mutation was not reconciled safely: result=%+v calls=%v err=%v", result, client.calls, err)
	}
}
