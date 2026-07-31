package application

import (
	"context"
	"testing"
	"time"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/relay/domain"
)

type blockingRuntime struct {
	started chan struct{}
	release chan struct{}
	stopped chan struct{}
}

func (runtime *blockingRuntime) Start() (domain.Snapshot, error) {
	close(runtime.started)
	<-runtime.release
	return domain.Snapshot{State: domain.StateLive, Address: "http://127.0.0.1:1", Port: 1}, nil
}
func (runtime *blockingRuntime) Stop(context.Context) error { close(runtime.stopped); return nil }
func (*blockingRuntime) CancelActive()                      {}

func TestStopWaitsForConcurrentStartAndWins(t *testing.T) {
	runtime := &blockingRuntime{started: make(chan struct{}), release: make(chan struct{}), stopped: make(chan struct{})}
	service := NewService(runtime)
	startDone := make(chan struct{})
	go func() { _, _ = service.Start(); close(startDone) }()
	<-runtime.started
	stopDone := make(chan struct{})
	go func() { _ = service.Stop(context.Background()); close(stopDone) }()
	select {
	case <-runtime.stopped:
		t.Fatal("stop raced ahead of the in-flight start")
	case <-time.After(20 * time.Millisecond):
	}
	close(runtime.release)
	<-startDone
	<-stopDone
	if service.Snapshot().State != domain.StateStopped {
		t.Fatalf("unexpected final relay state: %+v", service.Snapshot())
	}
}
