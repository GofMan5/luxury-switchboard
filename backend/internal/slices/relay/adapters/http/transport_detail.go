package relayhttp

import (
	"context"
	"errors"
	"io"
	"net"
	"syscall"
)

// Transport errors often embed the entire request URL (including query and
// userinfo) or provider/proxy host. History keeps the category — why the
// provider could not be reached — built from typed errors, never that text.
func transportErrorDetail(err error) string {
	if errors.Is(err, context.Canceled) {
		return "Request cancelled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "Request timed out"
	}
	var timeout net.Error
	if errors.As(err, &timeout) && timeout.Timeout() {
		return "Provider connection timed out"
	}
	const failed = "Provider connection failed"
	var dns *net.DNSError
	var operation *net.OpError
	switch {
	case errors.As(err, &dns):
		return failed + ": host not found"
	case errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, io.EOF):
		return failed + ": closed before the answer ended"
	case errors.Is(err, syscall.ECONNRESET), errors.Is(err, syscall.ECONNABORTED):
		return failed + ": connection dropped"
	case errors.As(err, &operation) && operation.Op == "dial":
		return failed + ": could not connect"
	case errors.As(err, &operation) && (operation.Op == "read" || operation.Op == "write"):
		return failed + ": connection dropped"
	}
	return failed
}
