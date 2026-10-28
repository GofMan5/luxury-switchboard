package relayhttp

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// Reconfigure retires the transport; its idle keep-alive sockets must close
// rather than linger for IdleConnTimeout (ten minutes) on every settings save.
func TestReconfigureClosesTheRetiredTransportsIdleConnections(t *testing.T) {
	closed := make(chan struct{}, 1)
	upstream := httptest.NewUnstartedServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(writer, "ok")
	}))
	upstream.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateClosed {
			select {
			case closed <- struct{}{}:
			default:
			}
		}
	}
	upstream.Start()
	defer upstream.Close()

	server := NewServer("127.0.0.1:0", Dependencies{})
	server.mu.Lock()
	client := server.client
	server.mu.Unlock()
	response, err := client.Get(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()

	server.Reconfigure(server.configSnapshot())
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("the retired transport kept its idle connection open")
	}
}
