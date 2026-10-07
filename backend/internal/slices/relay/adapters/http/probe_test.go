package relayhttp

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	relayapp "github.com/luxuryprivate/switchboard/backend/internal/slices/relay/application"
)

// The probe measures a real streaming request against the provider's own
// dialect: a delayed first frame must land in TTFT, the provider's usage count
// must win over frame counting, and a refusal must stay a refusal.
func TestTheProbeMeasuresFirstTokenAndProviderUsage(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		writer.WriteHeader(http.StatusOK)
		flusher, _ := writer.(http.Flusher)
		fmt.Fprint(writer, ": keep-alive\n\n")
		flusher.Flush()
		time.Sleep(60 * time.Millisecond)
		fmt.Fprint(writer, `data: {"choices":[{"delta":{"role":"assistant"}}]}`+"\n\n")
		flusher.Flush()
		fmt.Fprint(writer, `data: {"choices":[{"delta":{"content":"O"}}]}`+"\n\n")
		flusher.Flush()
		// The tail arrives measurably later, so total time cannot equal TTFT.
		time.Sleep(60 * time.Millisecond)
		fmt.Fprint(writer, `data: {"choices":[{"delta":{"content":"K"}}]}`+"\n\n")
		fmt.Fprint(writer, `data: {"choices":[],"usage":{"completion_tokens":17}}`+"\n\n")
		fmt.Fprint(writer, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer upstream.Close()
	parsed, _ := url.Parse(upstream.URL)
	server := NewServer("127.0.0.1:0", Dependencies{
		Routes: fixedRoute{route: relayapp.Route{
			ProviderID: "echo", ProviderName: "Echo", BaseURL: parsed, AuthMode: "bearer",
			Format: "chat", ChatPath: "/v1/chat/completions",
		}},
		Credentials: &credentialSource{values: []string{"test-key"}},
		Config:      Config{StreamIdleTimeout: 5 * time.Second},
	})
	report, err := server.Probe(context.Background(), "echo", "gpt-test")
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != http.StatusOK {
		t.Fatalf("probe failed: %+v", report)
	}
	// The keep-alive and the role-only frame arrived earlier; TTFT is the first
	// CONTENT frame, which the upstream delayed on purpose.
	if report.TTFTMs < 50 || report.TTFTMs >= report.TotalMs {
		t.Fatalf("ttft did not measure the first content token: %+v", report)
	}
	if report.OutputTokens != 17 {
		t.Fatalf("the provider's usage count lost to frame counting: %+v", report)
	}
}

func TestTheProbeCountsFramesWhenTheProviderNeverReportsUsage(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(writer, `data: {"type":"response.output_text.delta","delta":"O"}`+"\n\n")
		fmt.Fprint(writer, `data: {"type":"response.output_text.delta","delta":"K"}`+"\n\n")
		fmt.Fprint(writer, `data: {"type":"response.output_text.delta","delta":"!"}`+"\n\n")
		fmt.Fprint(writer, `data: {"type":"response.completed","response":{"status":"completed"}}`+"\n\n")
	}))
	defer upstream.Close()
	parsed, _ := url.Parse(upstream.URL)
	server := NewServer("127.0.0.1:0", Dependencies{
		Routes:      fixedRoute{route: relayapp.Route{ProviderID: "echo", BaseURL: parsed, AuthMode: "bearer", Format: "auto"}},
		Credentials: &credentialSource{values: []string{"test-key"}},
		Config:      Config{StreamIdleTimeout: 5 * time.Second},
	})
	report, err := server.Probe(context.Background(), "echo", "gpt-test")
	if err != nil {
		t.Fatal(err)
	}
	if report.OutputTokens != 3 {
		t.Fatalf("frame counting fell off: %+v", report)
	}
	if report.TTFTMs <= 0 || report.TotalMs <= 0 {
		t.Fatalf("timings were not measured: %+v", report)
	}
}

// A Responses-less provider answers the fallback the availability test would
// take: the probe must measure the chat answer, not report a 404.
func TestTheProbeFallsBackToChatLikeTheAvailabilityTest(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/v1/responses" {
			writer.WriteHeader(http.StatusNotFound)
			fmt.Fprint(writer, `{"error":{"message":"no such endpoint"}}`)
			return
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(writer, `data: {"choices":[{"delta":{"content":"OK"}}]}`+"\n\n")
		fmt.Fprint(writer, "data: [DONE]\n\n")
	}))
	defer upstream.Close()
	parsed, _ := url.Parse(upstream.URL)
	server := NewServer("127.0.0.1:0", Dependencies{
		Routes:      fixedRoute{route: relayapp.Route{ProviderID: "echo", BaseURL: parsed, AuthMode: "bearer", Format: "auto"}},
		Credentials: &credentialSource{values: []string{"test-key"}},
		Config:      Config{StreamIdleTimeout: 5 * time.Second},
	})
	report, err := server.Probe(context.Background(), "echo", "gpt-test")
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != http.StatusOK || report.OutputTokens != 1 {
		t.Fatalf("the chat fallback was not measured: %+v", report)
	}
}

