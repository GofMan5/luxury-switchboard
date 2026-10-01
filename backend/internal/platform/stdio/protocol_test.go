package stdio

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
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

// A panicking handler must cost one failed command, not the process: the
// worker goroutine an unrecovered panic would take down shares the binary
// with the relay, and the whole desktop app above it.
func TestAPanickingHandlerFailsOneCommandAndKeepsServing(t *testing.T) {
	input := strings.NewReader(strings.Join([]string{
		`{"v":1,"id":"req_1","type":"command","method":"boom"}`,
		`{"v":1,"id":"req_2","type":"command","method":"ok"}`,
	}, "\n") + "\n")
	output := &strings.Builder{}
	server := NewServer(input, output, 2)
	server.Handle("boom", func(context.Context, json.RawMessage) (any, error) {
		panic("handler exploded")
	})
	server.Handle("ok", func(context.Context, json.RawMessage) (any, error) {
		return map[string]bool{"alive": true}, nil
	})
	if err := server.Serve(context.Background()); err != nil {
		t.Fatalf("serve died on a panicking handler: %v", err)
	}
	if !strings.Contains(output.String(), `"code":"handler_panicked"`) {
		t.Fatalf("the panic did not answer with its own code: %s", output.String())
	}
	var second Response
	for _, line := range strings.Split(strings.TrimSpace(output.String()), "\n") {
		var response Response
		if json.Unmarshal([]byte(line), &response) == nil && response.ID == "req_2" {
			second = response
		}
	}
	if !second.OK {
		t.Fatalf("the server did not survive the panic: %s", output.String())
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

// Two slices size a bounded answer against MaxPayloadBytes - the guardrail findings
// list and the model catalog - and both measure the payload alone. Setting the budget
// ON the frame limit leaves a window where the payload fits and the frame does not,
// which is the crash both of them exist to avoid: the shell breaks its read loop on an
// oversized frame and kills the sidecar. This is the cheap invariant; the slices own
// the measured tests. The room is asserted as real rather than nominal, because a
// budget one byte under the limit would satisfy `<` and still overflow the envelope.
func TestThePayloadBudgetLeavesTheEnvelopeItsRoom(t *testing.T) {
	envelope := len(mustEncode(t, Response{Version: ProtocolVersion, ID: strings.Repeat("i", 80), Type: "result", Method: strings.Repeat("m", 120), OK: true}))
	if MaxPayloadBytes+envelope > MaxFrameBytes {
		t.Fatalf("a full payload plus a %d byte envelope is %d, over the %d the shell accepts", envelope, MaxPayloadBytes+envelope, MaxFrameBytes)
	}
}

func mustEncode(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
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

// A cancelled context must end Serve even while the reader has nothing more to
// say. The read used to run inline: a signal exit parked the whole drain in a
// blocked read until something killed the process, losing the buffered history
// the drain exists to flush. The reader may stay blocked — the process is on
// its way out — but Serve itself has to come back.
func TestCancellationEndsServeWhileTheReaderIsQuiet(t *testing.T) {
	reader, writer := io.Pipe()
	defer writer.Close()
	server := NewServer(reader, io.Discard, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx) }()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("serve returned %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a cancelled context could not end Serve: the drain behind it never ran")
	}
}

// The shell and the sidecar enforce one frame limit between them, and each
// side spells the number out on its own: Rust's MAX_FRAME_BYTES against Go's
// MaxFrameBytes. The command allowlist got a cross-side drift test that reads
// sidecar.rs; the frame limit got none — if Go ever raises its limit, the
// shell kills the sidecar on legitimate frames, and if Rust raises its own,
// every response_too_large answer Go sends becomes a lie about what the shell
// accepts.
func TestTheShellAcceptsTheFramesTheProtocolWrites(t *testing.T) {
	shell, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "src-tauri", "src", "sidecar.rs"))
	if err != nil {
		t.Skipf("desktop shell is unavailable: %v", err)
	}
	pattern := regexp.MustCompile(`MAX_FRAME_BYTES[^=\n]*=\s*(\d+)\s*\*\s*(\d+)\s*;`)
	match := pattern.FindSubmatch(shell)
	if match == nil {
		t.Fatal("the shell's frame limit could not be read; update this test with the new spelling")
	}
	shellLimit := 1
	for _, digits := range match[1:] {
		value, parseErr := strconv.Atoi(string(digits))
		if parseErr != nil {
			t.Fatalf("unreadable frame limit: %v", parseErr)
		}
		shellLimit *= value
	}
	if shellLimit != MaxFrameBytes {
		t.Fatalf("the shell accepts frames of %d bytes while the protocol writes at most %d", shellLimit, MaxFrameBytes)
	}
}

// An event the frame cannot carry is dropped — the shell would kill the
// sidecar on it — but "dropped" and "silently dropped" are different things
// for the operator watching a feed go quiet. The diagnostics writer hears
// about it, and an event that fits still says nothing.
func TestAnUndeliverableEventIsReportedToDiagnostics(t *testing.T) {
	var output strings.Builder
	server := NewServer(strings.NewReader(""), io.Discard, 1)
	server.Diagnostics(&output)
	if err := server.Emit("too.big", map[string]string{"payload": strings.Repeat("x", MaxFrameBytes)}); err == nil {
		t.Fatal("an oversized event was delivered")
	}
	if !strings.Contains(output.String(), "too.big") {
		t.Fatalf("the dropped event left no word on the console: %q", output.String())
	}

	server = NewServer(strings.NewReader(""), io.Discard, 1)
	server.Diagnostics(&output)
	if err := server.Emit("fits", map[string]string{"payload": "small"}); err != nil {
		t.Fatalf("a small event failed: %v", err)
	}
	if strings.Contains(output.String(), "fits") {
		t.Fatalf("a delivered event was reported as dropped: %q", output.String())
	}
}
