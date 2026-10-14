package relayhttp

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	relayapp "github.com/luxuryprivate/switchboard/backend/internal/slices/relay/application"
)

func TestF07ErrorHeadersCannotHoldARequestOrLease(t *testing.T) {
	for _, test := range []struct {
		name                   string
		status                 int
		probe, cancel, trickle bool
	}{
		{name: "silent 400", status: 400}, {name: "silent 429", status: 429},
		{name: "silent probe error", status: 401, probe: true}, {name: "cancel during error", status: 400, cancel: true},
		{name: "slowly arriving error", status: 400, trickle: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			stopped := make(chan struct{})
			headers := make(chan struct{})
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer close(stopped)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(test.status)
				w.(http.Flusher).Flush()
				close(headers)
				if test.trickle {
					for _, part := range []string{`{"error":{`, `"message":`, `"Content `, `policy `, `refusal"`, `}}`} {
						select {
						case <-r.Context().Done():
							return
						case <-time.After(25 * time.Millisecond):
						}
						fmt.Fprint(w, part)
						w.(http.Flusher).Flush()
					}
					return
				}
				<-r.Context().Done()
			}))
			defer upstream.Close()
			target, _ := url.Parse(upstream.URL)
			credentials := &countingCredentials{}
			server := NewServer("127.0.0.1:0", Dependencies{Routes: fixedRoute{route: relayapp.Route{ProviderID: "echo", BaseURL: target, AuthMode: "bearer", Format: "responses"}}, Credentials: credentials, Config: Config{StreamIdleTimeout: 60 * time.Millisecond, RetryBase: time.Microsecond, RetryMax: time.Microsecond, PermanentAttempts: 1}})
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if test.cancel {
				go func() { <-headers; cancel() }()
			}
			started := time.Now()
			if test.probe {
				_, _ = server.Probe(ctx, "echo", "public")
			} else {
				response, err := server.Dispatch(ctx, relayapp.DispatchRequest{ProviderID: "echo", UpstreamModel: "public", Method: http.MethodPost, Path: "/v1/responses", Headers: http.Header{"Content-Type": []string{"application/json"}}, Body: []byte(`{"model":"public","input":"hi"}`), AttemptLimit: 1})
				if test.trickle && (err != nil || response.Status != test.status) {
					t.Fatalf("healthy trickle timed out: status=%d err=%v", response.Status, err)
				}
			}
			if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
				t.Fatalf("error body bypassed idle/cancel budget: %v", elapsed)
			}
			if a, f := credentials.counts(); a != 1 || f != a {
				t.Fatalf("lease retained: %d/%d", a, f)
			}
			select {
			case <-stopped:
			case <-time.After(time.Second):
				t.Fatal("error upstream not closed")
			}
		})
	}
}

func TestF08LiveTerminalEndsWithoutWaitingForHTTPBody(t *testing.T) {
	for _, test := range []struct{ path, prefix, terminal, code string }{
		{path: "/v1/chat/completions", prefix: `data: {"choices":[{"delta":{"content":"start"}}]}` + "\n\n", terminal: "data: [DONE]\n\n"},
		{path: "/v1/responses", prefix: `data: {"type":"response.output_item.added","item":{"type":"message","status":"in_progress"}}` + "\n\n", terminal: `data: {"type":"response.completed","response":{"status":"completed"}}` + "\n\n"},
		{path: "/v1/responses", prefix: `data: {"type":"response.output_item.added","item":{"type":"message","status":"in_progress"}}` + "\n\n", terminal: `data: {"type":"response.incomplete","response":{"status":"incomplete"}}` + "\n\n", code: "stream_incomplete"},
		{path: "/v1/responses", prefix: `data: {"type":"response.output_item.added","item":{"type":"message","status":"in_progress"}}` + "\n\n", terminal: `data: {"type":"response.failed","response":{"status":"failed"}}` + "\n\n", code: "upstream_status"},
		{path: "/v1/messages", prefix: `data: {"type":"content_block_delta","delta":{"text":"start"}}` + "\n\n", terminal: `data: {"type":"message_stop"}` + "\n\n"},
	} {
		t.Run(test.terminal, func(t *testing.T) {
			stopped := make(chan struct{})
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer close(stopped)
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, test.prefix)
				w.(http.Flusher).Flush()
				// Force the native prefix to hand off before sending the terminal.
				time.Sleep(30 * time.Millisecond)
				fmt.Fprint(w, test.terminal+"data: trailing-after-terminal\n\n")
				w.(http.Flusher).Flush()
				<-r.Context().Done()
			}))
			defer upstream.Close()
			target, _ := url.Parse(upstream.URL)
			credentials := &countingCredentials{}
			activity := &recordingActivity{}
			server := NewServer("127.0.0.1:0", Dependencies{Routes: fixedRoute{route: relayapp.Route{ProviderID: "echo", BaseURL: target, AuthMode: "bearer"}}, Credentials: credentials, Activity: activity, Config: Config{StreamIdleTimeout: 100 * time.Millisecond, LiveStreamProbation: time.Millisecond}})
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			request := httptest.NewRequest(http.MethodPost, test.path, strings.NewReader(`{"model":"public","stream":true,"messages":[]}`)).WithContext(ctx)
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			server.ServeHTTP(response, request)
			if !strings.HasSuffix(response.Body.String(), test.terminal) {
				t.Fatalf("terminal was not the final bytes: %s", response.Body.String())
			}
			if activity.finish.ErrorCode != test.code {
				t.Fatalf("terminal outcome wrong: %+v", activity.finish)
			}
			if a, f := credentials.counts(); a != 1 || f != a {
				t.Fatalf("lease retained: %d/%d", a, f)
			}
			select {
			case <-stopped:
			case <-time.After(time.Second):
				t.Fatal("terminal did not close upstream")
			}
		})
	}
}
