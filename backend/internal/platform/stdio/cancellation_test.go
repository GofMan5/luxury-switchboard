package stdio

import (
	"context"
	"encoding/json"
	"io"
	"sync/atomic"
	"testing"
	"time"
)

func TestCanceledQueuedMutationNeverStarts(t *testing.T) {
	for _, stop := range []string{"cancel", "shutdown"} {
		t.Run(stop, func(t *testing.T) {
			reader, input := io.Pipe()
			defer input.Close()
			output := make(responseChannel, 8)
			server := NewServer(reader, output, 1)
			started, release := make(chan struct{}), make(chan struct{})
			defer close(release)
			server.Handle("hold", func(context.Context, json.RawMessage) (any, error) {
				close(started)
				<-release
				return nil, nil
			})
			var mutations atomic.Int32
			server.Handle("mutate", func(context.Context, json.RawMessage) (any, error) {
				mutations.Add(1) // Intentionally contextless: dispatch owns queued cancellation.
				return nil, nil
			})
			done := make(chan error, 1)
			go func() { done <- server.Serve(context.Background()) }()
			writeTestFrame(t, input, `{"v":1,"id":"hold","type":"command","method":"hold"}`)
			select {
			case <-started:
			case <-time.After(2 * time.Second):
				t.Fatal("worker never started")
			}
			writeTestFrame(t, input, `{"v":1,"id":"queued","type":"command","method":"mutate"}`)
			if stop == "cancel" {
				writeTestFrame(t, input, `{"v":1,"id":"stop","type":"command","method":"system.cancel","payload":{"id":"queued"}}`)
			} else {
				writeTestFrame(t, input, `{"v":1,"id":"stop","type":"command","method":"system.shutdown"}`)
			}
			if ack := nextResponse(t, output); ack.ID != "stop" || !ack.OK {
				t.Fatalf("stop not acknowledged: %+v", ack)
			}
			// The already running command is not rolled back, and finishes normally.
			release <- struct{}{}
			responses := map[string]Response{}
			for range 2 {
				response := nextResponse(t, output)
				responses[response.ID] = response
			}
			if !responses["hold"].OK {
				t.Fatal("running command's result was replaced by cancellation")
			}
			queued := responses["queued"]
			if mutations.Load() != 0 || queued.OK || queued.Error == nil || queued.Error.Code != "cancelled" {
				t.Fatalf("queued mutation ran after cancellation: count=%d response=%+v", mutations.Load(), queued)
			}
			input.Close()
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("server did not drain")
			}
		})
	}
}

type responseChannel chan Response

func (output responseChannel) Write(data []byte) (int, error) {
	var response Response
	if err := json.Unmarshal(data, &response); err != nil {
		return 0, err
	}
	output <- response
	return len(data), nil
}

func writeTestFrame(t *testing.T, writer io.Writer, frame string) {
	t.Helper()
	if _, err := io.WriteString(writer, frame+"\n"); err != nil {
		t.Fatal(err)
	}
}

func nextResponse(t *testing.T, output responseChannel) Response {
	t.Helper()
	select {
	case response := <-output:
		return response
	case <-time.After(2 * time.Second):
		t.Fatal("command has no terminal response")
		return Response{}
	}
}
