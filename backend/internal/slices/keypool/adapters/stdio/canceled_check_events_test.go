package keypoolstdio

import (
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"

	platform "github.com/luxuryprivate/switchboard/backend/internal/platform/stdio"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/keypool/application"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/keypool/domain"
)

type fixtureCancelProbe struct{ started chan struct{} }

func (p *fixtureCancelProbe) ProbeKey(ctx context.Context, _ string, key domain.Key) int {
	if key.Label == "finished" {
		return 401
	}
	close(p.started)
	<-ctx.Done()
	return 401
}

type fixtureEventFrame struct {
	Type  string             `json:"type"`
	Topic string             `json:"topic"`
	ID    string             `json:"id"`
	Error *platform.RPCError `json:"error"`
}
type fixtureEventOutput chan fixtureEventFrame

func (w fixtureEventOutput) Write(data []byte) (int, error) {
	var r fixtureEventFrame
	if err := json.Unmarshal(data, &r); err != nil {
		return 0, err
	}
	w <- r
	return len(data), nil
}

func TestCanceledPoolCheckPublishesAlreadyFiledVerdicts(t *testing.T) {
	manager, err := application.NewManager(application.NewScheduler(10), &fixtureRepository{}, map[string]application.Rate{"fixture": {}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, label := range []string{"finished", "canceled"} {
		if _, err := manager.Add(context.Background(), domain.Params{ProviderID: "fixture", Label: label, Secret: "synthetic-" + label}); err != nil {
			t.Fatal(err)
		}
	}
	inputReader, input := io.Pipe()
	defer input.Close()
	output := make(fixtureEventOutput, 10)
	server := platform.NewServer(inputReader, output, 1)
	probe := &fixtureCancelProbe{started: make(chan struct{})}
	Register(server, manager, probe)
	done := make(chan error, 1)
	go func() { done <- server.Serve(context.Background()) }()
	io.WriteString(input, "{\"v\":1,\"id\":\"check\",\"type\":\"command\",\"method\":\"keys.check\",\"payload\":{\"providerId\":\"fixture\"}}\n")
	select {
	case <-probe.started:
	case <-time.After(time.Second):
		t.Fatal("probe never started")
	}
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	poll := time.NewTicker(time.Millisecond)
	defer poll.Stop()
filed:
	for {
		for _, key := range manager.List("fixture") {
			if key.Label == "finished" && key.AuthStreak == 1 {
				break filed
			}
		}
		select {
		case <-poll.C:
		case <-deadline.C:
			t.Fatal("fast verdict was not filed before cancellation")
		}
	}
	io.WriteString(input, "{\"v\":1,\"id\":\"cancel\",\"type\":\"command\",\"method\":\"system.cancel\",\"payload\":{\"id\":\"check\"}}\n")
	input.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("check never stopped")
	}
	close(output)
	changed := false
	canceled := false
	for frame := range output {
		if frame.Type == "event" && frame.Topic == "keys.changed" {
			changed = true
		}
		if frame.ID == "check" && frame.Error != nil && frame.Error.Code == "cancelled" {
			canceled = true
		}
	}
	if !canceled {
		t.Fatal("canceled check not classified as cancellation")
	}
	for _, key := range manager.List("fixture") {
		if key.Label == "finished" && key.AuthStreak != 1 {
			t.Fatal("already filed refusal was discarded")
		}
		if key.Label == "canceled" && (key.AuthStreak != 0 || key.LastOutcome != "") {
			t.Fatalf("canceled sibling filed a late verdict: %+v", key)
		}
	}
	if !changed {
		t.Fatal("canceled check filed health but never notified keys view")
	}
}
