package relayhttp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	relayapp "github.com/luxuryprivate/switchboard/backend/internal/slices/relay/application"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/relay/domain"
)

const (
	absoluteMaxRequestBytes = 256 * 1024 * 1024
	maxProxyClients         = 64
)

var hopHeaders = map[string]struct{}{
	"connection": {}, "expect": {}, "host": {}, "keep-alive": {},
	"proxy-authenticate": {}, "proxy-authorization": {}, "proxy-connection": {},
	"te": {}, "trailer": {}, "transfer-encoding": {}, "upgrade": {},
}

var (
	errIncompleteSSE      = errors.New("upstream SSE ended without a terminal event")
	errClientDisconnected = errors.New("client disconnected")
)

type Server struct {
	address      string
	routes       relayapp.RouteSource
	credentials  relayapp.CredentialSource
	activity     relayapp.ActivitySink
	config       Config
	client       *http.Client
	transport    *http.Transport
	proxyClients map[string]*http.Client
	mu           sync.Mutex
	server       *http.Server
	listener     net.Listener
	routeCtx     context.Context
	cancelRoute  context.CancelFunc
}

type Dependencies struct {
	Routes      relayapp.RouteSource
	Credentials relayapp.CredentialSource
	Activity    relayapp.ActivitySink
	Config      Config
}

type Config struct {
	MaxRequestBytes       int64
	ResponseHeaderTimeout time.Duration
	StreamIdleTimeout     time.Duration
	RetryBase             time.Duration
	RetryMax              time.Duration
	PermanentAttempts     int
	HeartbeatInterval     time.Duration
}

func NewServer(address string, dependencies Dependencies) *Server {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.ForceAttemptHTTP2 = true
	transport.MaxIdleConns = 128
	transport.MaxIdleConnsPerHost = 32
	transport.IdleConnTimeout = 90 * time.Second
	config := dependencies.Config
	if config.MaxRequestBytes <= 0 || config.MaxRequestBytes > absoluteMaxRequestBytes {
		config.MaxRequestBytes = 64 * 1024 * 1024
	}
	if config.ResponseHeaderTimeout <= 0 {
		config.ResponseHeaderTimeout = 45 * time.Second
	}
	if config.StreamIdleTimeout <= 0 {
		config.StreamIdleTimeout = 60 * time.Second
	}
	if config.RetryBase <= 0 {
		config.RetryBase = 500 * time.Millisecond
	}
	if config.RetryMax <= 0 {
		config.RetryMax = 30 * time.Second
	}
	if config.PermanentAttempts < 1 {
		config.PermanentAttempts = 2
	}
	if config.HeartbeatInterval <= 0 {
		config.HeartbeatInterval = 15 * time.Second
	}
	transport.ResponseHeaderTimeout = config.ResponseHeaderTimeout
	ctx, cancel := context.WithCancel(context.Background())
	var activity relayapp.ActivitySink = relayapp.NoopActivity{}
	if dependencies.Activity != nil {
		activity = dependencies.Activity
	}
	return &Server{
		address:      address,
		routes:       dependencies.Routes,
		credentials:  dependencies.Credentials,
		activity:     activity,
		config:       config,
		client:       &http.Client{Transport: transport, CheckRedirect: rejectRedirect},
		transport:    transport,
		proxyClients: make(map[string]*http.Client),
		routeCtx:     ctx,
		cancelRoute:  cancel,
	}
}

func (server *Server) Start() (domain.Snapshot, error) {
	server.mu.Lock()
	defer server.mu.Unlock()
	if server.server != nil {
		return snapshot(server.listener.Addr()), nil
	}
	listener, err := net.Listen("tcp", server.address)
	if err != nil {
		return domain.Snapshot{}, err
	}
	httpServer := &http.Server{
		Handler:           server,
		ReadHeaderTimeout: 15 * time.Second,
		IdleTimeout:       90 * time.Second,
		MaxHeaderBytes:    64 * 1024,
	}
	server.listener = listener
	server.server = httpServer
	go func() {
		_ = httpServer.Serve(listener)
	}()
	return snapshot(listener.Addr()), nil
}

func (server *Server) Stop(ctx context.Context) error {
	server.mu.Lock()
	httpServer := server.server
	server.server = nil
	server.listener = nil
	server.mu.Unlock()
	if httpServer == nil {
		return nil
	}
	if err := httpServer.Shutdown(ctx); err != nil {
		if closeErr := httpServer.Close(); closeErr != nil {
			return errors.Join(err, closeErr)
		}
	}
	return nil
}

func (server *Server) CancelActive() {
	server.mu.Lock()
	server.cancelRoute()
	server.routeCtx, server.cancelRoute = context.WithCancel(context.Background())
	server.mu.Unlock()
}

