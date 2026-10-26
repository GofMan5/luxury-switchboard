package relayhttp

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	activityapp "github.com/luxuryprivate/switchboard/backend/internal/slices/activity/application"
	activitydomain "github.com/luxuryprivate/switchboard/backend/internal/slices/activity/domain"
	relayactivity "github.com/luxuryprivate/switchboard/backend/internal/slices/relay/adapters/activity"
	relayapp "github.com/luxuryprivate/switchboard/backend/internal/slices/relay/application"
)

func TestProbeConnectionFailureDoesNotPersistTheRequestURL(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		connection, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		connection.Close()
	}))
	defer upstream.Close()
	base, _ := url.Parse(upstream.URL + "/private-provider?api_key=fixture-query-secret")
	activity := activityapp.NewService(10)
	credentials := &credentialSource{values: []string{"fixture-key"}}
	server := NewServer("127.0.0.1:0", Dependencies{
		Routes:      fixedRoute{route: relayapp.Route{ProviderID: "fixture", BaseURL: base, AuthMode: "bearer"}},
		Credentials: credentials, Activity: relayactivity.NewRecorder(activity),
		Config: Config{StreamIdleTimeout: time.Second},
	})
	report, err := server.Probe(context.Background(), "fixture", "fixture-model")
	row := activity.List(1)[0]
	if err == nil || report.ErrorCode != "transport" || row.State != activitydomain.StateFailed || row.ErrorCode != "transport" {
		t.Fatalf("connection failure was lost: report=%+v row=%+v err=%v", report, row, err)
	}
	if row.ErrorDetail != "Provider connection failed: closed before the answer ended" {
		t.Fatalf("request URL/query entered history: %q", row.ErrorDetail)
	}
	if len(credentials.outcomes) != 1 || credentials.outcomes[0].Kind != relayapp.AttemptTransport {
		t.Fatalf("connection failure retained its lease: %+v", credentials.outcomes)
	}
}

// Keep the real loopback transport and annotate only its EOF, as a proxy or
// custom dialer can annotate a failed read with a private URL or address.
type probePrivateReadTransport struct{ base http.RoundTripper }

func (transport probePrivateReadTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := transport.base.RoundTrip(request)
	if err == nil {
		response.Body = probePrivateReadBody{response.Body}
	}
	return response, err
}

type probePrivateReadBody struct{ io.ReadCloser }

func (body probePrivateReadBody) Read(buffer []byte) (int, error) {
	n, err := body.ReadCloser.Read(buffer)
	if err == io.EOF {
		err = &url.Error{Op: "read", URL: "https://private-fixture.invalid/v1?api_key=fixture-query-secret", Err: io.ErrUnexpectedEOF}
	}
	return n, err
}

func TestProbeReadFailureKeepsUsageButNotPrivateTransportText(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		time.Sleep(20 * time.Millisecond)
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"partial"}}]}`+"\n\n"+`data: {"choices":[],"usage":{"prompt_tokens":100,"completion_tokens":17,"total_tokens":117}}`+"\n\n")
	}))
	defer upstream.Close()
	base, _ := url.Parse(upstream.URL)
	activity := activityapp.NewService(10)
	credentials := &credentialSource{values: []string{"fixture-key"}}
	server := NewServer("127.0.0.1:0", Dependencies{
		Routes:      fixedRoute{route: relayapp.Route{ProviderID: "fixture", BaseURL: base, AuthMode: "bearer", Format: "chat"}},
		Credentials: credentials, Activity: relayactivity.NewRecorder(activity),
		Config: Config{StreamIdleTimeout: time.Second},
	})
	transport := &http.Transport{}
	defer transport.CloseIdleConnections()
	server.client = &http.Client{Transport: probePrivateReadTransport{base: transport}}
	report, err := server.Probe(context.Background(), "fixture", "fixture-model")
	if err != nil {
		t.Fatal(err)
	}
	row := activity.List(1)[0]
	if report.ErrorCode != "transport" || row.ErrorCode != "transport" || row.State != activitydomain.StateFailed {
		t.Fatalf("read failure was lost: report=%+v row=%+v", report, row)
	}
	if report.ErrorDetail != "Provider connection failed: closed before the answer ended" || row.ErrorDetail != report.ErrorDetail {
		t.Fatalf("private read text entered report/history: report=%+v row=%+v", report, row)
	}
	if report.TTFTMs <= 0 || row.InputTokens != 100 || row.OutputTokens != 17 || row.TotalTokens != 117 || row.GenerationMS <= 0 || row.BytesOut == 0 {
		t.Fatalf("failed read erased partial timing/billing: report=%+v row=%+v", report, row)
	}
	if len(credentials.outcomes) != 1 || credentials.outcomes[0].Kind != relayapp.AttemptTransport {
		t.Fatalf("read failure retained its lease: %+v", credentials.outcomes)
	}
}
