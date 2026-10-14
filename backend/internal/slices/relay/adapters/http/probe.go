package relayhttp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"time"

	relayapp "github.com/luxuryprivate/switchboard/backend/internal/slices/relay/application"
)

// The probe asks for a little more than the availability test does: a handful
// of tokens so the decode rate has something to measure.
const (
	shortProbeOutputTokens = 32
	fullProbeOutputTokens  = 128
	probeOutputTokens      = shortProbeOutputTokens
)

// Probe measures one provider+model with a real streaming request: when the
// first content token arrived, when the answer finished, and how many tokens
// it carried. It deliberately does NOT ride requestWithRetry: that ladder
// buffers every stream before returning it, and a buffered stream has no
// first-token time left to measure. One attempt, one lease, one honest read.
func (server *Server) Probe(ctx context.Context, providerID, model string) (relayapp.ProbeReport, error) {
	return server.ProbeWithProfile(ctx, providerID, model, relayapp.ProbeProfileShort)
}

func (server *Server) ProbeWithProfile(ctx context.Context, providerID, model string, profile relayapp.ProbeProfile) (relayapp.ProbeReport, error) {
	if !profile.Valid() {
		return relayapp.ProbeReport{ErrorCode: "invalid_profile"}, errors.New("invalid probe profile")
	}
	ctx, cancel := server.requestContext(ctx)
	defer cancel()
	route, err := server.routes.Pinned(ctx, providerID, model)
	if err != nil {
		return relayapp.ProbeReport{}, err
	}
	if route.AuthMode == "passthrough" {
		route.AuthMode = "bearer"
	}
	upstreamModel := route.UpstreamModel
	if upstreamModel == "" {
		upstreamModel = model
	}
	path, body, dialect := probeRequestWithProfile(route, upstreamModel, profile)
	report, err := server.probeOnce(ctx, route, path, body, dialect, model)
	// A provider without a Responses endpoint gets the same probe over chat —
	// the answer the availability test would get, measured the same way.
	if err == nil && dialect == "responses" && responsesFallbackStatus(report.Status) {
		chatPath := route.ChatPath
		if chatPath == "" {
			chatPath = "/v1/chat/completions"
		}
		return server.probeOnce(ctx, route, chatPath, probeChatBodyWithProfile(upstreamModel, profile), "chat", model)
	}
	return report, err
}

func responsesFallbackStatus(status int) bool {
	switch status {
	case http.StatusBadRequest, http.StatusNotFound, http.StatusMethodNotAllowed, http.StatusUnprocessableEntity, http.StatusNotImplemented:
		return true
	default:
		return false
	}
}

// probeRequest picks the provider's own dialect: a chat-profiled provider is
// probed over chat, an Anthropic one over messages, everything else over
// Responses. The measurement must read the dialect the provider speaks;
// translating first would bill the translation into the provider's time.
func probeRequest(route relayapp.Route, model string) (string, []byte, string) {
	return probeRequestWithProfile(route, model, relayapp.ProbeProfileShort)
}

func probeRequestWithProfile(route relayapp.Route, model string, profile relayapp.ProbeProfile) (string, []byte, string) {
	outputTokens, prompt := probeBudget(profile)
	if route.Dialect == "anthropic" {
		body, _ := json.Marshal(map[string]any{
			"model": model, "max_tokens": outputTokens, "stream": true,
			"messages": []map[string]string{{"role": "user", "content": prompt}},
		})
		return "/v1/messages", body, "anthropic"
	}
	if route.Format == "chat" {
		path := route.ChatPath
		if path == "" {
			path = "/v1/chat/completions"
		}
		return path, probeChatBodyWithProfile(model, profile), "chat"
	}
	body, _ := json.Marshal(map[string]any{
		"model": model, "input": prompt, "max_output_tokens": outputTokens, "stream": true,
	})
	return "/v1/responses", body, "responses"
}

func probeBudget(profile relayapp.ProbeProfile) (int, string) {
	if profile == relayapp.ProbeProfileFull128K {
		return fullProbeOutputTokens, profile.Prompt()
	}
	return shortProbeOutputTokens, profile.Prompt()
}

func probeChatBody(model string) []byte {
	return probeChatBodyWithProfile(model, relayapp.ProbeProfileShort)
}