func (server *Server) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	ctx, cancel := server.requestContext(request.Context())
	defer cancel()
	body, err := readRequestBody(request, server.config.MaxRequestBytes)
	if err != nil {
		writeError(writer, http.StatusRequestEntityTooLarge, "Request body is too large")
		return
	}
	model := requestModel(body, request.Header.Get("Content-Type"))
	route, err := server.routes.Current(ctx, model)
	if err != nil {
		writeError(writer, http.StatusServiceUnavailable, "Relay route is unavailable")
		return
	}
	dispatchPath, preparedBody, imageCompat, imageErr := prepareImageRequest(request.Method, request.URL.Path, body, request.Header.Get("Content-Type"), route.UpstreamModel)
	if imageErr != nil {
		writeError(writer, http.StatusBadRequest, "Image generation request is invalid")
		return
	}
	upstreamRequest := request
	if imageCompat {
		body = preparedBody
		clone := new(http.Request)
		*clone = *request
		targetURL := *request.URL
		targetURL.Path = dispatchPath
		targetURL.RawPath = ""
		clone.URL = &targetURL
		upstreamRequest = clone
	} else if route.UpstreamModel != "" && route.UpstreamModel != model {
		body = rewriteRequestModel(body, request.Header.Get("Content-Type"), route.UpstreamModel)
	}
	if route.CacheTTL >= time.Hour {
		body = extendCacheTTL(body, request.Header.Get("Content-Type"))
	}
	activityID := server.activity.Begin(relayapp.ActivityStart{
		Model: model, ProviderID: route.ProviderID, ProviderName: route.ProviderName,
		Method: request.Method, Path: request.URL.Path, BytesIn: int64(len(body)),
	})
	status := 0
	bytesOut := int64(0)
	cancelled := false
	errorCode := ""
	usage := relayapp.TokenUsage{}
	generation := time.Duration(0)
	defer func() {
		server.activity.Finish(activityID, relayapp.ActivityFinish{
			Status: status, BytesOut: bytesOut, Cancelled: cancelled, ErrorCode: errorCode,
			Usage: usage, Generation: generation,
		})
	}()
	committed := false
	flusher, _ := writer.(http.Flusher)
	var heartbeat func() error
	clientStream := requiresStreamTerminal(request, body) && !imageCompat
	bufferTerminal := clientStream || imageCompat
	if clientStream {
		writer.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		writer.Header().Set("Cache-Control", "no-store")
		writer.Header().Set("Connection", "close")
		writer.WriteHeader(http.StatusOK)
		if flusher != nil {
			flusher.Flush()
		}
		committed = true
		heartbeat = func() error {
			if _, err := writer.Write([]byte(": switchboard keep-alive\n\n")); err != nil {
				return errClientDisconnected
			}
			if flusher != nil {
				flusher.Flush()
			}
			return nil
		}
	}
	requestUpstream := func(requestCtx context.Context) (*http.Response, error) {
		return server.requestWithRetry(requestCtx, upstreamRequest, body, route, activityID, bufferTerminal, 0, nil)
	}
	var response *http.Response
	if clientStream {
		response, err = server.withHeartbeat(ctx, heartbeat, requestUpstream)
	} else {
		response, err = requestUpstream(ctx)
	}
	if err != nil {
		if committed {
			cancelled = errors.Is(err, context.Canceled) || errors.Is(err, errClientDisconnected)
			errorCode = "stream_incomplete"
			writeStreamFailure(writer, request.URL.Path, model)
			return
		}
		if errors.Is(err, context.Canceled) {
			status = http.StatusServiceUnavailable
			cancelled = true
			errorCode = "cancelled"
			writeError(writer, http.StatusServiceUnavailable, "Request cancelled")
		} else {
			status = http.StatusBadGateway
			errorCode = "transport"
			writeError(writer, http.StatusBadGateway, "Provider is unavailable")
		}
		return
	}
	defer response.Body.Close()
	status = response.StatusCode
	if status >= 400 {
		errorCode = "request_rejected"
		if committed {
			writeStreamFailure(writer, request.URL.Path, model)
			return
		}
	}
	terminal := response.Header.Get("X-Switchboard-Terminal")
	response.Header.Del("X-Switchboard-Terminal")
	if terminal == "response.incomplete" {
		errorCode = "stream_incomplete"
	}
	if terminal == "response.failed" {
		errorCode = "upstream_status"
	}
	usage = usageFromHeaders(response.Header)
	generation = durationHeader(response.Header, "X-Switchboard-Generation-Nanoseconds")
	removeUsageHeaders(response.Header)
	if imageCompat {
		raw, readErr := io.ReadAll(io.LimitReader(response.Body, maxImageResponse+1))
		converted, convertErr := imagesResponse(raw)
		if readErr != nil || len(raw) > maxImageResponse || convertErr != nil {
			status = http.StatusBadGateway
			errorCode = "image_generation"
			writeError(writer, http.StatusBadGateway, "Image generation failed")
			return
		}
		response.Body = io.NopCloser(bytes.NewReader(converted))
		response.ContentLength = int64(len(converted))
		response.Header.Set("Content-Type", "application/json")
		response.Header.Set("Content-Length", strconv.Itoa(len(converted)))
		response.Header.Del("Content-Encoding")
	}
	if !committed {
		copyResponseHeaders(writer.Header(), response.Header)
		writer.WriteHeader(response.StatusCode)
	}
	buffer := make([]byte, 64*1024)
	for {
		count, readErr := response.Body.Read(buffer)
		if count > 0 {
			if _, writeErr := writer.Write(buffer[:count]); writeErr != nil {
				cancelled = true
				errorCode = "client_disconnected"
				return
			}
			bytesOut += int64(count)
			if flusher != nil {
				flusher.Flush()
			}
		}
		if errors.Is(readErr, io.EOF) {
			return
		}
		if readErr != nil {
			errorCode = "stream_incomplete"
			return
		}
	}
}

