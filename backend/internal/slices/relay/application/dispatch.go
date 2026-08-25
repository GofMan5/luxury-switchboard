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
