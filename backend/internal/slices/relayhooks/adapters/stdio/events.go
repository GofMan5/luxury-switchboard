package relayhooksstdio

import (
	"bytes"
	"encoding/json"

	platform "github.com/luxuryprivate/switchboard/backend/internal/platform/stdio"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/relayhooks/application"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/relayhooks/domain"
)

// Event topics.
const (
	TopicInvoke  = "relayhooks.invoke"
	TopicObserve = "relayhooks.observe"
	// TopicChanged reports a hook the sidecar removed on its own.
	TopicChanged = "relayhooks.changed"
)

// Emitter sends one event frame; *platform.Server is one.
type Emitter interface {
	Emit(topic string, payload any) error
}

// Events delivers hook traffic as protocol events. A body that would push an
// event over platform.MaxPayloadBytes is left out and marked bodyOmitted.
type Events struct {
	emitter Emitter
}

var _ application.Events = Events{}

// NewEvents returns the events of emitter.
func NewEvents(emitter Emitter) Events { return Events{emitter: emitter} }

type requestPayload struct {
	ActivityID  string  `json:"activityId"`
	Model       string  `json:"model"`
	Endpoint    string  `json:"endpoint"`
	Method      string  `json:"method"`
	Path        string  `json:"path"`
	Bytes       int64   `json:"bytes"`
	Body        *string `json:"body,omitempty"`
	BodyOmitted bool    `json:"bodyOmitted,omitempty"`
}

type invokePayload struct {
	CallID   string         `json:"callId"`
	PluginID string         `json:"pluginId"`
	HookID   string         `json:"hookId"`
	Phase    string         `json:"phase"`
	Request  requestPayload `json:"request"`
}

type tokensPayload struct {
	Input  int64 `json:"input"`
	Output int64 `json:"output"`
	Total  int64 `json:"total"`
}

type responsePayload struct {
	Status      int            `json:"status"`
	ErrorCode   string         `json:"errorCode,omitempty"`
	LatencyMS   int64          `json:"latencyMs"`
	Tokens      *tokensPayload `json:"tokens,omitempty"`
	Body        *string        `json:"body,omitempty"`
	BodyOmitted bool           `json:"bodyOmitted,omitempty"`
}

type observePayload struct {
	PluginID string          `json:"pluginId"`
	HookID   string          `json:"hookId"`
	Phase    string          `json:"phase"`
	Request  requestPayload  `json:"request"`
	Response responsePayload `json:"response"`
}

type changedPayload struct {
	PluginID string `json:"pluginId"`
	HookID   string `json:"hookId"`
	Phase    string `json:"phase"`
	Removed  bool   `json:"removed"`
	Reason   string `json:"reason"`
}

// Invoke emits relayhooks.invoke.
func (events Events) Invoke(invocation domain.Invocation) (bool, error) {
	payload := invokePayload{
		CallID: invocation.CallID, PluginID: invocation.PluginID, HookID: invocation.HookID,
		Phase: domain.PhaseRequest, Request: request(invocation.Request),
	}
	omitted := payload.Request.Body != nil && !fits(payload)
	if omitted {
		payload.Request.Body, payload.Request.BodyOmitted = nil, true
	}
	return payload.Request.BodyOmitted, events.emitter.Emit(TopicInvoke, payload)
}

// Observe emits relayhooks.observe.
func (events Events) Observe(observation domain.Observation) error {
	response := observation.Response
	payload := observePayload{
		PluginID: observation.PluginID, HookID: observation.HookID, Phase: domain.PhaseResponse,
		Request: request(domain.Request{
			ActivityID: observation.Request.ActivityID, Model: observation.Request.Model,
			Endpoint: observation.Request.Endpoint, Method: observation.Request.Method,
			Path: observation.Request.Path, Bytes: observation.Request.Bytes,
		}),
		Response: responsePayload{
			Status: response.Status, ErrorCode: response.ErrorCode, LatencyMS: response.LatencyMS,
			Body: text(response.Body), BodyOmitted: response.BodyOmitted,
		},
	}
	if tokens := response.Tokens; tokens != nil {
		payload.Response.Tokens = &tokensPayload{Input: tokens.Input, Output: tokens.Output, Total: tokens.Total}
	}
	if payload.Response.Body != nil && !fits(payload) {
		payload.Response.Body, payload.Response.BodyOmitted = nil, true
	}
	return events.emitter.Emit(TopicObserve, payload)
}

// Removed emits relayhooks.changed for a hook the sidecar removed.
func (events Events) Removed(removal application.Removal) error {
	return events.emitter.Emit(TopicChanged, changedPayload{
		PluginID: removal.PluginID, HookID: removal.HookID, Phase: removal.Phase, Removed: true, Reason: removal.Reason,
	})
}

func request(value domain.Request) requestPayload {
	return requestPayload{
		ActivityID: value.ActivityID, Model: value.Model, Endpoint: value.Endpoint,
		Method: value.Method, Path: value.Path, Bytes: value.Bytes,
		Body: text(value.Body), BodyOmitted: value.BodyOmitted,
	}
}

func text(body string) *string {
	if body == "" {
		return nil
	}
	return &body
}

// fits measures payload as the protocol encodes it (HTML unescaped).
func fits(payload any) bool {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(payload) == nil && buffer.Len() <= platform.MaxPayloadBytes
}