func (server *Server) Dispatch(ctx context.Context, request relayapp.DispatchRequest) (relayapp.DispatchResponse, error) {
	ctx, cancel := server.requestContext(ctx)
	defer cancel()
	route, err := server.routes.Pinned(ctx, request.ProviderID, request.UpstreamModel)
	if err != nil {
		return relayapp.DispatchResponse{}, err
	}
	path, body, imageCompat, err := prepareImageRequest(request.Method, request.Path, request.Body, request.Headers.Get("Content-Type"), request.UpstreamModel)
	if err != nil {
		return relayapp.DispatchResponse{}, err
	}
	if !imageCompat {
		body = rewriteRequestModel(request.Body, request.Headers.Get("Content-Type"), request.UpstreamModel)
	}
	if route.CacheTTL >= time.Hour {
		body = extendCacheTTL(body, request.Headers.Get("Content-Type"))
	}
	urlValue, err := url.Parse(path)
	if err != nil || !strings.HasPrefix(urlValue.Path, "/") {
		return relayapp.DispatchResponse{}, errors.New("invalid dispatch path")
	}
	incoming := &http.Request{Method: request.Method, URL: urlValue, Header: request.Headers.Clone()}
	terminalStream := requiresStreamTerminal(incoming, body)
	activityID := server.activity.Begin(relayapp.ActivityStart{
		Model: request.PublicModel, ProviderID: route.ProviderID,
		ProviderName: route.ProviderName, Method: request.Method,
		Path: urlValue.Path, BytesIn: int64(len(body)),
	})
	markers := make([]string, 0, 4)
	response, err := server.requestWithRetry(ctx, incoming, body, route, activityID, terminalStream, request.AttemptLimit, func(credential relayapp.Credential) {
		markers = sensitiveCredentialMarkers(credential)
	})
	if err != nil {
		server.activity.Finish(activityID, relayapp.ActivityFinish{Cancelled: errors.Is(err, context.Canceled), ErrorCode: "transport"})
		return relayapp.DispatchResponse{}, err
	}
	defer response.Body.Close()
	limit := min(max(server.config.MaxRequestBytes*4, 8*1024*1024), int64(absoluteMaxRequestBytes))
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil || int64(len(responseBody)) > limit {
		server.activity.Finish(activityID, relayapp.ActivityFinish{Status: response.StatusCode, ErrorCode: "stream_incomplete"})
		return relayapp.DispatchResponse{}, errIncompleteSSE
	}
	if imageCompat {
		responseBody, err = imagesResponse(responseBody)
		if err != nil {
			server.activity.Finish(activityID, relayapp.ActivityFinish{Status: http.StatusBadGateway, ErrorCode: "image_generation"})
			return relayapp.DispatchResponse{}, err
		}
		response.Header.Set("Content-Type", "application/json")
		response.Header.Set("Content-Length", strconv.Itoa(len(responseBody)))
		response.Header.Del("Content-Encoding")
	}
	terminal := response.Header.Get("X-Switchboard-Terminal")
	usage := usageFromHeaders(response.Header)
	generation := durationHeader(response.Header, "X-Switchboard-Generation-Nanoseconds")
	removeUsageHeaders(response.Header)
	response.Header.Del("X-Switchboard-Terminal")
	dispatchError := ""
	if response.StatusCode >= 400 {
		dispatchError = "request_rejected"
	}
	server.activity.Finish(activityID, relayapp.ActivityFinish{
		Status: response.StatusCode, BytesOut: int64(len(responseBody)),
		Usage: usage, Generation: generation, ErrorCode: dispatchError,
	})
	return relayapp.DispatchResponse{
		Status: response.StatusCode, Headers: response.Header.Clone(), Body: responseBody,
		Terminal: terminal, SensitiveMarkers: markers,
	}, nil
}

func (server *Server) requestWithRetry(ctx context.Context, incoming *http.Request, body []byte, route relayapp.Route, activityID string, terminalStream bool, attemptLimit int, onCredential func(relayapp.Credential)) (*http.Response, error) {
	requestFailures := 0
	model := requestModel(body, incoming.Header.Get("Content-Type"))
	for attempt := 0; ; attempt++ {
		lease, credential, waited, err := server.acquireCredential(ctx, route, model, func() { server.activity.Waiting(activityID) })
		if err != nil {
			return nil, err
		}
		if onCredential != nil {
			onCredential(credential)
		}
		server.activity.Resume(activityID, waited)
		upstream, err := buildUpstreamRequest(ctx, incoming, body, route, credential.Value)
		if err != nil {
			finishLease(lease, relayapp.AttemptOutcome{Kind: relayapp.AttemptRequestError})
			return nil, err
		}
		client, err := server.clientForProxy(credential.ProxyURL)
		if err != nil {
			finishLease(lease, relayapp.AttemptOutcome{Kind: relayapp.AttemptRequestError})
			return nil, err
		}
		attemptStarted := time.Now()
		response, err := client.Do(upstream)
		if err != nil {
			finishLease(lease, relayapp.AttemptOutcome{Kind: relayapp.AttemptTransport})
			if !canRetry(attempt, attemptLimit) {
				return nil, err
			}
			delay := retryDelay(attempt, nil, server.config)
			server.observeRetry(activityID, attempt, 0, delay)
			if err := waitRetry(ctx, delay); err != nil {
				return nil, err
			}
			continue
		}
		status := response.StatusCode
		if status >= 200 && status < 400 {
			if status >= 300 {
				finishLease(lease, relayapp.AttemptOutcome{Kind: relayapp.AttemptRequestError})
				drainResponse(response)
				return genericErrorResponse(http.StatusBadGateway), nil
			}
			if terminalStream {
				terminal, buffered, streamUsage, bufferErr := bufferTerminalSSE(ctx, response, incoming.URL.Path, server.config)
				if bufferErr != nil {
					finishLease(lease, relayapp.AttemptOutcome{Kind: relayapp.AttemptTransport})
					if errors.Is(bufferErr, errClientDisconnected) || errors.Is(bufferErr, context.Canceled) {
						return nil, bufferErr
					}
					if !canRetry(attempt, attemptLimit) {
						return nil, bufferErr
					}
					delay := retryDelay(attempt, nil, server.config)
					server.observeRetry(activityID, attempt, 0, delay)
					if err := waitRetry(ctx, delay); err != nil {
						return nil, err
					}
					continue
				}
				kind := relayapp.AttemptSuccess
				if terminal == "response.failed" || terminal == "response.incomplete" {
					kind = relayapp.AttemptRequestError
				}
				finishLease(lease, relayapp.AttemptOutcome{Kind: kind})
				response.Body = io.NopCloser(bytes.NewReader(buffered))
				response.ContentLength = -1
				response.Header.Del("Content-Length")
				response.Header.Del("Content-Encoding")
				response.Header.Set("X-Switchboard-Terminal", terminal)
				setUsageHeaders(response.Header, streamUsage, time.Since(attemptStarted))
				return response, nil
			}
			if strings.Contains(strings.ToLower(response.Header.Get("Content-Type")), "json") {
				buffered, bufferErr := bufferJSONResponse(ctx, response, server.config)
				if bufferErr != nil {
					finishLease(lease, relayapp.AttemptOutcome{Kind: relayapp.AttemptTransport})
					if !canRetry(attempt, attemptLimit) {
						return nil, bufferErr
					}
					delay := retryDelay(attempt, nil, server.config)
					server.observeRetry(activityID, attempt, 0, delay)
					if err := waitRetry(ctx, delay); err != nil {
						return nil, err
					}
					continue
				}
				terminal := jsonTerminal(buffered)
				kind := relayapp.AttemptSuccess
				if terminal != "" {
					kind = relayapp.AttemptRequestError
					response.Header.Set("X-Switchboard-Terminal", terminal)
				}
				finishLease(lease, relayapp.AttemptOutcome{Kind: kind})
				response.Body = io.NopCloser(bytes.NewReader(buffered))
				response.ContentLength = int64(len(buffered))
				response.Header.Set("Content-Length", strconv.Itoa(len(buffered)))
				response.Header.Del("Content-Encoding")
				setUsageHeaders(response.Header, usageFromJSON(buffered), time.Since(attemptStarted))
				return response, nil
			}
			finishLease(lease, relayapp.AttemptOutcome{Kind: relayapp.AttemptSuccess})
			return response, nil
		}

		if status == http.StatusTooManyRequests || status == 529 {
			delay := retryDelay(attempt, response, server.config)
			finishLease(lease, relayapp.AttemptOutcome{
				Kind: relayapp.AttemptRateLimited, RetryAfter: delay,
			})
			drainResponse(response)
			if !canRetry(attempt, attemptLimit) {
				return genericErrorResponse(status), nil
			}
			if err := server.retryCredentialFailure(ctx, lease, activityID, attempt, status, delay); err != nil {
				return nil, err
			}
			continue
		}

		if status == http.StatusRequestTimeout || status >= 500 {
			delay := retryDelay(attempt, response, server.config)
			finishLease(lease, relayapp.AttemptOutcome{Kind: relayapp.AttemptServerError})
			drainResponse(response)
			if !canRetry(attempt, attemptLimit) {
				return genericErrorResponse(status), nil
			}
			server.observeRetry(activityID, attempt, status, delay)
			if err := waitRetry(ctx, delay); err != nil {
				return nil, err
			}
			continue
		}

		errorBody := readErrorBody(response)
		switch {
		case balanceUnavailable(status, errorBody):
			finishLease(lease, relayapp.AttemptOutcome{Kind: relayapp.AttemptBalanceExhausted})
			if !canRetry(attempt, attemptLimit) {
				return genericErrorResponse(status), nil
			}
			if err := server.retryCredentialFailure(ctx, lease, activityID, attempt, status, retryDelay(attempt, nil, server.config)); err != nil {
				return nil, err
			}
			continue
		case status == http.StatusUnauthorized || status == http.StatusForbidden:
			finishLease(lease, relayapp.AttemptOutcome{Kind: relayapp.AttemptAuthentication})
			if !canRetry(attempt, attemptLimit) {
				return genericErrorResponse(status), nil
			}
			if err := server.retryCredentialFailure(ctx, lease, activityID, attempt, status, retryDelay(attempt, nil, server.config)); err != nil {
				return nil, err
			}
			continue
		case status == http.StatusNotFound && modelUnavailable(errorBody, model):
			finishLease(lease, relayapp.AttemptOutcome{Kind: relayapp.AttemptModelUnavailable, Model: model})
			if !canRetry(attempt, attemptLimit) {
				return genericErrorResponse(status), nil
			}
			if err := server.retryCredentialFailure(ctx, lease, activityID, attempt, status, retryDelay(attempt, nil, server.config)); err != nil {
				return nil, err
			}
			continue
		}

		finishLease(lease, relayapp.AttemptOutcome{Kind: relayapp.AttemptRequestError})
		if status == http.StatusRequestEntityTooLarge {
			return genericErrorResponse(status), nil
		}
		if status == 400 || status == 404 || status == 409 || status == 422 {
			requestFailures++
			if requestFailures < server.config.PermanentAttempts && canRetry(attempt, attemptLimit) {
				server.observeRetry(activityID, attempt, status, 0)
				continue
			}
		}
		return genericErrorResponse(status), nil
	}
}

