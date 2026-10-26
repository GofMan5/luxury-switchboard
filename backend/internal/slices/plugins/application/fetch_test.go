package application

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/plugins/domain"
)

// fakeTransport records what reached the network and answers as told.
type fakeTransport struct {
	mu       sync.Mutex
	requests []OutboundRequest
	answer   InboundResponse
	err      error
	hold     chan struct{}
}

func (transport *fakeTransport) Fetch(ctx context.Context, request OutboundRequest, maxBody int) (InboundResponse, error) {
	transport.mu.Lock()
	transport.requests = append(transport.requests, request)
	hold := transport.hold
	transport.mu.Unlock()
	if hold != nil {
		select {
		case <-hold:
		case <-ctx.Done():
			return InboundResponse{}, ErrFetchTimeout
		}
	}
	answer := transport.answer
	if len(answer.Body) > maxBody+1 {
		answer.Body = answer.Body[:maxBody+1]
	}
	return answer, transport.err
}

func (transport *fakeTransport) sent() []OutboundRequest {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	return append([]OutboundRequest(nil), transport.requests...)
}

func fetching() domain.Package {
	p := pkg("acme.fetch", "Fetch", domain.PermissionNetFetch)
	p.Network = &domain.Network{Hosts: []string{"api.example.com", "*.cdn.example.net"}}
	p.Settings = []domain.Setting{
		{Key: "token", Label: "Token", Type: domain.SettingSecret},
		{Key: "spare", Label: "Spare", Type: domain.SettingSecret},
	}
	return p
}

func newFetcher(t *testing.T) (*Service, *Fetcher, *fakeTransport) {
	t.Helper()
	service, _, _ := newService(t, &memory{}, 1<<20)
	if _, err := service.Install(context.Background(), fetching(), []string{domain.PermissionNetFetch}, ""); err != nil {
		t.Fatal(err)
	}
	token := "tok-123456"
	if _, err := service.SetSecret(context.Background(), "acme.fetch", "token", &token); err != nil {
		t.Fatal(err)
	}
	transport := &fakeTransport{answer: InboundResponse{Status: 200, Headers: map[string][]string{"Content-Type": {"text/plain"}}, Body: []byte("ok")}}
	fetcher, err := NewFetcher(service, transport)
	if err != nil {
		t.Fatal(err)
	}
	return service, fetcher, transport
}

func refusedWith(t *testing.T, err error, code string) {
	t.Helper()
	var refused FetchError
	if !errors.As(err, &refused) || refused.Code != code {
		t.Fatalf("want %s, got %v", code, err)
	}
}

