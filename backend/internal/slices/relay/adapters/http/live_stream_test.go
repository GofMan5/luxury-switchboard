package relayhttp

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	guardrailrelay "github.com/luxuryprivate/switchboard/backend/internal/slices/guardrails/adapters/relay"
	guardrailruleset "github.com/luxuryprivate/switchboard/backend/internal/slices/guardrails/adapters/ruleset"
	guardrailapp "github.com/luxuryprivate/switchboard/backend/internal/slices/guardrails/application"
	guardraildomain "github.com/luxuryprivate/switchboard/backend/internal/slices/guardrails/domain"
)

// The decision itself: a monitor-mode guardrail, a native streaming request,
// and nothing the relay still has to convert — live delivery. Block mode,
// translated dialects, images and non-stream requests keep the buffered
// path, each for its own reason.
func TestCanStreamLiveDecidesByModeAndDialect(t *testing.T) {
	engine, err := guardraildomain.NewEngine(guardrailruleset.RulesJSON, guardrailruleset.BlocklistJSON)
	if err != nil {
		t.Fatal(err)
	}
	monitor, err := guardrailapp.NewInspector(engine, guardraildomain.ModeMonitor, 16)
	if err != nil {
		t.Fatal(err)
	}
	block, err := guardrailapp.NewInspector(engine, guardraildomain.ModeBlock, 16)
	if err != nil {
		t.Fatal(err)
	}
	request := func(path string) *http.Request {
		return httptest.NewRequest(http.MethodPost, "http://relay"+path, nil)
	}
	body := []byte(`{"model":"glm","stream":true,"messages":[]}`)
	translated := true
	native := false

	live := NewServer("127.0.0.1:0", Dependencies{Guardrail: guardrailrelay.New(monitor)})
	if !live.canStreamLive(request("/v1/chat/completions"), body, &native) {
		t.Fatal("a monitor-mode native chat stream was buffered")
	}
	if !live.canStreamLive(request("/chat/completions"), body, &native) {
		t.Fatal("the unprefixed chat path was buffered")
	}

	guarded := NewServer("127.0.0.1:0", Dependencies{Guardrail: guardrailrelay.New(block)})
	if guarded.canStreamLive(request("/v1/chat/completions"), body, &native) {
		t.Fatal("block mode streamed live: the verdict decides whether the client sees a byte at all")
	}
	if live.canStreamLive(request("/v1/responses"), body, &translated) {
		t.Fatal("a translated dialect streamed live: the conversion needs the whole answer")
	}
	if live.canStreamLive(request("/v1/images/generations"), body, &native) {
		t.Fatal("the image bridge streamed live")
	}
	if live.canStreamLive(request("/v1/chat/completions"), []byte(`{"model":"glm","messages":[]}`), &native) {
		t.Fatal("a non-stream request took the live path")
	}
}

// The reader contract: a chunk larger than the caller's buffer is served
// across successive Reads. io.ReadAll grows its buffer from 512 bytes, and a
// channel reader that consumed a whole 64KB chunk per call and discarded the
// tail past the buffer silently corrupted every answer it carried.
func TestChannelReaderServesLargeChunksAcrossSmallBuffers(t *testing.T) {
	chunk := make([]byte, 200)
	for index := range chunk {
		chunk[index] = byte(index)
	}
	reads := make(chan bodyRead, 2)
	reads <- bodyRead{chunk: chunk}
	reads <- bodyRead{err: io.EOF}
	close(reads)
	reader := &channelReader{prefix: []byte("prefix-bytes"), reads: reads}

	var assembled []byte
	assembled = append(assembled, mustRead(t, reader, 7)...)  // the prefix first
	assembled = append(assembled, mustRead(t, reader, 11)...) // the prefix's tail
	assembled = append(assembled, mustRead(t, reader, 50)...) // chunk: 50 + 50 + 50
	assembled = append(assembled, mustRead(t, reader, 50)...)
	assembled = append(assembled, mustRead(t, reader, 50)...)
	// The remaining 50 chunk bytes: the tail is served before the error it
	// shares a chunk with.
	tail := make([]byte, 200)
	count, err := reader.Read(tail)
	if count != 50 || err != nil {
		t.Fatalf("the chunk's tail was lost: count=%d err=%v", count, err)
	}
	assembled = append(assembled, tail[:count]...)
	if _, err := reader.Read(make([]byte, 8)); err != io.EOF {
		t.Fatalf("the terminal error was lost: %v", err)
	}
	want := append([]byte("prefix-bytes"), chunk...)
	if string(assembled) != string(want) {
		t.Fatal("the assembled stream does not match what was sent")
	}
}

