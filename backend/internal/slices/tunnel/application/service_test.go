package application

import (
	"context"
	"errors"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/tunnel/domain"
	"testing"
	"time"
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

type fakeRuntime struct {
	running bool
	stopErr error
}

func (runtime *fakeRuntime) Start(context.Context, domain.Config) (string, error) {
	runtime.running = true
	return "http://127.0.0.1:8797/v1", nil
}
func (runtime *fakeRuntime) Stop(context.Context) error {
	if runtime.stopErr != nil {
		return runtime.stopErr
	}
	runtime.running = false
	return nil
}

type routeCount int

func (count routeCount) Count() int { return int(count) }

type fakePrivacyAuditor struct {
	address string
	token   string
	report  domain.PrivacyReport
	err     error
}

func (auditor *fakePrivacyAuditor) Audit(_ context.Context, address, token string) (domain.PrivacyReport, error) {
	auditor.address, auditor.token = address, token
	return auditor.report, auditor.err
}

func TestTunnelRequiresRoutesAndHidesTokenFromSnapshot(t *testing.T) {
	service, _ := NewService(&memoryRepo{}, &fakeRuntime{}, routeCount(0), &fakePrivacyAuditor{})
	if err := service.Start(context.Background()); !errors.Is(err, ErrNoRoutes) {
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
	service, _ := NewService(repo, runtime, routeCount(1), &fakePrivacyAuditor{})
	config := service.Config()
	config.Port = 18888
	config.RPMPerIP = 45
	if err := service.Configure(context.Background(), config); err != nil {
		t.Fatal(err)
	}
	if !repo.found || repo.config.Port != 18888 {
		t.Fatal("config not persisted")
	}
	if err := service.Start(context.Background()); err != nil {
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

func TestTunnelStopFailureKeepsTheRetryableAddress(t *testing.T) {
	runtime := &fakeRuntime{stopErr: errors.New("injected stop failure")}
	service, _ := NewService(&memoryRepo{}, runtime, routeCount(1), &fakePrivacyAuditor{})
	if err := service.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := service.Stop(context.Background()); err == nil {
		t.Fatal("injected stop failure was hidden")
	}
	if snapshot := service.Snapshot(); snapshot.State != domain.StateError || snapshot.Address == "" {
		t.Fatalf("failed stop lost its retryable runtime: %+v", snapshot)
	}
	if err := service.Configure(context.Background(), service.Config()); !errors.Is(err, ErrRunning) {
		t.Fatalf("active failed-stop runtime accepted configuration: %v", err)
	}
	if _, err := service.RotateToken(context.Background()); !errors.Is(err, ErrRunning) {
		t.Fatalf("active failed-stop runtime accepted token rotation: %v", err)
	}
	if err := service.Start(context.Background()); !errors.Is(err, ErrRunning) {
		t.Fatalf("active failed-stop runtime accepted a second start: %v", err)
	}
}

type blockingTunnelRuntime struct {
	started chan struct{}
	release chan struct{}
	stopped chan struct{}
}

func (runtime *blockingTunnelRuntime) Start(ctx context.Context, _ domain.Config) (string, error) {
	close(runtime.started)
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-runtime.release:
		return "http://127.0.0.1:8797/v1", nil
	}
}
func (runtime *blockingTunnelRuntime) Stop(context.Context) error { close(runtime.stopped); return nil }

func TestTunnelStopWaitsForConcurrentStartAndWins(t *testing.T) {
	runtime := &blockingTunnelRuntime{started: make(chan struct{}), release: make(chan struct{}), stopped: make(chan struct{})}
	service, _ := NewService(&memoryRepo{}, runtime, routeCount(1), &fakePrivacyAuditor{})
	startDone := make(chan struct{})
	go func() { _ = service.Start(context.Background()); close(startDone) }()
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
		t.Fatalf("unexpected final tunnel state: %+v", service.Snapshot())
	}
}

func TestTunnelStartupHonorsCancellation(t *testing.T) {
	runtime := &blockingTunnelRuntime{started: make(chan struct{}), release: make(chan struct{}), stopped: make(chan struct{})}
	service, _ := NewService(&memoryRepo{}, runtime, routeCount(1), &fakePrivacyAuditor{})
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- service.Start(ctx) }()
	<-runtime.started
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("startup cancellation was ignored: %v", err)
	}
	if service.Snapshot().State != domain.StateError {
		t.Fatalf("cancelled startup left a misleading state: %+v", service.Snapshot())
	}
}

type eventRuntime struct {
	started chan struct{}
	stopped chan struct{}
	handler func(domain.State, string, string)
}

func (runtime *eventRuntime) Start(context.Context, domain.Config) (string, error) {
	runtime.started <- struct{}{}
	return "https://fresh-words-here.trycloudflare.com/v1", nil
}
func (runtime *eventRuntime) Stop(context.Context) error {
	runtime.stopped <- struct{}{}
	return nil
}
func (runtime *eventRuntime) OnState(handler func(domain.State, string, string)) {
	runtime.handler = handler
}

// The connector's own events drive the page: a reconnect after a dropped
// session arrives with the new address, and the service publishes it.
func TestRuntimeEventsUpdateTheSnapshot(t *testing.T) {
	runtime := &eventRuntime{started: make(chan struct{}, 1), stopped: make(chan struct{}, 1)}
	service, _ := NewService(&memoryRepo{}, runtime, routeCount(1), &fakePrivacyAuditor{})
	if err := service.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	<-runtime.started
	runtime.handler(domain.StateOnline, "https://rolled-over.trycloudflare.com/v1", "")
	if service.Snapshot().Address != "https://rolled-over.trycloudflare.com/v1" {
		t.Fatalf("a reconnect did not replace the address: %+v", service.Snapshot())
	}
}

func TestPrivacyAuditUsesTheRunningAddressWithoutReturningTheToken(t *testing.T) {
	auditor := &fakePrivacyAuditor{report: domain.PrivacyReport{Status: 200}}
	service, _ := NewService(&memoryRepo{}, &fakeRuntime{}, routeCount(1), auditor)
	if _, err := service.TestPrivacy(context.Background()); !errors.Is(err, ErrPrivacyUnavailable) {
		t.Fatalf("stopped privacy test was accepted: %v", err)
	}
	if err := service.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	report, err := service.TestPrivacy(context.Background())
	if err != nil || report.Status != 200 {
		t.Fatalf("privacy test failed: report=%+v err=%v", report, err)
	}
	if auditor.address != service.Snapshot().Address || auditor.token != service.RevealToken() {
		t.Fatal("privacy auditor did not receive the active owner-only credentials")
	}
}
