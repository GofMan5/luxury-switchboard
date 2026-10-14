package application

import (
	"context"
	providerdomain "github.com/luxuryprivate/switchboard/backend/internal/slices/providers/domain"
	"testing"
	"time"
)

type countingProber struct{ calls chan struct{} }

func (prober countingProber) ProbeReachability(context.Context, providerdomain.Provider) (bool, string) {
	prober.calls <- struct{}{}
	return true, "reachable"
}
func TestDisabledHealthLoopCanBeEnabledWithoutRestart(t *testing.T) {
	provider, err := providerdomain.New(providerdomain.Params{ID: "test", Name: "Test", BaseURL: "https://test.invalid/v1", AuthMode: providerdomain.AuthBearer, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	catalog, _ := NewCatalog([]providerdomain.Provider{provider}, provider.ID)
	calls := make(chan struct{}, 8)
	monitor, _ := NewHealthMonitor(catalog, countingProber{calls})
	monitor.SetEnabled(false)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); monitor.Run(ctx, time.Hour) }()
	defer func() { cancel(); <-done }()
	select {
	case <-calls:
		t.Fatal("disabled boot still probed")
	case <-time.After(20 * time.Millisecond):
	}
	monitor.SetEnabled(true)
	select {
	case <-calls:
	case <-time.After(time.Second):
		t.Fatal("enable did not wake the disabled loop")
	}
	monitor.SetEnabled(false)
	monitor.SetEnabled(true)
	select {
	case <-calls:
	case <-time.After(time.Second):
		t.Fatal("re-enable did not wake the loop")
	}
}
