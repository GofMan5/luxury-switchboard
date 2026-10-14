package relayhttp

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	relayapp "github.com/luxuryprivate/switchboard/backend/internal/slices/relay/application"
)

// A partial inbound upload is not an upstream request. Route cancellation must
// release it on the real net/http server, without waiting for the client to send
// the missing Content-Length bytes.
func TestF11RouteSwitchCancelsPartialInboundUpload(t *testing.T) {
	server := NewServer("127.0.0.1:0", Dependencies{Routes: fixedRoute{route: relayapp.Route{ProviderID: "echo"}}, Config: Config{StreamIdleTimeout: time.Second}})
	snapshot, err := server.Start()
	if err != nil {
		t.Fatal(err)
	}
	defer server.Stop(context.Background())
	connection, err := net.Dial("tcp", strings.TrimPrefix(snapshot.Address, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	fmt.Fprint(connection, "POST /v1/responses HTTP/1.1\r\nHost: localhost\r\nContent-Type: application/json\r\nContent-Length: 1000\r\n\r\n{")
	time.Sleep(25 * time.Millisecond) // Permit the HTTP header to enter ServeHTTP.
	server.CancelActive()
	connection.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	response, err := http.ReadResponse(bufio.NewReader(connection), nil)
	if err != nil {
		t.Fatalf("partial upload retained socket after route cancellation: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("cancellation misclassified: %d", response.StatusCode)
	}
}

type blockedUpload struct {
	started, closed chan struct{}
	once            sync.Once
	closeOnce       sync.Once
}

func (body *blockedUpload) Read([]byte) (int, error) {
	body.once.Do(func() { close(body.started) })
	<-body.closed
	return 0, io.ErrClosedPipe
}
func (body *blockedUpload) Close() error {
	body.closeOnce.Do(func() { close(body.closed) })
	return nil
}
func TestF11BodyDeadlineAndShutdownReleaseTheReader(t *testing.T) {
	for _, shutdown := range []bool{false, true} {
		t.Run(fmt.Sprint(shutdown), func(t *testing.T) {
			body := &blockedUpload{started: make(chan struct{}), closed: make(chan struct{})}
			server := NewServer("127.0.0.1:0", Dependencies{Config: Config{ResponseHeaderTimeout: 30 * time.Millisecond}})
			request := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			request.Body = body
			response := httptest.NewRecorder()
			done := make(chan struct{})
			go func() { server.ServeHTTP(response, request); close(done) }()
			<-body.started
			expected := http.StatusRequestTimeout
			if shutdown {
				server.Stop(context.Background())
				expected = http.StatusServiceUnavailable
			}
			select {
			case <-done:
			case <-time.After(300 * time.Millisecond):
				body.Close()
				<-done
				t.Fatal("body deadline/shutdown left read active")
			}
			if response.Code != expected {
				t.Fatalf("wrong body termination: %d", response.Code)
			}
		})
	}
}

func TestF11ClientCancellationClosesInboundBody(t *testing.T) {
	body := &blockedUpload{started: make(chan struct{}), closed: make(chan struct{})}
	server := NewServer("127.0.0.1:0", Dependencies{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader("")).WithContext(ctx)
	request.Body = body
	response := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { server.ServeHTTP(response, request); close(done) }()
	<-body.started
	cancel()
	select {
	case <-done:
	case <-time.After(300 * time.Millisecond):
		body.Close()
		<-done
		t.Fatal("client cancellation did not close inbound read")
	}
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("cancellation misclassified: %d", response.Code)
	}
}
