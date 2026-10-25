package application

import (
	"context"
	"strings"
	"time"
)

// Endpoint names classify a request by its canonical path. Rules match on
// them, hooks are registered for them, and they are the only path detail a
// plugin ever learns besides the path itself.
const (
	EndpointResponses   = "responses"
	EndpointChat        = "chat"
	EndpointMessages    = "messages"
	EndpointCompletions = "completions"
	EndpointEmbeddings  = "embeddings"
	EndpointImages      = "images"
	EndpointModels      = "models"
	EndpointOther       = "other"
)

// Endpoints lists every endpoint name, for validating rule and hook matches.
var Endpoints = []string{
	EndpointResponses, EndpointChat, EndpointMessages, EndpointCompletions,
	EndpointEmbeddings, EndpointImages, EndpointModels, EndpointOther,
}

// ClassifyEndpoint names the endpoint of a canonical path (lower-cased and
// cleaned, as the relay's canonicalPath spells it). Both the /v1-prefixed and
// the bare spellings classify the same, so the spelling a client picks cannot
// steer a request past a rule or a hook.
func ClassifyEndpoint(canonical string) string {
	path := strings.TrimPrefix(canonical, "/v1")
	if path == "" {
		path = "/"
	}
	switch {
	case path == "/responses" || strings.HasPrefix(path, "/responses/"):
		return EndpointResponses
	case path == "/chat/completions":
		return EndpointChat
	case path == "/messages" || path == "/messages/count_tokens":
		return EndpointMessages
	case path == "/completions":
		return EndpointCompletions
	case path == "/embeddings":
		return EndpointEmbeddings
	case strings.HasPrefix(path, "/images/"):
		return EndpointImages
	case path == "/models" || strings.HasPrefix(path, "/models/"):
		return EndpointModels
	default:
		return EndpointOther
	}
}

// Hook phases and decision actions, as plugins spell them.
const (
	HookPhaseRequest  = "request"
	HookPhaseResponse = "response"

	HookActionContinue = "continue"
	HookActionRewrite  = "rewrite"
	HookActionBlock    = "block"
)

// HookRequest is what a request hook sees. Body is nil when it was omitted
// (too large, or not text); BodyOmitted then says so. Bytes is the original
// request body's length either way.
type HookRequest struct {
	ActivityID, Model, Endpoint, Method, Path string
	Body                                      []byte
	BodyOmitted                               bool
	Bytes                                     int64
}

// HookDecision is a request hook's answer. The zero value continues
// unchanged. Body and Model apply for "rewrite"; Tags apply for any action;
// BlockStatus and BlockMessage apply for "block".
type HookDecision struct {
	Action       string
	Body         []byte
	Model        string
	Tags         []string
	BlockStatus  int
	BlockMessage string
}

// HookObservation is what a response observer sees once the client already
// has its answer. Request.Body is always nil; Body is set only when the relay
// buffered the answer anyway.
type HookObservation struct {
	Request     HookRequest
	Status      int
	ErrorCode   string
	LatencyMS   int64
	Tokens      *TokenUsage
	Body        []byte
	BodyOmitted bool
}

// RequestHooks is the relay's view of plugin request hooks and response
// observers. It runs for the local relay only, never for Dispatch (the public
// tunnel and model tests). Implemented by the relayhooks slice.
type RequestHooks interface {
	// Active is cheap; false means the relay skips the body copies.
	Active(phase string, model, endpoint string) bool
	// Before returns within its own budget; the zero value continues.
	Before(ctx context.Context, request HookRequest) HookDecision
	// After must not block.
	After(observation HookObservation)
}

// RuleRequest is the part of a local relay request that request rules match
// on: the model the client asked for and the endpoint class of its path.
type RuleRequest struct {
	Model    string
	Endpoint string
}

// RuleBlock answers the request locally with Status and a plain-text Message.
type RuleBlock struct {
	Status  int
	Message string
}

// RuleLimit refuses the request with 429: a rate limit or budget of the named
// rule is exhausted until RetryAfter has passed.
type RuleLimit struct {
	RuleName   string
	RetryAfter time.Duration
}

// RuleOutcome is the merged effect of every enabled request rule that matched,
// in order, up to the first that stops evaluation.
type RuleOutcome struct {
	// ProviderID pins routing to that provider when it exists and is
	// enabled; otherwise routing is unchanged.
	ProviderID string
	// Model replaces the upstream model of the resolved route.
	Model string
	// Block, when set, is answered instead of forwarding; nothing was
	// counted against any window.
	Block *RuleBlock
	// Limit, when set, is answered as 429; nothing was counted against any
	// window.
	Limit *RuleLimit
	// Headers are extra upstream headers (X- names only, already validated).
	Headers map[string]string
	// Tags are recorded on the activity.
	Tags []string
	// Budgeted names the admitted rules whose token budgets the request's
	// usage is charged to; pass it to SpendTokens when the request finishes.
	Budgeted []string
}

// RetryQuery describes one failed upstream attempt after the content-policy
// refusal check. Status is zero for a transport error. ErrorText is the
// relay's normalized (lower-cased, redacted) error text.
type RetryQuery struct {
	ProviderID string
	Model      string
	Status     int
	ErrorCode  string
	ErrorText  string
}

// Retry rule actions.
const (
	RetryActionRetry          = "retry"
	RetryActionRotateKey      = "rotate_key"
	RetryActionSwitchProvider = "switch_provider"
	RetryActionFail           = "fail"
)

// RetryDecision is the first matching custom retry rule. MaxAttempts is the
// number of extra attempts the rule may spend on one request; Delay zero means
// the relay's normal backoff (Retry-After honoured).
type RetryDecision struct {
	RuleID      string
	Action      string
	MaxAttempts int
	Delay       time.Duration
}

// RequestRules is the relay's view of the operator's (and plugins') rules.
// Implemented by the rules slice; nil-safe at the call sites.
type RequestRules interface {
	// Evaluate merges the matching request rules, counts their hits, and
	// admits the request against every matching rate limit and budget
	// atomically: either all of the windows count it or none do. Local relay
	// only.
	Evaluate(request RuleRequest) RuleOutcome
	// SpendTokens charges a finished request's total tokens to the budgets
	// Evaluate admitted it under.
	SpendTokens(ruleIDs []string, tokens int64)
	// Retry returns the first enabled custom retry rule matching a failed
	// attempt and counts its hit. Built-in classification applies when it
	// returns false.
	Retry(query RetryQuery) (RetryDecision, bool)
}
