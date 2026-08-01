package relay

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/models/domain"
	relayapp "github.com/luxuryprivate/switchboard/backend/internal/slices/relay/application"
)

type dispatcherFunc func(context.Context, relayapp.DispatchRequest) (relayapp.DispatchResponse, error)

func (dispatch dispatcherFunc) Dispatch(ctx context.Context, request relayapp.DispatchRequest) (relayapp.DispatchResponse, error) {
	return dispatch(ctx, request)
}

func TestExtractModelIDsAcceptsCommonCatalogShapes(t *testing.T) {
	models, err := extractModelIDs([]byte(`{"data":[{"id":"gpt-z"},{"model":"gpt-a"},{"id":"gpt-z"}]}`))
	if err != nil || len(models) != 2 || models[0] != "gpt-a" || models[1] != "gpt-z" {
		t.Fatalf("unexpected models: %v err=%v", models, err)
	}
	models, err = extractModelIDs([]byte(`{"models":["claude-b",{"name":"claude-a"}]}`))
	if err != nil || len(models) != 2 || models[0] != "claude-a" {
		t.Fatalf("unexpected alternate catalog: %v err=%v", models, err)
	}
	models, err = extractModelIDs([]byte(`["model-b",{"id":"model-a"}]`))
	if err != nil || len(models) != 2 || models[0] != "model-a" {
		t.Fatalf("unexpected array catalog: %v err=%v", models, err)
	}
}

func TestExtractModelIDsRejectsAnUnboundedCatalog(t *testing.T) {
	var body strings.Builder
	body.WriteString(`{"data":[`)
	for index := 0; index <= maxDiscoveredModels; index++ {
		if index > 0 {
			body.WriteByte(',')
		}
		fmt.Fprintf(&body, `{"id":"model-%d"}`, index)
	}
	body.WriteString(`]}`)
	if _, err := extractModelIDs([]byte(body.String())); err == nil {
		t.Fatal("oversized model catalog reached the UI")
	}
}

func TestModelTestRejectsFailedApplicationResponseWithHTTP200(t *testing.T) {
	gateway := NewGateway(dispatcherFunc(func(context.Context, relayapp.DispatchRequest) (relayapp.DispatchResponse, error) {
		return relayapp.DispatchResponse{
			Status: http.StatusOK,
			Body:   []byte(`{"status":"failed","error":{"code":"model_unavailable"}}`),
		}, nil
	}), time.Second)
	result := gateway.Test(context.Background(), domain.Provider{ID: "provider", Dialect: "openai"}, "gpt-test")
	if result.State != "unavailable" || result.ErrorCode != "request_rejected" || result.Status != http.StatusOK {
		t.Fatalf("failed application response was reported available: %+v", result)
	}
}

func TestModelTestFallsBackWhenResponsesEndpointIsUnsupported(t *testing.T) {
	paths := make([]string, 0, 2)
	gateway := NewGateway(dispatcherFunc(func(_ context.Context, request relayapp.DispatchRequest) (relayapp.DispatchResponse, error) {
		paths = append(paths, request.Path)
		if request.Path == "/v1/responses" {
			return relayapp.DispatchResponse{Status: http.StatusBadRequest, Body: []byte(`{"error":"unsupported endpoint"}`)}, nil
		}
		return relayapp.DispatchResponse{Status: http.StatusOK, Body: []byte(`{"choices":[{"message":{"content":"OK"}}]}`)}, nil
	}), time.Second)
	result := gateway.Test(context.Background(), domain.Provider{ID: "provider", Dialect: "openai"}, "gpt-test")
	if result.State != "available" || len(paths) != 2 || paths[0] != "/v1/responses" || paths[1] != "/v1/chat/completions" {
		t.Fatalf("chat fallback was not used: result=%+v paths=%v", result, paths)
	}
}

func TestModelTestDoesNotRetryAnotherEndpointAfterAuthenticationFailure(t *testing.T) {
	calls := 0
	gateway := NewGateway(dispatcherFunc(func(context.Context, relayapp.DispatchRequest) (relayapp.DispatchResponse, error) {
		calls++
		return relayapp.DispatchResponse{Status: http.StatusUnauthorized, Body: []byte(`{"error":{"message":"unauthorized"}}`)}, nil
	}), time.Second)
	result := gateway.Test(context.Background(), domain.Provider{ID: "provider", Dialect: "openai"}, "gpt-test")
	if result.ErrorCode != "authentication" || calls != 1 {
		t.Fatalf("authentication failure was retried on another endpoint: result=%+v calls=%d", result, calls)
	}
}
