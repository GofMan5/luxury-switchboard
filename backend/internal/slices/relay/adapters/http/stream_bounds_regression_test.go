package relayhttp

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	relayapp "github.com/luxuryprivate/switchboard/backend/internal/slices/relay/application"
)

func TestF06ProbeBoundsEveryStreamShape(t *testing.T) {
	for _, shape := range []string{"comments", "line", "event", "terminal", "cancel"} {
		t.Run(shape, func(t *testing.T) {
			stopped := make(chan struct{})
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer close(stopped)
				w.Header().Set("Content-Type", "text/event-stream")
				w.(http.Flusher).Flush()
				if shape == "terminal" {
					fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n")
					w.(http.Flusher).Flush()
				} else if shape != "cancel" {
					chunk := strings.Repeat("x", 32*1024)
					if shape == "comments" {
						chunk = ": " + chunk + "\n\n"
					}
					if shape == "event" {
						chunk = "data: " + chunk + "\n"
					}
					for n := 0; n < 300; n++ {
						if _, err := io.WriteString(w, chunk); err != nil {
							return
						}
						w.(http.Flusher).Flush()
					}
				}
				<-r.Context().Done()
			}))
			defer upstream.Close()
			target, _ := url.Parse(upstream.URL)
			credentials := &countingCredentials{}
			server := NewServer("127.0.0.1:0", Dependencies{Routes: fixedRoute{route: relayapp.Route{ProviderID: "echo", BaseURL: target, AuthMode: "bearer"}}, Credentials: credentials, Config: Config{MaxRequestBytes: 1024, StreamIdleTimeout: 100 * time.Millisecond}})
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if shape == "cancel" {
				time.AfterFunc(20*time.Millisecond, cancel)
			}
			report, err := server.Probe(ctx, "echo", "public")
			if err != nil {
				t.Fatal(err)
			}
			if shape == "terminal" && report.ErrorCode != "" {
				t.Fatalf("terminal waited for EOF: %+v", report)
			}
			if shape != "terminal" && report.ErrorCode == "" {
				t.Fatalf("invalid probe succeeded: %+v", report)
			}
			if shape != "terminal" && shape != "cancel" && !strings.Contains(report.ErrorDetail, "buffer limit") {
				t.Fatalf("overflow was not named: %+v", report)
			}
			if a, f := credentials.counts(); a != 1 || f != a {
				t.Fatalf("lease retained: %d/%d", a, f)
			}
			select {
			case <-stopped:
			case <-time.After(time.Second):
				t.Fatal("probe upstream not closed")
			}
		})
	}
}

// A flowing native stream must not keep an unfinished line/event growing after
// live handoff. The lease and upstream connection end at the protocol ceiling.
func TestF05LiveStreamRejectsUnboundedParserState(t *testing.T) {
	for _, shape := range []string{"line", "event"} {
		t.Run(shape, func(t *testing.T) {
			stopped := make(chan struct{})
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer close(stopped)
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"start\"}}]}\n\n")
				w.(http.Flusher).Flush()
				chunk := strings.Repeat("x", 32*1024)
				if shape == "event" {
					chunk = "data: " + chunk + "\n"
				}
				// More than both the configured 8 MiB ceiling and the old 32 MiB copy.
				for n := 0; n < 1200; n++ {
					if _, err := io.WriteString(w, chunk); err != nil {
						return
					}
					w.(http.Flusher).Flush()
				}
				<-r.Context().Done()
			}))
			defer upstream.Close()
			target, _ := url.Parse(upstream.URL)
			credentials := &countingCredentials{}
			server := NewServer("127.0.0.1:0", Dependencies{
				Routes:      fixedRoute{route: relayapp.Route{ProviderID: "echo", BaseURL: target, AuthMode: "bearer", Format: "chat"}},
				Credentials: credentials, Config: Config{MaxRequestBytes: 1024, StreamIdleTimeout: time.Second, LiveStreamProbation: time.Millisecond},
			})
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"public","stream":true,"messages":[]}`)).WithContext(ctx)
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			server.ServeHTTP(response, request)
			if !strings.Contains(response.Body.String(), `"error"`) || response.Body.Len() > 9*1024*1024 {
				t.Fatalf("unfinished %s was not bounded: bytes=%d", shape, response.Body.Len())
			}
			if a, f := credentials.counts(); a != 1 || f != a {
				t.Fatalf("lease retained: %d/%d", a, f)
			}
			select {
			case <-stopped:
			case <-time.After(time.Second):
				t.Fatal("upstream not closed")
			}
		})
	}
}