func probeChatBodyWithProfile(model string, profile relayapp.ProbeProfile) []byte {
	outputTokens, prompt := probeBudget(profile)
	body, _ := json.Marshal(map[string]any{
		"model": model, "max_tokens": outputTokens, "stream": true,
		// Usage on a chat stream arrives only when asked; the probe wants the
		// provider's own count, not a guess from frames.
		"stream_options": map[string]any{"include_usage": true},
		"messages":       []map[string]string{{"role": "user", "content": prompt}},
	})
	return body
}

// probeOutcome classifies the refused attempt for the key pool exactly the way
// the retry ladder does: a dead key keeps counting its streak, a throttled one
// earns its cooldown, and a balance verdict is the provider's, not the key's.
// A probe that filed every refusal as a generic error would actively reset
// dead-key detection and re-pick hot keys for the next real request.
func probeOutcome(response *http.Response, body []byte, model string, config Config) relayapp.AttemptOutcome {
	status := response.StatusCode
	rateLimited := rateLimitedBody(body)
	switch {
	case policyRefused(string(body)):
		return relayapp.AttemptOutcome{Kind: relayapp.AttemptRequestError}
	case status != http.StatusPaymentRequired && (rateLimited || status == http.StatusTooManyRequests || status == 529):
		return relayapp.AttemptOutcome{Kind: relayapp.AttemptRateLimited, RetryAfter: retryDelay(0, response, config)}
	case status >= 500 || status == http.StatusRequestTimeout:
		return relayapp.AttemptOutcome{Kind: relayapp.AttemptServerError}
	case balanceUnavailable(status, body, rateLimited):
		if sharedPoolExhausted(body) {
			return relayapp.AttemptOutcome{Kind: relayapp.AttemptRequestError}
		}
		return relayapp.AttemptOutcome{Kind: relayapp.AttemptBalanceExhausted}
	case (status == http.StatusNotFound || status == http.StatusForbidden) && modelMissing(body, model):
		return relayapp.AttemptOutcome{Kind: relayapp.AttemptRequestError}
	case (status == http.StatusUnauthorized || status == http.StatusForbidden) && clientBlocked(body):
		return relayapp.AttemptOutcome{Kind: relayapp.AttemptRequestError}
	case (status == http.StatusNotFound || status == http.StatusForbidden) && modelUnavailable(body, model, status):
		return relayapp.AttemptOutcome{Kind: relayapp.AttemptModelUnavailable, Model: model}
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return relayapp.AttemptOutcome{Kind: relayapp.AttemptAuthentication}
	default:
		return relayapp.AttemptOutcome{Kind: relayapp.AttemptRequestError}
	}
}