// HTTP 200 is the standard streaming failure mode: the verdict rides the
// terminal event. A probe that read only the status line would report the
// failure as "available".
func TestTheProbeReadsTheTerminalVerdictNotTheStatusLine(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(writer, `data: {"type":"response.output_text.delta","delta":"O"}`+"\n\n")
		fmt.Fprint(writer, `data: {"type":"response.failed","response":{"status":"failed"}}`+"\n\n")
	}))
	defer upstream.Close()
	parsed, _ := url.Parse(upstream.URL)
	server := NewServer("127.0.0.1:0", Dependencies{
		Routes:      fixedRoute{route: relayapp.Route{ProviderID: "echo", BaseURL: parsed, AuthMode: "bearer", Format: "auto"}},
		Credentials: &credentialSource{values: []string{"test-key"}},
		Config:      Config{StreamIdleTimeout: 5 * time.Second},
	})
	report, err := server.Probe(context.Background(), "echo", "gpt-test")
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != http.StatusOK || report.ErrorCode != "upstream_status" {
		t.Fatalf("a failed terminal event was misreported: %+v", report)
	}
}

// A gateway that answers stream:true with one plain object never sends an SSE
// frame; the verdict sits in the body.
func TestTheProbeReadsAPlainJSONFailure(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(writer, `{"status":"failed","error":{"message":"model unavailable"}}`)
	}))
	defer upstream.Close()
	parsed, _ := url.Parse(upstream.URL)
	server := NewServer("127.0.0.1:0", Dependencies{
		Routes:      fixedRoute{route: relayapp.Route{ProviderID: "echo", BaseURL: parsed, AuthMode: "bearer", Format: "auto"}},
		Credentials: &credentialSource{values: []string{"test-key"}},
		Config:      Config{StreamIdleTimeout: 5 * time.Second},
	})
	report, err := server.Probe(context.Background(), "echo", "gpt-test")
	if err != nil {
		t.Fatal(err)
	}
	if report.ErrorCode != "upstream_status" {
		t.Fatalf("a plain JSON failure read as success: %+v", report)
	}
}

// A stream that ends without any terminal event is an incomplete answer, not
// a fast one.
func TestTheProbeReportsAMissingTerminal(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(writer, `data: {"choices":[{"delta":{"content":"OK"}}]}`+"\n\n")
	}))
	defer upstream.Close()
	parsed, _ := url.Parse(upstream.URL)
	server := NewServer("127.0.0.1:0", Dependencies{
		Routes:      fixedRoute{route: relayapp.Route{ProviderID: "echo", BaseURL: parsed, AuthMode: "bearer", Format: "chat", ChatPath: "/v1/chat/completions"}},
		Credentials: &credentialSource{values: []string{"test-key"}},
		Config:      Config{StreamIdleTimeout: 5 * time.Second},
	})
	report, err := server.Probe(context.Background(), "echo", "gpt-test")
	if err != nil {
		t.Fatal(err)
	}
	if report.ErrorCode != "stream_incomplete" {
		t.Fatalf("a stream without a terminal read as complete: %+v", report)
	}
}

// A reasoning model thinks out loud before it answers; that thinking IS the
// first token the operator waits for, and a probe blind to it reports no TTFT
// at all.
func TestTheProbeCountsReasoningAsTheFirstToken(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := writer.(http.Flusher)
		time.Sleep(60 * time.Millisecond)
		fmt.Fprint(writer, `data: {"choices":[{"delta":{"reasoning_content":"thinking…"}}]}`+"\n\n")
		flusher.Flush()
		time.Sleep(60 * time.Millisecond)
		fmt.Fprint(writer, `data: {"choices":[{"delta":{"content":"OK"}}]}`+"\n\n")
		fmt.Fprint(writer, `data: {"choices":[],"usage":{"completion_tokens":41}}`+"\n\n")
		fmt.Fprint(writer, "data: [DONE]\n\n")
	}))
	defer upstream.Close()
	parsed, _ := url.Parse(upstream.URL)
	server := NewServer("127.0.0.1:0", Dependencies{
		Routes:      fixedRoute{route: relayapp.Route{ProviderID: "echo", BaseURL: parsed, AuthMode: "bearer", Format: "chat", ChatPath: "/v1/chat/completions"}},
		Credentials: &credentialSource{values: []string{"test-key"}},
		Config:      Config{StreamIdleTimeout: 5 * time.Second},
	})
	report, err := server.Probe(context.Background(), "echo", "kimi-test")
	if err != nil {
		t.Fatal(err)
	}
	if report.TTFTMs < 50 || report.TTFTMs >= report.TotalMs {
		t.Fatalf("reasoning was not counted as the first token: %+v", report)
	}
	if report.OutputTokens != 41 {
		t.Fatalf("usage lost: %+v", report)
	}
}

