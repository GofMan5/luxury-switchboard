package tunnelhttp

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	tunnelapp "github.com/luxuryprivate/switchboard/backend/internal/slices/publictunnel/application"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/publictunnel/domain"
	relayapp "github.com/luxuryprivate/switchboard/backend/internal/slices/relay/application"
)

const maxTunnelBody = 64 * 1024 * 1024

var allowedPaths = map[string]struct{}{"/v1/responses": {}, "/v1/chat/completions": {}, "/v1/completions": {}, "/v1/messages": {}, "/v1/images/generations": {}, "/v1/images/edits": {}}
var textPaths = map[string]struct{}{"/v1/responses": {}, "/v1/chat/completions": {}, "/v1/completions": {}, "/v1/messages": {}}

type Gateway struct {
	config   domain.Config
	routes   tunnelapp.Routes
	markers  tunnelapp.Markers
	relay    relayapp.Dispatcher
	limiter  *ipLimiter
	activity tunnelapp.ClientActivity
}

func NewGateway(config domain.Config, routes tunnelapp.Routes, markers tunnelapp.Markers, relay relayapp.Dispatcher, activity tunnelapp.ClientActivity) (*Gateway, error) {
	if len(config.Token) < 32 || routes == nil || markers == nil || relay == nil || config.RPMPerIP < 0 || config.ContextLimitKiB < 0 {
		return nil, errors.New("invalid tunnel gateway settings")
	}
	return &Gateway{config: config, routes: routes, markers: markers, relay: relay, limiter: newIPLimiter(), activity: activity}, nil
}

func (gateway *Gateway) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if !gateway.authorized(request) {
		gateway.error(writer, http.StatusUnauthorized, "Request rejected")
		return
	}
	path := request.URL.Path
	if request.Method == http.MethodGet && path == "/v1/models" {
		gateway.models(writer)
		return
	}
	if request.Method != http.MethodPost {
		gateway.error(writer, http.StatusMethodNotAllowed, "Request rejected")
		return
	}
	if _, ok := allowedPaths[path]; !ok || request.URL.RawQuery != "" {
		gateway.error(writer, http.StatusNotFound, "Request rejected")
		return
	}
	ip := clientIP(request)
	body, err := io.ReadAll(io.LimitReader(request.Body, maxTunnelBody+1))
	if err != nil || len(body) > maxTunnelBody {
		gateway.error(writer, http.StatusRequestEntityTooLarge, "Request rejected")
		return
	}
	model, payload, err := requestPayload(body, request.Header.Get("Content-Type"), path)
	if err != nil {
		gateway.error(writer, http.StatusBadRequest, "Request rejected")
		return
	}
	route, ok := gateway.routes.Resolve(model)
	if !ok {
		gateway.error(writer, http.StatusForbidden, "Request rejected")
		return
	}
	activityID := ""
	started := time.Now()
	if gateway.activity != nil {
		activityID = gateway.activity.Begin(tunnelapp.ClientStart{IP: ip, Method: request.Method, Path: path, PublicModel: route.PublicModel, BytesIn: int64(len(body))})
	}
	status := http.StatusBadGateway
	bytesOut := int64(0)
	errorCode := ""
	defer func() {
		if gateway.activity != nil {
			gateway.activity.Finish(activityID, tunnelapp.ClientFinish{Status: status, BytesOut: bytesOut, ErrorCode: errorCode, Duration: time.Since(started)})
		}
	}()
	limit := route.ContextLimitKiB
	if limit == 0 {
		limit = gateway.config.ContextLimitKiB
	}
	_, textRequest := textPaths[path]
	if textRequest && limit > 0 && len(body) > limit*1024 {
		gateway.error(writer, http.StatusRequestEntityTooLarge, "Request rejected")
		return
	}
	if payload != nil && providerProbe(payload, path) {
		clean, contentType := localBrandResponse(path, route.PublicModel, payload, gateway.config.BrandResponse)
		writer.Header().Set("Content-Type", contentType)
		writer.Header().Set("Cache-Control", "no-store")
		writer.Header().Set("Content-Length", strconv.Itoa(len(clean)))
		writer.WriteHeader(http.StatusOK)
		status, bytesOut = http.StatusOK, int64(len(clean))
		_, _ = writer.Write(clean)
		return
	}
	if wait := gateway.limiter.Reserve(ip, gateway.config.RPMPerIP); wait > 0 {
		if gateway.activity != nil {
			gateway.activity.Queue(ip, 1)
			defer gateway.activity.Queue(ip, -1)
		}
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case <-request.Context().Done():
			return
		case <-timer.C:
		}
	}
	if payload != nil {
		body = applyBrandPolicy(payload, path, gateway.config.BrandResponse)
	}
	response, err := gateway.relay.Dispatch(request.Context(), relayapp.DispatchRequest{Method: request.Method, Path: path, Headers: forwardHeaders(request.Header), Body: body, ProviderID: route.ProviderID, PublicModel: route.PublicModel, UpstreamModel: route.UpstreamModel})
	if err != nil {
		errorCode = "upstream_rejected"
		gateway.error(writer, http.StatusBadGateway, "Request could not be completed")
		return
	}
	markers := gateway.markers.SensitiveMarkers(route.ProviderID)
	if route.UpstreamModel != route.PublicModel {
		markers = append(markers, route.UpstreamModel)
	}
	clean, contentType, err := sanitizeResponse(response, path, route.PublicModel, markers, gateway.config.BrandResponse)
	if err != nil {
		errorCode = "unsafe_response"
		gateway.error(writer, http.StatusBadGateway, "Request could not be completed")
		return
	}
	writer.Header().Set("Content-Type", contentType)
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("Content-Length", strconv.Itoa(len(clean)))
	writer.WriteHeader(response.Status)
	status = response.Status
	bytesOut = int64(len(clean))
	_, _ = writer.Write(clean)
}