// The (n>0, err) read: a chunk carrying both bytes and its terminal error
// serves the bytes first and the error only with — or after — the last of
// them, never discarding either. And a closed channel with a pending tail
// drains the tail before reporting the abort.
func TestChannelReaderServesBytesAndErrorAndClosedChannels(t *testing.T) {
	reads := make(chan bodyRead, 1)
	reads <- bodyRead{chunk: []byte("final-bytes"), err: io.EOF}
	close(reads)
	reader := &channelReader{reads: reads}
	buffer := make([]byte, 5)
	count, err := reader.Read(buffer)
	if count != 5 || err != nil || string(buffer[:count]) != "final" {
		t.Fatalf("a chunk's bytes were not served before its error: count=%d err=%v", count, err)
	}
	count, err = reader.Read(buffer)
	if count != 5 || err != nil || string(buffer[:count]) != "-byte" {
		t.Fatalf("the chunk's middle was mangled: count=%d err=%v %q", count, err, buffer[:count])
	}
	// The last byte travels WITH the error: (n>0, err) is the contract.
	count, err = reader.Read(buffer)
	if count != 1 || err != io.EOF || string(buffer[:count]) != "s" {
		t.Fatalf("the last byte and the error did not travel together: count=%d err=%v", count, err)
	}

	// A closed channel with a pending tail: the tail is served, then the
	// abort — the consumer is never parked on a channel nobody feeds.
	closed := make(chan bodyRead)
	close(closed)
	draining := &channelReader{pending: []byte("tail"), reads: closed}
	count, err = draining.Read(make([]byte, 16))
	if count != 4 || err != nil {
		t.Fatalf("the pending tail was not drained first: count=%d err=%v", count, err)
	}
	if _, err := draining.Read(make([]byte, 4)); err != errLiveStreamAborted {
		t.Fatalf("the closed channel did not report the abort: %v", err)
	}
}

