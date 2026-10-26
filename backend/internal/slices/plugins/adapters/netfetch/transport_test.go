package netfetch

import (
	"context"
	"crypto/x509"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/plugins/application"
)

func TestBlockedAddresses(t *testing.T) {
	for address, want := range map[string]bool{
		"8.8.8.8":                  false,
		"2606:4700:4700::1111":     false,
		"127.0.0.1":                true,
		"10.1.2.3":                 true,
		"172.16.0.1":               true,
		"192.168.1.1":              true,
		"169.254.169.254":          true,
		"100.64.0.1":               true,
		"0.0.0.0":                  true,
		"198.18.0.1":               true,
		"224.0.0.1":                true,
		"255.255.255.255":          true,
		"::1":                      true,
		"::":                       true,
		"fe80::1":                  true,
		"fc00::1":                  true,
		"::ffff:127.0.0.1":         true,
		"::ffff:10.0.0.1":          true,
		"64:ff9b::7f00:1":          true,
		"2002:7f00:1::":            true,
		"ff02::1":                  true,
		"::ffff:8.8.8.8":           false,
		"192.0.0.8":                true,
		"240.0.0.1":                true,
		"2001:4860:4860::8888":     false,
		"fd12:3456:789a:1::1":      true,
		"100.127.255.254":          true,
		"100.128.0.1":              false,
		"172.32.0.1":               false,
		"192.169.0.1":              false,
		"11.0.0.1":                 false,
		"198.20.0.1":               false,
		"64:ff9b:2::1":             true, // reserved ::/8
		"2003::1":                  false,
		"169.254.0.0":              true,
		"fe80::a:b:c:d":            true,
		"0.255.255.255":            true,
		"203.0.113.9":              true,
		"2001:db8::1":              true,
		"::ffff:169.254.169.254":   true,
		"::ffff:100.64.0.1":        true,
		"::ffff:0:0":               true,
		"fec0::1":                  true,
		"febf::1":                  true,
		"64:ff9b:1:ffff::1":        true,
		"2002::":                   true,
		"2002:ffff:ffff::":         true,
		"2001::1":                  true,
		"100::1":                   true, // discard-only
		"100:0:0:1::1":             false,
		"2001:2::1":                true, // benchmarking
		"2001:1f::1":               true, // ORCHIDv2
		"2001:200::1":              false,
		"3fff::1":                  true, // documentation
		"3fff:1000::1":             false,
		"::127.0.0.1":              true,
		"::0.0.0.1":                true,
		"0:0:0:0:0:ffff:7f00:0001": true,
		"192.0.2.1":                true,
		"192.0.3.1":                false,
		"198.51.100.1":             true,
		"198.51.101.1":             false,
		"203.0.112.1":              false,
		"192.88.99.1":              true,
		"192.88.98.1":              false,
		"2001:0:4136:e378::1":      true, // Teredo
		"2001:1::1":                true, // PCP anycast, inside 2001::/23
		"2001:db9::1":              false,
		"feff::1":                  true,
		"fc00::":                   true,
	} {
		if got := Blocked(netip.MustParseAddr(address)); got != want {
			t.Errorf("Blocked(%s) = %v, want %v", address, got, want)
		}
	}
	if !Blocked(netip.Addr{}) {
		t.Fatal("an invalid address was allowed")
	}
}

// tlsServer starts a TLS server and a transport that resolves example.com
// to it, with blocked deciding which addresses may be reached.
func tlsServer(t *testing.T, handler http.HandlerFunc, blocked func(netip.Addr) bool) *Transport {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	address, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	lookup := func(_ context.Context, _, host string) ([]netip.Addr, error) {
		if host != "example.com" {
			return nil, errors.New("no such host")
		}
		return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
	}
	transport := build(lookup, blocked, address.Port(), roots)
	t.Cleanup(transport.Close)
	return transport
}

func allowAll(netip.Addr) bool { return false }

