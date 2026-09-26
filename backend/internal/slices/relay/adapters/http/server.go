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
	pathpkg "path"
	"strconv"
	"strings"
	"sync"
	"time"

	relayapp "github.com/luxuryprivate/switchboard/backend/internal/slices/relay/application"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/relay/domain"
)

const (
	absoluteMaxRequestBytes         = 256 * 1024 * 1024
	maxBufferedResponseBytes        = 32 * 1024 * 1024
	maxProxyClients                 = 64
	maxRequestModelBytes            = 128
	maxCacheTraversalDepth          = 64
	maxRelayAttempts                = 64
	maxStreamFailuresBeforeFallback = 3
	// maxRouteFailovers caps how many providers one request walks through on
	// terminal verdicts. A chain longer than this is a misconfiguration; the
	// cap keeps a routing loop from ever outliving the request.
	maxRouteFailovers = 4
	// terminalRefused is the terminal reason for an answer the provider's content
	// policy declined. It is deliberately not "response.failed": a failure is worth
	// another attempt, a verdict is not.
	terminalRefused = "response.refused"
)

var hopHeaders = map[string]struct{}{
	"connection": {}, "expect": {}, "host": {}, "keep-alive": {},
	"proxy-authenticate": {}, "proxy-authorization": {}, "proxy-connection": {},
	"te": {}, "trailer": {}, "transfer-encoding": {}, "upgrade": {},
}

var (
	errIncompleteSSE           = errors.New("upstream SSE ended without a terminal event")
	errClientDisconnected      = errors.New("client disconnected")
	errResponseTooLarge        = errors.New("upstream response exceeds the buffer limit")
	errRetryableSSEFailure     = errors.New("upstream SSE failed before output")
	errInvalidFallbackResponse = errors.New("fallback response is not a completed Responses object")
)