func requestPayload(body []byte, contentType, path string) (string, map[string]any, error) {
	if strings.Contains(strings.ToLower(contentType), "json") {
		var payload map[string]any
		if json.Unmarshal(body, &payload) != nil {
			return "", nil, errors.New("invalid JSON request")
		}
		model, _ := payload["model"].(string)
		model = strings.TrimSpace(model)
		if model == "" || len(model) > 128 {
			return "", nil, errors.New("invalid request model")
		}
		return model, payload, nil
	}
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil || path != "/v1/images/edits" || mediaType != "multipart/form-data" || params["boundary"] == "" {
		return "", nil, errors.New("unsupported request content type")
	}
	reader := multipart.NewReader(bytes.NewReader(body), params["boundary"])
	for range 128 {
		part, nextErr := reader.NextPart()
		if errors.Is(nextErr, io.EOF) {
			break
		}
		if nextErr != nil {
			return "", nil, errors.New("invalid multipart request")
		}
		if part.FormName() == "model" && part.FileName() == "" {
			value, _ := io.ReadAll(io.LimitReader(part, 129))
			model := strings.TrimSpace(string(value))
			if model != "" && len(model) <= 128 {
				return model, nil, nil
			}
			break
		}
	}
	return "", nil, errors.New("invalid multipart model")
}

func (gateway *Gateway) authorized(request *http.Request) bool {
	authorization := request.Header.Values("Authorization")
	apiKeys := request.Header.Values("X-Api-Key")
	if len(authorization)+len(apiKeys) != 1 {
		return false
	}
	token := ""
	if len(authorization) == 1 {
		if !strings.HasPrefix(authorization[0], "Bearer ") {
			return false
		}
		token = strings.TrimSpace(strings.TrimPrefix(authorization[0], "Bearer "))
	} else {
		token = strings.TrimSpace(apiKeys[0])
	}
	if len(token) != len(gateway.config.Token) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(token), []byte(gateway.config.Token)) == 1
}
func clientIP(request *http.Request) string {
	if values := request.Header.Values("X-Tunnel-Client-IP"); len(values) == 1 {
		if ip := net.ParseIP(strings.TrimSpace(values[0])); ip != nil && !ip.IsUnspecified() {
			return ip.String()
		}
	}
	host, _, err := net.SplitHostPort(request.RemoteAddr)
	if err == nil {
		return host
	}
	return "unknown"
}
func (gateway *Gateway) models(writer http.ResponseWriter) {
	routes := gateway.routes.List()
	models := make([]map[string]any, 0, len(routes))
	for _, route := range routes {
		models = append(models, map[string]any{"id": route.PublicModel, "object": "model", "created": 0})
	}
	body, _ := json.Marshal(map[string]any{"object": "list", "data": models})
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("Content-Length", strconv.Itoa(len(body)))
	_, _ = writer.Write(body)
}
func (gateway *Gateway) error(writer http.ResponseWriter, status int, message string) {
	body, _ := json.Marshal(map[string]string{"error": message})
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("Content-Length", strconv.Itoa(len(body)))
	writer.WriteHeader(status)
	_, _ = writer.Write(body)
}
func forwardHeaders(source http.Header) http.Header {
	target := make(http.Header)
	for _, name := range []string{"Content-Type", "Accept", "Anthropic-Version", "Anthropic-Beta", "User-Agent"} {
		for _, value := range source.Values(name) {
			target.Add(name, value)
		}
	}
	return target
}
func applyBrandPolicy(payload map[string]any, path, brand string) []byte {
	if brand == "" {
		encoded, _ := json.Marshal(payload)
		return encoded
	}
	instruction := "When asked about the provider or source, answer exactly: " + brand
	switch path {
	case "/v1/responses":
		if existing, ok := payload["instructions"].(string); ok && existing != "" {
			payload["instructions"] = existing + "\n\n" + instruction
		} else {
			payload["instructions"] = instruction
		}
	case "/v1/chat/completions":
		messages, _ := payload["messages"].([]any)
		payload["messages"] = append([]any{map[string]any{"role": "system", "content": instruction}}, messages...)
	case "/v1/messages":
		if existing, ok := payload["system"].(string); ok && existing != "" {
			payload["system"] = existing + "\n\n" + instruction
		} else {
			payload["system"] = instruction
		}
	}
	encoded, _ := json.Marshal(payload)
	return encoded
}