func sensitiveCredentialMarkers(credential relayapp.Credential) []string {
	markers := make([]string, 0, 5)
	if credential.Value != "" {
		markers = append(markers, credential.Value)
	}
	if credential.ProxyURL == "" {
		return markers
	}
	markers = append(markers, credential.ProxyURL)
	proxy, err := url.Parse(credential.ProxyURL)
	if err != nil {
		return markers
	}
	markers = append(markers, proxy.Hostname())
	if proxy.User != nil {
		markers = append(markers, proxy.User.Username())
		if password, configured := proxy.User.Password(); configured {
			markers = append(markers, password)
		}
	}
	return markers
}

func (server *Server) retryCredentialFailure(ctx context.Context, lease relayapp.CredentialLease, activityID string, attempt, status int, delay time.Duration) error {
	// Credentialed routes delegate waiting to the scheduler so another key can
	// take over immediately. Passthrough routes have no scheduler and must back off.
	if lease != nil {
		delay = 0
	}
	server.observeRetry(activityID, attempt, status, delay)
	return waitRetry(ctx, delay)
}

func (server *Server) withHeartbeat(ctx context.Context, heartbeat func() error, operation func(context.Context) (*http.Response, error)) (*http.Response, error) {
	requestCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() {
		ticker := time.NewTicker(server.config.HeartbeatInterval)
		defer ticker.Stop()
		for {
			select {
			case <-requestCtx.Done():
				done <- nil
				return
			case <-ticker.C:
				if err := heartbeat(); err != nil {
					done <- err
					cancel()
					return
				}
			}
		}
	}()
	response, err := operation(requestCtx)
	cancel()
	heartbeatErr := <-done
	if heartbeatErr != nil {
		drainResponse(response)
		return nil, heartbeatErr
	}
	return response, err
}

func canRetry(attempt, limit int) bool {
	return limit <= 0 || attempt+1 < limit
}

func (server *Server) acquireCredential(ctx context.Context, route relayapp.Route, model string, waiting func()) (relayapp.CredentialLease, relayapp.Credential, time.Duration, error) {
	if route.AuthMode == "passthrough" {
		return nil, relayapp.Credential{}, 0, nil
	}
	if server.credentials == nil {
		return nil, relayapp.Credential{}, 0, errors.New("provider credential is unavailable")
	}
	lease, waited, err := server.credentials.Acquire(ctx, route.ProviderID, model, waiting)
	if err != nil {
		return nil, relayapp.Credential{}, waited, err
	}
	return lease, lease.Credential(), waited, nil
}

