// Package domain holds the rules of plugin relay hooks: what a plugin may
// register, which local relay requests a hook sees, and what a hook may answer.
package domain

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// PermissionRelayContent is the grant a plugin needs to register any hook. It
// mirrors the plugins slice's permission name.
const PermissionRelayContent = "relay:content"

// Phases, as plugins spell them. They match the relay's hook phases.
const (
	PhaseRequest  = "request"
	PhaseResponse = "response"
)

// Decision actions, as plugins spell them.
const (
	ActionContinue = "continue"
	ActionRewrite  = "rewrite"
	ActionBlock    = "block"
)

const (
	// MaxHooks bounds the hooks of every plugin together.
	MaxHooks = 8
	// MaxHooksPerPhase bounds one plugin's hooks in one phase.
	MaxHooksPerPhase = 2
	// MaxModels bounds a hook's model globs; each is at most MaxModelRunes.
	MaxModels     = 16
	MaxModelRunes = 200
	// A request hook waits DefaultTimeoutMS for its answer unless it asks for
	// another wait in [MinTimeoutMS, MaxTimeoutMS].
	MinTimeoutMS     = 100
	MaxTimeoutMS     = 5000
	DefaultTimeoutMS = 2000
	// TotalBudget bounds every request hook of one request together.
	TotalBudget = 5 * time.Second
	// MaxConsecutiveTimeouts unanswered calls in a row unregister a hook.
	MaxConsecutiveTimeouts = 3
	// MaxBodyBytes bounds a body a hook sees and a body it writes back.
	MaxBodyBytes = 192 * 1024
	// MaxTags bounds the tags of one decision; MaxActivityTags bounds the
	// tags all hooks add to one request together.
	MaxTags         = 8
	MaxActivityTags = 16
	// MaxBlockMessageRunes bounds the plain-text message of a block.
	MaxBlockMessageRunes = 200
)

// Endpoints mirrors the relay's endpoint classification; hooks filter on it.
var Endpoints = []string{"responses", "chat", "messages", "completions", "embeddings", "images", "models", "other"}

var (
	pluginIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,62}[a-z0-9]$`)
	hookIDPattern   = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)
	tagPattern      = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,31}$`)
	callIDPattern   = regexp.MustCompile(`^[0-9a-f]{32}$`)
)

// ValidationError names the field that failed and the rule it broke, never
// the value: the value may be part of a prompt.
type ValidationError struct {
	Field  string
	Reason string
}

func (err ValidationError) Error() string { return err.Field + " " + err.Reason }

// ValidPluginID mirrors the plugins slice's id rule.
func ValidPluginID(id string) bool { return pluginIDPattern.MatchString(id) }

// ValidHookID reports whether id names a hook within its plugin.
func ValidHookID(id string) bool { return hookIDPattern.MatchString(id) }

// ValidCallID reports whether id has the shape of a call id: 128 bits in
// lower-case hex.
func ValidCallID(id string) bool { return callIDPattern.MatchString(id) }

// Hook is one plugin's registration for one phase. Empty Models or Endpoints
// match everything. TimeoutMS applies to request hooks.
type Hook struct {
	PluginID  string
	HookID    string
	Phase     string
	Models    []string
	Endpoints []string
	TimeoutMS int
}

// Validate checks every field against its rule.
func (hook Hook) Validate() error {
	switch {
	case !ValidPluginID(hook.PluginID):
		return ValidationError{"pluginId", "must be 3-64 lowercase letters, digits, dots or dashes"}
	case !ValidHookID(hook.HookID):
		return ValidationError{"hookId", "must be 1-32 lowercase letters, digits, underscores or dashes"}
	case hook.Phase != PhaseRequest && hook.Phase != PhaseResponse:
		return ValidationError{"phase", "must be request or response"}
	case len(hook.Models) > MaxModels:
		return ValidationError{"models", fmt.Sprintf("must list at most %d globs", MaxModels)}
	case len(hook.Endpoints) > len(Endpoints):
		return ValidationError{"endpoints", fmt.Sprintf("must list at most %d endpoints", len(Endpoints))}
	case hook.TimeoutMS < MinTimeoutMS || hook.TimeoutMS > MaxTimeoutMS:
		return ValidationError{"timeoutMs", fmt.Sprintf("must be %d-%d", MinTimeoutMS, MaxTimeoutMS)}
	}
	for _, glob := range hook.Models {
		if !plainText(glob, MaxModelRunes) {
			return ValidationError{"models", fmt.Sprintf("must be 1-%d characters without control characters", MaxModelRunes)}
		}
	}
	for _, endpoint := range hook.Endpoints {
		if !slices.Contains(Endpoints, endpoint) {
			return ValidationError{"endpoints", "include an unknown endpoint"}
		}
	}
	return nil
}

// Timeout is how long one call to the hook waits for its answer.
func (hook Hook) Timeout() time.Duration { return time.Duration(hook.TimeoutMS) * time.Millisecond }

