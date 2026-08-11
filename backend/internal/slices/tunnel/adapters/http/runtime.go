package tunnelhttp

import (
	"context"
	"errors"
	"fmt"
	gatewayhttp "github.com/luxuryprivate/switchboard/backend/internal/slices/publictunnel/adapters/http"
	tunnelapp "github.com/luxuryprivate/switchboard/backend/internal/slices/publictunnel/application"
	tunneldomain "github.com/luxuryprivate/switchboard/backend/internal/slices/publictunnel/domain"
	relayapp "github.com/luxuryprivate/switchboard/backend/internal/slices/relay/application"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/tunnel/domain"
	"net"
	"net/http"
	"sync"
	"time"
)

type Runtime struct {
	mu       sync.Mutex
	routes   tunnelapp.Routes
	markers  tunnelapp.Markers
	relay    relayapp.Dispatcher
	activity tunnelapp.ClientActivity
	bans     tunnelapp.Bans
	server   *http.Server
	listener net.Listener
}

func NewRuntime(routes tunnelapp.Routes, markers tunnelapp.Markers, relay relayapp.Dispatcher, activity tunnelapp.ClientActivity, bans tunnelapp.Bans) *Runtime {
	return &Runtime{routes: routes, markers: markers, relay: relay, activity: activity, bans: bans}
}
func (runtime *Runtime) Start(ctx context.Context, config domain.Config) (string, error) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if runtime.server != nil {
		return "", errors.New("tunnel already running")
	}
	gateway, err := gatewayhttp.NewGateway(tunneldomain.Config{Token: config.Token, RPMPerIP: config.RPMPerIP, ContextLimitKiB: config.ContextLimitKiB, BrandResponse: config.BrandResponse}, runtime.routes, runtime.markers, runtime.relay, runtime.activity, runtime.bans)
	if err != nil {
		return "", err
	}
	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", config.Port))
	if err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		_ = listener.Close()
		return "", err
	}
	server := &http.Server{Handler: gateway, ReadHeaderTimeout: 15 * time.Second, IdleTimeout: 90 * time.Second, MaxHeaderBytes: 64 * 1024}
	runtime.server = server
	runtime.listener = listener
	go func() { _ = server.Serve(listener) }()
	return "http://" + listener.Addr().String() + "/v1", nil
}
func (runtime *Runtime) Stop(ctx context.Context) error {
	runtime.mu.Lock()
	server := runtime.server
	runtime.mu.Unlock()
	if server == nil {
		return nil
	}
	if err := server.Shutdown(ctx); err != nil {
		if closeErr := server.Close(); closeErr != nil {
			return errors.Join(err, closeErr)
		}
	}
	runtime.mu.Lock()
	if runtime.server == server {
		runtime.server = nil
		runtime.listener = nil
	}
	runtime.mu.Unlock()
	return nil
}