// A probe never parks in the production queue: measurement must not displace
// service. A busy pool answers "busy" instead of queueing the test ahead of
// the operator's real traffic.
func TestTheProbeNeverQueuesAheadOfRealTraffic(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		t.Error("a probe reached the provider with no free key")
	}))
	defer upstream.Close()
	parsed, _ := url.Parse(upstream.URL)
	server := NewServer("127.0.0.1:0", Dependencies{
		Routes:      fixedRoute{route: relayapp.Route{ProviderID: "echo", BaseURL: parsed, AuthMode: "bearer"}},
		Credentials: delayedCredentialSource{delay: time.Minute, value: "test-key"}, // TryAcquire misses by design
		Config:      Config{StreamIdleTimeout: 5 * time.Second},
	})
	report, err := server.Probe(context.Background(), "echo", "gpt-test")
	if err != nil {
		t.Fatal(err)
	}
	if report.ErrorCode != "pool_busy" {
		t.Fatalf("a busy pool did not read as busy: %+v", report)
	}
}

func TestTheProbeReportsARefusalAsARefusal(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(writer, `{"error":{"message":"bad key"}}`)
	}))
	defer upstream.Close()
	parsed, _ := url.Parse(upstream.URL)
	server := NewServer("127.0.0.1:0", Dependencies{
		Routes:      fixedRoute{route: relayapp.Route{ProviderID: "echo", BaseURL: parsed, AuthMode: "bearer"}},
		Credentials: &credentialSource{values: []string{"test-key"}},
		Config:      Config{StreamIdleTimeout: 5 * time.Second},
	})
	report, err := server.Probe(context.Background(), "echo", "gpt-test")
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != http.StatusUnauthorized || report.ErrorCode != "request_rejected" {
		t.Fatalf("a refusal was misreported: %+v", report)
	}
}

// A probe that hits a revoked token files the provider's own verdict with
// the key pool, not just a bare authentication outcome: the codex source
// reads this code off the lease to stop serving a session the provider
// has already killed.
func TestTheProbeCarriesTheRevocationVerdictToTheKeyPool(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(writer, `{"error":{"code":"token_revoked","message":"Your token was revoked"}}`)
	}))
	defer upstream.Close()
	parsed, _ := url.Parse(upstream.URL)
	credentials := &credentialSource{values: []string{"test-key"}}
	server := NewServer("127.0.0.1:0", Dependencies{
		Routes:      fixedRoute{route: relayapp.Route{ProviderID: "echo", BaseURL: parsed, AuthMode: "bearer"}},
		Credentials: credentials,
		Config:      Config{StreamIdleTimeout: 5 * time.Second},
	})
	report, err := server.Probe(context.Background(), "echo", "gpt-test")
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != http.StatusUnauthorized || report.ErrorCode != "request_rejected" {
		t.Fatalf("a refusal was misreported: %+v", report)
	}
	if len(credentials.outcomes) != 1 {
		t.Fatalf("expected one filed outcome, saw %+v", credentials.outcomes)
	}
	outcome := credentials.outcomes[0]
	if outcome.Kind != relayapp.AttemptAuthentication ||
		outcome.ErrorCode != "token_revoked" ||
		outcome.ErrorMessage != "Your token was revoked" {
		t.Fatalf("the probe lost the provider's revocation verdict: %+v", outcome)
	}
}

func TestTheProbeReadsTheAnthropicDialect(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/messages" {
			writer.WriteHeader(http.StatusNotFound)
			return
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(writer, `data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"OK"}}`+"\n\n")
		fmt.Fprint(writer, `data: {"type":"message_delta","usage":{"output_tokens":9}}`+"\n\n")
		fmt.Fprint(writer, `data: {"type":"message_stop"}`+"\n\n")
	}))
	defer upstream.Close()
	parsed, _ := url.Parse(upstream.URL)
	server := NewServer("127.0.0.1:0", Dependencies{
		Routes:      fixedRoute{route: relayapp.Route{ProviderID: "echo", BaseURL: parsed, AuthMode: "bearer", Dialect: "anthropic"}},
		Credentials: &credentialSource{values: []string{"test-key"}},
		Config:      Config{StreamIdleTimeout: 5 * time.Second},
	})
	report, err := server.Probe(context.Background(), "echo", "claude-test")
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != http.StatusOK || report.OutputTokens != 9 || report.TTFTMs <= 0 {
		t.Fatalf("the anthropic stream was not measured: %+v", report)
	}
}
