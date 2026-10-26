// Package netfetch performs plugin fetches on a dedicated client that never
// follows redirects, never uses a proxy and only connects to public
// addresses: every DNS answer is checked before dialing, and the address
// actually dialed is checked again.
package netfetch

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"maps"
	"net"
	"net/http"
	"net/netip"
	"syscall"
	"time"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/plugins/application"
)

// blockedPrefixes are ranges plugins may not reach beyond what netip's own
// predicates cover: CGNAT, "this network", IETF protocol assignments,
// benchmarking, documentation (TEST-NET-1/2/3, 2001:db8::/32, 3fff::/20), the
// 6to4 relay anycast, reserved space, the IPv6 discard prefix 100::/64, the
// IETF protocol assignments 2001::/23 (Teredo, benchmarking, ORCHID and
// service anycast), deprecated site-local fec0::/10 and the IPv6
// prefixes that embed IPv4: Teredo, 6to4 and the reserved ::/8, which holds
// the IPv4-compatible ::a.b.c.d and both NAT64 prefixes (64:ff9b::/96 and
// 64:ff9b:1::/48).
var blockedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("::/8"),
	netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("2001::/23"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("3fff::/20"),
	netip.MustParsePrefix("fec0::/10"),
}

// Blocked reports whether a plugin must not connect to address.
func Blocked(address netip.Addr) bool {
	address = address.Unmap()
	if !address.IsValid() || !address.IsGlobalUnicast() || address.IsLoopback() || address.IsPrivate() ||
		address.IsLinkLocalUnicast() || address.IsMulticast() || address.IsUnspecified() {
		return true
	}
	for _, prefix := range blockedPrefixes {
		if prefix.Contains(address) {
			return true
		}
	}
	return false
}

type resolver func(ctx context.Context, network, host string) ([]netip.Addr, error)

type Transport struct {
	client *http.Client
}

// New returns the production transport: system DNS, public addresses only,
// port 443, system roots.
func New() *Transport {
	return build(net.DefaultResolver.LookupNetIP, Blocked, "", nil)
}

// build wires the transport. Tests pass a resolver, an address policy, the
// port their TLS server listens on and its root; production passes none of
// those, so the URL's port (always 443 after the application's check) is
// dialed.
func build(lookup resolver, blocked func(netip.Addr) bool, dialPort string, roots *x509.CertPool) *Transport {
	dialer := &net.Dialer{
		Timeout: 10 * time.Second,
		// Control sees the address actually connected to, so nothing that
		// changes between resolving and dialing can reach a blocked one.
		Control: func(_, address string, _ syscall.RawConn) error {
			parsed, err := netip.ParseAddrPort(address)
			if err != nil || blocked(parsed.Addr()) {
				return application.ErrBlockedAddress
			}
			return nil
		},
	}
	dial := func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		if dialPort != "" {
			port = dialPort
		}
		addresses, err := lookup(ctx, "ip", host)
		if err != nil {
			return nil, err
		}
		if len(addresses) == 0 {
			return nil, errors.New("no addresses")
		}
		// One blocked answer refuses the name: a host that points anywhere
		// private is not one plugins should reach.
		for _, resolved := range addresses {
			if blocked(resolved) {
				return nil, application.ErrBlockedAddress
			}
		}
		var last error
		for _, resolved := range addresses {
			connection, err := dialer.DialContext(ctx, network, net.JoinHostPort(resolved.Unmap().String(), port))
			if err == nil {
				return connection, nil
			}
			last = err
		}
		return nil, last
	}
	transport := &http.Transport{
		Proxy:                  nil,
		DialContext:            dial,
		ForceAttemptHTTP2:      true,
		TLSClientConfig:        &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots},
		TLSHandshakeTimeout:    10 * time.Second,
		MaxIdleConns:           16,
		MaxIdleConnsPerHost:    4,
		IdleConnTimeout:        30 * time.Second,
		MaxResponseHeaderBytes: 64 * 1024,
	}
	return &Transport{client: &http.Client{
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}}
}

func (transport *Transport) Fetch(ctx context.Context, request application.OutboundRequest, maxBody int) (application.InboundResponse, error) {
	var body io.Reader
	if len(request.Body) > 0 {
		body = bytes.NewReader(request.Body)
	}
	outbound, err := http.NewRequestWithContext(ctx, request.Method, request.URL, body)
	if err != nil {
		return application.InboundResponse{}, application.ErrNetwork
	}
	for name, value := range request.Headers {
		outbound.Header.Set(name, value)
	}
	response, err := transport.client.Do(outbound)
	if err != nil {
		return application.InboundResponse{}, classify(ctx, err)
	}
	defer response.Body.Close()
	answer, err := io.ReadAll(io.LimitReader(response.Body, int64(maxBody)+1))
	if err != nil {
		return application.InboundResponse{}, classify(ctx, err)
	}
	return application.InboundResponse{Status: response.StatusCode, Headers: maps.Clone(response.Header), Body: answer}, nil
}

// Close drops idle connections; the client stays usable.
func (transport *Transport) Close() { transport.client.CloseIdleConnections() }

func classify(ctx context.Context, err error) error {
	var timeout interface{ Timeout() bool }
	switch {
	case errors.Is(err, application.ErrBlockedAddress):
		return application.ErrBlockedAddress
	case errors.Is(ctx.Err(), context.DeadlineExceeded), errors.As(err, &timeout) && timeout.Timeout():
		return application.ErrFetchTimeout
	default:
		return application.ErrNetwork
	}
}
