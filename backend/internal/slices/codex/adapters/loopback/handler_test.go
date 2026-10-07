package loopback

import (
	"context"
	"errors"
	"io"
	"net/http"
	"testing"
	"time"
)

// testClient bounds every request the tests fire at the server: a wedged
// handler fails the request after two seconds instead of hanging the
// test run.
var testClient = &http.Client{Timeout: 2 * time.Second}

// request performs one HTTP request against the server and returns its
// status, body and headers.
func request(t *testing.T, method, url string) (int, string, http.Header) {
	t.Helper()
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		t.Fatalf("building %s %s: %v", method, url, err)
	}
	response, err := testClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
	return response.StatusCode, string(body), response.Header
}

func TestACallbackWithTheWrongStateIsRejectedAndTheWaitContinues(t *testing.T) {
	server := startTestServer(t, "expected-state")
	base := callbackURL(t, server)

	status, body, _ := request(t, http.MethodGet, base+"/auth/callback?state=wrong-state&code=code-1")
	if status != http.StatusBadRequest {
		t.Fatalf("wrong-state callback status = %d, want %d", status, http.StatusBadRequest)
	}
	if body != "State mismatch" {
		t.Fatalf("wrong-state callback body = %q, want %q", body, "State mismatch")
	}

	// The flow survives the impostor: the wait runs to its own deadline
	// rather than resolving, because nothing was delivered.
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	code, err := server.AwaitCode(ctx)
	if code != "" {
		t.Fatalf("AwaitCode() code = %q, want empty", code)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("AwaitCode() error = %v, want context.DeadlineExceeded inside", err)
	}
	if err.Error() != "codex login await: context deadline exceeded" {
		t.Fatalf("AwaitCode() error = %q, want %q", err.Error(), "codex login await: context deadline exceeded")
	}
}

func TestACallbackWithoutACodeIsRejected(t *testing.T) {
	server := startTestServer(t, "expected-state")
	base := callbackURL(t, server)

	// OpenAI routes authorize errors back to the callback without a
	// code; the server must reject that instead of delivering emptiness.
	status, body, _ := request(t, http.MethodGet, base+"/auth/callback?state=expected-state")
	if status != http.StatusBadRequest {
		t.Fatalf("codeless callback status = %d, want %d", status, http.StatusBadRequest)
	}
	if body != "Missing code" {
		t.Fatalf("codeless callback body = %q, want %q", body, "Missing code")
	}
}

func TestTheMatchingCallbackDeliversTheCodeAndClosesTheTab(t *testing.T) {
	server := startTestServer(t, "the-state")
	base := callbackURL(t, server)

	status, body, headers := request(t, http.MethodGet, base+"/auth/callback?state=the-state&code=the-code")
	if status != http.StatusOK {
		t.Fatalf("matching callback status = %d, want %d", status, http.StatusOK)
	}
	if body != "Login completed. You can close this tab." {
		t.Fatalf("matching callback body = %q, want the completion page", body)
	}
	if contentType := headers.Get("Content-Type"); contentType != "text/plain" {
		t.Fatalf("matching callback Content-Type = %q, want %q", contentType, "text/plain")
	}

	ctx, cancel := context.WithTimeout(context.Background(), awaitTimeout)
	defer cancel()
	code, err := server.AwaitCode(ctx)
	if err != nil {
		t.Fatalf("AwaitCode() error = %v, want nil", err)
	}
	if code != "the-code" {
		t.Fatalf("AwaitCode() code = %q, want %q", code, "the-code")
	}
}

func TestALateSecondCallbackNeverBlocksTheHandler(t *testing.T) {
	server := startTestServer(t, "the-state")
	base := callbackURL(t, server)
	url := base + "/auth/callback?state=the-state&code=code-1"

	// Two redirects for one flow — a reload, a second tab — both get
	// their answer; the first code stays the winner.
	for i := 0; i < 2; i++ {
		status, body, _ := request(t, http.MethodGet, url)
		if status != http.StatusOK {
			t.Fatalf("callback %d status = %d, want %d", i+1, status, http.StatusOK)
		}
		if body != "Login completed. You can close this tab." {
			t.Fatalf("callback %d body = %q, want the completion page", i+1, body)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), awaitTimeout)
	defer cancel()
	code, err := server.AwaitCode(ctx)
	if err != nil {
		t.Fatalf("AwaitCode() error = %v, want nil", err)
	}
	if code != "code-1" {
		t.Fatalf("AwaitCode() code = %q, want %q", code, "code-1")
	}
}

func TestTheCancelRouteEndsTheWaitWithACancelledVerdict(t *testing.T) {
	server := startTestServer(t, "the-state")
	base := callbackURL(t, server)

	// The route may be hit more than once; the flow ends exactly once,
	// with no panic on the second hit.
	for i := 0; i < 2; i++ {
		status, body, _ := request(t, http.MethodGet, base+"/cancel")
		if status != http.StatusOK {
			t.Fatalf("cancel %d status = %d, want %d", i+1, status, http.StatusOK)
		}
		if body != "Login cancelled" {
			t.Fatalf("cancel %d body = %q, want %q", i+1, body, "Login cancelled")
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), awaitTimeout)
	defer cancel()
	code, err := server.AwaitCode(ctx)
	if code != "" {
		t.Fatalf("AwaitCode() code = %q, want empty", code)
	}
	if err == nil {
		t.Fatal("AwaitCode() after cancel must fail")
	}
	if err.Error() != "codex login cancelled" {
		t.Fatalf("AwaitCode() error = %q, want %q", err.Error(), "codex login cancelled")
	}
}

func TestUnknownPathsAreNotFound(t *testing.T) {
	server := startTestServer(t, "the-state")
	base := callbackURL(t, server)

	// The browser asks for the page's favicon right after the redirect;
	// that request must not disturb anything or leak a flow-specific
	// answer.
	status, body, _ := request(t, http.MethodGet, base+"/favicon.ico")
	if status != http.StatusNotFound {
		t.Fatalf("unknown path status = %d, want %d", status, http.StatusNotFound)
	}
	if body != "" {
		t.Fatalf("unknown path body = %q, want empty", body)
	}
}

func TestNonGetRequestsAreRefused(t *testing.T) {
	server := startTestServer(t, "the-state")
	base := callbackURL(t, server)

	// The flow is driven by browser navigations; every other method gets
	// the same refusal on every path, callback and cancel included.
	paths := []string{"/auth/callback?state=the-state&code=the-code", "/cancel", "/anything"}
	for _, path := range paths {
		status, body, headers := request(t, http.MethodPost, base+path)
		if status != http.StatusMethodNotAllowed {
			t.Fatalf("POST %s status = %d, want %d", path, status, http.StatusMethodNotAllowed)
		}
		if allow := headers.Get("Allow"); allow != http.MethodGet {
			t.Fatalf("POST %s Allow = %q, want %q", path, allow, http.MethodGet)
		}
		if body != "" {
			t.Fatalf("POST %s body = %q, want empty", path, body)
		}
	}
}