func (server *Server) probeOnce(ctx context.Context, route relayapp.Route, path string, body []byte, dialect, publicModel string) (relayapp.ProbeReport, error) {
	activityID := server.activity.Begin(relayapp.ActivityStart{
		Model: publicModel, ProviderID: route.ProviderID, ProviderName: route.ProviderName,
		Method: http.MethodPost, Path: path, BytesIn: int64(len(body)),
	})
	model := requestModel(body, "application/json")
	// A probe never parks in the production queue: measurement must not
	// displace service. A pool without a free key answers "busy" and the page
	// says so — a test that queues ahead of real traffic is a test that makes
	// the outage it measures.
	lease, credential, ok := server.tryAcquireCredential(route, model)
	if !ok {
		server.activity.Finish(activityID, relayapp.ActivityFinish{
			Status: http.StatusServiceUnavailable, ErrorCode: "pool_busy",
		})
		return relayapp.ProbeReport{ErrorCode: "pool_busy"}, nil
	}
	markers := sensitiveCredentialMarkers(credential)
	targetURL := url.URL{Path: path}
	incoming := &http.Request{
		Method: http.MethodPost, URL: &targetURL,
		Header: http.Header{"Content-Type": []string{"application/json"}, "Accept": []string{"text/event-stream"}},
	}
	upstream, err := buildUpstreamRequest(ctx, incoming, body, route, credential.Value)
	if err != nil {
		finishLease(lease, relayapp.AttemptOutcome{Kind: relayapp.AttemptRequestError})
		server.activity.Finish(activityID, relayapp.ActivityFinish{Status: http.StatusBadGateway, ErrorCode: "transport"})
		return relayapp.ProbeReport{ErrorCode: "transport"}, err
	}
	client, err := server.clientForProxy(credential.ProxyURL)
	if err != nil {
		finishLease(lease, relayapp.AttemptOutcome{Kind: relayapp.AttemptRequestError})
		server.activity.Finish(activityID, relayapp.ActivityFinish{Status: http.StatusBadGateway, ErrorCode: "transport"})
		return relayapp.ProbeReport{ErrorCode: "transport"}, err
	}
	sent := time.Now()
	response, err := client.Do(upstream)
	if err != nil {
		finishLease(lease, relayapp.AttemptOutcome{Kind: relayapp.AttemptTransport})
		server.activity.Finish(activityID, relayapp.ActivityFinish{
			Status: http.StatusBadGateway, Cancelled: errors.Is(err, context.Canceled),
			ErrorCode: "transport", ErrorDetail: transportErrorDetail(err),
		})
		return relayapp.ProbeReport{ErrorCode: "transport"}, err
	}
	defer response.Body.Close()
	report := relayapp.ProbeReport{Status: response.StatusCode}
	if response.StatusCode >= 400 {
		// The body of a refused probe is small and carries the reason; read it
		// bounded, scrub it, file it. It is read before the lease finishes:
		// an auth refusal's own code and message ride the outcome, and they
		// live in this body — filing the verdict first would leave them
		// unread.
		detail, readErr := readErrorBody(ctx, response, server.configSnapshot().StreamIdleTimeout)
		if readErr != nil {
			finishLease(lease, relayapp.AttemptOutcome{Kind: relayapp.AttemptTransport})
			server.activity.Finish(activityID, relayapp.ActivityFinish{Status: response.StatusCode, Cancelled: true, ErrorCode: "cancelled"})
			return report, readErr
		}
		outcome := probeOutcome(response, detail, model, server.configSnapshot())
		if outcome.Kind == relayapp.AttemptAuthentication {
			outcome.ErrorCode, outcome.ErrorMessage = authErrorDetail(detail)
		}
		finishLease(lease, outcome)
		report.ErrorCode = "request_rejected"
		report.ErrorDetail = truncateErrorDetail(redactRequestEchoes(jsonErrorDetailUnbounded(detail, markers), body))
		server.activity.Finish(activityID, relayapp.ActivityFinish{
			Status: response.StatusCode, ErrorCode: report.ErrorCode, ErrorDetail: report.ErrorDetail,
		})
		return report, nil
	}
	measured, collected, readErr := server.measureProbeStream(ctx, response.Body, sent, dialect)
	usageInspector := sseInspector{path: path}
	usageInspector.Feed(collected)
	usageInspector.Finish()
	usage := usageInspector.usage
	if bytes.HasPrefix(bytes.TrimSpace(collected), []byte("{")) {
		usage = usageFromJSON(collected)
	}
	generation := time.Duration(measured.total * float64(time.Millisecond))
	report.TTFTMs = measured.ttft
	report.TotalMs = measured.total
	report.OutputTokens = measured.tokens
	if usage.OutputTokens > 0 {
		report.OutputTokens = int(usage.OutputTokens)
	}
	cancelled := readErr != nil && errors.Is(ctx.Err(), context.Canceled)
	switch {
	case cancelled:
		report.ErrorCode = "cancelled"
	case readErr != nil:
		report.ErrorCode = "transport"
		report.ErrorDetail = transportErrorDetail(readErr)
		if errors.Is(readErr, errResponseTooLarge) {
			// This local sentinel names the safety cap, never upstream data.
			report.ErrorDetail = errResponseTooLarge.Error()
		}
	case measured.terminal == probeTerminalFailed:
		// HTTP 200 is the standard streaming failure mode: the verdict rides a
		// terminal event, not the status line.
		report.ErrorCode = "upstream_status"
	case measured.terminal == probeTerminalMissing:
		// No terminal event at all: the answer ended mid-stream, or the
		// provider answered a stream:true request with a plain object. The
		// second shape can still carry the verdict in the body.
		if probeJSONFailure(collected) {
			report.ErrorCode = "upstream_status"
		} else {
			report.ErrorCode = "stream_incomplete"
		}
	}
	if readErr != nil {
		finishLease(lease, relayapp.AttemptOutcome{Kind: relayapp.AttemptTransport})
	} else if report.ErrorCode != "" {
		finishLease(lease, relayapp.AttemptOutcome{Kind: relayapp.AttemptRequestError})
	} else {
		finishLease(lease, relayapp.AttemptOutcome{Kind: relayapp.AttemptSuccess})
	}
	// The probe's answer passes the same inspection every other answer does:
	// a probe is exactly where a provider would put the payload it wants
	// measured kindly.
	if code, blocked := server.reviewBody(collected, "text/event-stream", response.StatusCode, relayapp.GuardrailSubject{
		ProviderID: route.ProviderID, ProviderName: route.ProviderName, Model: publicModel,
		Secrets: markers,
	}); blocked {
		report.ErrorCode = code
		server.activity.Finish(activityID, relayapp.ActivityFinish{
			Status: http.StatusBadGateway, ErrorCode: code, ErrorDetail: code,
			BytesOut: int64(len(collected)), Usage: usage, Generation: generation,
		})
		return report, nil
	}
	server.activity.Finish(activityID, relayapp.ActivityFinish{
		Status: response.StatusCode, BytesOut: int64(len(collected)),
		ErrorCode: report.ErrorCode, ErrorDetail: report.ErrorDetail,
		Usage: usage, Generation: generation, Cancelled: cancelled,
	})
	return report, nil
}

