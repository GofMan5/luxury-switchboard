package application

import (
	"context"
	"net/http"
)

type DispatchRequest struct {
	Method              string
	Path                string
	Headers             http.Header
	Body                []byte
	ProviderID          string
	PublicModel         string
	UpstreamModel       string
	AttemptLimit        int
	UseStoredCredential bool
}

type DispatchResponse struct {
	Status  int
	Headers http.Header
	Body    []byte
	// Which terminal event the upstream stream ended on, or "" for a clean one.
	// No caller reads it: the tunnel decides for itself whether an answer is safe
	// to forward, and it looks at the body rather than trusting this. It survives
	// because it is the only place the decision is still visible from outside —
	// the header it comes from is deleted before the response leaves Dispatch —
	// and three tests observe the retry ladder through it. Deleting it as dead
	// would blind them without failing anything.
	Terminal         string
	SensitiveMarkers []string `json:"-"`
}

type Dispatcher interface {
	Dispatch(context.Context, DispatchRequest) (DispatchResponse, error)
}

// ProbeReport is one measured streaming request: when the first content token
// arrived, when the answer finished, and how many tokens the provider reported
// (or the probe counted). ErrorCode carries the failure class like a history
// row does.
type ProbeReport struct {
	Status       int     `json:"status"`
	TTFTMs       float64 `json:"ttftMs"`
	TotalMs      float64 `json:"totalMs"`
	OutputTokens int     `json:"outputTokens"`
	ErrorCode    string  `json:"errorCode"`
	ErrorDetail  string  `json:"errorDetail"`
}

// StreamProber measures a provider+model with a real streaming request. The
// buffered Dispatch cannot answer "when did the first token arrive" because it
// sees the answer whole; the probe reads the stream as it arrives.
type StreamProber interface {
	Probe(ctx context.Context, providerID, model string) (ProbeReport, error)
}
