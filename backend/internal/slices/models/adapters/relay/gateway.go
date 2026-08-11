package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/models/domain"
	relayapp "github.com/luxuryprivate/switchboard/backend/internal/slices/relay/application"
)

const (
	maxDiscoveredModels = 5_000
	maxProbeTimeout     = 30 * time.Second
)

type Gateway struct {
	dispatcher   relayapp.Dispatcher
	timeout      time.Duration
	probeTimeout time.Duration
}

func NewGateway(dispatcher relayapp.Dispatcher, timeout time.Duration) *Gateway {
	if timeout <= 0 {
		timeout = 90 * time.Second
	}
	return &Gateway{dispatcher: dispatcher, timeout: timeout, probeTimeout: min(timeout, maxProbeTimeout)}
}

func (gateway *Gateway) Discover(ctx context.Context, provider domain.Provider) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, gateway.timeout)
	defer cancel()
	response, err := gateway.dispatcher.Dispatch(ctx, relayapp.DispatchRequest{
		Method: http.MethodGet, Path: provider.ModelsPath, ProviderID: provider.ID,
		UpstreamModel: "__model_catalog__", AttemptLimit: 1,
		UseStoredCredential: true,
		Headers:             http.Header{"Accept": []string{"application/json"}},
	})
	if err != nil || response.Status < 200 || response.Status >= 300 {
		return nil, errors.New("model catalog is unavailable")
	}
	return extractModelIDs(response.Body)
}

func (gateway *Gateway) Test(ctx context.Context, provider domain.Provider, model string) domain.TestResult {
	started := time.Now()
	ctx, cancel := context.WithTimeout(ctx, gateway.probeTimeout)
	defer cancel()
	response, err := gateway.testRequest(ctx, provider, model)
	result := domain.TestResult{ProviderID: provider.ID, Model: model, LatencyMS: float64(time.Since(started).Microseconds()) / 1000}
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			result.State = "timeout"
			result.ErrorCode = "timeout"
		} else {
			result.State = "unavailable"
			result.ErrorCode = "transport"
		}
		return result
	}
	result.Status = response.Status
	if response.Status >= 200 && response.Status < 300 {
		if failedResponse(response.Body) {
			result.State = "unavailable"
			result.ErrorCode = "request_rejected"
			return result
		}
		result.State = "available"
		return result
	}
	result.State = "unavailable"
	result.ErrorCode = statusCode(response.Status)
	return result
}

func failedResponse(body []byte) bool {
	var payload map[string]any
	if len(body) == 0 || json.Unmarshal(body, &payload) != nil {
		return false
	}
	for _, candidate := range []map[string]any{payload, mapValue(payload["response"])} {
		status, _ := candidate["status"].(string)
		eventType, _ := candidate["type"].(string)
		if status == "failed" || status == "cancelled" || eventType == "error" || eventType == "response.failed" || candidate["error"] != nil {
			return true
		}
	}
	return false
}

func mapValue(value any) map[string]any {
	result, _ := value.(map[string]any)
	return result
}

func (gateway *Gateway) testRequest(ctx context.Context, provider domain.Provider, model string) (relayapp.DispatchResponse, error) {
	if provider.Dialect == "anthropic" {
		return gateway.dispatch(ctx, provider.ID, model, "/v1/messages", map[string]any{
			"model": model, "max_tokens": 8,
			"messages": []map[string]string{{"role": "user", "content": "Reply OK"}},
		})
	}
	response, err := gateway.dispatch(ctx, provider.ID, model, "/v1/responses", map[string]any{
		"model": model, "input": "Reply OK", "max_output_tokens": 8,
	})
	if err == nil && !responsesFallback(response) {
		return response, nil
	}
	return gateway.dispatch(ctx, provider.ID, model, "/v1/chat/completions", map[string]any{
		"model": model, "max_tokens": 8,
		"messages": []map[string]string{{"role": "user", "content": "Reply OK"}},
	})
}

func responsesFallback(response relayapp.DispatchResponse) bool {
	if response.Status >= 200 && response.Status < 300 && failedResponse(response.Body) {
		return true
	}
	switch response.Status {
	case http.StatusBadRequest, http.StatusNotFound, http.StatusMethodNotAllowed, http.StatusUnprocessableEntity, http.StatusNotImplemented:
		return true
	default:
		return false
	}
}

func (gateway *Gateway) dispatch(ctx context.Context, providerID, model, path string, payload any) (relayapp.DispatchResponse, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return relayapp.DispatchResponse{}, err
	}
	return gateway.dispatcher.Dispatch(ctx, relayapp.DispatchRequest{
		Method: http.MethodPost, Path: path, ProviderID: providerID,
		PublicModel: model, UpstreamModel: model, Body: body, AttemptLimit: 1,
		UseStoredCredential: true,
		Headers:             http.Header{"Content-Type": []string{"application/json"}, "Accept": []string{"application/json"}},
	})
}

func extractModelIDs(body []byte) ([]string, error) {
	var payload any
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(&payload); err != nil {
		return nil, errors.New("invalid model catalog")
	}
	var values []any
	switch root := payload.(type) {
	case map[string]any:
		values, _ = root["data"].([]any)
		if values == nil {
			values, _ = root["models"].([]any)
		}
	case []any:
		values = root
	}
	if values == nil {
		return nil, errors.New("model catalog is missing models")
	}
	seen := make(map[string]struct{}, len(values))
	models := make([]string, 0, len(values))
	for _, value := range values {
		model := ""
		switch item := value.(type) {
		case string:
			model = item
		case map[string]any:
			for _, key := range []string{"id", "model", "name"} {
				if candidate, valid := item[key].(string); valid {
					model = candidate
					break
				}
			}
		}
		model = strings.TrimSpace(model)
		if model == "" || len(model) > 128 || strings.ContainsAny(model, "\r\n\x00") {
			continue
		}
		if _, exists := seen[model]; exists {
			continue
		}
		if len(models) == maxDiscoveredModels {
			return nil, errors.New("model catalog exceeds the safety limit")
		}
		seen[model] = struct{}{}
		models = append(models, model)
	}
	if len(models) == 0 {
		return nil, errors.New("model catalog is empty")
	}
	sort.Strings(models)
	return models, nil
}

func statusCode(status int) string {
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return "authentication"
	case http.StatusNotFound:
		return "model_unavailable"
	case http.StatusTooManyRequests:
		return "rate_limited"
	default:
		if status >= 500 {
			return "provider_unavailable"
		}
		return "request_rejected"
	}
}
