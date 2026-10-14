package relayhttp

import (
	"context"
	"errors"
	"net"
)

// Transport errors often embed the entire request URL (including query and
// userinfo) or provider/proxy host. History keeps the category, not that data.
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
	return "Provider connection failed"
}