type probeTerminal int

const (
	probeTerminalMissing probeTerminal = iota
	probeTerminalOK
	probeTerminalFailed
)

type probeMeasurement struct {
	ttft     float64
	total    float64
	tokens   int
	terminal probeTerminal
}

// measureProbeStream reads the SSE frames as they arrive, under the same idle
// budget a client stream gets: a provider that goes silent mid-probe is a
// provider worth knowing about, not a hung test.
func (server *Server) measureProbeStream(ctx context.Context, body io.ReadCloser, sent time.Time, dialect string) (probeMeasurement, []byte, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer body.Close()
	limit := responseBufferLimit(server.configSnapshot())
	type line struct {
		value []byte
		err   error
	}
	lines := make(chan line, 1)
	go func() {
		defer close(lines)
		reader := bufio.NewReaderSize(io.LimitReader(body, limit+1), 64*1024)
		for {
			value, err := reader.ReadSlice('\n')
			if len(value) > 0 {
				select {
				case lines <- line{value: append([]byte(nil), value...)}:
				case <-ctx.Done():
					return
				}
			}
			if errors.Is(err, bufio.ErrBufferFull) {
				continue
			}
			if err != nil {
				select {
				case lines <- line{err: err}:
				case <-ctx.Done():
				}
				return
			}
		}
	}()
	idle := server.configSnapshot().StreamIdleTimeout
	timer := time.NewTimer(idle)
	defer timer.Stop()
	var measurement probeMeasurement
	var collected bytes.Buffer
	var frame bytes.Buffer
	var pendingLine []byte
	contentFrames := 0
	finish := func(err error) (probeMeasurement, []byte, error) {
		measurement.total = float64(time.Since(sent).Microseconds()) / 1000
		if measurement.tokens == 0 {
			measurement.tokens = contentFrames
		}
		if err == io.EOF {
			return measurement, collected.Bytes(), nil
		}
		return measurement, collected.Bytes(), err
	}
	for {
		select {
		case <-ctx.Done():
			return finish(ctx.Err())
		case <-timer.C:
			return finish(errors.New("stream idle timeout"))
		case next, open := <-lines:
			if !open {
				return finish(io.EOF)
			}
			if next.err != nil {
				return finish(next.err)
			}
			if int64(collected.Len())+int64(len(next.value)) > limit {
				return finish(errResponseTooLarge)
			}
			collected.Write(next.value)
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(idle)
			if int64(len(pendingLine))+int64(len(next.value)) > limit {
				return finish(errResponseTooLarge)
			}
			pendingLine = append(pendingLine, next.value...)
			if !bytes.HasSuffix(next.value, []byte{'\n'}) {
				continue
			}
			value := pendingLine
			pendingLine = nil
			trimmed := bytes.TrimRight(value, "\r\n")
			if len(trimmed) == 0 {
				delta, tokens, terminal := probeFrameDelta(frame.Bytes(), dialect)
				frame.Reset()
				if delta != "" {
					contentFrames++
					if measurement.ttft == 0 {
						measurement.ttft = float64(time.Since(sent).Microseconds()) / 1000
					}
				}
				if tokens > 0 {
					measurement.tokens = tokens
				}
				if terminal != probeTerminalMissing {
					measurement.terminal = terminal
					return finish(nil)
				}
			} else {
				if int64(frame.Len())+int64(len(value)) > limit {
					return finish(errResponseTooLarge)
				}
				frame.Write(value)
			}
		}
	}
}

