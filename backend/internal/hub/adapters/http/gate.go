package http

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/luxuryprivate/switchboard/backend/internal/hub/application"
)

type Gate struct{ service *application.Service }

func NewGate(service *application.Service) *Gate { return &Gate{service: service} }

func (gate *Gate) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	writer.Header()["Date"] = nil
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("Connection", "close")
	request.Close = true
	const prefix = "/authorize/"
	id := strings.TrimPrefix(request.URL.Path, prefix)
	ctx, cancel := context.WithTimeout(request.Context(), 2*time.Second)
	defer cancel()
	if request.Method == http.MethodGet && strings.HasPrefix(request.URL.Path, prefix) && !strings.Contains(id, "/") && gate.service.Running(ctx, id) {
		writer.Header().Set("Content-Length", "0")
		writer.WriteHeader(http.StatusNoContent)
		return
	}
	body := []byte(`{"error":"tunnel_unavailable"}` + "\n")
	writer.Header().Set("Content-Length", strconv.Itoa(len(body)))
	writer.WriteHeader(http.StatusServiceUnavailable)
	_, _ = writer.Write(body)
}
