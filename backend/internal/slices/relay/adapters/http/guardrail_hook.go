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
// WHETHER an answer is inspected follows the path the client called and not only the
// provider's Content-Type: a hostile provider would otherwise label its answer
// `text/plain`, skip inspection, and still be parsed by any client that reads the body
// rather than the header. WHICH dialect it is read as follows the bytes themselves:
// the label once looked sound here because every upstream layer sets it to match
// what it produced, but that promise is exactly what a buggy layer breaks, and the
// extractor pays nothing to look — an event-stream body read as JSON still extracts
// through the concatenated-object fallback, while a JSON body read as an event
// stream extracts nothing at all. The framing decides, not the header.
//
// The body is buffered under the same ceiling as every other rewrite on this path —
// for streams and translated answers it is already a byte slice in memory, so this
// costs a read, not a second copy of the ceiling.
func (server *Server) reviewResponse(response *http.Response, path string, subject relayapp.GuardrailSubject) (string, bool) {
	if response.Body == nil || response.StatusCode >= 400 {
		return "", false
	}
	contentType := strings.ToLower(response.Header.Get("Content-Type"))
	labelledStream := strings.Contains(contentType, "event-stream")
	if !labelledStream && !strings.Contains(contentType, "json") && !expectsJSONResponse(path) {
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
		verdict := server.guardrail.Review(buffered, bodyLooksLikeEventStream(buffered), subject)
		return verdict.Code, verdict.Blocked
	}
	response.Body.Close()
	verdict := server.guardrail.Review(buffered, bodyLooksLikeEventStream(buffered), subject)
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

// reviewBody inspects an answer the caller already holds as bytes. It is the tunnel's
// path, where the caller has already normalised both the body and its label, so the
// Content-Type is the relay's own statement about the bytes rather than the provider's.
func (server *Server) reviewBody(body []byte, contentType string, status int, subject relayapp.GuardrailSubject) (string, bool) {
	if len(body) == 0 || status >= 400 {
		return "", false
	}
	verdict := server.guardrail.Review(body, strings.Contains(strings.ToLower(contentType), "event-stream"), subject)
	return verdict.Code, verdict.Blocked
}
