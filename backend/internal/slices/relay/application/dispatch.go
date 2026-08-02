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
	Status           int
	Headers          http.Header
	Body             []byte
	Terminal         string
	SensitiveMarkers []string `json:"-"`
}

type Dispatcher interface {
	Dispatch(context.Context, DispatchRequest) (DispatchResponse, error)
}
