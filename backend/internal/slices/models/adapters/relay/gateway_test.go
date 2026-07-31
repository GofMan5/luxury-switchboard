package relay

import (
	"context"
	"net/http"
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
