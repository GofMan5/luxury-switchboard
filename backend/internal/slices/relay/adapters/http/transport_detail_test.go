package relayhttp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// The history keeps why a provider could not be reached — refused, dropped,
// unresolved — never the URL, host or credentials the error text carries.
func TestTransportErrorDetailNamesTheCauseWithoutTheAddress(t *testing.T) {
	const secret = "https://user:hunter22@relay.example.invalid:8799/v1?key=sk-fictional"
	wrap := func(err error) error { return &url.Error{Op: "Post", URL: secret, Err: err} }
	dial := &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connectex: No connection could be made because the target machine actively refused it.")}
	read := &net.OpError{Op: "read", Net: "tcp", Err: errors.New("wsarecv: An existing connection was forcibly closed by the remote host.")}
	cases := map[string]struct {
		err  error
		want string
	}{
		"cancelled":     {wrap(context.Canceled), "Request cancelled"},
		"deadline":      {wrap(context.DeadlineExceeded), "Request timed out"},
		"refused":       {wrap(dial), "Provider connection failed: could not connect"},
		"reset":         {wrap(read), "Provider connection failed: connection dropped"},
		"closed early":  {wrap(io.ErrUnexpectedEOF), "Provider connection failed: closed before the answer ended"},
		"closed at eof": {wrap(fmt.Errorf("read: %w", io.EOF)), "Provider connection failed: closed before the answer ended"},
		"dns":           {wrap(&net.DNSError{Err: "no such host", Name: "relay.example.invalid", IsNotFound: true}), "Provider connection failed: host not found"},
		"unknown":       {wrap(errors.New("something odd")), "Provider connection failed"},
	}
	for name, test := range cases {
		got := transportErrorDetail(test.err)
		if got != test.want {
			t.Errorf("%s: detail = %q, want %q", name, got, test.want)
		}
		for _, leak := range []string{"hunter22", "relay.example", "8799", "sk-fictional"} {
			if strings.Contains(got, leak) {
				t.Errorf("%s: detail %q leaks %q", name, got, leak)
			}
		}
	}
}

// A real dial to a closed loopback port and a real TLS handshake timeout,
// through net/http, land in their categories on every platform.
func TestTransportErrorDetailClassifiesRealTransportFailures(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closed := listener.Addr().String()
	_ = listener.Close()
	_, err = http.Get("http://" + closed + "/v1/models")
	if got := transportErrorDetail(err); got != "Provider connection failed: could not connect" {
		t.Fatalf("refused dial detail = %q (%v)", got, err)
	}

	// A listener that accepts and never speaks: the TLS handshake times out.
	mute, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer mute.Close()
	go func() {
		var held []net.Conn
		defer func() {
			for _, conn := range held {
				_ = conn.Close()
			}
		}()
		for {
			conn, acceptErr := mute.Accept()
			if acceptErr != nil {
				return
			}
			held = append(held, conn)
		}
	}()
	client := &http.Client{Transport: &http.Transport{TLSHandshakeTimeout: 50 * time.Millisecond}}
	_, err = client.Get("https://" + mute.Addr().String() + "/v1/models")
	if got := transportErrorDetail(err); got != "Provider connection timed out" {
		t.Fatalf("handshake detail = %q (%v)", got, err)
	}
}