func (server *Server) clientForProxy(rawURL string) (*http.Client, error) {
	if rawURL == "" {
		return server.client, nil
	}
	proxyURL, err := url.Parse(rawURL)
	if err != nil || proxyURL.Host == "" || (proxyURL.Scheme != "http" && proxyURL.Scheme != "https" && proxyURL.Scheme != "socks5" && proxyURL.Scheme != "socks5h") {
		return nil, errors.New("invalid credential proxy")
	}
	server.mu.Lock()
	defer server.mu.Unlock()
	if client := server.proxyClients[rawURL]; client != nil {
		return client, nil
	}
	if len(server.proxyClients) >= maxProxyClients {
		// ponytail: proxy edits are rare, so a bounded full eviction is simpler
		// and safer than retaining an LRU plus idle sockets forever.
		for key, client := range server.proxyClients {
			client.CloseIdleConnections()
			delete(server.proxyClients, key)
		}
	}
	transport := server.transport.Clone()
	transport.Proxy = http.ProxyURL(proxyURL)
	client := &http.Client{Transport: transport, CheckRedirect: rejectRedirect}
	server.proxyClients[rawURL] = client
	return client, nil
}

func rejectRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

func (server *Server) observeRetry(activityID string, attempt, status int, delay time.Duration) {
	server.activity.Retry(activityID, relayapp.ActivityRetry{
		Attempt: attempt + 1, Status: status, Delay: delay,
	})
}

func (server *Server) requestContext(client context.Context) (context.Context, context.CancelFunc) {
	server.mu.Lock()
	routeCtx := server.routeCtx
	server.mu.Unlock()
	ctx, cancel := context.WithCancel(client)
	stop := context.AfterFunc(routeCtx, cancel)
	return ctx, func() {
		stop()
		cancel()
	}
}