// The seamlessness case: a provider whose prefill is silent for far longer
// than the client's own idle patience. The buffered path fed the client
// keep-alives through the whole wait; the live path stopped at the handoff,
// and the client died of silence ("stream idle timeout after 300000ms",
// then "stream_incomplete / context canceled" on the relay's side). The
// live body now emits the dialect's keep-alive while the provider is
// silent, and a data chunk resets the cadence.
func TestALiveStreamKeepsASilentPrefillFed(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		writer.(http.Flusher).Flush()
		// A silent prefill: longer than the keep-alive interval, shorter
		// than the idle timeout.
		time.Sleep(120 * time.Millisecond)
		_, _ = writer.Write([]byte("data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"model\":\"glm\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"Hello\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":5,\"total_tokens\":15}}\n\n"))
		writer.(http.Flusher).Flush()
		_, _ = writer.Write([]byte("data: [DONE]\n\n"))
		writer.(http.Flusher).Flush()
	}))
	defer upstream.Close()
	sink := &recordingActivity{}
	server, _ := guardedServerWatchedBy(t, guardraildomain.ModeMonitor, upstream.URL, sink)
	server.config.HeartbeatInterval = 25 * time.Millisecond
	server.config.LiveStreamProbation = 40 * time.Millisecond
	server.config.RetryMax = 5 * time.Millisecond

	request := httptest.NewRequest(http.MethodPost, "http://relay/v1/chat/completions", strings.NewReader(`{"model":"glm-5.3","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)

	body := response.Body.String()
	if !strings.Contains(body, ": switchboard keep-alive\n\n") {
		t.Fatalf("a silent prefill starved the client: no keep-alive in the stream: %q", body)
	}
	if !strings.Contains(body, "[DONE]") || !strings.Contains(body, "usage") {
		t.Fatalf("the keep-alives corrupted the answer: %q", body)
	}
	// The cadence: keep-alives ride the silence BEFORE the first token, not
	// after it — the stream ends with the answer, not trailing comments.
	if index := strings.Index(body, "usage"); index >= 0 && strings.LastIndex(body, ": switchboard keep-alive") > index {
		t.Fatalf("keep-alives continued after the stream had data: %q", body)
	}
}

// The seamless retry: a live stream that breaks BEFORE its first content
// byte delivered nothing the client could see — headers and keep-alives — so
// the break is a transport failure like any other and the ladder re-requests
// invisibly (measured in the field: "use of closed network connection" was
// killing turns whose client had seen nothing but silence). A break AFTER
// content is final: no re-request can unsend bytes the client already read.
func TestABreakBeforeContentIsRetriedInvisibly(t *testing.T) {
	var attempts int32
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		if atomic.AddInt32(&attempts, 1) == 1 {
			// Headers, then a silence past the probation, then the
			// connection dies without a byte of answer and without [DONE].
			writer.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
			writer.(http.Flusher).Flush()
			time.Sleep(120 * time.Millisecond)
			return
		}
		writer.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		writer.(http.Flusher).Flush()
		_, _ = writer.Write([]byte("data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"model\":\"glm\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"Hello\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":5,\"total_tokens\":15}}\n\n"))
		writer.(http.Flusher).Flush()
		_, _ = writer.Write([]byte("data: [DONE]\n\n"))
		writer.(http.Flusher).Flush()
	}))
	defer upstream.Close()
	sink := &recordingActivity{}
	server, _ := guardedServerWatchedBy(t, guardraildomain.ModeMonitor, upstream.URL, sink)
	server.config.HeartbeatInterval = 25 * time.Millisecond
	server.config.LiveStreamProbation = 40 * time.Millisecond
	server.config.RetryBase = time.Millisecond
	server.config.RetryMax = 5 * time.Millisecond

	request := httptest.NewRequest(http.MethodPost, "http://relay/v1/chat/completions", strings.NewReader(`{"model":"glm-5.3","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)

	if atomic.LoadInt32(&attempts) < 2 {
		t.Fatalf("the broken attempt was never retried: attempts=%d body=%q", atomic.LoadInt32(&attempts), response.Body.String())
	}
	body := response.Body.String()
	if !strings.Contains(body, "usage") || !strings.Contains(body, "[DONE]") {
		t.Fatalf("the retried answer did not reach the client whole: %q", body)
	}
	if strings.Contains(body, "upstream_unavailable") {
		t.Fatalf("an invisible break surfaced as a failure event: %q", body)
	}
	if sink.finish.ErrorCode != "" || sink.finish.Status != http.StatusOK {
		t.Fatalf("the seamless retry was filed as a failure: %+v", sink.finish)
	}
	// The wasted attempt is billed: a discarded generation is still a
	// generation. The retry's usage rides the same record.
	if sink.finish.Usage.TotalTokens != 15 {
		t.Fatalf("usage was lost through the retry: %+v", sink.finish.Usage)
	}
	if len(sink.retries) == 0 || sink.retries[0].Attempt < 1 {
		t.Fatalf("the retry was not visible in the activity record: %+v", sink.retries)
	}
}

// The same break AFTER content is final: the client already read bytes no
// re-request can unsend, and the stream ends in the dialect's failure event.
func TestABreakAfterContentIsFinal(t *testing.T) {
	var attempts int32
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		if atomic.AddInt32(&attempts, 1) == 1 {
			writer.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
			writer.(http.Flusher).Flush()
			_, _ = writer.Write([]byte("data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"model\":\"glm\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"partial\"}}]}\n\n"))
			writer.(http.Flusher).Flush()
			time.Sleep(120 * time.Millisecond)
			return
		}
		writer.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		_, _ = writer.Write([]byte("data: [DONE]\n\n"))
	}))
	defer upstream.Close()
	sink := &recordingActivity{}
	server, _ := guardedServerWatchedBy(t, guardraildomain.ModeMonitor, upstream.URL, sink)
	server.config.HeartbeatInterval = 25 * time.Millisecond
	server.config.LiveStreamProbation = 40 * time.Millisecond
	server.config.RetryBase = time.Millisecond
	server.config.RetryMax = 5 * time.Millisecond

	request := httptest.NewRequest(http.MethodPost, "http://relay/v1/chat/completions", strings.NewReader(`{"model":"glm-5.3","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)

	if atomic.LoadInt32(&attempts) != 1 {
		t.Fatalf("a break after content was retried — the client would have seen two answers: attempts=%d", atomic.LoadInt32(&attempts))
	}
	body := response.Body.String()
	if !strings.Contains(body, "partial") || !strings.Contains(body, "upstream_unavailable") {
		t.Fatalf("the delivered content or the failure event was lost: %q", body)
	}
	if sink.finish.ErrorCode != "stream_incomplete" {
		t.Fatalf("the final break was not filed: %+v", sink.finish)
	}
}

func mustRead(t *testing.T, reader io.Reader, size int) []byte {
	t.Helper()
	buffer := make([]byte, size)
	count, err := reader.Read(buffer)
	if err != nil || count == 0 {
		t.Fatalf("a read failed: count=%d err=%v", count, err)
	}
	return buffer[:count]
}

// A stream the tool repair owns stays buffered even after the probation
// window: the repair rewrites announcements the provider sent in shapes the
// client would drop (an item already "completed" while its deltas still
// come), and the client must receive the rewritten bytes, not the raw ones.
// The announcement rides the stream's opening events, so the probation sees
// it before deciding.
func TestARepairableStreamStaysBufferedPastTheProbation(t *testing.T) {
	// The announcement split across data lines AND already marked completed —
	// the exact shape the repair exists to fix, twice over.
	stream := "data: {\"type\":\"response.output_item.added\",\"output_index\":0,\n" +
		"data: \"item\":{\"id\":\"i1\",\"type\":\"message\",\"status\":\"completed\",\"content\":null}}\n\n"
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		writer.(http.Flusher).Flush()
		_, _ = writer.Write([]byte(stream))
		writer.(http.Flusher).Flush()
		// Longer than the probation: the stream is flowing when the window
		// closes, and only the announcement can keep it buffered.
		time.Sleep(400 * time.Millisecond)
		_, _ = writer.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"item_id\":\"i1\",\"output_index\":0,\"content_index\":0,\"delta\":\"done\"}\n\n"))
		writer.(http.Flusher).Flush()
		_, _ = writer.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"error\":null}}\n\n"))
		writer.(http.Flusher).Flush()
	}))
	defer upstream.Close()
	sink := &recordingActivity{}
	server, _ := guardedServerWatchedBy(t, guardraildomain.ModeMonitor, upstream.URL, sink)
	server.config.LiveStreamProbation = 100 * time.Millisecond
	server.config.RetryMax = 5 * time.Millisecond

	request := httptest.NewRequest(http.MethodPost, "http://relay/v1/responses", strings.NewReader(
		`{"model":"gpt-test","stream":true,"tools":[{"type":"function","name":"sh_cmd"}],"input":"go"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)

	body := response.Body.String()
	if !strings.Contains(body, "response.completed") {
		t.Fatalf("the buffered stream was not delivered whole: %s", body)
	}
	// The item the provider announced as already finished is now in_progress:
	// a completed announcement makes the client drop every delta after it.
	if !strings.Contains(body, `"in_progress"`) {
		t.Fatalf("the raw completed announcement reached the client unrepaired: %s", body)
	}
}