// Matches reports whether the hook sees a request for model at endpoint.
func (hook Hook) Matches(model, endpoint string) bool {
	if len(hook.Endpoints) > 0 && !slices.Contains(hook.Endpoints, endpoint) {
		return false
	}
	if len(hook.Models) == 0 {
		return true
	}
	for _, glob := range hook.Models {
		if MatchGlob(glob, model) {
			return true
		}
	}
	return false
}

// MatchGlob matches value against pattern case-insensitively; '*' stands for
// any run of characters, every other character for itself.
func MatchGlob(pattern, value string) bool {
	pattern, value = strings.ToLower(pattern), strings.ToLower(value)
	parts := strings.Split(pattern, "*")
	if len(parts) == 1 {
		return pattern == value
	}
	if !strings.HasPrefix(value, parts[0]) {
		return false
	}
	value = value[len(parts[0]):]
	for _, part := range parts[1 : len(parts)-1] {
		index := strings.Index(value, part)
		if index < 0 {
			return false
		}
		value = value[index+len(part):]
	}
	return strings.HasSuffix(value, parts[len(parts)-1])
}

// Block answers a request locally, like a blocking request rule.
type Block struct {
	Status  int
	Message string
}

// Decision is a request hook's answer. Body and Model apply to a rewrite;
// Tags apply to any action; Block is required to block. An empty Body or
// Model leaves that part unchanged.
type Decision struct {
	Action string
	Body   string
	Model  string
	Tags   []string
	Block  *Block
}

// Validate checks the answer. A rewritten body is not checked here: one that
// is not usable is ignored rather than refused (see UsableBody).
func (decision Decision) Validate() error {
	switch decision.Action {
	case ActionContinue, ActionRewrite, ActionBlock:
	default:
		return ValidationError{"action", "must be continue, rewrite or block"}
	}
	if decision.Model != "" && !plainText(decision.Model, MaxModelRunes) {
		return ValidationError{"model", fmt.Sprintf("must be 1-%d characters without control characters", MaxModelRunes)}
	}
	if len(decision.Tags) > MaxTags {
		return ValidationError{"tags", fmt.Sprintf("must list at most %d tags", MaxTags)}
	}
	for _, tag := range decision.Tags {
		if !tagPattern.MatchString(tag) {
			return ValidationError{"tags", "must be 1-32 lowercase letters, digits, dots, underscores or dashes"}
		}
	}
	if decision.Action == ActionBlock && decision.Block == nil {
		return ValidationError{"block", "is required to block"}
	}
	if block := decision.Block; block != nil {
		switch block.Status {
		case 400, 403, 429, 503:
		default:
			return ValidationError{"block.status", "must be 400, 403, 429 or 503"}
		}
		if !plainText(block.Message, MaxBlockMessageRunes) {
			return ValidationError{"block.message", fmt.Sprintf("must be 1-%d characters without control characters", MaxBlockMessageRunes)}
		}
	}
	return nil
}

// UsableBody reports whether body can replace a request body: it is at most
// MaxBodyBytes of valid JSON.
func UsableBody(body string) bool {
	return body != "" && len(body) <= MaxBodyBytes && json.Valid([]byte(body))
}

// MergeTags adds tags to merged, skipping repeats, up to MaxActivityTags.
func MergeTags(merged, tags []string) []string {
	for _, tag := range tags {
		if len(merged) >= MaxActivityTags {
			break
		}
		if !slices.Contains(merged, tag) {
			merged = append(merged, tag)
		}
	}
	return merged
}

// Request is what the relay tells hooks about one local request. Body is
// empty when it was omitted (too large, or not text); BodyOmitted then says
// so. Bytes is the size of the client's body.
type Request struct {
	ActivityID string
	Model      string
	Endpoint   string
	Method     string
	Path       string
	Bytes      int64
	Body       string
	// BodyOmitted is set when Body was not given; such a body cannot be
	// rewritten.
	BodyOmitted bool
}

// Invocation is one call to one request hook.
type Invocation struct {
	CallID   string
	PluginID string
	HookID   string
	Request  Request
}

// Tokens is the token usage of a finished request.
type Tokens struct {
	Input  int64
	Output int64
	Total  int64
}

// Response is how a local request ended. Body is set only when the relay
// buffered the answer and it was at most MaxBodyBytes of text.
type Response struct {
	Status      int
	ErrorCode   string
	LatencyMS   int64
	Tokens      *Tokens
	Body        string
	BodyOmitted bool
}

// Observation is what one response observer is told. Request carries no
// body.
type Observation struct {
	PluginID string
	HookID   string
	Request  Request
	Response Response
}

func plainText(value string, maxRunes int) bool {
	if strings.TrimSpace(value) == "" || !utf8.ValidString(value) || utf8.RuneCountInString(value) > maxRunes {
		return false
	}
	return !strings.ContainsFunc(value, unicode.IsControl)
}
