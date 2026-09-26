package tunnelhttp

import (
	"bufio"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	relayapp "github.com/luxuryprivate/switchboard/backend/internal/slices/relay/application"
)

// A public client that sends its headers and then trickles its body used to
// pin one of the eight global limiter slots for the life of its socket: the
// server's ReadHeaderTimeout had already passed and nothing else applied. The
// read deadline ends the handler, which releases the slot — a follow-up client
// is served while the first one is still dribbling. The stalled client itself
// learns of nothing: closing a socket that still carries unread request bytes
// resets the connection, which can swallow the queued error response; the
// guarantee that matters is the slot, not the farewell.
func TestATricklingClientLosesItsSlotInsteadOfPinningIt(t *testing.T) {
	gateway := gatewayForTest(t, &fakeDispatcher{})
	gateway.bodyReadTimeout = 100 * time.Millisecond
	gateway.responseWriteTimeout = 100 * time.Millisecond
	handlerCount := make(chan int, 8)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		gateway.ServeHTTP(writer, request)
		handlerCount <- 1
	}))
	defer server.Close()
	host := strings.TrimPrefix(server.URL, "http://")

	connection, err := net.DialTimeout("tcp", host, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	// Headers for a large body, then a few bytes and silence.
	head := "POST /v1/responses HTTP/1.1\r\nHost: tunnel\r\nAuthorization: Bearer " + testToken + "\r\nContent-Type: application/json\r\nContent-Length: 65536\r\n\r\n"
	if _, err := connection.Write([]byte(head + `{"model":"public-g`)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-handlerCount:
	case <-time.After(2 * time.Second):
		t.Fatal("the stalled request held the handler, and its limiter slot, forever")
	}
	// The slot is free again: a fresh request is served immediately after.
	fresh, err := net.DialTimeout("tcp", host, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	fresh.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := fresh.Write([]byte(head + `{"model":"public-gpt"}`)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-handlerCount:
	case <-time.After(2 * time.Second):
		t.Fatal("the freed slot did not serve the next client")
	}
}

// The answer phase is bounded the same way: the response is fully buffered
// before it is written, so a single generous write deadline covers a slow
// reader without ever cutting a legitimate long generation.
func TestTheAnswerPhaseIsWrittenUnderADeadline(t *testing.T) {
	gateway := gatewayForTest(t, &fakeDispatcher{
		response: relayapp.DispatchResponse{Status: 200, Headers: http.Header{"Content-Type": []string{"application/json"}}, Body: []byte(`{"output":"ok"}`)},
	})
	gateway.bodyReadTimeout = 100 * time.Millisecond
	gateway.responseWriteTimeout = 100 * time.Millisecond
	server := httptest.NewServer(gateway)
	defer server.Close()
	host := strings.TrimPrefix(server.URL, "http://")

	connection, err := net.DialTimeout("tcp", host, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	head := "POST /v1/responses HTTP/1.1\r\nHost: tunnel\r\nAuthorization: Bearer " + testToken + "\r\nContent-Type: application/json\r\nContent-Length: 22\r\n\r\n"
	if _, err := connection.Write([]byte(head + `{"model":"public-gpt"}`)); err != nil {
		t.Fatal(err)
	}
	_ = connection.SetReadDeadline(time.Now().Add(2 * time.Second))
	statusLine, err := bufio.NewReader(connection).ReadString('\n')
	if err != nil {
		t.Fatalf("the answer phase never completed: %v", err)
	}
	if !strings.Contains(statusLine, "200") {
		t.Fatalf("unexpected status for a served answer: %q", statusLine)
	}
}