type Server struct {
	address      string
	routes       relayapp.RouteSource
	credentials  relayapp.CredentialSource
	activity     relayapp.ActivitySink
	guardrail    relayapp.Guardrail
	failovers    relayapp.FailoverSource
	routeEvents  relayapp.RouteEventSink
	config       Config
	client       *http.Client
	transport    *http.Transport
	proxyClients map[string]*http.Client
	chatOnly     sync.Map
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
	Guardrail   relayapp.Guardrail
	// Failovers is optional: nil leaves terminal verdicts as final answers,
	// the behavior of a build without route chains.
	Failovers relayapp.FailoverSource
	// RouteEvents is optional: when set, a failover switch is reported with
	// the provider names the operator knows.
	RouteEvents relayapp.RouteEventSink
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
	} else if config.PermanentAttempts > 3 {
		config.PermanentAttempts = 3
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
	var guardrail relayapp.Guardrail = relayapp.NoopGuardrail{}
	if dependencies.Guardrail != nil {
		guardrail = dependencies.Guardrail
	}
	return &Server{
		address:      address,
		routes:       dependencies.Routes,
		credentials:  dependencies.Credentials,
		activity:     activity,
		guardrail:    guardrail,
		failovers:    dependencies.Failovers,
		routeEvents:  dependencies.RouteEvents,
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
	server.mu.Unlock()
	if httpServer == nil {
		return nil
	}
	if err := httpServer.Shutdown(ctx); err != nil {
		if closeErr := httpServer.Close(); closeErr != nil {
			return errors.Join(err, closeErr)
		}
	}
	server.mu.Lock()
	if server.server == httpServer {
		server.server = nil
		server.listener = nil
	}
	server.mu.Unlock()
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
	body, clientTools := normalizeResponsesTools(request.Method, request.URL.Path, request.Header.Get("Content-Type"), body)
	// Read from the normalized body: the relay has just folded any nested
	// declaration into the documented tools array.
	clientDeclaredTools := server.guardrail.ClientDeclaredTools(body)
	model := requestModel(body, request.Header.Get("Content-Type"))
	// The name the client asked for, before any rewrite for the first route.
	// Failover resolves chains by this name: it is the identity the routes
	// slice knows, and it stays the same while every attempt speaks the
	// upstream name of whichever provider is serving.
	publicModel := model
	route, err := server.routes.Current(ctx, model)
	if err != nil {
		writeError(writer, http.StatusServiceUnavailable, "Relay route is unavailable")
		return
	}
	streamRequested := requiresStreamTerminal(request, body)
	dispatchPath, preparedBody, imageCompat, imageErr := prepareImageRequest(request.Method, request.URL.Path, body, request.Header.Get("Content-Type"), route.UpstreamModel, route.ImageCompat)
	if imageErr != nil {
		writeError(writer, http.StatusBadRequest, "Image generation request is invalid")
		return
	}
	chatMode := route.Format == "chat" || (route.Format == "auto" && server.chatOnlyLoaded(route.ProviderID))
	chatPath, chatBody, chatCompat, chatErr := prepareChatCompletions(request.Method, request.URL.Path, body, request.Header.Get("Content-Type"), route.ChatPath, chatMode)
	if chatErr != nil {
		writeError(writer, http.StatusBadRequest, "Chat completions compatibility request is invalid")
		return
	}
	// The upstream request always gets its own URL. requestWithRetry rewrites the
	// path in place when a provider turns out to be chat-only, and everything below
	// - the terminal stream frame, the heartbeat dialect, the tool-call restoration
	// - keys off request.URL.Path to answer in the dialect the client opened.
	// Sharing the URL would answer a Responses caller in chat dialect the moment
	// that rewrite happened.
	upstreamRequest := new(http.Request)
	*upstreamRequest = *request
	targetURL := *request.URL
	upstreamRequest.URL = &targetURL
	switch {
	case imageCompat:
		body = preparedBody
		targetURL.Path = dispatchPath
		targetURL.RawPath = ""
	case chatCompat:
		body = chatBody
		if route.UpstreamModel != "" && route.UpstreamModel != model {
			body = rewriteRequestModel(body, request.Header.Get("Content-Type"), route.UpstreamModel)
		}
		targetURL.Path = chatPath
		targetURL.RawPath = ""
	case route.UpstreamModel != "" && route.UpstreamModel != model:
		body = rewriteRequestModel(body, request.Header.Get("Content-Type"), route.UpstreamModel)
	}
	if route.CacheTTL >= time.Hour {
		extended, carriesHour := extendCacheTTL(body, request.Header.Get("Content-Type"))
		body = extended
		if carriesHour {
			// upstreamRequest was copied from the client's request and still shares its
			// header map, so the declaration gets its own copy rather than being written
			// into what the caller sent us.
			upstreamRequest.Header = request.Header.Clone()
			declareBetaFeature(upstreamRequest.Header, extendedCacheTTLBeta)
		}
	}
	activityID := server.activity.Begin(relayapp.ActivityStart{
		Model: model, ProviderID: route.ProviderID, ProviderName: route.ProviderName,
		Method: request.Method, Path: request.URL.Path, BytesIn: int64(len(body)),
	})
	status := 0
	bytesOut := int64(0)
	cancelled := false
	errorCode := ""
	errorDetail := ""
	usage := relayapp.TokenUsage{}
	generation := time.Duration(0)
	defer func() {
		server.activity.Finish(activityID, relayapp.ActivityFinish{
			Status: status, BytesOut: bytesOut, Cancelled: cancelled, ErrorCode: errorCode,
			ErrorDetail: errorDetail,
			Usage:       usage, Generation: generation,
		})
	}()
	committed := false
	// What the credential of the attempt in flight is, so the answer's headers can be
	// checked against it before they reach the client.
	var secrets []string
	flusher, _ := writer.(http.Flusher)
	var heartbeat func() error
	clientStream := streamRequested && !imageCompat
	bufferTerminal := clientStream || imageCompat || chatCompat
	chatActive := chatCompat
	// The provider's own words for the terminal failure, filed into history
	// below. The client body stays neutral; this travels out-of-band.
	var upstreamDetail string
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
			payload := []byte(": switchboard keep-alive\n\n")
			if canonicalPath(request.URL.Path) == "/v1/responses" {
				payload = []byte("event: response.in_progress\ndata: {\"type\":\"response.in_progress\",\"response\":{}}\n\n")
			}
			if _, err := writer.Write(payload); err != nil {
				return errClientDisconnected
			}
			if flusher != nil {
				flusher.Flush()
			}
			return nil
		}
	}
	requestUpstream := func(requestCtx context.Context) (*http.Response, error) {
		return server.requestWithRetry(requestCtx, upstreamRequest, body, route, activityID, &bufferTerminal, 0, func(credential relayapp.Credential) {
			// Kept so the answer's headers can be checked against it: a provider that
			// echoes the key we sent it must not hand that key to the client.
			secrets = sensitiveCredentialMarkers(credential)
		}, &chatActive, &upstreamDetail, publicModel)
	}
	var response *http.Response
	// A refused answer is a wasted attempt, not a dead request. The rules match shell
	// and network idiom an honest assistant produces all day, so in Block mode one
	// unlucky answer would otherwise end a run the caller cannot restart from here —
	// and a provider that sampled something ugly once usually does not do it twice.
	// The attempt is repeated on the same budget as a provider that failed out loud,
	// and the refusal still stands if every attempt earns one: the client never sees
	// a payload, it only waits longer. Nothing of the answer has been written yet at
	// this point, so a committed stream can be retried too — the caller has had
	// headers and keep-alives, no content.
	for attempt := 0; ; attempt++ {
		var err error
		// Every attempt reports itself from scratch. A terminal event on the attempt that
		// was refused must not be what the history shows for the attempt that succeeded.
		// Its COST is the exception: `refused` is a fact about the answer, and a refused
		// answer was still generated and still billed, so those tokens are kept.
		errorCode = ""
		errorDetail = ""
		refusedUsage := usage
		if clientStream {
			response, err = server.withHeartbeat(ctx, heartbeat, requestUpstream)
		} else {
			response, err = requestUpstream(ctx)
		}
		if err != nil {
			if committed {
				cancelled = errors.Is(err, context.Canceled) || errors.Is(err, errClientDisconnected)
				errorCode = "stream_incomplete"
				errorDetail = err.Error()
				writeStreamFailure(writer, request.URL.Path, model)
				return
			}
			if errors.Is(err, context.Canceled) {
				status = http.StatusServiceUnavailable
				cancelled = true
				errorCode = "cancelled"
				errorDetail = err.Error()
				writeError(writer, http.StatusServiceUnavailable, "Request cancelled")
			} else {
				status = http.StatusBadGateway
				errorCode = "transport"
				errorDetail = err.Error()
				writeError(writer, http.StatusBadGateway, "Provider is unavailable")
			}
			return
		}
		status = response.StatusCode
		// Read before the rejection exit below: a refusal arrives as a status too, and
		// that exit used to return while its reason was still sitting in the header.
		terminal := response.Header.Get("X-Switchboard-Terminal")
		response.Header.Del("X-Switchboard-Terminal")
		switch {
		case terminal == terminalRefused:
			errorCode = "policy_refusal"
			errorDetail = responseErrorDetail(response, "Provider response was refused", secrets)
		case terminal == "response.incomplete":
			errorCode = "stream_incomplete"
			errorDetail = responseErrorDetail(response, "Provider stream ended incomplete", secrets)
		case terminal == "response.failed":
			errorCode = "upstream_status"
			errorDetail = responseErrorDetail(response, "Provider reported a failed response", secrets)
		case status >= 400:
			errorCode = "request_rejected"
			// Prefer the provider's own words when it gave any; the fallback
			// below only covers verdicts without a readable body (429s, empty
			// answers). The client still receives the neutral error body.
			errorDetail = truncateErrorDetail(redactSecrets(upstreamDetail, secrets))
			if errorDetail == "" {
				errorDetail = responseErrorDetail(response, fmt.Sprintf("provider returned HTTP %d", status), secrets)
			}
		}
		if status >= 400 && committed {
			response.Body.Close()
			writeStreamFailure(writer, request.URL.Path, model)
			return
		}
		usage = usageWith(refusedUsage, usageFromHeaders(response.Header))
		generation = durationHeader(response.Header, "X-Switchboard-Generation-Nanoseconds")
		removeUsageHeaders(response.Header)
		// A rejected request already carries a neutral error body; translating it
		// would replace the reason with an empty "completed" response.
		if chatActive && status < 400 {
			limit := responseBufferLimit(server.config)
			raw, readErr := io.ReadAll(io.LimitReader(response.Body, limit+1))
			response.Body.Close()
			if readErr != nil || int64(len(raw)) > limit {
				status = http.StatusBadGateway
				errorCode = "chat_compatibility"
				errorDetail = errorText(readErr, "Chat completions stream could not be converted")
				if committed {
					writeStreamFailure(writer, request.URL.Path, model)
				} else {
					writeError(writer, http.StatusBadGateway, "Chat completions stream could not be converted")
				}
				return
			}
			converted, convertErr := chatToResponses(raw, clientStream)
			if convertErr != nil {
				status = http.StatusBadGateway
				errorCode = "chat_compatibility"
				errorDetail = convertErr.Error()
				if committed {
					writeStreamFailure(writer, request.URL.Path, model)
				} else {
					writeError(writer, http.StatusBadGateway, "Chat completions response could not be converted")
				}
				return
			}
			response.Body = io.NopCloser(bytes.NewReader(converted))
			response.ContentLength = int64(len(converted))
			if clientStream {
				response.Header.Set("Content-Type", "text/event-stream; charset=utf-8")
				response.Header.Del("Content-Length")
			} else {
				response.Header.Set("Content-Type", "application/json")
				response.Header.Set("Content-Length", strconv.Itoa(len(converted)))
			}
			response.Header.Del("Content-Encoding")
		}
		if imageCompat {
			raw, readErr := io.ReadAll(io.LimitReader(response.Body, maxImageResponse+1))
			response.Body.Close()
			converted, convertErr := imagesResponse(raw)
			if readErr != nil || len(raw) > maxImageResponse || convertErr != nil {
				status = http.StatusBadGateway
				errorCode = "image_generation"
				errorDetail = errorText(convertErr, errorText(readErr, "Image generation response could not be converted"))
				writeError(writer, http.StatusBadGateway, "Image generation failed")
				return
			}
			response.Body = io.NopCloser(bytes.NewReader(converted))
			response.ContentLength = int64(len(converted))
			response.Header.Set("Content-Type", "application/json")
			response.Header.Set("Content-Length", strconv.Itoa(len(converted)))
			response.Header.Del("Content-Encoding")
		}
		restoreResponseToolCalls(response, clientTools, request.URL.Path, server.config)
		// The guardrails judge exactly what the client is about to read, after every
		// translation and repair, so a payload cannot hide in a dialect the relay was
		// still rewriting.
		code, blocked := server.reviewResponse(response, request.URL.Path, relayapp.GuardrailSubject{
			ProviderID: route.ProviderID, ProviderName: route.ProviderName, Model: model,
			ClientDeclaredTools: clientDeclaredTools,
		})
		if !blocked {
			break
		}
		// The refused body is dropped here rather than by the deferred close below: this
		// path returns before the loop ever falls through to it.
		response.Body.Close()
		if attempt+1 < server.guardrailAttempts() {
			delay := retryDelay(attempt, nil, server.config)
			server.observeRetry(activityID, attempt, status, delay)
			if waitErr := waitRetry(ctx, delay); waitErr != nil {
				cancelled = true
				errorCode = "cancelled"
				errorDetail = waitErr.Error()
				return
			}
			continue
		}
		status = http.StatusBadGateway
		errorCode = code
		errorDetail = code
		if committed {
			writeStreamFailure(writer, request.URL.Path, model)
		} else {
			writeError(writer, http.StatusBadGateway, "Provider response was refused by the local guardrails")
		}
		return
	}
	defer response.Body.Close()
	if !committed {
		copyResponseHeaders(writer.Header(), response.Header, secrets)
		writer.WriteHeader(response.StatusCode)
	}
	buffer := make([]byte, 64*1024)
	for {
		count, readErr := response.Body.Read(buffer)
		if count > 0 {
			if _, writeErr := writer.Write(buffer[:count]); writeErr != nil {
				cancelled = true
				errorCode = "client_disconnected"
				errorDetail = writeErr.Error()
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
			errorDetail = readErr.Error()
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
	if request.UseStoredCredential && route.AuthMode == "passthrough" {
		route.AuthMode = "bearer"
	}
	normalizedBody, clientTools := normalizeResponsesTools(request.Method, request.Path, request.Headers.Get("Content-Type"), request.Body)
	request.Body = normalizedBody
	clientDeclaredTools := server.guardrail.ClientDeclaredTools(request.Body)
	path, body, imageCompat, err := prepareImageRequest(request.Method, request.Path, request.Body, request.Headers.Get("Content-Type"), request.UpstreamModel, route.ImageCompat)
	if err != nil {
		return relayapp.DispatchResponse{}, err
	}
	urlValue, err := url.Parse(path)
	if err != nil || !strings.HasPrefix(urlValue.Path, "/") {
		return relayapp.DispatchResponse{}, errors.New("invalid dispatch path")
	}
	// Same ownership rule as ServeHTTP: the upstream request carries its own URL,
	// because requestWithRetry rewrites the path in place and urlValue.Path is what
	// tells restoreClientToolCalls which dialect the caller asked for.
	targetURL := *urlValue
	incoming := &http.Request{Method: request.Method, URL: &targetURL, Header: request.Headers.Clone()}
	streamRequested := requiresStreamTerminal(incoming, request.Body)
	chatMode := route.Format == "chat" || (route.Format == "auto" && server.chatOnlyLoaded(route.ProviderID))
	chatPath, chatBody, chatCompat, chatErr := prepareChatCompletions(request.Method, request.Path, body, request.Headers.Get("Content-Type"), route.ChatPath, chatMode)
	if chatErr != nil {
		return relayapp.DispatchResponse{}, chatErr
	}
	if chatCompat {
		body = chatBody
		if request.UpstreamModel != "" && request.UpstreamModel != requestModel(request.Body, request.Headers.Get("Content-Type")) {
			body = rewriteRequestModel(body, request.Headers.Get("Content-Type"), request.UpstreamModel)
		}
		targetURL.Path = chatPath
		targetURL.RawPath = ""
	} else if !imageCompat {
		body = rewriteRequestModel(request.Body, request.Headers.Get("Content-Type"), request.UpstreamModel)
	}
	if route.CacheTTL >= time.Hour {
		extended, carriesHour := extendCacheTTL(body, request.Headers.Get("Content-Type"))
		body = extended
		if carriesHour {
			// incoming.Header is already a clone of the caller's, so the declaration that
			// carries the hour can be added to it directly.
			declareBetaFeature(incoming.Header, extendedCacheTTLBeta)
		}
	}
	terminalStream := streamRequested || imageCompat || chatCompat
	chatActive := chatCompat
	activityID := server.activity.Begin(relayapp.ActivityStart{
		Model: request.PublicModel, ProviderID: route.ProviderID,
		ProviderName: route.ProviderName, Method: request.Method,
		Path: urlValue.Path, BytesIn: int64(len(body)),
	})
	markers := make([]string, 0, 4)
	// Usage and generation live here so every Finish below files the same
	// report ServeHTTP's defer does, including the failure exits.
	usage := relayapp.TokenUsage{}
	generation := time.Duration(0)
	var upstreamDetail string
	response, err := server.requestWithRetry(ctx, incoming, body, route, activityID, &terminalStream, request.AttemptLimit, func(credential relayapp.Credential) {
		markers = sensitiveCredentialMarkers(credential)
	}, &chatActive, &upstreamDetail, "")
	// The empty public model above is deliberate: Dispatch is pinned routing —
	// the tunnel and the model test ask for one provider by name, and the
	// tunnel's privacy markers are computed for exactly that route. A chain
	// switch mid-dispatch would serve a provider the public boundary never
	// vetted, so terminal verdicts stay final here.
	if err != nil {
		server.activity.Finish(activityID, relayapp.ActivityFinish{
			Status: http.StatusBadGateway, Cancelled: errors.Is(err, context.Canceled),
			ErrorCode: "transport", ErrorDetail: truncateErrorDetail(redactSecrets(err.Error(), markers)),
			Usage: usage, Generation: generation,
		})
		return relayapp.DispatchResponse{}, err
	}
	defer response.Body.Close()
	usage = usageFromHeaders(response.Header)
	generation = durationHeader(response.Header, "X-Switchboard-Generation-Nanoseconds")
	limit := responseBufferLimit(server.config)
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil || int64(len(responseBody)) > limit {
		server.activity.Finish(activityID, relayapp.ActivityFinish{
			Status: response.StatusCode, ErrorCode: "stream_incomplete",
			ErrorDetail: truncateErrorDetail(redactSecrets(errorText(err, "response exceeded the buffer limit"), markers)),
			Usage:       usage, Generation: generation,
		})
		return relayapp.DispatchResponse{}, errIncompleteSSE
	}
	if imageCompat {
		responseBody, err = imagesResponse(responseBody)
		if err != nil {
			server.activity.Finish(activityID, relayapp.ActivityFinish{
				Status: http.StatusBadGateway, ErrorCode: "image_generation",
				ErrorDetail: truncateErrorDetail(redactSecrets(err.Error(), markers)),
				Usage:       usage, Generation: generation,
			})
			return relayapp.DispatchResponse{}, err
		}
		response.Header.Set("Content-Type", "application/json")
		response.Header.Set("Content-Length", strconv.Itoa(len(responseBody)))
		response.Header.Del("Content-Encoding")
	}
	// A rejected request already carries a neutral error body; translating it
	// would replace the reason with an empty "completed" response.
	if chatActive && response.StatusCode < 400 {
		responseBody, err = chatToResponses(responseBody, streamRequested)
		if err != nil {
			server.activity.Finish(activityID, relayapp.ActivityFinish{
				Status: http.StatusBadGateway, ErrorCode: "chat_compatibility",
				ErrorDetail: truncateErrorDetail(redactSecrets(err.Error(), markers)),
				Usage:       usage, Generation: generation,
			})
			return relayapp.DispatchResponse{}, err
		}
		if streamRequested {
			response.Header.Set("Content-Type", "text/event-stream; charset=utf-8")
			response.Header.Del("Content-Length")
		} else {
			response.Header.Set("Content-Type", "application/json")
			response.Header.Set("Content-Length", strconv.Itoa(len(responseBody)))
		}
		response.Header.Del("Content-Encoding")
	}
	terminal := response.Header.Get("X-Switchboard-Terminal")
	removeUsageHeaders(response.Header)
	response.Header.Del("X-Switchboard-Terminal")
	responseBody = restoreClientToolCalls(responseBody, clientTools, urlValue.Path, strings.Contains(strings.ToLower(response.Header.Get("Content-Type")), "event-stream"))
	// Tunnel traffic reaches the relay through here, so the same review protects it.
	// A refusal leaves as a plain dispatch error, which every public caller already
	// receives as one neutral message.
	if code, blocked := server.reviewBody(responseBody, response.Header.Get("Content-Type"), response.StatusCode, relayapp.GuardrailSubject{
		ProviderID: route.ProviderID, ProviderName: route.ProviderName, Model: request.PublicModel,
		ClientDeclaredTools: clientDeclaredTools,
	}); blocked {
		server.activity.Finish(activityID, relayapp.ActivityFinish{
			Status: http.StatusBadGateway, ErrorCode: code, ErrorDetail: code,
			Usage: usage, Generation: generation,
		})
		return relayapp.DispatchResponse{}, errGuardrailBlocked
	}
	if response.Header.Get("Content-Length") != "" {
		response.Header.Set("Content-Length", strconv.Itoa(len(responseBody)))
	}
	dispatchError := ""
	dispatchDetail := ""
	if response.StatusCode >= 400 {
		dispatchError = "request_rejected"
		// Same rule as the local path above: the provider's own words when it
		// gave any, redacted with the attempt's markers before filing. The
		// dispatch body itself stays neutral.
		dispatchDetail = truncateErrorDetail(redactSecrets(upstreamDetail, markers))
		if dispatchDetail == "" {
			dispatchDetail = jsonErrorDetail(responseBody, markers)
		}
		if dispatchDetail == "" {
			dispatchDetail = fmt.Sprintf("provider returned HTTP %d", response.StatusCode)
		}
	}
	server.activity.Finish(activityID, relayapp.ActivityFinish{
		Status: response.StatusCode, BytesOut: int64(len(responseBody)),
		Usage: usage, Generation: generation, ErrorCode: dispatchError,
		ErrorDetail: dispatchDetail,
	})
	return relayapp.DispatchResponse{
		Status: response.StatusCode, Headers: response.Header.Clone(), Body: responseBody,
		Terminal: terminal, SensitiveMarkers: markers,
	}, nil
}

// chatOnlyLoaded reports whether the provider was already proven to speak only
// the chat completions format by an earlier endpoint-missing 404 probe.
func (server *Server) chatOnlyLoaded(providerID string) bool {
	_, loaded := server.chatOnly.Load(providerID)
	return loaded
}

func (server *Server) requestWithRetry(ctx context.Context, incoming *http.Request, body []byte, route relayapp.Route, activityID string, terminalStream *bool, attemptLimit int, onCredential func(relayapp.Credential), chatActive *bool, upstreamDetail *string, publicModel string) (*http.Response, error) {
	// A retried call starts wordless: the detail below always describes the
	// attempt this call ended on, never a previous call's verdict.
	if upstreamDetail != nil {
		*upstreamDetail = ""
	}
	// Each failure class below spends its own PermanentAttempts budget:
	// requestFailures counts rejections of this payload, serverFailures plain 5xx
	// answers, and credentialFailures key rotations (bounded by the pool size
	// instead whenever the source reports one). A mixed sequence can therefore
	// spend up to one budget per class before the request ends. That is
	// deliberate, not a leak: a rotation must not eat the retries a flapping
	// upstream still deserves, and sharing one counter would couple rotation (a
	// per-key decision) to payload and upstream verdicts it has nothing to do with.
	requestFailures := 0
	serverFailures := 0
	credentialFailures := 0
	streamFailures := 0
	fallbackActive := false
	fallbackPath := canonicalPath(incoming.URL.Path)
	// What the attempts before this one cost. A retried answer was still generated and
	// still billed, so its tokens belong to the request even though nobody read it.
	discarded := relayapp.TokenUsage{}
	model := requestModel(body, incoming.Header.Get("Content-Type"))
	// rotateImmediate carries a credential rotation into the next iteration
	// without queueing: the rotation takes over only from a key that is free
	// right now, and rotationStatus is the rejection it ends on when none is.
	rotateImmediate := false
	rotationStatus := http.StatusBadGateway
	// routeFailures counts failover switches: the chain is finite, and a
	// request that walked it must not walk it again on a later verdict. Each
	// switch resets the per-class budgets — the new provider is a fresh
	// context, and its key pool has not refused anything yet.
	routeFailures := 0
	attempt := 0
	// switchRoute walks the failover chain past a provider that answered
	// terminally: a verdict no key and no retry of this provider can change.
	// The body is rewritten to the sibling's upstream model, and the failed
	// provider is parked behind its siblings for the routes slice's TTL so the
	// next request starts where this one ended up.
	switchRoute := func(status int) bool {
		if server.failovers == nil || publicModel == "" || routeFailures >= maxRouteFailovers {
			return false
		}
		server.failovers.Degrade(route.ProviderID)
		next, ok, err := server.failovers.Next(ctx, route.ProviderID, publicModel)
		if err != nil || !ok {
			return false
		}
		if next.UpstreamModel != "" && next.UpstreamModel != requestModel(body, incoming.Header.Get("Content-Type")) {
			body = rewriteRequestModel(body, incoming.Header.Get("Content-Type"), next.UpstreamModel)
			model = next.UpstreamModel
		}
		if server.routeEvents != nil {
			server.routeEvents.FailoverOccurred(route.ProviderName, next.ProviderName, publicModel)
		}
		route = next
		routeFailures++
		serverFailures, credentialFailures, requestFailures = 0, 0, 0
		rotateImmediate = false
		// The new provider's dialect is unknown to the probe cache: let the
		// endpoint-missing logic re-evaluate instead of trusting the old one.
		if chatActive != nil {
			*chatActive = false
		}
		server.observeRetry(activityID, attempt, status, 0)
		return true
	}
	for ; ; attempt++ {
		var lease relayapp.CredentialLease
		var credential relayapp.Credential
		if rotateImmediate {
			rotateImmediate = false
			var ok bool
			lease, credential, ok = server.tryAcquireCredential(route, model)
			if !ok {
				return genericErrorResponse(rotationStatus), nil
			}
			server.activity.Resume(activityID, 0)
		} else {
			var waited time.Duration
			var err error
			lease, credential, waited, err = server.acquireCredential(ctx, route, model, func() { server.activity.Waiting(activityID) })
			if err != nil {
				return nil, err
			}
			server.activity.Resume(activityID, waited)
		}
		if onCredential != nil {
			onCredential(credential)
		}
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
			if *terminalStream && !fallbackActive {
				ssePath := incoming.URL.Path
				if chatActive != nil && *chatActive {
					ssePath = defaultChatCompletionsPath
				}
				terminal, buffered, streamUsage, bufferErr := bufferTerminalSSE(ctx, response, ssePath, server.config)
				if bufferErr != nil {
					outcome := relayapp.AttemptOutcome{Kind: relayapp.AttemptTransport}
					if errors.Is(bufferErr, errRetryableSSEFailure) {
						outcome.Kind = relayapp.AttemptRequestError
					}
					finishLease(lease, outcome)
					if errors.Is(bufferErr, errClientDisconnected) || errors.Is(bufferErr, context.Canceled) {
						return nil, bufferErr
					}
					if errors.Is(bufferErr, errResponseTooLarge) {
						return nil, bufferErr
					}
					if errors.Is(bufferErr, errIncompleteSSE) && fallbackPath == responsesPath && (chatActive == nil || !*chatActive) {
						streamFailures++
						if streamFailures >= maxStreamFailuresBeforeFallback && canRetry(attempt, attemptLimit) {
							if fallback, ok := nonStreamingResponsesRequest(body, server.config.MaxRequestBytes); ok {
								body = fallback
								fallbackActive = true
								*terminalStream = false
								server.observeRetry(activityID, attempt, http.StatusOK, 0)
								continue
							}
						}
					}
					if errors.Is(bufferErr, errRetryableSSEFailure) {
						requestFailures++
						if requestFailures >= server.config.PermanentAttempts || !canRetry(attempt, attemptLimit) {
							response.Body = io.NopCloser(bytes.NewReader(buffered))
							response.ContentLength = -1
							response.Header.Set("Content-Type", "text/event-stream; charset=utf-8")
							response.Header.Del("Content-Length")
							response.Header.Del("Content-Encoding")
							response.Header.Set("X-Switchboard-Terminal", terminal)
							addDiscardedUsage(&discarded, streamUsage)
							setUsageHeaders(response.Header, discarded, time.Since(attemptStarted))
							return response, nil
						}
						// The half-stream is thrown away, but the provider generated it.
						addDiscardedUsage(&discarded, streamUsage)
						delay := retryDelay(attempt, nil, server.config)
						server.observeRetry(activityID, attempt, http.StatusOK, delay)
						if err := waitRetry(ctx, delay); err != nil {
							return nil, err
						}
						continue
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
				switch terminal {
				case "response.failed", "response.incomplete", terminalRefused:
					finishLease(lease, relayapp.AttemptOutcome{Kind: relayapp.AttemptRequestError})
				default:
					finishLease(lease, relayapp.AttemptOutcome{Kind: relayapp.AttemptSuccess})
				}
				response.Body = io.NopCloser(bytes.NewReader(buffered))
				response.ContentLength = -1
				response.Header.Set("Content-Type", "text/event-stream; charset=utf-8")
				response.Header.Del("Content-Length")
				response.Header.Del("Content-Encoding")
				response.Header.Set("X-Switchboard-Terminal", terminal)
				addDiscardedUsage(&discarded, streamUsage)
				setUsageHeaders(response.Header, discarded, time.Since(attemptStarted))
				return response, nil
			}
			if strings.Contains(strings.ToLower(response.Header.Get("Content-Type")), "json") || expectsJSONResponse(incoming.URL.Path) {
				buffered, bufferErr := bufferJSONResponse(ctx, response, server.config)
				if bufferErr != nil {
					finishLease(lease, relayapp.AttemptOutcome{Kind: relayapp.AttemptTransport})
					if errors.Is(bufferErr, errResponseTooLarge) {
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
				terminal := jsonTerminal(buffered)
				var fallbackSSE []byte
				fallbackTerminal := ""
				if fallbackActive {
					fallbackSSE, fallbackTerminal, bufferErr = responsesJSONToSSE(buffered)
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
				}
				// A 200 with no body is not an answer. The client reports it as an
				// empty or malformed response and the run ends there, so it is retried
				// on the same budget as a provider that failed out loud. Malformed
				// JSON already retries through the buffering error above.
				empty := terminal == "" && emptyUpstreamAnswer(buffered, incoming.URL.Path, response.StatusCode)
				if terminal == "response.failed" || empty {
					finishLease(lease, relayapp.AttemptOutcome{Kind: relayapp.AttemptRequestError})
					requestFailures++
					if requestFailures < server.config.PermanentAttempts && canRetry(attempt, attemptLimit) {
						// The answer is discarded, but the provider generated and billed it.
						addDiscardedUsage(&discarded, usageFromJSON(buffered))
						delay := retryDelay(attempt, nil, server.config)
						server.observeRetry(activityID, attempt, http.StatusOK, delay)
						if err := waitRetry(ctx, delay); err != nil {
							return nil, err
						}
						continue
					}
					// Out of budget the provider's own answer travels, empty as it is:
					// replacing it with an error of ours would hide which side is
					// broken, and a client that dislikes it says so already.
					if !empty {
						response.Header.Set("X-Switchboard-Terminal", terminal)
					}
				} else if terminal != "" {
					finishLease(lease, relayapp.AttemptOutcome{Kind: relayapp.AttemptRequestError})
					response.Header.Set("X-Switchboard-Terminal", terminal)
				} else {
					finishLease(lease, relayapp.AttemptOutcome{Kind: relayapp.AttemptSuccess})
				}
				if fallbackActive {
					response.Body = io.NopCloser(bytes.NewReader(fallbackSSE))
					response.ContentLength = -1
					response.Header.Set("Content-Type", "text/event-stream; charset=utf-8")
					response.Header.Del("Content-Length")
					response.Header.Set("X-Switchboard-Terminal", fallbackTerminal)
				} else {
					response.Body = io.NopCloser(bytes.NewReader(buffered))
					response.ContentLength = int64(len(buffered))
					response.Header.Set("Content-Length", strconv.Itoa(len(buffered)))
				}
				response.Header.Del("Content-Encoding")
				setUsageHeaders(response.Header, usageWith(discarded, usageFromJSON(buffered)), time.Since(attemptStarted))
				return response, nil
			}
			finishLease(lease, relayapp.AttemptOutcome{Kind: relayapp.AttemptSuccess})
			return response, nil
		}

		// A 429 or 529 needs no prose: the status alone is the throttle verdict, so
		// the key rotates without paying for a body read and scan. Draining (not
		// reading) keeps the connection reusable without retaining the body.
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

		// Error branches classify on prose, so the body is read once here and
		// lowered once for the classifiers below. A 402 never reaches the rate
		// branch: the provider's billing verdict stays authoritative even when
		// its prose mentions limits (see balanceUnavailable).
		errorBody := readErrorBody(response)
		// The response that leaves the relay stays neutral by design; the
		// provider's own words travel out-of-band instead, so history shows the
		// reason ("Budget pool quota has been exhausted") rather than a bare
		// status. Redaction happens at the boundary before filing.
		noteUpstreamDetail(upstreamDetail, errorBody)
		failureText := normalizeErrorText(errorBody)
		rateLimited := rateLimitedText(failureText)
		if status != http.StatusPaymentRequired && rateLimited {
			delay := retryDelay(attempt, response, server.config)
			finishLease(lease, relayapp.AttemptOutcome{
				Kind: relayapp.AttemptRateLimited, RetryAfter: delay,
			})
			if !canRetry(attempt, attemptLimit) {
				return genericErrorResponse(status), nil
			}
			if err := server.retryCredentialFailure(ctx, lease, activityID, attempt, status, delay); err != nil {
				return nil, err
			}
			continue
		}

		// A 408 is a transient timeout, not a deterministically failing upstream:
		// the request simply never got an answer, so it retries on the attempt
		// ceiling like a transport failure instead of spending the
		// permanent-attempt budget a 5xx that answers out loud spends.
		if status == http.StatusRequestTimeout {
			delay := retryDelay(attempt, response, server.config)
			finishLease(lease, relayapp.AttemptOutcome{Kind: relayapp.AttemptServerError})
			if !canRetry(attempt, attemptLimit) {
				return genericErrorResponse(status), nil
			}
			server.observeRetry(activityID, attempt, status, delay)
			if err := waitRetry(ctx, delay); err != nil {
				return nil, err
			}
			continue
		}

		// A 5xx that names congestion is a queue to wait in, not a verdict to
		// hand the caller: "no channel available", "service overloaded",
		// "upstream load is saturated" (new-api's wording, measured on
		// the reseller) mean the provider has nothing free right now and usually
		// does seconds later. The permanent-attempt budget that ends a
		// deterministic 5xx after two tries was ending work the caller cannot
		// restart from here — so the overloaded class waits the congestion out
		// on the same ceiling a 429 uses. The verdict cools nothing: every key
		// answers it identically, so pool damage would only spread the wait to
		// models that work.
		if status >= 500 && serviceOverloaded(failureText) {
			delay := retryDelay(attempt, response, server.config)
			finishLease(lease, relayapp.AttemptOutcome{Kind: relayapp.AttemptServerError})
			if !canRetry(attempt, attemptLimit) {
				return genericErrorResponse(status), nil
			}
			server.observeRetry(activityID, attempt, status, delay)
			if err := waitRetry(ctx, delay); err != nil {
				return nil, err
			}
			continue
		}

		if status >= 500 {
			delay := retryDelay(attempt, response, server.config)
			finishLease(lease, relayapp.AttemptOutcome{Kind: relayapp.AttemptServerError})
			serverFailures++
			// A 5xx without a rate-limit or congestion verdict is a
			// deterministically failing upstream, not a queue to wait in: it
			// retries on the permanent-attempt budget like every other failure
			// the relay answers out loud, instead of burning the full
			// 64-attempt ceiling with backoff per request. Transport errors,
			// 408s and congestion keep that ceiling — a dropped socket, a
			// timed-out wait and a busy channel all say nothing about the next
			// attempt.
			if serverFailures >= server.config.PermanentAttempts || !canRetry(attempt, attemptLimit) {
				return genericErrorResponse(status), nil
			}
			server.observeRetry(activityID, attempt, status, delay)
			if err := waitRetry(ctx, delay); err != nil {
				return nil, err
			}
			continue
		}

		// A refusal is the provider's verdict on this exact payload, so none of the
		// repairs below apply to it and neither does the next key: stripping reasoning
		// or downgrading tools resends the same content, and the plain retry after
		// them hands that content to another credential in the pool.
		refused := policyRefused(string(errorBody))
		if status == http.StatusBadRequest && !refused && canRetry(attempt, attemptLimit) {
			if repaired, changed := repairRejectedParameters(body, errorBody); changed {
				finishLease(lease, relayapp.AttemptOutcome{Kind: relayapp.AttemptRequestError})
				body = repaired
				server.observeRetry(activityID, attempt, status, 0)
				continue
			}
			if freeformToolRejected(errorBody) {
				if downgraded, changed := downgradeFreeformTools(body); changed {
					finishLease(lease, relayapp.AttemptOutcome{Kind: relayapp.AttemptRequestError})
					body = downgraded
					server.observeRetry(activityID, attempt, status, 0)
					continue
				}
			}
			// Last resort, and deliberately not gated on what the provider said: a
			// refusal of a turn that replays sealed reasoning is almost always about
			// the seal, and providers phrase it every way imaginable. Retrying without
			// it loses the earlier chain of thought and keeps the turn alive.
			if stripped, changed := stripEncryptedReasoning(body); changed {
				finishLease(lease, relayapp.AttemptOutcome{Kind: relayapp.AttemptRequestError})
				body = stripped
				server.observeRetry(activityID, attempt, status, 0)
				continue
			}
		}
		switch {
		case balanceUnavailable(status, errorBody, rateLimited) && !sharedPoolExhausted(errorBody):
			finishLease(lease, relayapp.AttemptOutcome{Kind: relayapp.AttemptBalanceExhausted})
			credentialFailures++
			if !server.canRotateCredential(attempt, attemptLimit, lease, credentialFailures, route.ProviderID) {
				if switchRoute(status) {
					continue
				}
				return genericErrorResponse(status), nil
			}
			if err := server.retryCredentialFailure(ctx, lease, activityID, attempt, status, retryDelay(attempt, nil, server.config)); err != nil {
				return nil, err
			}
			continue
		case balanceUnavailable(status, errorBody, rateLimited) && sharedPoolExhausted(errorBody):
			// The batch quota is the provider's, not any key's: rotating would
			// walk the request through keys that all answer the same way, and
			// the midnight ban this class used to file froze every working
			// model behind a verdict no key was responsible for. A sibling
			// provider still hosts the model, so the chain is worth one walk.
			finishLease(lease, relayapp.AttemptOutcome{Kind: relayapp.AttemptRequestError})
			if switchRoute(status) {
				continue
			}
			return genericErrorResponse(status), nil
		case (status == http.StatusNotFound || status == http.StatusForbidden) && modelMissing(errorBody, model):
			// The provider does not host the model at all. Retrying this
			// provider is pointless and blocking the keys would only stall the
			// next request as well. Some gateways answer this with 403 rather
			// than 404, so the check runs before the authentication rotation
			// below. The chain's whole point is that a sibling hosts it.
			finishLease(lease, relayapp.AttemptOutcome{Kind: relayapp.AttemptRequestError})
			if switchRoute(status) {
				continue
			}
			return genericErrorResponse(status), nil
		case (status == http.StatusNotFound || status == http.StatusForbidden) && modelUnavailable(errorBody, model, status):
			finishLease(lease, relayapp.AttemptOutcome{Kind: relayapp.AttemptModelUnavailable, Model: model})
			credentialFailures++
			if !server.canRotateCredential(attempt, attemptLimit, lease, credentialFailures, route.ProviderID) {
				if switchRoute(status) {
					continue
				}
				return genericErrorResponse(status), nil
			}
			// Same rule as the authentication rotation below: the next attempt
			// takes over only from a key that is free right now instead of
			// parking on the model blocks other requests left behind.
			rotateImmediate = true
			rotationStatus = status
			if err := server.retryCredentialFailure(ctx, lease, activityID, attempt, status, retryDelay(attempt, nil, server.config)); err != nil {
				return nil, err
			}
			continue
		case (status == http.StatusUnauthorized || status == http.StatusForbidden) && clientBlocked(errorBody):
			// A verdict about the client, not the credential: every key answers
			// it identically, so rotation is wasted attempts and the first
			// refusal's cooldown would freeze models that work behind one
			// probe that does not. The provider is gone for this client — the
			// chain is the only move left.
			finishLease(lease, relayapp.AttemptOutcome{Kind: relayapp.AttemptRequestError})
			if switchRoute(status) {
				continue
			}
			return genericErrorResponse(status), nil
		case status == http.StatusUnauthorized || status == http.StatusForbidden:
			// The first refusal cools the key that produced it. A second refusal
			// inside the same request is evidence about the verdict, not the key:
			// every key answers the same way, so the key stays usable for other
			// requests and only this one pays.
			if credentialFailures == 0 {
				finishLease(lease, relayapp.AttemptOutcome{Kind: relayapp.AttemptAuthentication})
			} else {
				finishLease(lease, relayapp.AttemptOutcome{Kind: relayapp.AttemptRequestError})
			}
			credentialFailures++
			if !server.canRotateCredential(attempt, attemptLimit, lease, credentialFailures, route.ProviderID) {
				// The whole pool refused to authenticate: no key of this
				// provider will change that, but a sibling's might.
				if switchRoute(status) {
					continue
				}
				return genericErrorResponse(status), nil
			}
			// The rotation takes over only from a key that is free right now.
			// Parking it on the cooldowns other requests left behind is what
			// turned one refused answer into tens of minutes of queueing.
			rotateImmediate = true
			rotationStatus = status
			if err := server.retryCredentialFailure(ctx, lease, activityID, attempt, status, retryDelay(attempt, nil, server.config)); err != nil {
				return nil, err
			}
			continue
		case status == http.StatusNotFound && route.Format == "auto" && chatActive != nil && *chatActive:
			// The provider was already proven chat-only, yet the chat path 404s.
			// aieva-style gateways (aieva.io, nele.ai) serve chat at a custom
			// /chat-completion path, so give that one attempt before giving up
			// and forgetting the probe.
			//
			// The lease is finished on BOTH exits. It used to be finished on neither,
			// and a lease left open is a credential the pool still believes is in
			// flight: a provider whose endpoint 404s would leak one key per attempt
			// until every key it owns was marked busy and the next request had none to
			// take. The wrong endpoint is a configuration mistake, so it happens on
			// every request until someone notices — which is exactly how long the pool
			// had to survive it.
			finishLease(lease, relayapp.AttemptOutcome{Kind: relayapp.AttemptRequestError})
			if chatPathFallback(incoming.URL.Path, route.ChatPath) {
				incoming.URL.Path = chatCompletionFallbackPath
				incoming.URL.RawPath = ""
				continue
			}
			server.chatOnly.Delete(route.ProviderID)
			return genericErrorResponse(status), nil
		case status == http.StatusNotFound && route.Format == "auto" && !modelUnavailable(errorBody, model, status) && endpointMissing404(status, errorBody) && canonicalPath(incoming.URL.Path) == responsesPath:
			finishLease(lease, relayapp.AttemptOutcome{Kind: relayapp.AttemptRequestError})
			server.chatOnly.Store(route.ProviderID, struct{}{})
			chatPath, translated, ok, chatErr := prepareChatCompletions(incoming.Method, incoming.URL.Path, body, incoming.Header.Get("Content-Type"), route.ChatPath, true)
			if chatErr != nil || !ok {
				server.chatOnly.Delete(route.ProviderID)
				return genericErrorResponse(status), nil
			}
			body = translated
			incoming.URL.Path = chatPath
			incoming.URL.RawPath = ""
			if terminalStream != nil {
				*terminalStream = true
			}
			if chatActive != nil {
				*chatActive = true
			}
			continue
		}

		finishLease(lease, relayapp.AttemptOutcome{Kind: relayapp.AttemptRequestError})
		if status == http.StatusRequestEntityTooLarge {
			return genericErrorResponse(status), nil
		}
		if refused {
			rejection := genericErrorResponse(status)
			rejection.Header.Set("X-Switchboard-Terminal", terminalRefused)
			return rejection, nil
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

// canonicalPath is the spelling every endpoint decision in this package is made
// against. The relay answers on its own ServeHTTP rather than through a ServeMux, so
// nothing has cleaned the caller's path by the time it arrives: `/v1//responses`,
// `/v1/./responses` and `/V1/Responses` all reach the handler verbatim. Compared raw,
// each of them silently turned the relay into a plain forwarder for that request —
// no tool folding for a Codex-style declaration, no image or chat compatibility, no
// stream dialect, and no inspection of an answer whose label contradicts its path.
// Measured: `/V1/Responses` served a refusable payload with a 200 and zero findings.
//
// Only decisions use this. The upstream request keeps its own copy of the caller's
// URL, so what the provider receives is still exactly what the client sent.
func canonicalPath(path string) string {
	return strings.ToLower(pathpkg.Clean("/" + path))
}

func expectsJSONResponse(path string) bool {
	switch canonicalPath(path) {
	case "/v1/responses", "/v1/chat/completions", "/v1/completions", "/v1/messages", "/v1/images/generations", "/v1/images/edits":
		return true
	default:
		return false
	}
}

// emptyUpstreamAnswer reports whether an inference endpoint answered 200 with no
// body. Only the inference paths count: an empty list is a legitimate answer from
// a catalog or an image endpoint, while a chat turn with nothing in it is a
// generation that never happened.
//
// Only a plain 200 counts. A 201 or 202 with no body is how an API says it accepted
// the work, and that is an answer, not a lost one.
//
// A well-formed envelope with no items is deliberately not counted. It carries
// the provider's own account of the turn - finish reason, filtered content,
// usage - and retrying it would spend the budget on a decision the provider
// already made.
func emptyUpstreamAnswer(body []byte, path string, status int) bool {
	if status != http.StatusOK {
		return false
	}
	switch canonicalPath(path) {
	case "/v1/responses", "/v1/chat/completions", "/v1/completions", "/v1/messages":
		return len(bytes.TrimSpace(body)) == 0
	default:
		return false
	}
}

// sensitiveCredentialMarkers lists what must never reach a public reader. All of it
// is secret except one value: the proxy hostname identifies infrastructure the way a
// provider hostname does, and the public sanitizer refuses any answer a marker
// survives in, so a short one would refuse every answer instead of being removed from
// one. Measured — `socks5://tor:9050`, an ordinary container name, produced the marker
// "tor", which is a substring of constructor, monitor, iterator, vector and editor.
// The full proxy URL still carries the identity and is always long enough to redact.
//
// The secrets stay unfiltered on purpose. A two-character key is unlikely and a
// leaked one is unrecoverable, so those refuse at any width.
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
	if host := proxy.Hostname(); len(host) >= relayapp.MinRedactableMarkerBytes {
		markers = append(markers, host)
	}
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

// guardrailAttempts is how many times a request may be sent again after its answer
// was refused. A refusal is a wasted attempt, so it spends the same budget as a
// provider that failed out loud rather than a budget of its own — bounded, because
// every extra attempt is another roll of the dice for a provider that is trying.
func (server *Server) guardrailAttempts() int {
	return max(server.config.PermanentAttempts, 1)
}

// canRetry reports whether another attempt is allowed.
//
// A caller that sets no limit still gets a ceiling. Without one a provider that
// answers 429 or 5xx indefinitely keeps the attempt loop running, and on a
// streaming request the keep-alives hold the caller there with it: history holds
// single requests that retried two thousand times across half a day. The cap is
// loose enough that a pool-wide rate limit still has room to clear, since the
// delay tops out at RetryMax, and tight enough that a provider which is simply
// gone ends the request rather than the day.
func canRetry(attempt, limit int) bool {
	if limit > 0 {
		return attempt+1 < limit
	}
	return attempt+1 < maxRelayAttempts
}

// canRotateCredential reports whether a credential-level rejection - bad key,
// spent balance, model the account cannot reach - is worth another key.
// Passthrough routes have no pool and spend the permanent-attempt budget.
// A credentialed route gets one attempt per key: every rejection cools the key
// that produced it, so once the pool has answered the same way throughout,
// waiting for those cooldowns to lapse would only park the request until the
// whole request times out.
func (server *Server) canRotateCredential(attempt, limit int, lease relayapp.CredentialLease, failures int, providerID string) bool {
	if !canRetry(attempt, limit) {
		return false
	}
	if lease == nil {
		return attempt+1 < server.config.PermanentAttempts
	}
	return failures < server.credentialCount(providerID)
}

// credentialCount returns how many keys the provider has, falling back to the
// permanent-attempt budget for sources that do not report a count.
func (server *Server) credentialCount(providerID string) int {
	counter, ok := server.credentials.(relayapp.CredentialCounter)
	if !ok {
		return max(server.config.PermanentAttempts, 1)
	}
	return max(counter.Count(providerID), 1)
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

// tryAcquireCredential takes the next rotation key without queueing.
// Passthrough routes hold no keys, so there is nothing to wait on and the
// rotation always proceeds.
func (server *Server) tryAcquireCredential(route relayapp.Route, model string) (relayapp.CredentialLease, relayapp.Credential, bool) {
	if route.AuthMode == "passthrough" {
		return nil, relayapp.Credential{}, true
	}
	if server.credentials == nil {
		return nil, relayapp.Credential{}, false
	}
	lease, ok := server.credentials.TryAcquire(route.ProviderID, model)
	if !ok {
		return nil, relayapp.Credential{}, false
	}
	return lease, lease.Credential(), true
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
		path := canonicalPath(request.URL.Path)
		anthropic := route.Dialect == "anthropic" || (route.Dialect != "openai" && (path == "/v1/messages" || strings.HasSuffix(path, "/v1/messages") || strings.Contains(path, "/v1/messages/") || request.Header.Get("Anthropic-Version") != ""))
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
	request.Header.Del("Accept-Encoding")
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
	path := canonicalPath(request.URL.Path)
	if path != "/v1/responses" && path != "/v1/chat/completions" && path != "/v1/completions" && path != "/v1/messages" {
		return false
	}
	var payload struct {
		Stream bool `json:"stream"`
	}
	return json.Unmarshal(body, &payload) == nil && payload.Stream
}

// nonStreamingResponsesRequest is the last compatibility attempt for providers
// whose SSE connection repeatedly closes before response.completed. The full JSON
// response is converted back to SSE before it reaches the caller, so a client that
// asked for a stream keeps the same wire contract.
func nonStreamingResponsesRequest(body []byte, maxBytes int64) ([]byte, bool) {
	var payload map[string]any
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if decoder.Decode(&payload) != nil || payload == nil {
		return nil, false
	}
	stream, ok := payload["stream"].(bool)
	if !ok || !stream {
		return nil, false
	}
	payload["stream"] = false
	encoded, err := json.Marshal(payload)
	if err != nil || (maxBytes > 0 && int64(len(encoded)) > maxBytes) {
		return nil, false
	}
	return encoded, true
}

func responsesJSONToSSE(body []byte) ([]byte, string, error) {
	var payload map[string]any
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if decoder.Decode(&payload) != nil || payload == nil {
		return nil, "", errInvalidFallbackResponse
	}
	terminal := "response.completed"
	status, _ := payload["status"].(string)
	if status == "completed" && (payload["error"] != nil || payload["incomplete_details"] != nil) {
		return nil, "", errInvalidFallbackResponse
	}
	if status != "completed" {
		switch status {
		case "failed", "cancelled":
			terminal = "response.failed"
		case "incomplete":
			terminal = "response.incomplete"
		default:
			if payload["error"] != nil {
				terminal = "response.failed"
			} else {
				return nil, "", errInvalidFallbackResponse
			}
		}
	}
	event := map[string]any{"type": terminal, "response": payload}
	encoded, err := json.Marshal(event)
	if err != nil {
		return nil, "", errInvalidFallbackResponse
	}
	return append(append([]byte("event: "+terminal+"\ndata: "), encoded...), []byte("\n\n")...), terminal, nil
}

type bodyRead struct {
	chunk []byte
	err   error
}

func bufferTerminalSSE(ctx context.Context, response *http.Response, path string, config Config) (string, []byte, relayapp.TokenUsage, error) {
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
	limit := responseBufferLimit(config)
	buffered := make([]byte, 0, min(limit, 1024*1024))
	inspector := &sseInspector{path: canonicalPath(path)}
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
					return "", nil, relayapp.TokenUsage{}, errResponseTooLarge
				}
				buffered = append(buffered, result.chunk...)
				previousTerminal := inspector.terminal
				if terminal := inspector.Feed(result.chunk); terminal != "" && previousTerminal == "" {
					if terminal == "response.invalid" {
						return "", nil, relayapp.TokenUsage{}, errIncompleteSSE
					}
					if inspector.retryableFailure() {
						return terminal, buffered, inspector.usage, errRetryableSSEFailure
					}
					return inspector.reason(), buffered, inspector.usage, nil
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
				previousTerminal := inspector.terminal
				if terminal := inspector.Finish(); terminal != "" && previousTerminal == "" {
					if terminal == "response.invalid" {
						return "", nil, relayapp.TokenUsage{}, errIncompleteSSE
					}
					if inspector.retryableFailure() {
						return terminal, buffered, inspector.usage, errRetryableSSEFailure
					}
					return inspector.reason(), buffered, inspector.usage, nil
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
	limit := responseBufferLimit(config)
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
				return nil, errResponseTooLarge
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
				if !errors.Is(result.err, io.EOF) {
					return nil, errIncompleteSSE
				}
				if len(buffered) == 0 {
					return buffered, nil
				}
				if !json.Valid(buffered) {
					return nil, errIncompleteSSE
				}
				return buffered, nil
			}
		}
	}
}

type sseInspector struct {
	path      string
	line      []byte
	event     []byte
	eventName string
	terminal  string
	finished  bool
	output    bool
	refused   bool
	usage     relayapp.TokenUsage
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
	if inspector.terminal == "" && inspector.finished && (inspector.path == "/v1/chat/completions" || inspector.path == "/v1/completions") {
		inspector.terminal = "done"
	}
	return inspector.terminal
}

func (inspector *sseInspector) consumeLine(line []byte) {
	if len(line) == 0 {
		inspector.finishEvent()
		return
	}
	if !bytes.HasPrefix(line, []byte("data:")) {
		if bytes.HasPrefix(line, []byte("event:")) {
			inspector.eventName = strings.TrimSpace(string(bytes.TrimPrefix(line, []byte("event:"))))
		}
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
		inspector.eventName = ""
		return
	}
	data := inspector.event
	inspector.event = nil
	eventName := inspector.eventName
	inspector.eventName = ""
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
	if eventType == "" {
		eventType = typelessEventType(eventName, payload)
	}
	if inspector.path == "/v1/responses" && (strings.HasSuffix(eventType, ".delta") || strings.HasSuffix(eventType, ".partial_image")) {
		inspector.output = true
	}
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
	if eventType == "response.failed" || eventType == "response.incomplete" {
		inspector.refused = policyRefused(refusalText(payload))
	}
	if eventType == "response.completed" || eventType == "response.failed" || eventType == "response.incomplete" {
		inspector.terminal = eventType
	}
	if eventType == "message_stop" && inspector.path == "/v1/messages" {
		inspector.terminal = eventType
	}
	if inspector.path == "/v1/chat/completions" || inspector.path == "/v1/completions" {
		if choices, ok := payload["choices"].([]any); ok {
			for _, choice := range choices {
				item, _ := choice.(map[string]any)
				if reason, ok := item["finish_reason"].(string); ok && reason != "" {
					inspector.finished = true
					break
				}
			}
		}
	}
}

func (inspector *sseInspector) retryableFailure() bool {
	return inspector.path == "/v1/responses" && inspector.terminal == "response.failed" && !inspector.output && !inspector.refused
}

// typelessEventType names typeless stream data from its `event:` line, but only
// when the payload carries the response object that line reports on. A bare
// `event: response.completed` over `data: {}` used to fail the whole stream as
// invalid, and a bare `event: response.output_text.delta` over `{}` counted as
// output and kept a failed stream from the retry it was owed — so typeless data
// without a response object is ignored instead of named.
func typelessEventType(eventName string, payload map[string]any) string {
	if eventName == "" {
		return ""
	}
	response, ok := payload["response"].(map[string]any)
	if !ok || response == nil {
		return ""
	}
	return eventName
}

// reason is the terminal as the rest of the relay files it. A refusal keeps a
// name of its own so the operator reads what happened instead of "the provider
// failed", and so the retry ladder above never mistakes a verdict for a fault.
func (inspector *sseInspector) reason() string {
	if inspector.refused {
		return terminalRefused
	}
	return inspector.terminal
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
	if policyRefused(refusalText(object)) {
		return terminalRefused
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
	path = canonicalPath(path)
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

// copyResponseHeaders forwards the provider's headers to the client, minus the ones
// that are ours to decide and the ones that carry the provider's secrets rather than
// its answer.
//
// The whole point of the relay is that the client never learns a provider key: it
// authenticates to Switchboard and Switchboard authenticates upstream. A provider that
// echoes the request's `Authorization` back in a response header — carelessly, or on
// purpose — handed that key straight to the client, because every header that was not a
// hop header was copied verbatim. Anything whose value contains a credential is dropped
// rather than masked: a masked header still tells the client a key was there, and the
// header was never part of the answer to begin with.
//
// `Set-Cookie` goes too. It is the provider's session with US, the client has no use
// for it, and forwarding it lets a provider set state in whatever the client happens to
// be — a browser-based one included.
func copyResponseHeaders(target, source http.Header, secrets []string) {
	for name, values := range source {
		lower := strings.ToLower(name)
		if _, blocked := hopHeaders[lower]; blocked {
			continue
		}
		if lower == "set-cookie" || lower == "set-cookie2" {
			continue
		}
		for _, value := range values {
			if containsSecret(value, secrets) {
				continue
			}
			target.Add(name, value)
		}
	}
}

// containsSecret reports whether a header value quotes something the client must not
// learn. Comparison is on the raw value: a credential travels verbatim in the header
// that leaked it, and a marker short enough to appear in ordinary text is not a
// credential worth protecting.
func containsSecret(value string, secrets []string) bool {
	for _, secret := range secrets {
		if len(secret) >= minSecretMatchBytes && strings.Contains(value, secret) {
			return true
		}
	}
	return false
}

func joinPath(base, request string) string {
	base = strings.TrimRight(base, "/")
	if base == "" || request == base || strings.HasPrefix(request, base+"/") {
		if request == "" {
			return "/"
		}
		return request
	}
	if strings.HasSuffix(base, "/v1") && (request == "/v1" || strings.HasPrefix(request, "/v1/")) {
		return base + strings.TrimPrefix(request, "/v1")
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

// policyRefusalMarkers name a provider's content policy answering, as opposed to
// the provider failing to answer. Each is a phrase a moderation layer produces and
// ordinary faults do not: bare "policy" or "safety" also appear in quota and
// rate-limit prose, and matching those would end a request that another attempt
// would have served.
var policyRefusalMarkers = []string{
	"content_policy", "content policy",
	"content_filter", "content filter",
	"invalid_prompt",
	"moderation",
	"usage polic",
	"safety system",
	"flagged for possible",
	"trusted access for cyber",
}

// policyRefused reports whether text is a provider refusing the content rather
// than failing to serve it. The difference decides whether the request is sent
// again: a fault is worth another attempt, while a refusal is the provider's
// decision about this exact payload. Repeating it cannot change the answer, and
// repeating it is what walks one refused request through every key in the pool -
// so a refusal ends the request on the attempt that earned it, with the
// provider's own words left intact for the caller to read.
func policyRefused(text string) bool {
	text = strings.ToLower(text)
	for _, marker := range policyRefusalMarkers {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

// refusalText is the failure a terminal event reports, and nothing else. Reading
// the whole payload would let an answer that merely discusses content policy be
// filed as one refused by it.
func refusalText(payload map[string]any) string {
	candidates := []any{payload["error"], payload["incomplete_details"], payload["message"]}
	if response, ok := payload["response"].(map[string]any); ok {
		candidates = append(candidates, response["error"], response["incomplete_details"])
	}
	text := strings.Builder{}
	for _, candidate := range candidates {
		if candidate == nil {
			continue
		}
		if encoded, err := json.Marshal(candidate); err == nil {
			text.Write(encoded)
		}
	}
	return text.String()
}

// freeformToolRejected reports a refusal aimed at the freeform ("custom") tool
// type rather than at the request in general. Only a complaint that names the
// type or its grammar counts: a wrong guess would downgrade the tools of a
// provider that supports them, and the model would lose the grammar its payload
// has to follow.
func freeformToolRejected(errorBody []byte) bool {
	if len(errorBody) == 0 {
		return false
	}
	text := strings.ToLower(string(errorBody))
	for _, marker := range []string{"custom", "freeform", "grammar", "lark"} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

// repairRejectedParameters rewrites a request the provider turned down over a
// parameter its newer models no longer accept, and reports whether anything
// changed. GPT-5 and the o-series answer 400 for max_tokens ("use
// max_completion_tokens instead") and for any temperature or top_p other than
// the default. The rewrite only happens when the error names the parameter and
// the body actually carries it, so a genuine 400 still fails at once and the
// retry cannot cycle: every pass drops a field it can never drop again.
func repairRejectedParameters(body, errorBody []byte) ([]byte, bool) {
	if len(body) == 0 || len(errorBody) == 0 {
		return body, false
	}
	text := strings.ToLower(string(errorBody))
	if !strings.Contains(text, "unsupported") && !strings.Contains(text, "not support") {
		return body, false
	}
	var payload map[string]any
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if decoder.Decode(&payload) != nil {
		return body, false
	}
	changed := false
	if tokens, carried := payload["max_tokens"]; carried && strings.Contains(text, "max_completion_tokens") {
		delete(payload, "max_tokens")
		payload["max_completion_tokens"] = tokens
		changed = true
	}
	for _, field := range []string{"temperature", "top_p"} {
		if _, carried := payload[field]; carried && strings.Contains(text, field) {
			delete(payload, field)
			changed = true
		}
	}
	if !changed {
		return body, false
	}
	repaired, err := json.Marshal(payload)
	if err != nil {
		return body, false
	}
	return repaired, true
}

// modelMissing reports a 404 that names the requested model and says it simply
// is not there. No key can conjure a model the provider does not host, so the
// relay answers straight away instead of rotating the pool and blocking every
// key for the model cooldown, which would stall later requests too.
func modelMissing(body []byte, model string) bool {
	if model == "" || len(body) == 0 {
		return false
	}
	text := strings.ToLower(string(body))
	if !strings.Contains(text, strings.ToLower(model)) {
		return false
	}
	for _, marker := range []string{
		"does not exist", "doesn't exist", "no such model",
		"unknown model", "model not found", "invalid model",
	} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

// modelUnavailable reports a 404 or 403 the account, not the catalogue, is
// responsible for: the model exists but this key may not call it. Another key
// can succeed, so the request rotates through the pool.
//
// A 403 that names the model is a verdict about the model whatever the wording:
// the token is not entitled to it, the plan does not carry it, the daily batch
// for it is spent (measured: `403 该令牌无权访问模型 gpt-5`; resellers reword
// these per release, so the model's own name in a forbidden verdict is the one
// stable signal). It is still a rotation rather than a refusal, because
// another account's key may hold the entitlement — but it blocks the model,
// not the key, so the key stays usable for everything else. A 404 keeps its
// marker list: on a not-found endpoint the wording is what separates a missing
// model from a plan restriction.
func modelUnavailable(body []byte, model string, status int) bool {
	if model == "" || len(body) == 0 {
		return false
	}
	text := strings.ToLower(string(body))
	if !strings.Contains(text, strings.ToLower(model)) {
		return false
	}
	if status == http.StatusForbidden {
		return true
	}
	return strings.Contains(text, "not available on your plan") ||
		strings.Contains(text, "not available for your account") ||
		strings.Contains(text, "no access") ||
		strings.Contains(text, "access denied") ||
		strings.Contains(text, "not entitled")
}

// sharedPoolExhausted reports a billing verdict about the provider's shared
// pool rather than the key's own balance: the reseller releases GPT/Claude
// capacity in daily batches, and "Budget pool quota has been exhausted" means
// the batch is spent for everyone (measured, and stated in their own
// announcements). Banning keys until midnight for it froze the whole pool
// behind a verdict no key was responsible for.
func sharedPoolExhausted(body []byte) bool {
	if len(body) == 0 {
		return false
	}
	text := strings.ToLower(string(body))
	return strings.Contains(text, "budget pool") ||
		strings.Contains(text, "pool quota") ||
		strings.Contains(text, "quota pool") ||
		strings.Contains(text, "shared pool")
}

// clientBlocked reports an auth verdict about the caller's client rather than
// the credential: reseller edges answer "unauthorized client detected" the
// same way for every key, so rotating is wasted attempts and cooling the first
// key freezes models that work behind one that does not (measured: a
// /v1/models probe rotated nine keys on this verdict).
func clientBlocked(body []byte) bool {
	if len(body) == 0 {
		return false
	}
	text := strings.ToLower(string(body))
	return strings.Contains(text, "unauthorized client") ||
		strings.Contains(text, "unauthorized_client") ||
		strings.Contains(text, "unauthorized client error")
}

// errorTextReplacer unpunctuates error prose for the classifiers below. One
// compiled replacer for every failed attempt instead of one per check.
var errorTextReplacer = strings.NewReplacer("_", " ", "-", " ")

// normalizeErrorText lowers an error body once for the classifiers below. Every
// failed attempt used to pay this lowering-plus-unpunctuation pass per check
// (rate, then balance, then balance's own inner rate check); now the retry path
// pays it once and hands the result down.
//
// Only the rate/balance verdicts share it. policyRefused and the repair matchers
// keep reading the raw body: their markers name identifiers with underscores
// ("invalid_prompt", "max_completion_tokens") that this pass turns into spaces.
func normalizeErrorText(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	return errorTextReplacer.Replace(strings.ToLower(string(body)))
}

// billingExhaustionMarkers name a spent account rather than a throttled key: the
// quota is gone until the next billing period, so the key waits out the midnight
// ban instead of rotating through the pool on a short cooldown. They win over any
// rate wording — "You exceeded your current quota, please check your plan"
// carries "exceeded" plus "quota" and used to read as throttling, retrying every
// key for an account that has nothing left.
//
// Bare "sufficient"/"insufficient" is deliberately not one of them: it also
// matches "insufficient permissions", a 403 about access rather than money that
// must keep rotating as an authentication failure instead of banning the key
// until midnight. The "insufficient balance/funds/quota" markers below already
// cover genuine billing exhaustion worded that way.
var billingExhaustionMarkers = []string{
	"billing period",
	"current quota",
	"check your plan",
	"check your billing",
	"billing details",
	"top up",
	"topup",
}

func balanceUnavailable(status int, body []byte, rateLimited bool) bool {
	if status == http.StatusPaymentRequired {
		// A 402 is the provider's billing verdict and stays authoritative even
		// when the prose around it mentions limits.
		return true
	}
	if status < 400 || status >= 500 || len(body) == 0 {
		return false
	}
	text := normalizeErrorText(body)
	for _, marker := range billingExhaustionMarkers {
		if strings.Contains(text, marker) {
			return true
		}
	}
	// rateLimited is the verdict the caller already computed on this same body:
	// taking it as a parameter keeps this check to its own single scan instead
	// of re-running the rate classifier (and its normalization) from scratch.
	if rateLimited {
		return false
	}
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

// rateLimitedBody reports whether an error body carries a rate-limit verdict
// rather than any other failure. The signal wins over every other
// classification except billing: a 503 that says "rate limit" is a throttled key,
// not a dead server (blind 5xx retries burned 64 attempts per request while the
// heartbeat held the client stream open), and a 4xx with quota wording is a
// throttled key, not a dead balance (which bans the key until Moscow midnight,
// shown in the UI as a ~1200-minute cooldown). A 402 never reaches this check on
// the retry path — the provider's billing verdict stays authoritative even when
// its prose mentions limits. Genuine billing exhaustion (billing-period and
// "check your plan" wording, "insufficient balance/funds/quota" with no rate
// wording) and content-policy verdicts deliberately do not match, so none of
// them is downgraded to a retry.
func rateLimitedBody(body []byte) bool {
	return rateLimitedText(normalizeErrorText(body))
}

// rateLimitedText is rateLimitedBody on prose the caller already normalized, so a
// failed attempt pays the lowering pass once no matter how many classifiers read it.
func rateLimitedText(text string) bool {
	if text == "" {
		return false
	}
	// Billing exhaustion borrows quota wording ("You exceeded your current quota,
	// please check your plan"), so it is excluded before any rate marker runs.
	for _, marker := range billingExhaustionMarkers {
		if strings.Contains(text, marker) {
			return false
		}
	}
	for _, marker := range []string{
		"rate limit", "ratelimit",
		"too many requests",
		"throttl",
		"request limit",
		"quota exceeded",
	} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	// "you have exceeded your request/quota/..." states the verb before the
	// noun, so the check is ordered: "exceed" first, then what was exceeded.
	// "limit" is not one of those nouns on purpose: by itself it also matches
	// permanent caps on this payload ("input exceeds token limit", "context size
	// exceeds limit"), and matching those rotated keys to the ceiling for a
	// request no key can serve.
	if index := strings.Index(text, "exceed"); index >= 0 {
		rest := text[index+len("exceed"):]
		for _, noun := range []string{"request", "rate", "quota"} {
			if strings.Contains(rest, noun) {
				return true
			}
		}
	}
	return false
}

// serviceOverloaded reports a 5xx whose prose names congestion rather than
// breakage. These answers resolve on their own once a channel frees up, so the
// request retries instead of surfacing the status. Wording is taken from the
// resellers this relay actually serves: new-api's Chinese saturation message
// (measured on the reseller) and the English overload idioms.
func serviceOverloaded(text string) bool {
	if text == "" {
		return false
	}
	for _, marker := range []string{
		"overloaded", "overload", "no channel", "try again later", "try again",
		"resource exhausted", "temporarily unavailable", "server is busy",
		"capacity", "saturated", "backpressure",
		"负载已饱和", "请稍后再试", "无可用渠道", "繁忙",
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

func errorText(err error, fallback string) string {
	if err != nil {
		return err.Error()
	}
	return fallback
}

// responseErrorDetail keeps the provider's terminal error reason available without
// copying streamed output into activity history, then restores the body for the
// normal response path. Only the scalar code and message are kept, with any
// credential echoed back by the provider redacted: the full error object is
// provider-controlled, and filing it verbatim persisted whatever it quoted.
func responseErrorDetail(response *http.Response, fallback string, secrets []string) string {
	if response == nil || response.Body == nil {
		return fallback
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 64*1024))
	response.Body = readCloser{Reader: io.MultiReader(bytes.NewReader(body), response.Body), Closer: response.Body}
	if err != nil || len(bytes.TrimSpace(body)) == 0 {
		return fallback
	}
	if detail := terminalErrorDetail(body, secrets); detail != "" {
		return detail
	}
	return fallback
}

func terminalErrorDetail(body []byte, secrets []string) string {
	for _, block := range bytes.Split(bytes.ReplaceAll(body, []byte("\r\n"), []byte("\n")), []byte("\n\n")) {
		data := sseData(block)
		if len(data) == 0 {
			continue
		}
		if detail := jsonErrorDetail(data, secrets); detail != "" {
			return detail
		}
	}
	return jsonErrorDetail(body, secrets)
}

// maxErrorDetailRunes bounds the provider text filed in activity history. It
// matches the activity store cap: the filed reason is the raw upstream body,
// not an extract, so anything the provider answered travels. A history pull
// that still exceeds the protocol frame degrades to response_too_large on that
// command — it never takes the sidecar down.
const maxErrorDetailRunes = 4096

const redactedSecret = "[redacted]"

// minSecretMatchBytes is the shortest credential fragment worth scrubbing out of
// filed text. Shorter fragments also match ordinary prose, so editing those here
// would mangle the reason; they stay for the public sanitizer to fail closed on.
const minSecretMatchBytes = 8

// noteUpstreamDetail keeps the terminal failure's raw upstream body for the
// call that ends on it. No extract, no field filter: every error the provider
// answered travels — a code, a message, a reseller envelope, plain prose. It
// always overwrites, even with an empty body: a later attempt that answered
// unreadably must not leave an earlier attempt's words filed as this
// request's reason. Redaction and the rune bound apply at the boundary before
// filing, never here.
func noteUpstreamDetail(into *string, body []byte) {
	if into == nil {
		return
	}
	*into = strings.TrimSpace(string(body))
}

func jsonErrorDetail(body []byte, secrets []string) string {
	var payload map[string]any
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if decoder.Decode(&payload) != nil || payload == nil {
		return ""
	}
	candidates := []any{payload["error"], payload["incomplete_details"]}
	if response, ok := payload["response"].(map[string]any); ok {
		candidates = append(candidates, response["error"], response["incomplete_details"])
	}
	// Reseller envelopes also carry the reason beside the error object: a
	// top-level message (the reseller) or a code/msg pair (its current API).
	// The envelope itself is read before the bare strings, so a code/msg pair
	// files as "401: Invalid API Key!" rather than losing its code — and a
	// present error object still wins over all of it.
	candidates = append(candidates, payload, payload["message"], payload["msg"])
	for _, candidate := range candidates {
		if detail := errorCandidateDetail(candidate); detail != "" {
			return truncateErrorDetail(redactSecrets(detail, secrets))
		}
	}
	return ""
}

// errorCandidateDetail reads one error-shaped value the way the history files
// it: a string travels as itself, an object contributes only its scalar code
// and message, and anything else (nil, numbers, arrays, nested objects) is not
// a reason and is skipped.
func errorCandidateDetail(candidate any) string {
	switch candidate := candidate.(type) {
	case string:
		return candidate
	case map[string]any:
		return errorObjectDetail(candidate)
	default:
		return ""
	}
}

// errorObjectDetail keeps the code and the message and drops everything else.
// type stands in for a missing code; a message on its own still travels. msg
// is the same field under its reseller name.
func errorObjectDetail(candidate map[string]any) string {
	code := errorScalarText(candidate["code"])
	if code == "" {
		code = errorScalarText(candidate["type"])
	}
	message := errorScalarText(candidate["message"])
	if message == "" {
		message = errorScalarText(candidate["msg"])
	}
	switch {
	case code != "" && message != "":
		return code + ": " + message
	case code != "":
		return code
	default:
		return message
	}
}

func errorScalarText(value any) string {
	switch value := value.(type) {
	case string:
		return value
	case json.Number:
		return value.String()
	default:
		return ""
	}
}

// redactSecrets scrubs the credentials of the attempt in flight out of text
// about to be filed. A provider that echoes the key it was sent must not have
// that key persisted into history and rendered back in the UI.
func redactSecrets(value string, secrets []string) string {
	for _, secret := range secrets {
		if len(secret) >= minSecretMatchBytes && strings.Contains(value, secret) {
			value = strings.ReplaceAll(value, secret, redactedSecret)
		}
	}
	return value
}

func truncateErrorDetail(value string) string {
	runes := []rune(strings.TrimSpace(value))
	if len(runes) > maxErrorDetailRunes {
		return string(runes[:maxErrorDetailRunes])
	}
	return string(runes)
}

// addDiscardedUsage folds the cost of an attempt that was thrown away into the cost
// of the request as a whole.
//
// A provider bills for every answer it generates, including the ones nobody reads: an
// answer it called `failed`, an empty 200, a stream that died mid-flight, an answer the
// guardrails refused. All of those are retried, and usage used to be read off the
// surviving attempt alone — so a request that cost two generations was recorded as one,
// and the history the owner checks their spend against was quietly short by however many
// attempts it took.
//
// Billing totals add. ContextTokens does not: it is the size of the window the request
// occupied, the same window each time it was sent, so it takes the largest attempt
// rather than their sum.
func addDiscardedUsage(total *relayapp.TokenUsage, attempt relayapp.TokenUsage) {
	total.InputTokens += attempt.InputTokens
	total.OutputTokens += attempt.OutputTokens
	total.CachedTokens += attempt.CachedTokens
	total.ReasoningTokens += attempt.ReasoningTokens
	total.TotalTokens += attempt.TotalTokens
	total.ContextTokens = max(total.ContextTokens, attempt.ContextTokens)
}

// usageWith is addDiscardedUsage as a value, for the call sites that report a total
// without keeping one.
func usageWith(discarded, attempt relayapp.TokenUsage) relayapp.TokenUsage {
	addDiscardedUsage(&discarded, attempt)
	return discarded
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

func responseBufferLimit(config Config) int64 {
	return min(max(config.MaxRequestBytes*4, 8*1024*1024), int64(maxBufferedResponseBytes))
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
			return normalizeRequestModel(payload.Model)
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
			return normalizeRequestModel(string(value))
		}
	}
	return ""
}

func normalizeRequestModel(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > maxRequestModelBytes {
		return ""
	}
	for _, character := range value {
		if character < 32 || character == 127 {
			return ""
		}
	}
	return value
}

func rewriteRequestModel(body []byte, contentType, model string) []byte {
	if model == "" {
		return body
	}
	if strings.Contains(strings.ToLower(contentType), "json") {
		var payload map[string]any
		decoder := json.NewDecoder(bytes.NewReader(body))
		decoder.UseNumber()
		if decoder.Decode(&payload) != nil {
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

// extendCacheTTL raises every ephemeral cache breakpoint in the body to the hour
// the provider profile asked for, and reports whether the body it returns carries
// an hour-long breakpoint at all.
//
// The caller needs that second answer because the hour is not only a body field:
// a client asking for it sends `anthropic-beta: extended-cache-ttl-2025-04-11`
// alongside, and an upstream that gates the feature reads the header, not the
// body. What matters is therefore the body being sent, not who wrote it - the
// relay raising a five-minute breakpoint on the client's behalf has to declare
// the hour, and so does forwarding an hour the client asked for and forgot to
// declare. Either way the alternative is a request no client produces.
//
// The nesting limit is the one case that reports nothing: there the body is
// returned exactly as it arrived, so the relay has made no claim about it and the
// caller's own declaration is left to stand.
func extendCacheTTL(body []byte, contentType string) ([]byte, bool) {
	if !strings.Contains(strings.ToLower(contentType), "json") || len(body) == 0 {
		return body, false
	}
	var payload any
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if decoder.Decode(&payload) != nil {
		return body, false
	}
	changed, carriesHour, withinLimit := extendCacheValue(payload, 0)
	if !withinLimit {
		return body, false
	}
	if !changed {
		return body, carriesHour
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return body, false
	}
	return encoded, carriesHour
}

// extendedCacheTTLBeta is the capability token that carries the hour. `cache_control`
// is an Anthropic construct, so the body shape is the whole gate: a Chat Completions
// or Responses request has no breakpoint to carry an hour and is never given an
// Anthropic capability it did not ask for.
const extendedCacheTTLBeta = "extended-cache-ttl-2025-04-11"

// declareBetaFeature appends a capability to the header the client sent instead of
// replacing it. The client picks its own beta features - Claude Code sends several
// - and overwriting the list to add one would switch the others off.
func declareBetaFeature(header http.Header, feature string) {
	existing := header.Get("Anthropic-Beta")
	if existing == "" {
		header.Set("Anthropic-Beta", feature)
		return
	}
	for _, declared := range strings.Split(existing, ",") {
		if strings.TrimSpace(declared) == feature {
			return
		}
	}
	header.Set("Anthropic-Beta", existing+","+feature)
}

// extendCacheValue reports, in order: whether it raised a breakpoint, whether the
// value now carries an hour-long one, and whether it stayed inside the nesting
// limit.
func extendCacheValue(value any, depth int) (bool, bool, bool) {
	if depth > maxCacheTraversalDepth {
		return false, false, false
	}
	changed := false
	carriesHour := false
	switch value := value.(type) {
	case map[string]any:
		if control, ok := value["cache_control"].(map[string]any); ok && control["type"] == "ephemeral" {
			if control["ttl"] != "1h" {
				control["ttl"] = "1h"
				changed = true
			}
			carriesHour = true
		}
		for _, child := range value {
			childChanged, childCarries, withinLimit := extendCacheValue(child, depth+1)
			if !withinLimit {
				return false, false, false
			}
			changed = childChanged || changed
			carriesHour = childCarries || carriesHour
		}
	case []any:
		for _, child := range value {
			childChanged, childCarries, withinLimit := extendCacheValue(child, depth+1)
			if !withinLimit {
				return false, false, false
			}
			changed = childChanged || changed
			carriesHour = childCarries || carriesHour
		}
	}
	return changed, carriesHour, true
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
