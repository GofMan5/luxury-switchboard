package relayhttp

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	relayapp "github.com/luxuryprivate/switchboard/backend/internal/slices/relay/application"
)

// errGuardrailBlocked is returned by Dispatch when the guardrails refused the
// answer. Callers that serve a public client already turn any dispatch error into
// one neutral message, so the reason never crosses that boundary.
var errGuardrailBlocked = errors.New("provider response was refused by the local guardrails")

// reviewResponse inspects the finished answer in the dialect the client will
// actually read, and reports whether it must be refused.
//
// Only JSON and SSE bodies are inspected: nothing else carries assistant text or
// tool calls. The body is buffered under the same ceiling as every other rewrite
// on this path — for streams and translated answers it is already a byte slice in
// memory, so this costs a read, not a second copy of the ceiling.
func (server *Server) reviewResponse(response *http.Response, subject relayapp.GuardrailSubject) (string, bool) {
	if response.Body == nil || response.StatusCode >= 400 {
		return "", false
	}
	contentType := strings.ToLower(response.Header.Get("Content-Type"))
	eventStream := strings.Contains(contentType, "event-stream")
	if !eventStream && !strings.Contains(contentType, "json") {
		return "", false
	}
	limit := responseBufferLimit(server.config)
	buffered, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	// A body larger than the ceiling is left to the buffering layers that own that
	// error. The prefix already read is spliced back in front of the rest instead of
	// being handed back on its own: returning a truncated body would corrupt the
	// answer, which is worse than not inspecting it.
	//
	// The prefix is still inspected. Truncated bytes are unsafe to forward, not
	// unsafe to read, and skipping inspection here would sell silence for padding:
	// a provider that wants a payload through only has to pad the answer past the
	// ceiling first.
	if err != nil || int64(len(buffered)) > limit {
		response.Body = readCloser{Reader: io.MultiReader(bytes.NewReader(buffered), response.Body), Closer: response.Body}
		verdict := server.guardrail.Review(buffered, eventStream, subject)
		return verdict.Code, verdict.Blocked
	}
	response.Body.Close()
	verdict := server.guardrail.Review(buffered, eventStream, subject)
	response.Body = io.NopCloser(bytes.NewReader(buffered))
	response.ContentLength = int64(len(buffered))
	if response.Header.Get("Content-Length") != "" {
		response.Header.Set("Content-Length", strconv.Itoa(len(buffered)))
	}
	return verdict.Code, verdict.Blocked
}

// readCloser rejoins a body that was partly read with the connection that still
// owns the rest of it, so closing the response still closes the socket.
type readCloser struct {
	io.Reader
	io.Closer
}

// reviewBody inspects an answer the caller already holds as bytes.
func (server *Server) reviewBody(body []byte, contentType string, status int, subject relayapp.GuardrailSubject) (string, bool) {
	if len(body) == 0 || status >= 400 {
		return "", false
	}
	lowered := strings.ToLower(contentType)
	eventStream := strings.Contains(lowered, "event-stream")
	if !eventStream && !strings.Contains(lowered, "json") {
		return "", false
	}
	verdict := server.guardrail.Review(body, eventStream, subject)
	return verdict.Code, verdict.Blocked
}
