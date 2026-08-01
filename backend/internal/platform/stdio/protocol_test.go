package stdio

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestServerDispatchesVersionedCommand(t *testing.T) {
	input := strings.NewReader(`{"v":1,"id":"req_1","type":"command","method":"system.echo","payload":{"value":"ok"}}` + "\n")
	output := &strings.Builder{}
	server := NewServer(input, output, 2)
	server.Handle("system.echo", func(_ context.Context, payload json.RawMessage) (any, error) {
		var value map[string]string
		if err := json.Unmarshal(payload, &value); err != nil {
			t.Fatal(err)
		}
		return value, nil
	})
	if err := server.Serve(context.Background()); err != nil {
		t.Fatal(err)
	}
	var response Response
	if err := json.Unmarshal([]byte(output.String()), &response); err != nil {
		t.Fatal(err)
	}
	if !response.OK || response.ID != "req_1" || response.Method != "system.echo" {
		t.Fatalf("unexpected response: %+v", response)
	}
}

func TestRequestValidationRejectsUntrustedEnvelope(t *testing.T) {
	tests := []Request{
		{Version: 2, ID: "req", Type: "command", Method: "ok"},
		{Version: 1, ID: "bad id", Type: "command", Method: "ok"},
		{Version: 1, ID: "req", Type: "event", Method: "ok"},
		{Version: 1, ID: "req", Type: "command"},
	}
	for _, request := range tests {
		if request.Validate() == nil {
			t.Fatalf("request should be rejected: %+v", request)
		}
	}
}

func TestDecoderRejectsUnknownAndDuplicateFields(t *testing.T) {
	frames := []string{
		`{"v":1,"id":"req","type":"command","method":"ok","unknown":true}`,
		`{"v":1,"id":"req","id":"other","type":"command","method":"ok"}`,
		`{"v":1,"id":"req","type":"command","method":"ok","payload":{"x":1,"x":2}}`,
	}
	for _, frame := range frames {
		if _, err := decodeRequest([]byte(frame)); err == nil {
			t.Fatalf("frame should be rejected: %s", frame)
		}
	}
}

func TestOversizedResultBecomesTypedFailureWithoutBreakingProtocol(t *testing.T) {
	input := strings.NewReader(`{"v":1,"id":"large","type":"command","method":"large"}` + "\n")
	var output strings.Builder
	server := NewServer(input, &output, 1)
	server.Handle("large", func(context.Context, json.RawMessage) (any, error) {
		return strings.Repeat("x", MaxFrameBytes), nil
	})
	if err := server.Serve(context.Background()); err != nil {
		t.Fatal(err)
	}
	if output.Len() > MaxFrameBytes || !strings.Contains(output.String(), `"code":"response_too_large"`) {
		t.Fatalf("oversized response escaped the protocol bound: bytes=%d body=%s", output.Len(), output.String())
	}
}

func TestShutdownAcknowledgesThenStopsDispatch(t *testing.T) {
	input := strings.NewReader(
		`{"v":1,"id":"shutdown","type":"command","method":"system.shutdown"}` + "\n" +
			`{"v":1,"id":"late","type":"command","method":"system.echo"}` + "\n",
	)
	output := &strings.Builder{}
	server := NewServer(input, output, 1)
	called := false
	server.Handle("system.echo", func(context.Context, json.RawMessage) (any, error) { called = true; return nil, nil })
	if err := server.Serve(context.Background()); err != nil {
		t.Fatal(err)
	}
	var response Response
	if err := json.Unmarshal([]byte(output.String()), &response); err != nil || !response.OK || response.ID != "shutdown" || called {
		t.Fatalf("shutdown did not stop cleanly: response=%+v called=%v err=%v", response, called, err)
	}
}
