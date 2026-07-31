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
)

var requestIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,80}$`)

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