func buildUpstreamRequest(ctx context.Context, incoming *http.Request, body []byte, route relayapp.Route, credential string) (*http.Request, error) {
	target := *route.BaseURL
	target.Path = joinPath(target.Path, incoming.URL.Path)
	target.RawQuery = joinQuery(target.RawQuery, incoming.URL.RawQuery)
	request, err := http.NewRequestWithContext(ctx, incoming.Method, target.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	copyRequestHeaders(request.Header, incoming.Header)
	switch route.AuthMode {
	case "auto":
		request.Header.Del("Authorization")
		request.Header.Del("x-api-key")
		path := strings.TrimRight(request.URL.Path, "/")
		anthropic := path == "/v1/messages" || strings.HasSuffix(path, "/v1/messages") || strings.Contains(path, "/v1/messages/") || request.Header.Get("Anthropic-Version") != ""
		if credential != "" && anthropic {
			request.Header.Set("x-api-key", credential)
		} else if credential != "" {
			request.Header.Set("Authorization", "Bearer "+credential)
		}
	case "bearer":
		request.Header.Del("x-api-key")
		if credential != "" {
			request.Header.Set("Authorization", "Bearer "+credential)
		}
	case "x-api-key":
		request.Header.Del("Authorization")
		if credential != "" {
			request.Header.Set("x-api-key", credential)
		}
	case "custom":
		request.Header.Del("Authorization")
		request.Header.Del("x-api-key")
		if credential != "" && route.AuthHeader != "" {
			request.Header.Set(route.AuthHeader, credential)
		}
	}
	request.Host = route.BaseURL.Host
	request.Header.Set("Accept-Encoding", "identity")
	return request, nil
}

func readRequestBody(request *http.Request, maxRequestBytes int64) ([]byte, error) {
	if request.Body == nil {
		return nil, nil
	}
	defer request.Body.Close()
	body, err := io.ReadAll(io.LimitReader(request.Body, maxRequestBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > maxRequestBytes {
		return nil, errors.New("request body too large")
	}
	return body, nil
}

func requiresStreamTerminal(request *http.Request, body []byte) bool {
	if request.Method != http.MethodPost {
		return false
	}
	path := strings.TrimRight(request.URL.Path, "/")
	if path != "/v1/responses" && path != "/v1/chat/completions" && path != "/v1/completions" && path != "/v1/messages" {
		return false
	}
	var payload struct {
		Stream bool `json:"stream"`
	}
	return json.Unmarshal(body, &payload) == nil && payload.Stream
}

type bodyRead struct {
	chunk []byte
	err   error
}

func bufferTerminalSSE(ctx context.Context, response *http.Response, path string, config Config) (string, []byte, relayapp.TokenUsage, error) {
	if !strings.Contains(strings.ToLower(response.Header.Get("Content-Type")), "text/event-stream") {
		drainResponse(response)
		return "", nil, relayapp.TokenUsage{}, errIncompleteSSE
	}
	defer response.Body.Close()
	readCtx, cancelRead := context.WithCancel(ctx)
	defer cancelRead()
	reads := make(chan bodyRead, 1)
	go func() {
		buffer := make([]byte, 64*1024)
		for {
			count, err := response.Body.Read(buffer)
			value := bodyRead{err: err}
			if count > 0 {
				value.chunk = append([]byte(nil), buffer[:count]...)
			}
			select {
			case reads <- value:
			case <-readCtx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()
	idle := time.NewTimer(config.StreamIdleTimeout)
	defer idle.Stop()
	limit := max(config.MaxRequestBytes*4, 8*1024*1024)
	limit = min(limit, int64(absoluteMaxRequestBytes))
	buffered := make([]byte, 0, min(limit, 1024*1024))
	inspector := sseInspector{path: strings.TrimRight(path, "/")}
	for {
		select {
		case <-ctx.Done():
			response.Body.Close()
			return "", nil, relayapp.TokenUsage{}, ctx.Err()
		case <-idle.C:
			response.Body.Close()
			return "", nil, relayapp.TokenUsage{}, errIncompleteSSE
		case result := <-reads:
			if len(result.chunk) > 0 {
				if int64(len(buffered))+int64(len(result.chunk)) > limit {
					response.Body.Close()
					return "", nil, relayapp.TokenUsage{}, errIncompleteSSE
				}
				buffered = append(buffered, result.chunk...)
				if terminal := inspector.Feed(result.chunk); terminal != "" {
					if terminal == "response.invalid" {
						return "", nil, relayapp.TokenUsage{}, errIncompleteSSE
					}
					return terminal, buffered, inspector.usage, nil
				}
				if !idle.Stop() {
					select {
					case <-idle.C:
					default:
					}
				}
				idle.Reset(config.StreamIdleTimeout)
			}
			if result.err != nil {
				if terminal := inspector.Finish(); terminal != "" {
					if terminal == "response.invalid" {
						return "", nil, relayapp.TokenUsage{}, errIncompleteSSE
					}
					return terminal, buffered, inspector.usage, nil
				}
				return "", nil, relayapp.TokenUsage{}, errIncompleteSSE
			}
		}
	}
}

func bufferJSONResponse(ctx context.Context, response *http.Response, config Config) ([]byte, error) {
	defer response.Body.Close()
	readCtx, cancelRead := context.WithCancel(ctx)
	defer cancelRead()
	reads := make(chan bodyRead, 1)
	go func() {
		buffer := make([]byte, 64*1024)
		for {
			count, err := response.Body.Read(buffer)
			result := bodyRead{err: err}
			if count > 0 {
				result.chunk = append([]byte(nil), buffer[:count]...)
			}
			select {
			case reads <- result:
			case <-readCtx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()
	idle := time.NewTimer(config.StreamIdleTimeout)
	defer idle.Stop()
	limit := min(max(config.MaxRequestBytes*4, 8*1024*1024), int64(absoluteMaxRequestBytes))
	buffered := make([]byte, 0, min(limit, 1024*1024))
	for {
		select {
		case <-ctx.Done():
			response.Body.Close()
			return nil, ctx.Err()
		case <-idle.C:
			response.Body.Close()
			return nil, errIncompleteSSE
		case result := <-reads:
			if int64(len(buffered))+int64(len(result.chunk)) > limit {
				response.Body.Close()
				return nil, errIncompleteSSE
			}
			buffered = append(buffered, result.chunk...)
			if len(result.chunk) > 0 {
				if !idle.Stop() {
					select {
					case <-idle.C:
					default:
					}
				}
				idle.Reset(config.StreamIdleTimeout)
			}
			if result.err != nil {
				if !errors.Is(result.err, io.EOF) || len(buffered) == 0 || !json.Valid(buffered) {
					return nil, errIncompleteSSE
				}
				return buffered, nil
			}
		}
	}
}

type sseInspector struct {
	path     string
	line     []byte
	event    []byte
	terminal string
	usage    relayapp.TokenUsage
}

func (inspector *sseInspector) Feed(chunk []byte) string {
	inspector.line = append(inspector.line, chunk...)
	for {
		newline := bytes.IndexByte(inspector.line, '\n')
		if newline < 0 {
			break
		}
		line := append([]byte(nil), inspector.line[:newline]...)
		inspector.line = inspector.line[newline+1:]
		inspector.consumeLine(bytes.TrimSuffix(line, []byte{'\r'}))
		if inspector.terminal != "" {
			return inspector.terminal
		}
	}
	return ""
}

func (inspector *sseInspector) Finish() string {
	if len(inspector.line) > 0 {
		inspector.consumeLine(bytes.TrimSuffix(inspector.line, []byte{'\r'}))
		inspector.line = nil
	}
	inspector.finishEvent()
	return inspector.terminal
}

func (inspector *sseInspector) consumeLine(line []byte) {
	if len(line) == 0 {
		inspector.finishEvent()
		return
	}
	if !bytes.HasPrefix(line, []byte("data:")) {
		return
	}
	data := bytes.TrimPrefix(line, []byte("data:"))
	data = bytes.TrimPrefix(data, []byte{' '})
	if len(inspector.event) > 0 {
		inspector.event = append(inspector.event, '\n')
	}
	inspector.event = append(inspector.event, data...)
}

func (inspector *sseInspector) finishEvent() {
	if len(inspector.event) == 0 || inspector.terminal != "" {
		inspector.event = nil
		return
	}
	data := inspector.event
	inspector.event = nil
	if bytes.Equal(data, []byte("[DONE]")) {
		if inspector.path == "/v1/chat/completions" || inspector.path == "/v1/completions" {
			inspector.terminal = "done"
		}
		return
	}
	var payload map[string]any
	if json.Unmarshal(data, &payload) != nil {
		return
	}
	inspector.mergeUsage(payload)
	eventType, _ := payload["type"].(string)
	if eventType == "response.completed" {
		response, ok := payload["response"].(map[string]any)
		if !ok {
			inspector.terminal = "response.invalid"
			return
		}
		if response["status"] != "completed" || response["incomplete_details"] != nil || response["error"] != nil {
			inspector.terminal = "response.invalid"
			return
		}
	}
	if eventType == "response.completed" || eventType == "response.failed" || eventType == "response.incomplete" {
		inspector.terminal = eventType
	}
	if eventType == "message_stop" && inspector.path == "/v1/messages" {
		inspector.terminal = eventType
	}
}

func (inspector *sseInspector) mergeUsage(payload map[string]any) {
	candidates := []map[string]any{payload}
	for _, field := range []string{"response", "message", "delta"} {
		if value, ok := payload[field].(map[string]any); ok {
			candidates = append(candidates, value)
		}
	}
	for _, candidate := range candidates {
		usage, ok := candidate["usage"].(map[string]any)
		if !ok {
			continue
		}
		input := positiveInt(usage["input_tokens"])
		if input == 0 {
			input = positiveInt(usage["prompt_tokens"])
		}
		output := positiveInt(usage["output_tokens"])
		if output == 0 {
			output = positiveInt(usage["completion_tokens"])
		}
		cacheRead := positiveInt(usage["cache_read_input_tokens"])
		cacheWrite := positiveInt(usage["cache_creation_input_tokens"])
		cached := positiveInt(usage["cached_tokens"])
		reasoning := positiveInt(usage["reasoning_tokens"])
		if details, ok := usage["input_tokens_details"].(map[string]any); ok {
			cached = max(cached, positiveInt(details["cached_tokens"]))
		}
		if details, ok := usage["prompt_tokens_details"].(map[string]any); ok {
			cached = max(cached, positiveInt(details["cached_tokens"]))
		}
		if details, ok := usage["output_tokens_details"].(map[string]any); ok {
			reasoning = max(reasoning, positiveInt(details["reasoning_tokens"]))
		}
		if details, ok := usage["completion_tokens_details"].(map[string]any); ok {
			reasoning = max(reasoning, positiveInt(details["reasoning_tokens"]))
		}
		inputTotal := input
		if cacheRead > 0 || cacheWrite > 0 {
			inputTotal = input + cacheRead + cacheWrite
			cached = max(cached, cacheRead)
		}
		inspector.usage.InputTokens = max(inspector.usage.InputTokens, inputTotal)
		inspector.usage.OutputTokens = max(inspector.usage.OutputTokens, output)
		inspector.usage.CachedTokens = min(max(inspector.usage.CachedTokens, cached), inspector.usage.InputTokens)
		inspector.usage.ReasoningTokens = min(max(inspector.usage.ReasoningTokens, reasoning), inspector.usage.OutputTokens)
		inspector.usage.ContextTokens = max(inspector.usage.ContextTokens, inspector.usage.InputTokens)
		reported := positiveInt(usage["total_tokens"])
		inspector.usage.TotalTokens = max(inspector.usage.TotalTokens, reported, inspector.usage.ContextTokens+inspector.usage.OutputTokens)
	}
}

func usageFromJSON(body []byte) relayapp.TokenUsage {
	var payload map[string]any
	if json.Unmarshal(body, &payload) != nil {
		return relayapp.TokenUsage{}
	}
	inspector := sseInspector{}
	inspector.mergeUsage(payload)
	return inspector.usage
}

func jsonTerminal(body []byte) string {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if decoder.Decode(&value) != nil || !responseFailed(value) {
		return ""
	}
	object, _ := value.(map[string]any)
	response, _ := object["response"].(map[string]any)
	eventType, _ := object["type"].(string)
	status, _ := object["status"].(string)
	if status == "" {
		status, _ = response["status"].(string)
	}
	if eventType == "response.incomplete" || status == "incomplete" || object["incomplete_details"] != nil || response["incomplete_details"] != nil {
		return "response.incomplete"
	}
	return "response.failed"
}

func positiveInt(value any) int64 {
	number, ok := value.(float64)
	if !ok || number <= 0 || number > float64(^uint64(0)>>1) {
		return 0
	}
	return int64(number)
}

func writeStreamFailure(writer http.ResponseWriter, path, model string) {
	path = strings.TrimRight(path, "/")
	if path == "/v1/messages" {
		body, _ := json.Marshal(map[string]any{"type": "error", "error": map[string]string{"type": "api_error", "message": "The request could not be completed"}})
		_, _ = writer.Write(append(append([]byte("event: error\ndata: "), body...), []byte("\n\n")...))
		return
	}
	if path == "/v1/chat/completions" || path == "/v1/completions" {
		body, _ := json.Marshal(map[string]any{"error": map[string]string{"code": "upstream_unavailable", "message": "The request could not be completed"}})
		_, _ = writer.Write(append(append([]byte("data: "), body...), []byte("\n\ndata: [DONE]\n\n")...))
		return
	}
	if len(model) > 128 {
		model = model[:128]
	}
	event := map[string]any{
		"type": "response.failed",
		"response": map[string]any{
			"id": "resp_switchboard_failed", "object": "response",
			"status": "failed", "model": model, "output": []any{},
			"error": map[string]string{"code": "upstream_unavailable", "message": "The request could not be completed"},
		},
	}
	body, _ := json.Marshal(event)
	_, _ = writer.Write(append(append([]byte("event: response.failed\ndata: "), body...), []byte("\n\n")...))
}

func copyRequestHeaders(target, source http.Header) {
	for name, values := range source {
		lower := strings.ToLower(name)
		if _, blocked := hopHeaders[lower]; blocked || strings.HasPrefix(lower, "x-provider-switch-") {
			continue
		}
		for _, value := range values {
			target.Add(name, value)
		}
	}
}

func copyResponseHeaders(target, source http.Header) {
	for name, values := range source {
		if _, blocked := hopHeaders[strings.ToLower(name)]; blocked {
			continue
		}
		for _, value := range values {
			target.Add(name, value)
		}
	}
}

func joinPath(base, request string) string {
	base = strings.TrimRight(base, "/")
	if base == "" || request == base || strings.HasPrefix(request, base+"/") {
		if request == "" {
			return "/"
		}
		return request
	}
	return base + "/" + strings.TrimLeft(request, "/")
}

func joinQuery(base, request string) string {
	if base == "" {
		return request
	}
	if request == "" {
		return base
	}
	return base + "&" + request
}

func finishLease(lease relayapp.CredentialLease, outcome relayapp.AttemptOutcome) {
	if lease != nil {
		lease.Finish(outcome)
	}
}

func waitRetry(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			return nil
		}
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func drainResponse(response *http.Response) {
	if response == nil || response.Body == nil {
		return
	}
	_, _ = io.CopyN(io.Discard, response.Body, 64*1024)
	response.Body.Close()
}

func readErrorBody(response *http.Response) []byte {
	if response == nil || response.Body == nil {
		return nil
	}
	body, _ := io.ReadAll(io.LimitReader(response.Body, 64*1024))
	response.Body.Close()
	return body
}

func modelUnavailable(body []byte, model string) bool {
	if model == "" || len(body) == 0 {
		return false
	}
	text := strings.ToLower(string(body))
	return strings.Contains(text, strings.ToLower(model)) &&
		(strings.Contains(text, "not available on your plan") ||
			strings.Contains(text, "does not exist") ||
			strings.Contains(text, "not available for your account"))
}

func balanceUnavailable(status int, body []byte) bool {
	if status == http.StatusPaymentRequired {
		return true
	}
	if status < 400 || status >= 500 || len(body) == 0 {
		return false
	}
	text := strings.NewReplacer("_", " ", "-", " ").Replace(strings.ToLower(string(body)))
	for _, marker := range []string{
		"insufficient balance", "insufficient funds", "insufficient quota",
		"not enough balance", "balance is too low", "balance too low", "balance exhausted",
		"недостаточно средств", "недостаточный баланс", "баланс исчерпан",
	} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

func genericErrorResponse(status int) *http.Response {
	if status < 400 || status > 599 {
		status = http.StatusBadGateway
	}
	body, _ := json.Marshal(map[string]string{"error": "The request could not be completed"})
	return &http.Response{
		StatusCode: status,
		Header: http.Header{
			"Content-Type":   []string{"application/json"},
			"Content-Length": []string{strconv.Itoa(len(body))},
		},
		Body:          io.NopCloser(bytes.NewReader(body)),
		ContentLength: int64(len(body)),
	}
}

var usageHeaderNames = []string{
	"X-Switchboard-Input-Tokens",
	"X-Switchboard-Output-Tokens",
	"X-Switchboard-Cached-Tokens",
	"X-Switchboard-Reasoning-Tokens",
	"X-Switchboard-Total-Tokens",
	"X-Switchboard-Context-Tokens",
	"X-Switchboard-Generation-Nanoseconds",
}

func setUsageHeaders(header http.Header, usage relayapp.TokenUsage, generation time.Duration) {
	values := []int64{
		usage.InputTokens, usage.OutputTokens, usage.CachedTokens,
		usage.ReasoningTokens, usage.TotalTokens, usage.ContextTokens,
		generation.Nanoseconds(),
	}
	for index, name := range usageHeaderNames {
		header.Set(name, strconv.FormatInt(values[index], 10))
	}
}

func usageFromHeaders(header http.Header) relayapp.TokenUsage {
	return relayapp.TokenUsage{
		InputTokens:     parseIntHeader(header, usageHeaderNames[0]),
		OutputTokens:    parseIntHeader(header, usageHeaderNames[1]),
		CachedTokens:    parseIntHeader(header, usageHeaderNames[2]),
		ReasoningTokens: parseIntHeader(header, usageHeaderNames[3]),
		TotalTokens:     parseIntHeader(header, usageHeaderNames[4]),
		ContextTokens:   parseIntHeader(header, usageHeaderNames[5]),
	}
}

func durationHeader(header http.Header, name string) time.Duration {
	return time.Duration(parseIntHeader(header, name))
}

func parseIntHeader(header http.Header, name string) int64 {
	value, err := strconv.ParseInt(header.Get(name), 10, 64)
	if err != nil || value < 0 {
		return 0
	}
	return value
}

func removeUsageHeaders(header http.Header) {
	for _, name := range usageHeaderNames {
		header.Del(name)
	}
}

func retryDelay(attempt int, response *http.Response, config Config) time.Duration {
	if response != nil {
		retryAfter := strings.TrimSpace(response.Header.Get("Retry-After"))
		if seconds, err := strconv.ParseFloat(retryAfter, 64); err == nil && seconds > 0 && !math.IsInf(seconds, 0) && !math.IsNaN(seconds) {
			return min(time.Duration(seconds*float64(time.Second)), config.RetryMax)
		}
		if when, err := http.ParseTime(retryAfter); err == nil {
			if delay := time.Until(when); delay > 0 {
				return min(delay, config.RetryMax)
			}
		}
	}
	multiplier := time.Duration(1 << min(attempt, 10))
	return min(config.RetryBase*multiplier, config.RetryMax)
}

func requestModel(body []byte, contentType string) string {
	if strings.Contains(strings.ToLower(contentType), "json") {
		var payload struct {
			Model string `json:"model"`
		}
		if json.Unmarshal(body, &payload) == nil {
			return strings.TrimSpace(payload.Model)
		}
	}
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil || mediaType != "multipart/form-data" || params["boundary"] == "" {
		return ""
	}
	reader := multipart.NewReader(bytes.NewReader(body), params["boundary"])
	for range 128 {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			return ""
		}
		if err != nil {
			return ""
		}
		if part.FormName() == "model" && part.FileName() == "" {
			value, _ := io.ReadAll(io.LimitReader(part, 129))
			return strings.TrimSpace(string(value))
		}
	}
	return ""
}

func rewriteRequestModel(body []byte, contentType, model string) []byte {
	if model == "" {
		return body
	}
	if strings.Contains(strings.ToLower(contentType), "json") {
		var payload map[string]any
		if json.Unmarshal(body, &payload) != nil {
			return body
		}
		payload["model"] = model
		encoded, err := json.Marshal(payload)
		if err == nil {
			return encoded
		}
		return body
	}
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil || mediaType != "multipart/form-data" || params["boundary"] == "" {
		return body
	}
	reader := multipart.NewReader(bytes.NewReader(body), params["boundary"])
	buffer := bytes.Buffer{}
	writer := multipart.NewWriter(&buffer)
	if writer.SetBoundary(params["boundary"]) != nil {
		return body
	}
	rewritten := false
	for range 128 {
		part, readErr := reader.NextPart()
		if errors.Is(readErr, io.EOF) {
			if writer.Close() == nil && rewritten {
				return buffer.Bytes()
			}
			return body
		}
		if readErr != nil {
			return body
		}
		target, createErr := writer.CreatePart(part.Header)
		if createErr != nil {
			return body
		}
		if part.FormName() == "model" && part.FileName() == "" {
			if _, createErr = io.WriteString(target, model); createErr != nil {
				return body
			}
			rewritten = true
			continue
		}
		if _, createErr = io.Copy(target, part); createErr != nil {
			return body
		}
	}
	return body
}

func extendCacheTTL(body []byte, contentType string) []byte {
	if !strings.Contains(strings.ToLower(contentType), "json") || len(body) == 0 {
		return body
	}
	var payload any
	if json.Unmarshal(body, &payload) != nil {
		return body
	}
	changed := extendCacheValue(payload)
	if !changed {
		return body
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return body
	}
	return encoded
}

func extendCacheValue(value any) bool {
	changed := false
	switch value := value.(type) {
	case map[string]any:
		if control, ok := value["cache_control"].(map[string]any); ok && control["type"] == "ephemeral" && control["ttl"] != "1h" {
			control["ttl"] = "1h"
			changed = true
		}
		for _, child := range value {
			changed = extendCacheValue(child) || changed
		}
	case []any:
		for _, child := range value {
			changed = extendCacheValue(child) || changed
		}
	}
	return changed
}

func snapshot(address net.Addr) domain.Snapshot {
	host, portValue, _ := net.SplitHostPort(address.String())
	port, _ := strconv.Atoi(portValue)
	return domain.Snapshot{State: domain.StateLive, Address: fmt.Sprintf("http://%s:%d", host, port), Port: port}
}

func writeError(writer http.ResponseWriter, status int, message string) {
	body, _ := json.Marshal(map[string]string{"error": message})
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("Content-Length", strconv.Itoa(len(body)))
	writer.WriteHeader(status)
	_, _ = writer.Write(body)
}
