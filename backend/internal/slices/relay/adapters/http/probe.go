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
const probeOutputTokens = 32

// Probe measures one provider+model with a real streaming request: when the
// first content token arrived, when the answer finished, and how many tokens
// it carried. It deliberately does NOT ride requestWithRetry: that ladder
// buffers every stream before returning it, and a buffered stream has no
// first-token time left to measure. One attempt, one lease, one honest read.
func (server *Server) Probe(ctx context.Context, providerID, model string) (relayapp.ProbeReport, error) {
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
	path, body, dialect := probeRequest(route, upstreamModel)
	report, err := server.probeOnce(ctx, route, path, body, dialect, model)
	// A provider without a Responses endpoint gets the same probe over chat —
	// the answer the availability test would get, measured the same way.
	if err == nil && dialect == "responses" && responsesFallbackStatus(report.Status) {
		chatPath := route.ChatPath
		if chatPath == "" {
			chatPath = "/v1/chat/completions"
		}
		return server.probeOnce(ctx, route, chatPath, probeChatBody(upstreamModel), "chat", model)
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
	if route.Dialect == "anthropic" {
		body, _ := json.Marshal(map[string]any{
			"model": model, "max_tokens": probeOutputTokens, "stream": true,
			"messages": []map[string]string{{"role": "user", "content": "Reply OK"}},
		})
		return "/v1/messages", body, "anthropic"
	}
	if route.Format == "chat" {
		path := route.ChatPath
		if path == "" {
			path = "/v1/chat/completions"
		}
		return path, probeChatBody(model), "chat"
	}
	body, _ := json.Marshal(map[string]any{
		"model": model, "input": "Reply OK", "max_output_tokens": probeOutputTokens, "stream": true,
	})
	return "/v1/responses", body, "responses"
}

func probeChatBody(model string) []byte {
	body, _ := json.Marshal(map[string]any{
		"model": model, "max_tokens": probeOutputTokens, "stream": true,
		// Usage on a chat stream arrives only when asked; the probe wants the
		// provider's own count, not a guess from frames.
		"stream_options": map[string]any{"include_usage": true},
		"messages":       []map[string]string{{"role": "user", "content": "Reply OK"}},
	})
	return body
}

// probeOutcome classifies the refused attempt for the key pool exactly the way
// the retry ladder does: a dead key keeps counting its streak, a throttled one
// earns its cooldown, and a balance verdict is the provider's, not the key's.
// A probe that filed every refusal as a generic error would actively reset
// dead-key detection and re-pick hot keys for the next real request.
func probeOutcome(response *http.Response, config Config) relayapp.AttemptOutcome {
	switch {
	case response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden:
		return relayapp.AttemptOutcome{Kind: relayapp.AttemptAuthentication}
	case response.StatusCode == http.StatusPaymentRequired:
		return relayapp.AttemptOutcome{Kind: relayapp.AttemptBalanceExhausted}
	case response.StatusCode == http.StatusTooManyRequests || response.StatusCode == 529:
		return relayapp.AttemptOutcome{Kind: relayapp.AttemptRateLimited, RetryAfter: retryDelay(0, response, config)}
	case response.StatusCode >= 500:
		return relayapp.AttemptOutcome{Kind: relayapp.AttemptServerError}
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
			ErrorCode: "transport", ErrorDetail: truncateErrorDetail(redactSecrets(err.Error(), markers)),
		})
		return relayapp.ProbeReport{ErrorCode: "transport"}, err
	}
	defer response.Body.Close()
	report := relayapp.ProbeReport{Status: response.StatusCode}
	if response.StatusCode >= 400 {
		finishLease(lease, probeOutcome(response, server.configSnapshot()))
		// The body of a refused probe is small and carries the reason; read it
		// bounded, scrub it, file it.
		detail, _ := io.ReadAll(io.LimitReader(response.Body, 64*1024))
		report.ErrorCode = "request_rejected"
		report.ErrorDetail = truncateErrorDetail(redactRequestEchoes(jsonErrorDetailUnbounded(detail, markers), body))
		server.activity.Finish(activityID, relayapp.ActivityFinish{
			Status: response.StatusCode, ErrorCode: report.ErrorCode, ErrorDetail: report.ErrorDetail,
		})
		return report, nil
	}
	measured, collected, readErr := server.measureProbeStream(ctx, response.Body, sent, dialect)
	report.TTFTMs = measured.ttft
	report.TotalMs = measured.total
	report.OutputTokens = measured.tokens
	switch {
	case readErr != nil:
		report.ErrorCode = "transport"
		report.ErrorDetail = truncateErrorDetail(redactSecrets(readErr.Error(), markers))
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
		})
		return report, nil
	}
	server.activity.Finish(activityID, relayapp.ActivityFinish{
		Status: response.StatusCode, BytesOut: int64(len(collected)),
		ErrorCode: report.ErrorCode, ErrorDetail: report.ErrorDetail,
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
func (server *Server) measureProbeStream(ctx context.Context, body io.Reader, sent time.Time, dialect string) (probeMeasurement, []byte, error) {
	type line struct {
		value []byte
		err   error
	}
	lines := make(chan line, 128)
	go func() {
		reader := bufio.NewReaderSize(body, 64*1024)
		for {
			value, err := reader.ReadBytes('\n')
			if len(value) > 0 {
				select {
				case lines <- line{value: value}:
				case <-ctx.Done():
					return
				}
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
		case next := <-lines:
			if next.err != nil {
				return finish(next.err)
			}
			collected.Write(next.value)
			trimmed := bytes.TrimRight(next.value, "\r\n")
			if len(trimmed) == 0 {
				delta, tokens, terminal := probeFrameDelta(frame.Bytes(), dialect)
				frame.Reset()
				if terminal != probeTerminalMissing {
					measurement.terminal = terminal
				}
				if delta != "" {
					contentFrames++
					if measurement.ttft == 0 {
						measurement.ttft = float64(time.Since(sent).Microseconds()) / 1000
					}
				}
				if tokens > 0 {
					measurement.tokens = tokens
				}
			} else {
				frame.Write(next.value)
			}
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(idle)
		}
	}
}

// probeFrameDelta extracts the content delta, the reported output-token count
// and the terminal verdict from one SSE frame, in the dialect the provider
// speaks. The terminal is what makes an HTTP 200 failure a failure.
func probeFrameDelta(frame []byte, dialect string) (string, int, probeTerminal) {
	for _, raw := range bytes.Split(frame, []byte("\n")) {
		line := bytes.TrimSpace(raw)
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		data := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
		if len(data) == 0 {
			continue
		}
		if bytes.Equal(data, []byte("[DONE]")) {
			return "", 0, probeTerminalOK
		}
		var payload map[string]any
		if json.Unmarshal(data, &payload) != nil {
			continue
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
			if usage, ok := payload["usage"].(map[string]any); ok {
				if tokens := intNumber(usage["completion_tokens"]); tokens > 0 {
					return "", tokens, probeTerminalMissing
				}
			}
			choices, _ := payload["choices"].([]any)
			for _, rawChoice := range choices {
				choice, _ := rawChoice.(map[string]any)
				if delta, ok := choice["delta"].(map[string]any); ok {
					if text, _ := delta["content"].(string); text != "" {
						return text, 0, probeTerminalMissing
					}
					// Reasoning models think out loud before they answer; that
					// thinking is the first token the user waits for.
					if reasoning, _ := delta["reasoning_content"].(string); reasoning != "" {
						return reasoning, 0, probeTerminalMissing
					}
				}
			}
		default:
			switch eventType {
			case "response.output_text.delta", "response.reasoning_text.delta", "response.reasoning_summary_text.delta":
				if text, _ := payload["delta"].(string); text != "" {
					return text, 0, probeTerminalMissing
				}
			case "response.completed":
				tokens := 0
				if response, ok := payload["response"].(map[string]any); ok {
					if usage, ok := response["usage"].(map[string]any); ok {
						tokens = intNumber(usage["output_tokens"])
					}
				}
				return "", tokens, probeTerminalOK
			case "response.failed", "response.incomplete", "error":
				return "", 0, probeTerminalFailed
			}
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
