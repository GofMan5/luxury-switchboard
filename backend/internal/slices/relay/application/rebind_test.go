package application

import (
	"context"
	"errors"
	"testing"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/relay/domain"
)

type addressRuntime struct {
	address string
	bound   string
}

func (runtime *addressRuntime) Address() string           { return runtime.address }
func (runtime *addressRuntime) SetAddress(address string) { runtime.address = address }
func (runtime *addressRuntime) Start() (domain.Snapshot, error) {
	if runtime.address == "occupied" {
		return domain.Snapshot{}, errors.New("occupied")
	}
	runtime.bound = runtime.address
	return domain.Snapshot{State: domain.StateLive, Address: runtime.address}, nil
}
func (runtime *addressRuntime) Stop(context.Context) error { runtime.bound = ""; return nil }
func (*addressRuntime) CancelActive()                      {}

func TestFailedRebindRestoresTheOldLiveListener(t *testing.T) {
	runtime := &addressRuntime{address: "old"}
	service := NewService(runtime)
	if _, err := service.Start(); err != nil {
		t.Fatal(err)
	}
	if err := service.Rebind(context.Background(), "occupied"); err == nil {
		t.Fatal("failed bind reported success")
	}
	if runtime.address != "old" || runtime.bound != "old" || service.Snapshot().State != domain.StateLive {
		t.Fatalf("old listener not restored: %+v %+v", runtime, service.Snapshot())
	}
	if err := service.Rebind(context.Background(), "new"); err != nil {
		t.Fatal(err)
	}
	if runtime.bound != "new" {
		t.Fatal("valid port did not bind")
	}
	if err := service.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := service.Rebind(context.Background(), "stopped"); err != nil {
		t.Fatal(err)
	}
	if runtime.bound != "" || service.Snapshot().State != domain.StateStopped || runtime.address != "stopped" {
		t.Fatal("port update resurrected a stopped relay")
	}
}
