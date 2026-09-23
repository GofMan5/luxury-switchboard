package relayhttp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	relayapp "github.com/luxuryprivate/switchboard/backend/internal/slices/relay/application"
)

// countingCredentials hands out leases and counts how many came back. A lease is
// finished at most once, so a double finish would show as a count mismatch too.
type countingCredentials struct {
	mutex    sync.Mutex
	acquired int
	finished int
}

type countedLease struct {
	source *countingCredentials
	done   atomic.Bool
}

func (lease *countedLease) Credential() relayapp.Credential {
	return relayapp.Credential{Value: "key"}
}

func (lease *countedLease) Finish(relayapp.AttemptOutcome) {
	if !lease.done.CompareAndSwap(false, true) {
		lease.source.mutex.Lock()
		lease.source.finished += 1000 // makes a double finish unmistakable
		lease.source.mutex.Unlock()
		return
	}
	lease.source.mutex.Lock()
	lease.source.finished++
	lease.source.mutex.Unlock()
}

func (source *countingCredentials) Acquire(context.Context, string, string, func()) (relayapp.CredentialLease, time.Duration, error) {
	source.mutex.Lock()
	source.acquired++
	source.mutex.Unlock()
	return &countedLease{source: source}, 0, nil
}

func (source *countingCredentials) TryAcquire(string, string) (relayapp.CredentialLease, bool) {
	source.mutex.Lock()
	source.acquired++
	source.mutex.Unlock()
	return &countedLease{source: source}, true
}

func (source *countingCredentials) counts() (int, int) {
	source.mutex.Lock()
	defer source.mutex.Unlock()
	return source.acquired, source.finished
}

// A leased credential that is never finished is a credential the pool still believes
// is in flight, so it is never handed to the next request. The retry ladder has around
// twenty exits — every provider failure mode has its own — and two of them used to
// return without finishing: the pair that handle a provider whose endpoint 404s. That
// is a configuration mistake rather than a transient fault, so it repeats on every
// request, and the pool lost one credential per attempt until it had none left to give.
//
// The failures below are the ones that reach a DIFFERENT exit each, so the test
// measures the ladder rather than one branch of it. It asserts a balance, not a
// number: a new exit added without a finish fails here without anyone having to
// remember this test exists.
func TestEveryCredentialLeaseIsFinished(t *testing.T) {
	const endpointMissing = `{"error":{"message":"The requested endpoint was not found"}}`
	for _, testCase := range []struct {
		name     string
		format   string
		chatPath string
		status   int
		body     string
		path     string
		request  string
	}{
		{name: "endpoint 404 on the responses path", format: "auto", status: http.StatusNotFound, body: endpointMissing},
		{name: "chat path 404 after the probe", format: "auto", chatPath: "/chat-completion", status: http.StatusNotFound, body: endpointMissing},
		{name: "model missing", status: http.StatusNotFound, body: `{"error":{"message":"The model gpt-test does not exist"}}`},
		{name: "unauthorised", status: http.StatusUnauthorized, body: `{"error":{"message":"Invalid key"}}`},
		{name: "payment required", status: http.StatusPaymentRequired, body: `{"error":{"message":"Balance exhausted"}}`},
		{name: "rate limited", status: http.StatusTooManyRequests, body: `{"error":{"message":"Slow down"}}`},
		{name: "server error", status: http.StatusInternalServerError, body: `{"error":{"message":"Boom"}}`},
		{name: "bad request", status: http.StatusBadRequest, body: `{"error":{"message":"Nope"}}`},
		{name: "payload too large", status: http.StatusRequestEntityTooLarge, body: `{"error":{"message":"Too big"}}`},
		{name: "a clean answer", status: http.StatusOK, body: `{"id":"r","status":"completed","output":[]}`},
		{name: "an empty answer", status: http.StatusOK, body: ""},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.Header().Set("Content-Type", "application/json")
				writer.WriteHeader(testCase.status)
				_, _ = writer.Write([]byte(testCase.body))
			}))
			defer upstream.Close()
			parsed, _ := url.Parse(upstream.URL)
			credentials := &countingCredentials{}
			server := NewServer("127.0.0.1:0", Dependencies{
				Routes: fixedRoute{route: relayapp.Route{
					ProviderID: "echo", BaseURL: parsed, AuthMode: "bearer",
					Format: testCase.format, ChatPath: testCase.chatPath,
				}},
				Credentials: credentials,
				Config: Config{
					RetryBase: time.Microsecond, RetryMax: time.Microsecond,
					StreamIdleTimeout: time.Second, PermanentAttempts: 2,
				},
			})
			path := testCase.path
			if path == "" {
				path = "/v1/responses"
			}
			body := testCase.request
			if body == "" {
				body = `{"model":"gpt-test","input":"hi"}`
			}
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
			request.Header.Set("Content-Type", "application/json")
			server.ServeHTTP(recorder, request)

			acquired, finished := credentials.counts()
			if acquired == 0 {
				t.Fatalf("no credential was leased, so this case exercises nothing")
			}
			if finished != acquired {
				t.Fatalf("%d leases taken, %d returned (status %d): a credential the pool thinks is still in flight is never handed out again",
					acquired, finished, recorder.Code)
			}
		})
	}
}

// The client authenticates to Switchboard, never to the provider: that separation is
// the reason the relay exists. A provider that echoes the key we sent it back in a
// response header used to hand that key straight to the client, because every header
// that was not a hop header was copied verbatim.
//
// Set-Cookie is dropped for a related reason: it is the provider's session with us, the
// client has no use for it, and forwarding it lets a provider set state in whatever the
// client happens to be.
func TestAProviderCannotHandTheClientItsOwnKey(t *testing.T) {
	const key = "sk-provider-secret-value"
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		header := writer.Header()
		header.Set("Content-Type", "application/json")
		header.Set("X-Echoed-Authorization", request.Header.Get("Authorization"))
		header.Set("Set-Cookie", "session=provider-state; Path=/")
		// An ordinary header still has to arrive: this must not become a blanket strip.
		header.Set("X-Request-Id", "req_123")
		_, _ = writer.Write([]byte(`{"id":"r","status":"completed","output":[{"id":"m","type":"message",` +
			`"role":"assistant","content":[{"type":"output_text","text":"hi"}]}]}`))
	}))
	defer upstream.Close()
	parsed, _ := url.Parse(upstream.URL)
	server := NewServer("127.0.0.1:0", Dependencies{
		Routes:      fixedRoute{route: relayapp.Route{ProviderID: "echo", BaseURL: parsed, AuthMode: "bearer"}},
		Credentials: &credentialSource{values: []string{key}},
		Config: Config{
			RetryBase: time.Microsecond, RetryMax: time.Microsecond,
			StreamIdleTimeout: time.Second, PermanentAttempts: 2,
		},
	})
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-test","input":"hi"}`))
	request.Header.Set("Content-Type", "application/json")
	server.ServeHTTP(recorder, request)

	for name, values := range recorder.Header() {
		for _, value := range values {
			if strings.Contains(value, key) {
				t.Errorf("the provider key reached the client in %s: %q", name, value)
			}
		}
	}
	if got := recorder.Header().Get("Set-Cookie"); got != "" {
		t.Errorf("the provider set a cookie in the client: %q", got)
	}
	if got := recorder.Header().Get("X-Request-Id"); got != "req_123" {
		t.Errorf("an ordinary provider header was lost, so this is stripping more than secrets: %q", got)
	}
}