func TestFetchReachesAnAllowedServerWithoutFollowingRedirects(t *testing.T) {
	var seen atomic.Value
	transport := tlsServer(t, func(writer http.ResponseWriter, request *http.Request) {
		seen.Store(request.Method + " " + request.URL.String() + " " + request.Header.Get("X-Test") + " " + request.Host)
		if request.URL.Path == "/hop" {
			http.Redirect(writer, request, "https://example.com/elsewhere", http.StatusFound)
			return
		}
		writer.Header().Set("X-Answer", "yes")
		_, _ = writer.Write([]byte(strings.Repeat("a", 100)))
	}, allowAll)
	ctx := context.Background()
	answer, err := transport.Fetch(ctx, application.OutboundRequest{Method: "POST", URL: "https://example.com/data?q=1", Headers: map[string]string{"X-Test": "1"}, Body: []byte("{}")}, 1000)
	if err != nil || answer.Status != 200 || len(answer.Body) != 100 || http.Header(answer.Headers).Get("X-Answer") != "yes" {
		t.Fatalf("answer %+v %v", answer, err)
	}
	if seen.Load() != "POST /data?q=1 1 example.com" {
		t.Fatalf("the server saw %v", seen.Load())
	}
	answer, err = transport.Fetch(ctx, application.OutboundRequest{Method: "GET", URL: "https://example.com/hop"}, 1000)
	if err != nil || answer.Status != http.StatusFound || http.Header(answer.Headers).Get("Location") == "" || seen.Load() != "GET /hop  example.com" {
		t.Fatalf("a redirect was followed: %d %v %v", answer.Status, seen.Load(), err)
	}
	// The body is read to one byte past the cap so the caller can refuse it.
	answer, err = transport.Fetch(ctx, application.OutboundRequest{Method: "GET", URL: "https://example.com/"}, 10)
	if err != nil || len(answer.Body) != 11 {
		t.Fatalf("a capped body: %d %v", len(answer.Body), err)
	}
	if _, err := transport.Fetch(ctx, application.OutboundRequest{Method: "GET", URL: "https://unknown.example/"}, 10); !errors.Is(err, application.ErrNetwork) {
		t.Fatalf("an unresolvable host: %v", err)
	}
}

func TestFetchRefusesBlockedAddresses(t *testing.T) {
	handler := func(http.ResponseWriter, *http.Request) { t.Error("a blocked address was reached") }
	ctx := context.Background()
	request := application.OutboundRequest{Method: "GET", URL: "https://example.com/"}

	// The DNS answer is blocked.
	if _, err := tlsServer(t, handler, Blocked).Fetch(ctx, request, 10); !errors.Is(err, application.ErrBlockedAddress) {
		t.Fatalf("a private answer: %v", err)
	}
	// The answer passed, but the address actually dialed is checked again.
	var checks atomic.Int32
	rebinding := func(netip.Addr) bool { return checks.Add(1) > 1 }
	if _, err := tlsServer(t, handler, rebinding).Fetch(ctx, request, 10); !errors.Is(err, application.ErrBlockedAddress) || checks.Load() < 2 {
		t.Fatalf("a dial-time check: %v after %d checks", err, checks.Load())
	}

	// The production transport refuses a loopback server outright.
	server := httptest.NewTLSServer(http.HandlerFunc(handler))
	defer server.Close()
	production := New()
	defer production.Close()
	if _, err := production.Fetch(ctx, application.OutboundRequest{Method: "GET", URL: server.URL}, 10); !errors.Is(err, application.ErrBlockedAddress) {
		t.Fatalf("loopback through the production transport: %v", err)
	}
	host, port, _ := net.SplitHostPort(strings.TrimPrefix(server.URL, "https://"))
	if _, err := production.Fetch(ctx, application.OutboundRequest{Method: "GET", URL: "https://localhost:" + port + "/"}, 10); !errors.Is(err, application.ErrBlockedAddress) {
		t.Fatalf("localhost (%s) through the production transport: %v", host, err)
	}
}

func TestFetchTimesOut(t *testing.T) {
	release := make(chan struct{})
	transport := tlsServer(t, func(http.ResponseWriter, *http.Request) { <-release }, allowAll)
	defer close(release)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := transport.Fetch(ctx, application.OutboundRequest{Method: "GET", URL: "https://example.com/"}, 10); !errors.Is(err, application.ErrFetchTimeout) {
		t.Fatalf("a slow server: %v", err)
	}
}
