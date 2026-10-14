package relayhttp

import (
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	relayapp "github.com/luxuryprivate/switchboard/backend/internal/slices/relay/application"
)

type fixtureAddressErrorBody struct {
	sent   bool
	stream bool
}

func (b *fixtureAddressErrorBody) Read(target []byte) (int, error) {
	if !b.sent {
		b.sent = true
		chunk := "fixture content"
		if b.stream {
			chunk = "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"fixture\"}}]}\n\n"
		}
		return copy(target, chunk), nil
	}
	time.Sleep(5 * time.Millisecond)
	return 0, &net.OpError{Op: "read", Net: "tcp", Addr: &net.TCPAddr{IP: net.ParseIP("192.0.2.12"), Port: 443}, Err: errors.New("fixture disconnect")}
}
func (*fixtureAddressErrorBody) Close() error { return nil }

type fixtureAddressTransport struct{ stream bool }

func (tr fixtureAddressTransport) RoundTrip(*http.Request) (*http.Response, error) {
	contentType := "text/plain"
	if tr.stream {
		contentType = "text/event-stream"
	}
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{contentType}}, Body: &fixtureAddressErrorBody{stream: tr.stream}}, nil
}

func TestBodyTransportFailureDoesNotPersistUpstreamAddress(t *testing.T) {
	for _, live := range []bool{false, true} {
		t.Run(map[bool]string{false: "copy", true: "live"}[live], func(t *testing.T) {
			sink := &recordingActivity{}
			base, _ := url.Parse("http://127.0.0.1:1")
			server := NewServer("127.0.0.1:0", Dependencies{Routes: fixedRoute{route: relayapp.Route{ProviderID: "fixture", BaseURL: base, AuthMode: "bearer"}}, Credentials: &credentialSource{values: []string{"fixture-secret"}}, Activity: sink, Config: Config{PermanentAttempts: 1, LiveStreamProbation: time.Millisecond}})
			server.client = &http.Client{Transport: fixtureAddressTransport{stream: live}}
			path := "/fixture"
			body := `{"model":"fixture-model"}`
			if live {
				path = "/v1/chat/completions"
				body = `{"model":"fixture-model","stream":true,"messages":[{"role":"user","content":"fixture"}]}`
			}
			r := httptest.NewRequest("POST", path, strings.NewReader(body))
			r.Header.Set("Content-Type", "application/json")
			server.ServeHTTP(httptest.NewRecorder(), r)
			if strings.Contains(sink.finish.ErrorDetail, "192.0.2.12") || strings.Contains(sink.finish.ErrorDetail, "tcp") {
				t.Fatalf("body transport address persisted: %+v", sink.finish)
			}
			if sink.finish.ErrorCode != "stream_incomplete" {
				t.Fatalf("failure was lost: %+v", sink.finish)
			}
		})
	}
}
