package health

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	providerdomain "github.com/luxuryprivate/switchboard/backend/internal/slices/providers/domain"
)

// ReachabilityProber answers whether a provider's endpoint is alive. Any HTTP
// answer at all — including a 401 with no credential — means reachable: the
// question is transport, not entitlement, so a dead token or a suspicious edge
// must not paint a live provider red.
type ReachabilityProber interface {
	ProbeReachability(ctx context.Context, provider providerdomain.Provider) (up bool, reason string)
}

type HTTPProber struct{ client *http.Client }

func NewHTTPProber() *HTTPProber {
	return &HTTPProber{client: &http.Client{Timeout: 10 * time.Second, CheckRedirect: rejectRedirect}}
}

func (prober *HTTPProber) ProbeReachability(ctx context.Context, provider providerdomain.Provider) (bool, string) {
	// The catalog the provider actually serves, not a guess at where "models"
	// lives: the configured path is part of the provider's surface, and every
	// other catalog reader joins it the same way.
	target := *provider.BaseURL
	target.Path = providerdomain.JoinCatalogPath(target.Path, provider.ModelsPath)
	target.RawPath = ""
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return false, "endpoint is invalid"
	}
	response, err := prober.client.Do(request)
	if err != nil {
		if errors.Is(err, errProberRedirect) {
			// A redirect is still an answer from somewhere that owns the name:
			// the endpoint is alive, and the reason says so honestly.
			return true, "redirected"
		}
		if ctx.Err() != nil {
			return false, "probe was cancelled"
		}
		return false, classifyTransportError(err)
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4*1024))
	response.Body.Close()
	// The status does not matter: an answer is an answer. A provider that
	// replies 401 to an anonymous catalog request is alive and merely gated.
	return true, ""
}

// classifyTransportError narrows a raw transport error into a short, neutral
// reason. The provider's own endpoint never travels: the operator configured
// it, but a reason is a sentence, not a log line.
func classifyTransportError(err error) string {
	text := strings.ToLower(err.Error())
	switch {
	case strings.Contains(text, "timeout"), strings.Contains(text, "deadline"):
		return "timed out"
	case strings.Contains(text, "connection refused"):
		return "connection refused"
	case strings.Contains(text, "no such host"), strings.Contains(text, "dns"), strings.Contains(text, "lookup"):
		return "name does not resolve"
	case strings.Contains(text, "tls"), strings.Contains(text, "certificate"):
		return "TLS handshake failed"
	default:
		return "unreachable"
	}
}

var errProberRedirect = errors.New("provider endpoint redirected")

// rejectRedirect keeps the probe honest about where it landed: a redirect to a
// captive portal is an answer, but not the answer about this provider.
func rejectRedirect(*http.Request, []*http.Request) error { return errProberRedirect }
