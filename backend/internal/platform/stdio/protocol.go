package stdio

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
)

const (
	ProtocolVersion = 1
	MaxFrameBytes   = 256 * 1024
	// MaxPayloadBytes is what a handler may return. The frame that carries a payload
	// adds the version, request id, type, method and ok keys around it, so a handler
	// that measured its answer against MaxFrameBytes would still overflow the frame.
	// Callers that build a bounded answer size it against this instead of guessing at
	// the envelope, and the slack is deliberately far wider than the envelope needs -
	// 253 bytes at the longest id and method the validator accepts, measured in
	// TestThePayloadBudgetLeavesTheEnvelopeItsRoom - because being a kilobyte
	// conservative costs nothing and being one byte over costs the whole app: the
	// shell breaks its read loop on an oversized frame and kills the sidecar.
	MaxPayloadBytes = MaxFrameBytes - 4*1024
)

var requestIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,80}$`)

// TrimToPayloadBudget drops the tail of a list until it fits the byte budget
// a handler may answer with (MaxPayloadBytes, one constant the whole platform
// measures against). It is the shared body of the four stdio lists that carry
// bounded journals — keys, activity rows, guardrail findings, tunnel clients —
// each of which says so honestly with an untruncated count beside the trimmed
// list. The shrink is proportional then verified, never halved: rows are
// within an order of magnitude of each other, so one estimate normally lands,
// and taking at least one off guarantees this ends. The caller owns which end
// is least valuable and orders its list accordingly: newest-first lists drop
// the oldest rows here, priority-ordered lists drop the lowest priority.
func TrimToPayloadBudget[T any](items []T) []T {
	for len(items) > 0 {
		encoded, err := json.Marshal(items)
		if err != nil {
			return nil
		}
		if len(encoded) <= MaxPayloadBytes {
			return items
		}
		next := len(items) * MaxPayloadBytes / len(encoded)
		items = items[:min(next, len(items)-1)]
	}
	return items
}

type Request struct {
	Version int             `json:"v"`
	ID      string          `json:"id"`
	Type    string          `json:"type"`
	Method  string          `json:"method"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

func DecodePayload(raw json.RawMessage, target any) error {
	if len(raw) == 0 {
		raw = []byte("{}")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return errors.New("payload contains multiple values")
	}
	return nil
}

type Response struct {
	Version int       `json:"v"`
	ID      string    `json:"id"`
	Type    string    `json:"type"`
	Method  string    `json:"method,omitempty"`
	OK      bool      `json:"ok"`
	Payload any       `json:"payload,omitempty"`
	Error   *RPCError `json:"error,omitempty"`
}

type Event struct {
	Version int    `json:"v"`
	Type    string `json:"type"`
	Topic   string `json:"topic"`
	Seq     uint64 `json:"seq"`
	Payload any    `json:"payload,omitempty"`
}

type RPCError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (request Request) Validate() error {
	if request.Version != ProtocolVersion {
		return fmt.Errorf("unsupported protocol version %d", request.Version)
	}
	if request.Type != "command" {
		return errors.New("frame type must be command")
	}
	if !requestIDPattern.MatchString(request.ID) {
		return errors.New("invalid request id")
	}
	if request.Method == "" || len(request.Method) > 120 {
		return errors.New("invalid method")
	}
	return nil
}

func Failure(request Request, code, message string) Response {
	return Response{
		Version: ProtocolVersion,
		ID:      request.ID,
		Type:    "result",
		Method:  request.Method,
		OK:      false,
		Error:   &RPCError{Code: code, Message: message},
	}
}
