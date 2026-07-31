package application

import (
	"context"
	"net/http"
)

type DispatchRequest struct {
	Method        string
	Path          string
	Headers       http.Header
	Body          []byte
	ProviderID    string
	PublicModel   string
	UpstreamModel string
	AttemptLimit  int
}

type DispatchResponse struct {
	Status   int
	Headers  http.Header
	Body     []byte
	Terminal string
}

type Dispatcher interface {
	Dispatch(context.Context, DispatchRequest) (DispatchResponse, error)
}