// probeFrameDelta extracts the content delta, the reported output-token count
// and the terminal verdict from one SSE frame, in the dialect the provider
// speaks. The terminal is what makes an HTTP 200 failure a failure.
func probeFrameDelta(frame []byte, dialect string) (string, int, probeTerminal) {
	data := sseData(frame)
	if bytes.Equal(data, []byte("[DONE]")) {
		if dialect == "chat" {
			return "", 0, probeTerminalOK
		}
		return "", 0, probeTerminalMissing
	}
	var payload map[string]any
	if json.Unmarshal(data, &payload) != nil {
		return "", 0, probeTerminalMissing
	}
	// Any dialect's error object ends the probe as a failure.
	if payload["error"] != nil {
		return "", 0, probeTerminalFailed
	}
	eventType, _ := payload["type"].(string)
	switch dialect {
	case "anthropic":
		switch eventType {
		case "content_block_delta":
			if delta, ok := payload["delta"].(map[string]any); ok {
				// Text and thinking both count: the first token the model
				// spent on either is the latency the operator feels.
				if text, _ := delta["text"].(string); text != "" {
					return text, 0, probeTerminalMissing
				}
				if thinking, _ := delta["thinking"].(string); thinking != "" {
					return thinking, 0, probeTerminalMissing
				}
			}
		case "message_delta":
			if usage, ok := payload["usage"].(map[string]any); ok {
				return "", intNumber(usage["output_tokens"]), probeTerminalMissing
			}
		case "message_stop":
			return "", 0, probeTerminalOK
		case "error":
			return "", 0, probeTerminalFailed
		}
	case "chat":
		tokens := 0
		if usage, ok := payload["usage"].(map[string]any); ok {
			tokens = intNumber(usage["completion_tokens"])
		}
		choices, _ := payload["choices"].([]any)
		for _, rawChoice := range choices {
			choice, _ := rawChoice.(map[string]any)
			if delta, ok := choice["delta"].(map[string]any); ok {
				if text, _ := delta["content"].(string); text != "" {
					return text, tokens, probeTerminalMissing
				}
				// Reasoning models think out loud before they answer; that
				// thinking is the first token the user waits for.
				if reasoning, _ := delta["reasoning_content"].(string); reasoning != "" {
					return reasoning, tokens, probeTerminalMissing
				}
			}
		}
		return "", tokens, probeTerminalMissing
	default:
		switch eventType {
		case "response.output_text.delta", "response.reasoning_text.delta", "response.reasoning_summary_text.delta":
			if text, _ := payload["delta"].(string); text != "" {
				return text, 0, probeTerminalMissing
			}
		case "response.completed":
			response, ok := payload["response"].(map[string]any)
			// Match sseInspector: an event name alone is not completion.
			if !ok || response["status"] != "completed" || response["incomplete_details"] != nil || response["error"] != nil {
				return "", 0, probeTerminalFailed
			}
			tokens := 0
			if usage, ok := response["usage"].(map[string]any); ok {
				tokens = intNumber(usage["output_tokens"])
			}
			return "", tokens, probeTerminalOK
		case "response.failed", "response.incomplete", "error":
			return "", 0, probeTerminalFailed
		}
	}
	return "", 0, probeTerminalMissing
}

// probeJSONFailure answers the non-stream shape: a gateway that replies to a
// stream:true body with one plain JSON object whose own status says failed.
func probeJSONFailure(body []byte) bool {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return false
	}
	var payload map[string]any
	if json.Unmarshal(trimmed, &payload) != nil {
		return false
	}
	if status, _ := payload["status"].(string); status == "failed" || status == "cancelled" {
		return true
	}
	return payload["error"] != nil
}

func intNumber(value any) int {
	switch number := value.(type) {
	case float64:
		return int(number)
	case json.Number:
		parsed, _ := number.Int64()
		return int(parsed)
	default:
		return 0
	}
}