func TestFetchSendsOnlyWhatTheRulesAllow(t *testing.T) {
	_, fetcher, transport := newFetcher(t)
	transport.answer = InboundResponse{
		Status: 302,
		Headers: map[string][]string{
			"Location":     {"https://evil.example/?t=tok-123456"},
			"X-Echo":       {"Bearer tok-123456"},
			"Set-Cookie":   {"a=1", "b=2"},
			"Content-Type": {"text/plain"},
		},
		Body: []byte("you sent tok-123456 \xff"),
	}
	result, err := fetcher.Fetch(context.Background(), FetchRequest{
		ID:     "acme.fetch",
		URL:    "https://API.example.com./v1/items?q=1#frag",
		Method: "POST",
		Headers: map[string]string{
			"Authorization":   "Bearer {{secret:token}}",
			"Host":            "internal.example",
			"Cookie":          "session=1",
			"Proxy-Auth":      "x",
			"X-Switchboard-A": "x",
			"X-Forwarded-For": "1.2.3.4",
			"User-Agent":      "spoofed",
			"X-Literal":       "{{secret:token",
		},
		Body: `{"key":"{{secret:token}}"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	sent := transport.sent()
	if len(sent) != 1 {
		t.Fatalf("%d requests reached the network", len(sent))
	}
	request := sent[0]
	if request.URL != "https://api.example.com/v1/items?q=1" || request.Method != "POST" {
		t.Fatalf("sent %s %s", request.Method, request.URL)
	}
	want := map[string]string{
		"Authorization": "Bearer tok-123456",
		"X-Literal":     "{{secret:token",
		"User-Agent":    "LuxurySwitchboard-Plugin/" + runningVersion + " (acme.fetch)",
	}
	if len(request.Headers) != len(want) {
		t.Fatalf("sent headers %v", request.Headers)
	}
	for name, value := range want {
		if request.Headers[name] != value {
			t.Fatalf("header %s = %q, want %q", name, request.Headers[name], value)
		}
	}
	if string(request.Body) != `{"key":"tok-123456"}` {
		t.Fatalf("sent body %s", request.Body)
	}
	if result.Status != 302 || result.Redirected {
		t.Fatalf("result %+v", result)
	}
	if _, found := result.Headers["location"]; found {
		t.Fatal("location reached the plugin")
	}
	if result.Headers["x-echo"] != "Bearer [secret]" || result.Headers["set-cookie"] != "a=1, b=2" || result.Headers["content-type"] != "text/plain" {
		t.Fatalf("response headers %v", result.Headers)
	}
	if result.Body != "you sent [secret] �" {
		t.Fatalf("response body %q", result.Body)
	}
}

func TestFetchRefusals(t *testing.T) {
	service, fetcher, transport := newFetcher(t)
	ctx := context.Background()
	for name, test := range map[string]struct {
		request FetchRequest
		code    string
	}{
		"unknown plugin":      {FetchRequest{ID: "acme.other", URL: "https://api.example.com/"}, "not_allowed"},
		"http":                {FetchRequest{ID: "acme.fetch", URL: "http://api.example.com/"}, "not_allowed"},
		"other port":          {FetchRequest{ID: "acme.fetch", URL: "https://api.example.com:8443/"}, "not_allowed"},
		"undeclared host":     {FetchRequest{ID: "acme.fetch", URL: "https://example.com/"}, "not_allowed"},
		"wildcard apex":       {FetchRequest{ID: "acme.fetch", URL: "https://cdn.example.net/"}, "not_allowed"},
		"userinfo":            {FetchRequest{ID: "acme.fetch", URL: "https://u:p@api.example.com/"}, "not_allowed"},
		"ip literal":          {FetchRequest{ID: "acme.fetch", URL: "https://127.0.0.1/"}, "not_allowed"},
		"bad method":          {FetchRequest{ID: "acme.fetch", URL: "https://api.example.com/", Method: "TRACE"}, "invalid_plugin"},
		"short timeout":       {FetchRequest{ID: "acme.fetch", URL: "https://api.example.com/", TimeoutMS: 999}, "invalid_plugin"},
		"header injection":    {FetchRequest{ID: "acme.fetch", URL: "https://api.example.com/", Headers: map[string]string{"X-A": "a\r\nHost: b"}}, "invalid_plugin"},
		"bad header name":     {FetchRequest{ID: "acme.fetch", URL: "https://api.example.com/", Headers: map[string]string{"X A": "a"}}, "invalid_plugin"},
		"empty secret":        {FetchRequest{ID: "acme.fetch", URL: "https://api.example.com/", Headers: map[string]string{"X-A": "{{secret:spare}}"}}, "missing_secret"},
		"unknown secret":      {FetchRequest{ID: "acme.fetch", URL: "https://api.example.com/", Body: "{{secret:nope}}"}, "missing_secret"},
		"large body":          {FetchRequest{ID: "acme.fetch", URL: "https://api.example.com/", Body: strings.Repeat("x", MaxFetchRequestBytes+1)}, "too_large"},
		"secret in the url":   {FetchRequest{ID: "acme.fetch", URL: "https://api.example.com/{{secret:token}}"}, ""},
		"placeholder in host": {FetchRequest{ID: "acme.fetch", URL: "https://{{secret:token}}.cdn.example.net/"}, "not_allowed"},
	} {
		_, err := fetcher.Fetch(ctx, test.request)
		if test.code == "" {
			if err != nil {
				t.Errorf("%s: %v", name, err)
			}
			continue
		}
		var refused FetchError
		if !errors.As(err, &refused) || refused.Code != test.code {
			t.Errorf("%s: want %s, got %v", name, test.code, err)
		}
	}
	// Only the URL placeholder case reached the network, and unfilled.
	sent := transport.sent()
	if len(sent) != 1 || !strings.Contains(sent[0].URL, "secret:token") || strings.Contains(sent[0].URL, "tok-123456") {
		t.Fatalf("requests that reached the network: %+v", sent)
	}

	// A body under the cap that the filled secret pushes over it.
	long := strings.Repeat("s", 64)
	if _, err := service.SetSecret(ctx, "acme.fetch", "spare", &long); err != nil {
		t.Fatal(err)
	}
	_, err := fetcher.Fetch(ctx, FetchRequest{ID: "acme.fetch", URL: "https://api.example.com/", Body: strings.Repeat("{{secret:spare}}", MaxFetchRequestBytes/16)})
	refusedWith(t, err, "too_large")
	// A header under its cap that the filled secrets push over it.
	_, err = fetcher.Fetch(ctx, FetchRequest{ID: "acme.fetch", URL: "https://api.example.com/", Headers: map[string]string{"X-A": strings.Repeat("{{secret:spare}}", MaxFetchHeaderBytes/32)}})
	refusedWith(t, err, "invalid_plugin")

	off := false
	if _, err := service.Update(ctx, "acme.fetch", &off, nil); err != nil {
		t.Fatal(err)
	}
	_, err = fetcher.Fetch(ctx, FetchRequest{ID: "acme.fetch", URL: "https://api.example.com/"})
	refusedWith(t, err, "not_allowed")
	if len(transport.sent()) != 1 {
		t.Fatal("a refused request reached the network")
	}
}

func TestFetchMapsTransportOutcomes(t *testing.T) {
	_, fetcher, transport := newFetcher(t)
	request := FetchRequest{ID: "acme.fetch", URL: "https://a.cdn.example.net/"}
	for err, code := range map[error]string{
		ErrBlockedAddress:            "blocked_address",
		ErrFetchTimeout:              "timeout",
		errors.New("tls: handshake"): "network",
		errors.Join(ErrNetwork, nil): "network",
	} {
		transport.err = err
		_, got := fetcher.Fetch(context.Background(), request)
		refusedWith(t, got, code)
		if strings.Contains(got.Error(), "tls") {
			t.Fatal("a transport detail reached the plugin")
		}
	}
	transport.err = nil
	transport.answer = InboundResponse{Status: 200, Body: []byte(strings.Repeat("x", MaxFetchResponseBytes+1))}
	_, err := fetcher.Fetch(context.Background(), request)
	refusedWith(t, err, "too_large")
	transport.answer = InboundResponse{Status: 200, Body: []byte(strings.Repeat("x", MaxFetchResponseBytes))}
	if result, err := fetcher.Fetch(context.Background(), request); err != nil || len(result.Body) != MaxFetchResponseBytes {
		t.Fatalf("a body at the limit: %v", err)
	}
}

func TestFetchRateLimits(t *testing.T) {
	_, fetcher, transport := newFetcher(t)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	fetcher.limits.now = func() time.Time { return now }
	request := FetchRequest{ID: "acme.fetch", URL: "https://api.example.com/"}
	for range FetchesPerMinute {
		if _, err := fetcher.Fetch(context.Background(), request); err != nil {
			t.Fatal(err)
		}
	}
	_, err := fetcher.Fetch(context.Background(), request)
	refusedWith(t, err, "rate_limited")
	now = now.Add(time.Minute)
	if _, err := fetcher.Fetch(context.Background(), request); err != nil {
		t.Fatalf("after a minute: %v", err)
	}

	// Concurrency: four in flight, the fifth is refused at once, and a
	// finished one makes room again.
	_, fetcher, transport = newFetcher(t)
	transport.hold = make(chan struct{})
	var group sync.WaitGroup
	for range MaxConcurrentFetches {
		group.Go(func() {
			if _, err := fetcher.Fetch(context.Background(), request); err != nil {
				t.Error(err)
			}
		})
	}
	for len(transport.sent()) < MaxConcurrentFetches {
		time.Sleep(time.Millisecond)
	}
	_, err = fetcher.Fetch(context.Background(), request)
	refusedWith(t, err, "rate_limited")
	close(transport.hold)
	group.Wait()
	if _, err := fetcher.Fetch(context.Background(), request); err != nil {
		t.Fatalf("after the others finished: %v", err)
	}
	if len(fetcher.limits.plugins) != 1 {
		t.Fatal("the limiter lost or leaked its entry")
	}
}

func TestSecretsAreNamedNeverShown(t *testing.T) {
	store := &memory{}
	service, _, notifications := newService(t, store, 1<<20)
	ctx := context.Background()
	if _, err := service.Install(ctx, fetching(), nil, ""); err != nil {
		t.Fatal(err)
	}
	before := len(*notifications)
	value := "hunter2-value"
	names, err := service.SetSecret(ctx, "acme.fetch", "token", &value)
	if err != nil || len(names) != 1 || names[0] != "token" {
		t.Fatalf("set: %v %v", names, err)
	}
	if len(*notifications) != before+1 {
		t.Fatal("a secret change was not announced")
	}
	if _, err := service.SetSecret(ctx, "acme.fetch", "token", &value); err != nil || len(*notifications) != before+1 {
		t.Fatal("an unchanged secret was saved again")
	}
	detail, err := service.Get("acme.fetch")
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(detail)
	list, _ := service.List()
	listed, _ := json.Marshal(list)
	if !strings.Contains(string(encoded), `"secrets":["token"]`) || strings.Contains(string(encoded)+string(listed), value) {
		t.Fatalf("detail %s", encoded)
	}
	if store.plugins[0].Secrets["token"] != value {
		t.Fatal("the secret was not saved")
	}
	if _, err := service.SetSecret(ctx, "acme.fetch", "nope", &value); err == nil {
		t.Fatal("an undeclared secret was saved")
	}
	if _, err := service.SetSecret(ctx, "acme.none", "token", &value); !errors.Is(err, ErrNotFound) {
		t.Fatalf("absent plugin: %v", err)
	}

	// A new version that drops the setting drops its value.
	next := fetching()
	next.Version = "2.0.0"
	next.Settings = next.Settings[1:]
	summary, err := service.Install(ctx, next, nil, "")
	if err != nil || len(summary.Secrets) != 0 || len(store.plugins[0].Secrets) != 0 {
		t.Fatalf("after the setting went: %+v %v %v", summary.Secrets, store.plugins[0].Secrets, err)
	}
	if names, err := service.SetSecret(ctx, "acme.fetch", "spare", nil); err != nil || len(names) != 0 {
		t.Fatalf("clearing an unset secret: %v %v", names, err)
	}
}

func TestFetchRedactsEncodedSecrets(t *testing.T) {
	service, fetcher, transport := newFetcher(t)
	ctx := context.Background()
	quoted := `p"w<1>&\x`
	if _, err := service.SetSecret(ctx, "acme.fetch", "spare", &quoted); err != nil {
		t.Fatal(err)
	}
	short := "k"
	if _, err := service.SetSecret(ctx, "acme.fetch", "token", &short); err != nil {
		t.Fatal(err)
	}
	const bs = string(rune(0x5c)) // a backslash, spelled out to keep the escapes legible
	encoded := []string{
		base64.StdEncoding.EncodeToString([]byte(quoted)),
		base64.RawStdEncoding.EncodeToString([]byte(quoted)),
		base64.URLEncoding.EncodeToString([]byte(quoted)),
		"p" + bs + `"w` + bs + "u003c1" + bs + "u003e" + bs + "u0026" + bs + bs + "x", // Go's HTML-escaping encoder
		"p" + bs + `"w<1>&` + bs + bs + "x",                                           // other encoders
	}
	transport.answer = InboundResponse{Status: 200, Headers: map[string][]string{"X-Echo": {encoded[0]}}, Body: []byte(strings.Join(encoded, " "))}
	result, err := fetcher.Fetch(ctx, FetchRequest{ID: "acme.fetch", URL: "https://api.example.com/"})
	if err != nil {
		t.Fatal(err)
	}
	// The one-byte "k" is redacted like any other: there is no minimum length.
	if want := strings.TrimSpace(strings.Repeat("[secret] ", len(encoded))); result.Body != want || result.Headers["x-echo"] != "[secret]" {
		t.Fatalf("body %q headers %v", result.Body, result.Headers)
	}
}

// Hex, percent, `\/`, ensure_ascii and offset base64 (Basic auth) forms are
// redacted too; a secret under four bytes only in its raw form.
func TestFetchRedactsTransportEncodedSecrets(t *testing.T) {
	service, fetcher, transport := newFetcher(t)
	ctx := context.Background()
	secret := "s/é k+1~fictional"
	if _, err := service.SetSecret(ctx, "acme.fetch", "spare", &secret); err != nil {
		t.Fatal(err)
	}
	short := "k"
	if _, err := service.SetSecret(ctx, "acme.fetch", "token", &short); err != nil {
		t.Fatal(err)
	}
	escaped := strings.ReplaceAll(secret, "/", `\/`)
	forms := map[string]string{
		"hex":          hex.EncodeToString([]byte(secret)),
		"HEX":          strings.ToUpper(hex.EncodeToString([]byte(secret))),
		"query":        url.QueryEscape(secret),
		"path":         url.PathEscape(secret),
		"json slash":   escaped,
		"ensure_ascii": strings.ReplaceAll(secret, "é", string(rune(0x5c))+"u00e9"),
		"php default":  strings.ReplaceAll(escaped, "é", string(rune(0x5c))+"u00e9"),
		"basic":        base64.StdEncoding.EncodeToString([]byte("user:" + secret)),
		"basic url":    base64.RawURLEncoding.EncodeToString([]byte("me:" + secret + "!")),
		"offset 1":     base64.StdEncoding.EncodeToString([]byte("x" + secret)),
	}
	for name, form := range forms {
		transport.answer = InboundResponse{Status: 200, Body: []byte("6b " + form + " 6b")}
		result, err := fetcher.Fetch(ctx, FetchRequest{ID: "acme.fetch", URL: "https://api.example.com/"})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(result.Body, "[secret]") || strings.Contains(result.Body, form) || !strings.HasPrefix(result.Body, "6b ") {
			t.Errorf("%s: %q became %q", name, form, result.Body)
		}
		if leftover := strings.Trim(strings.ReplaceAll(result.Body, "[secret]", ""), "6b "); len(leftover) > 10 { // the "user:" prefix, boundary characters and padding
			t.Errorf("%s: %q leaves %q", name, form, leftover)
		}
	}
}
