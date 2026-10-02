package relayhttp

import (
	"context"
	"io"
	"net/http"
	"time"

	relayapp "github.com/luxuryprivate/switchboard/backend/internal/slices/relay/application"
)

// defaultLiveStreamProbation is the window a stream must survive before the
// relay commits to live delivery. Short enough that the client's wait stays
// the provider's own first token plus a fraction of a second, long enough
// that a connection which is going to break breaks inside it — the flaky
// provider's truncation then never reaches the client and is repaired by the
// buffered path's retry ladder exactly as before.
const defaultLiveStreamProbation = 250 * time.Millisecond

// liveStreamBody carries a provider's stream to the client as it arrives,
// instead of holding the whole answer back for inspection. Its source is the
// reader goroutine the probation started — the goroutine that was already
// reading the provider keeps reading it, so the handoff races nothing: one
// reader before the window, the same reader after.
//
// Every chunk that passes through feeds the sseInspector — usage, terminal,
// lifecycle — and lands in the collected copy; the accounting and the review
// run when the stream ends. Whether this body exists at all is a two-gate
// decision: the mode and dialect gate (canStreamLive) says the answer MAY be
// delivered live, and the probation window says it earned it.
type liveStreamBody struct {
	source    io.Reader
	inspector *sseInspector
	collected []byte
	limit     int64
	completed bool
	// idle aborts the source when the provider stalls mid-stream: the
	// buffered path had the idle timeout of its reader loop; a live read
	// would otherwise block the client on a silent connection forever.
	idle    *time.Timer
	idleFor time.Duration
	// abort closes the provider's body (unblocking a stalled read), cancel
	// stops the reader goroutine. Both are the probation's, transferred with
	// the body.
	abort  func()
	cancel context.CancelFunc
	// onEnd runs once, on the stream's natural end or its break, with the
	// assembled copy, the usage read from it, and the terminal the provider
	// reached ("" when it reached none).
	onEnd func(collected []byte, usage relayapp.TokenUsage, terminal string)
}

// channelReader is the live body's source: the reader goroutine's channel.
// A Read parks until the goroutine delivers the provider's next chunk.
type channelReader struct {
	reads <-chan bodyRead
}

func (reader channelReader) Read(into []byte) (int, error) {
	value := <-reader.reads
	count := copy(into, value.chunk)
	return count, value.err
}

func (body *liveStreamBody) Read(into []byte) (int, error) {
	count, err := body.source.Read(into)
	if count > 0 {
		if body.idle != nil {
			body.idle.Reset(body.idleFor)
		}
		body.inspector.Feed(into[:count])
		if int64(len(body.collected))+int64(count) <= body.limit {
			body.collected = append(body.collected, into[:count]...)
		}
	}
	if err != nil && !body.completed {
		body.complete()
	}
	return count, err
}

func (body *liveStreamBody) Close() error {
	// A client that walks away mid-stream ends the answer here too: the
	// deferred close is the last thing that runs, and the usage the provider
	// already reported belongs to the request either way.
	if body.idle != nil {
		body.idle.Stop()
	}
	if !body.completed {
		body.complete()
	}
	body.cancel()
	if body.abort != nil {
		body.abort()
	}
	return nil
}

// complete runs the accounting exactly once, on the stream's end.
func (body *liveStreamBody) complete() {
	body.completed = true
	terminal := body.inspector.Finish()
	if body.onEnd != nil {
		body.onEnd(body.collected, body.inspector.usage, terminal)
	}
}

// terminal reports the terminal the provider's stream reached.
func (body *liveStreamBody) terminal() string {
	return body.inspector.terminal
}

// canStreamLive decides whether this answer may be delivered as it arrives.
// The dialect must be the caller's own — translation and the image bridge
// convert the whole answer before re-emitting it — the request must have
// asked for a stream in the first place, and the guardrails must be unable
// to refuse anything: monitor mode's verdicts are recorded, never enforced,
// so withholding bytes buys nothing the client could observe.
func (server *Server) canStreamLive(request *http.Request, body []byte, chatActive *bool) bool {
	if server.guardrail == nil || server.guardrail.CanBlock() {
		return false
	}
	if chatActive != nil && *chatActive {
		return false
	}
	path := canonicalPath(request.URL.Path)
	if imageDialectPath(path) || !inferenceDialectPath(path) {
		return false
	}
	return requiresStreamTerminal(request, body)
}

// liveStreamProbation is how long a stream must survive before the relay
// commits to delivering it as it arrives: a provider that truncates inside
// the window never reaches the client at all, and its break is repaired
// invisibly the way the buffered path always did. Only real generation —
// still flowing after the window — crosses it, and the client's wait
// becomes the window plus the provider's own first token instead of the
// whole generation.
func (server *Server) liveStreamProbation() time.Duration {
	if server.config.LiveStreamProbation <= 0 {
		return defaultLiveStreamProbation
	}
	return server.config.LiveStreamProbation
}
