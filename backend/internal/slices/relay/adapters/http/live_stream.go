package relayhttp

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"time"

	relayapp "github.com/luxuryprivate/switchboard/backend/internal/slices/relay/application"
)

// channelReader is the live body's source: the probation's buffered prefix
// first, then the reader goroutine's chunks as they keep arriving. It honors
// the io.Reader contract — a chunk larger than the caller's buffer is served
// across successive Reads, its tail held in pending, because production
// readers grow their buffers incrementally (io.ReadAll starts at 512 bytes)
// and a discarded tail is a silently corrupted answer. A closed channel —
// the goroutine left, its terminal error possibly dropped by the exit race —
// ends the stream with an explicit error instead of parking the consumer on
// a channel nobody will ever feed.
type channelReader struct {
	prefix     []byte
	reads      <-chan bodyRead
	pending    []byte
	pendingErr error
}

func (reader *channelReader) Read(into []byte) (int, error) {
	if len(reader.prefix) > 0 {
		count := copy(into, reader.prefix)
		reader.prefix = reader.prefix[count:]
		return count, nil
	}
	if len(reader.pending) == 0 {
		if reader.pendingErr != nil {
			err := reader.pendingErr
			reader.pendingErr = nil
			return 0, err
		}
		value, open := <-reader.reads
		if !open {
			return 0, errLiveStreamAborted
		}
		reader.pending = value.chunk
		reader.pendingErr = value.err
	}
	count := copy(into, reader.pending)
	reader.pending = reader.pending[count:]
	if len(reader.pending) > 0 {
		// The tail is served before the error it shares a chunk with.
		return count, nil
	}
	err := reader.pendingErr
	reader.pendingErr = nil
	return count, err
}

// errLiveStreamAborted says the reader goroutine ended without delivering a
// terminal: the request was cancelled under it. The copy loop files it as a
// stream failure and the client sees the dialect failure event — the row
// settles instead of hanging active forever.
var errLiveStreamAborted = errors.New("live stream aborted")

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
// decision: the mode and dialect gate (canStreamLive plus the caller's
// liveAllowed) says the answer MAY be delivered live, and the probation
// window says it earned it.
type liveStreamBody struct {
	source    *channelReader
	inspector *sseInspector
	collected []byte
	limit     int64
	completed bool
	// started is the attempt's start: generation time is measured to the
	// stream's end, not to the handoff.
	started time.Time
	// idle aborts the source when the provider stalls mid-stream: the
	// buffered path had the idle timeout of its reader loop; a live read
	// would otherwise block the client on a silent connection forever. The
	// reset rides the client's reads, so a backpressured writer can abort a
	// healthy provider after the timeout — the documented trade of live
	// delivery: the bytes were already delivered, and the alternative was
	// holding the whole answer back.
	idle    *time.Timer
	idleFor time.Duration
	// abort closes the provider's body (unblocking a stalled read), cancel
	// stops the reader goroutine. Both are the probation's, transferred with
	// the body.
	abort  func()
	cancel context.CancelFunc
	// release finishes the credential lease: a live generation is still
	// generating after the handoff, and the key's concurrency accounting
	// must count it for as long as it runs — the buffered path held the
	// lease through the whole answer, and live delivery is not a license to
	// overbook the key.
	release func()
	// onEnd runs once, on the stream's natural end or its break, with the
	// assembled copy, the usage read from it, the terminal the provider
	// reached ("" when it reached none), and the generation time.
	onEnd func(collected []byte, usage relayapp.TokenUsage, terminal string, generation time.Duration)
}

func (body *liveStreamBody) Read(into []byte) (int, error) {
	// The probation already fed the prefix to this inspector and counted
	// its usage: serving the prefix again would corrupt the parser mid-event
	// and double the review's body. The collected copy still takes every
	// byte the client receives — the review judges the whole answer.
	prefixServing := len(body.source.prefix) > 0
	count, err := body.source.Read(into)
	if count > 0 {
		if body.idle != nil {
			body.idle.Reset(body.idleFor)
		}
		if !prefixServing {
			body.inspector.Feed(into[:count])
		}
		// The copy stays bounded: the review at the end reads the prefix,
		// the way the buffered path inspects an over-long answer. The wire
		// flows as the provider writes it — a proxy's memory is the bound,
		// not the answer's length.
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
	if body.release != nil {
		body.release()
	}
	if body.onEnd != nil {
		body.onEnd(body.collected, body.inspector.usage, terminal, time.Since(body.started))
	}
}

// terminal reports the terminal the provider's stream reached.
func (body *liveStreamBody) terminal() string {
	return body.inspector.terminal
}

// canStreamLive decides whether this answer may be delivered as it arrives.
// The dialect must be the caller's own — translation converts the whole
// answer before re-emitting it — the request must have asked for a stream in
// the first place, and the guardrails must be unable to refuse anything:
// monitor mode's verdicts are recorded, never enforced, so withholding bytes
// buys nothing the client could observe. The caller adds the entries this
// cannot see: the image bridge and the tool repair (their caller knows the
// compat flags), and the tunnel's dispatch (it reads the whole answer back).
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

// livePrefixReady reports whether the probation's bytes show a stream the
// tool repair will never need to rewrite. Chat-family dialects have no
// Responses item lifecycle to repair, so the window alone decides. A
// Responses stream goes live only on positive evidence: its output lifecycle
// already opened — an item announcement that is not pre-completed. Anything
// else (deltas without their announcement, a pre-completed announcement,
// nothing arrived yet) keeps the buffered path, because the announcement
// that arrives later is exactly what the repair exists to rewrite.
func livePrefixReady(path string, buffered []byte) bool {
	if chatDialectPath(path) || completionsDialectPath(path) || messagesDialectPath(path) {
		return true
	}
	deframed := deframedStreamBytes(buffered)
	if !bytes.Contains(deframed, []byte("response.output_item.added")) {
		return false
	}
	// An announcement already carrying "completed" is the repair's own case:
	// the client would drop every delta after it.
	return !bytes.Contains(deframed, []byte(`"status":"completed"`))
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
