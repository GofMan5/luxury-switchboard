package stdio

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
)

type Handler func(context.Context, json.RawMessage) (any, error)

type MethodError struct {
	Code    string
	Message string
}

func (err MethodError) Error() string { return err.Message }

type Server struct {
	reader     io.Reader
	writer     io.Writer
	handlers   map[string]Handler
	writeMu    sync.Mutex
	sequence   atomic.Uint64
	maxWorkers int
	cancelMu   sync.Mutex
	cancels    map[string]context.CancelFunc
}

type job struct {
	request Request
	ctx     context.Context
	cancel  context.CancelFunc
}

func NewServer(reader io.Reader, writer io.Writer, maxConcurrent int) *Server {
	if maxConcurrent < 1 {
		maxConcurrent = 1
	}
	return &Server{
		reader:     reader,
		writer:     writer,
		handlers:   make(map[string]Handler),
		maxWorkers: maxConcurrent,
		cancels:    make(map[string]context.CancelFunc),
	}
}

func (server *Server) Handle(method string, handler Handler) {
	if method == "" || handler == nil {
		panic("stdio: invalid handler")
	}
	if _, exists := server.handlers[method]; exists {
		panic("stdio: duplicate handler " + method)
	}
	server.handlers[method] = handler
}

func (server *Server) Serve(ctx context.Context) error {
	serveCtx, cancelServe := context.WithCancel(ctx)
	defer cancelServe()
	scanner := bufio.NewScanner(server.reader)
	scanner.Buffer(make([]byte, 64*1024), MaxFrameBytes)
	jobs := make(chan job, server.maxWorkers*2)
	var group sync.WaitGroup
	for range server.maxWorkers {
		group.Add(1)
		go func() {
			defer group.Done()
			for pending := range jobs {
				server.handleRequest(pending)
			}
		}()
	}
	defer func() {
		cancelServe()
		close(jobs)
		group.Wait()
	}()
	for scanner.Scan() {
		if err := serveCtx.Err(); err != nil {
			return err
		}
		request, err := decodeRequest(scanner.Bytes())
		if err != nil {
			_ = server.write(Failure(request, "invalid_request", "Malformed protocol frame"))
			continue
		}
		if request.Method == "system.cancel" {
			server.cancelRequest(request)
			continue
		}
		if request.Method == "system.shutdown" {
			_ = server.write(Response{Version: ProtocolVersion, ID: request.ID, Type: "result", Method: request.Method, OK: true})
			return nil
		}
		pending, err := server.prepareJob(serveCtx, request)
		if err != nil {
			_ = server.write(Failure(request, "duplicate_id", "Request id is already active"))
			continue
		}
		select {
		case jobs <- pending:
		case <-serveCtx.Done():
			server.finishJob(pending)
			return serveCtx.Err()
		default:
			server.finishJob(pending)
			_ = server.write(Failure(request, "busy", "Control plane is busy"))
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read stdio frame: %w", err)
	}
	return nil
}

func (server *Server) Emit(topic string, payload any) error {
	if topic == "" {
		return errors.New("event topic is required")
	}
	return server.write(Event{
		Version: ProtocolVersion,
		Type:    "event",
		Topic:   topic,
		Seq:     server.sequence.Add(1),
		Payload: payload,
	})
}

func (server *Server) handleRequest(pending job) {
	defer server.finishJob(pending)
	request := pending.request
	handler := server.handlers[request.Method]
	if handler == nil {
		_ = server.write(Failure(request, "method_not_found", "Unknown command"))
		return
	}
	payload, err := handler(pending.ctx, request.Payload)
	if err != nil {
		methodError := MethodError{Code: "command_failed", Message: "Command failed"}
		if errors.As(err, &methodError) {
			_ = server.write(Failure(request, methodError.Code, methodError.Message))
			return
		}
		_ = server.write(Failure(request, "command_failed", "Command failed"))
		return
	}
	_ = server.write(Response{
		Version: ProtocolVersion,
		ID:      request.ID,
		Type:    "result",
		Method:  request.Method,
		OK:      true,
		Payload: payload,
	})
}

func (server *Server) prepareJob(parent context.Context, request Request) (job, error) {
	ctx, cancel := context.WithCancel(parent)
	server.cancelMu.Lock()
	defer server.cancelMu.Unlock()
	if _, exists := server.cancels[request.ID]; exists {
		cancel()
		return job{}, errors.New("duplicate request id")
	}
	server.cancels[request.ID] = cancel
	return job{request: request, ctx: ctx, cancel: cancel}, nil
}

func (server *Server) finishJob(pending job) {
	pending.cancel()
	server.cancelMu.Lock()
	delete(server.cancels, pending.request.ID)
	server.cancelMu.Unlock()
}

func (server *Server) cancelRequest(request Request) {
	var payload struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(request.Payload, &payload); err != nil || payload.ID == "" {
		_ = server.write(Failure(request, "invalid_payload", "Cancellation id is required"))
		return
	}
	server.cancelMu.Lock()
	cancel := server.cancels[payload.ID]
	server.cancelMu.Unlock()
	if cancel != nil {
		cancel()
	}
	_ = server.write(Response{
		Version: ProtocolVersion,
		ID:      request.ID,
		Type:    "result",
		Method:  request.Method,
		OK:      true,
		Payload: map[string]bool{"cancelled": cancel != nil},
	})
}

func (server *Server) write(value any) error {
	server.writeMu.Lock()
	defer server.writeMu.Unlock()
	encoder := json.NewEncoder(server.writer)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(value)
}

func decodeRequest(line []byte) (Request, error) {
	request := Request{}
	if len(bytes.TrimSpace(line)) == 0 {
		return request, errors.New("empty frame")
	}
	if err := rejectDuplicateKeys(line); err != nil {
		return request, err
	}
	decoder := json.NewDecoder(bytes.NewReader(line))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return request, err
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return request, errors.New("multiple JSON values")
	}
	return request, request.Validate()
}

func rejectDuplicateKeys(line []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(line))
	decoder.UseNumber()
	return walkJSONValue(decoder, 0)
}

func walkJSONValue(decoder *json.Decoder, depth int) error {
	if depth > 32 {
		return errors.New("JSON nesting exceeds the limit")
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, composite := token.(json.Delim)
	if !composite {
		return nil
	}
	switch delimiter {
	case '{':
		keys := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("object key is not a string")
			}
			if _, duplicate := keys[key]; duplicate {
				return errors.New("duplicate JSON key")
			}
			keys[key] = struct{}{}
			if err := walkJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
	case '[':
		for decoder.More() {
			if err := walkJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
	default:
		return errors.New("invalid JSON delimiter")
	}
	_, err = decoder.Token()
	return err
}
