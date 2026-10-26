package relayhttp

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"strconv"
	"time"
	"unicode/utf8"

	relayapp "github.com/luxuryprivate/switchboard/backend/internal/slices/relay/application"
)

// hookBodyLimit is the largest request body a hook sees and the largest
// rewrite the relay accepts back.
const hookBodyLimit = 192 * 1024

// runRequestHooks asks the plugin request hooks about the client's body. It
// returns the body to forward (rewritten or not), the hooks' tags, and the
// block decision when a hook refused the request.
func (server *Server) runRequestHooks(ctx context.Context, request *http.Request, body []byte, endpoint, activityID string) ([]byte, []string, relayapp.HookDecision, bool) {
	contentType := request.Header.Get("Content-Type")
	hookRequest := relayapp.HookRequest{
		ActivityID: activityID, Model: requestModel(body, contentType), Endpoint: endpoint,
		Method: request.Method, Path: request.URL.Path, Bytes: int64(len(body)),
	}
	if len(body) <= hookBodyLimit && utf8.Valid(body) {
		hookRequest.Body = body
	} else {
		hookRequest.BodyOmitted = true
	}
	decision := server.hooks.Before(ctx, hookRequest)
	switch decision.Action {
	case relayapp.HookActionBlock:
		return body, decision.Tags, decision, true
	case relayapp.HookActionRewrite:
		// A hook that never saw the body cannot replace it.
		if len(decision.Body) > 0 && !hookRequest.BodyOmitted && len(decision.Body) <= hookBodyLimit && json.Valid(decision.Body) {
			body = decision.Body
		}
		if decision.Model != "" {
			body = rewriteRequestModel(body, contentType, decision.Model)
		}
	}
	return body, decision.Tags, decision, false
}

// observeResponse hands a finished local request to the response observers.
// The client already has its answer; observers never see a body.
func (server *Server) observeResponse(request relayapp.HookRequest, status int, errorCode string, started time.Time, usage relayapp.TokenUsage) {
	if server.hooks == nil || !server.hooks.Active(relayapp.HookPhaseResponse, request.Model, request.Endpoint) {
		return
	}
	observation := relayapp.HookObservation{
		Request: request, Status: status, ErrorCode: errorCode,
		LatencyMS: time.Since(started).Milliseconds(), BodyOmitted: true,
	}
	if usage.TotalTokens > 0 {
		tokens := usage
		observation.Tokens = &tokens
	}
	server.hooks.After(observation)
}

// answerLocally answers a request a rule or hook stopped before routing, and
// files it in history like any other request, under activityID when the
// request hooks already saw that name.
func (server *Server) answerLocally(writer http.ResponseWriter, request *http.Request, activityID, model string, bytesIn int, tags []string, status int, message string, retryAfter time.Duration, errorCode, endpoint string) {
	if status < 400 || status > 599 {
		status = http.StatusForbidden
	}
	if message == "" {
		message = "The request was blocked by a rule"
	}
	started := time.Now()
	activityID = server.activity.Begin(relayapp.ActivityStart{
		ID: activityID, Model: model, Method: request.Method, Path: request.URL.Path, BytesIn: int64(bytesIn), Tags: tags,
	})
	payload, _ := json.Marshal(map[string]any{"error": map[string]string{"message": message, "type": "switchboard_rule"}})
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("Content-Length", strconv.Itoa(len(payload)))
	if retryAfter > 0 || status == http.StatusTooManyRequests {
		writer.Header().Set("Retry-After", strconv.Itoa(max(1, int(math.Ceil(retryAfter.Seconds())))))
	}
	writer.WriteHeader(status)
	_, _ = writer.Write(payload)
	server.activity.Finish(activityID, relayapp.ActivityFinish{Status: status, ErrorCode: errorCode, ErrorDetail: message})
	server.observeResponse(relayapp.HookRequest{
		ActivityID: activityID, Model: model, Endpoint: endpoint,
		Method: request.Method, Path: request.URL.Path, Bytes: int64(bytesIn),
	}, status, errorCode, started, relayapp.TokenUsage{})
}

// ruleLeaseOutcome is what a key lived through on an attempt a custom retry
// rule took over: the same verdict class the built-in ladder files for the
// status, so the pool's cooldowns stay honest whichever path decided.
func ruleLeaseOutcome(status int, delay time.Duration) relayapp.AttemptOutcome {
	switch {
	case status == 0:
		return relayapp.AttemptOutcome{Kind: relayapp.AttemptTransport}
	case status == http.StatusTooManyRequests || status == 529:
		return relayapp.AttemptOutcome{Kind: relayapp.AttemptRateLimited, RetryAfter: delay}
	case status >= 500:
		return relayapp.AttemptOutcome{Kind: relayapp.AttemptServerError}
	default:
		return relayapp.AttemptOutcome{Kind: relayapp.AttemptRequestError}
	}
}

// withRuleHeaders adds the rules' extra headers to an upstream header copy.
// A header the client or the provider route already set (its credential, a
// preset identity) is never replaced, whatever the rule validation allowed.
func withRuleHeaders(headers http.Header, extra map[string]string) http.Header {
	for name, value := range extra {
		if headers.Get(name) == "" {
			headers.Set(name, value)
		}
	}
	return headers
}
