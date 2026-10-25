// Package domain holds the operator's relay rules: request rules that route,
// rewrite, block, limit, tag and decorate local relay requests, and custom
// retry rules that decide what a failed upstream attempt does next.
package domain

import (
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

// Limits are absolute caps: the rules file, rules.list and every evaluation
// are bounded by them.
const (
	MaxRequestRules = 64
	MaxRetryRules   = 64
	// MaxTags bounds the tags one request carries after the union of every
	// matching rule (and any hook) — the activity record's own cap.
	MaxTags = 16

	maxNameRunes      = 60
	maxGlobs          = 16
	maxGlobRunes      = 200
	maxProviderRunes  = 64
	maxModelRunes     = 200
	maxBlockRunes     = 200
	maxHeaders        = 8
	maxHeaderValue    = 256
	maxRuleTags       = 8
	maxStatuses       = 16
	maxContains       = 8
	minContainsRunes  = 2
	maxContainsRunes  = 120
	maxErrorCodes     = 8
	maxRateRequests   = 100_000
	maxRateWindow     = 86_400
	maxBudgetRequests = 10_000_000
	maxBudgetTokens   = 10_000_000_000
	maxRetryAttempts  = 10
	maxRetryDelayMS   = 60_000
	minErrorStatus    = 400
	maxErrorStatus    = 599
	forwardedPrefix   = "x-forwarded-"
	switchboardPrefix = "x-switchboard-"
	realIPHeader      = "x-real-ip"
	stainlessPrefix   = "x-stainless-"
)

// Budget periods and retry actions, as the contract spells them.
const (
	BudgetPeriodHour = "hour"
	BudgetPeriodDay  = "day"

	RetryRetry     = "retry"
	RetryRotateKey = "rotate_key"
	RetrySwitch    = "switch_provider"
	RetryFail      = "fail"
)

// Endpoint names, matching the relay's classification of canonical paths.
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

// Endpoints are the request classes a request rule may match, as the relay
// classifies a request's canonical path.
var Endpoints = []string{
	EndpointResponses, EndpointChat, EndpointMessages, EndpointCompletions,
	EndpointEmbeddings, EndpointImages, EndpointModels, EndpointOther,
}

var (
	ruleIDPattern    = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,47}$`)
	headerPattern    = regexp.MustCompile(`^X-[A-Za-z0-9-]{1,62}$`)
	tagPattern       = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,31}$`)
	errorCodePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
	// actorPattern is "plugin:" and the plugins slice's own id shape.
	actorPattern  = regexp.MustCompile(`^plugin:[a-z0-9][a-z0-9.-]{1,62}[a-z0-9]$`)
	blockStatuses = []int{400, 403, 429, 503}
)

// ValidationError names the field that failed in text that is safe to show:
// it never repeats the offending value.
type ValidationError struct {
	Field  string
	Reason string
}

func (err ValidationError) Error() string { return err.Field + " " + err.Reason }

// Set is every rule the operator and plugins configured, in evaluation order.
type Set struct {
	RequestRules []RequestRule `json:"requestRules"`
	RetryRules   []RetryRule   `json:"retryRules"`
}

type RequestRule struct {
	ID      string       `json:"id"`
	Name    string       `json:"name"`
	Enabled bool         `json:"enabled"`
	Owner   string       `json:"owner"`
	Match   RequestMatch `json:"match"`
	Effects Effects      `json:"effects"`
}

// RequestMatch is a conjunction of its non-empty fields; an empty list
// matches anything.
type RequestMatch struct {
	Models    []string `json:"models"`
	Endpoints []string `json:"endpoints"`
}

type Effects struct {
	Provider  string            `json:"provider,omitempty"`
	Model     string            `json:"model,omitempty"`
	Block     *Block            `json:"block,omitempty"`
	RateLimit *RateLimit        `json:"rateLimit,omitempty"`
	Budget    *Budget           `json:"budget,omitempty"`
	Headers   map[string]string `json:"headers,omitempty"`
	Tags      []string          `json:"tags,omitempty"`
	Stop      bool              `json:"stop,omitempty"`
}

type Block struct {
	Status  int    `json:"status"`
	Message string `json:"message"`
}

// RateLimit admits Requests per fixed window of WindowSeconds, opened by the
// first request it admits.
type RateLimit struct {
	Requests      int64 `json:"requests"`
	WindowSeconds int64 `json:"windowSeconds"`
}

// Budget caps the requests and/or tokens per local clock hour or day. Zero
// leaves that dimension unlimited; at least one is set.
type Budget struct {
	Requests int64  `json:"requests,omitempty"`
	Tokens   int64  `json:"tokens,omitempty"`
	Period   string `json:"period"`
}

type RetryRule struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Enabled     bool       `json:"enabled"`
	Owner       string     `json:"owner"`
	Match       RetryMatch `json:"match"`
	Action      string     `json:"action"`
	MaxAttempts int        `json:"maxAttempts"`
	DelayMS     int        `json:"delayMs"`
}

// RetryMatch is a conjunction of its non-empty fields, each a disjunction of
// its entries. At least one of Statuses, Contains or ErrorCodes is set.
type RetryMatch struct {
	Statuses   []int    `json:"statuses"`
	Contains   []string `json:"contains"`
	ErrorCodes []string `json:"errorCodes"`
	Providers  []string `json:"providers"`
	Models     []string `json:"models"`
}

// Failure is one failed upstream attempt as a retry rule sees it. Status is
// zero for a transport error; ErrorText is already NormalizeErrorText'ed.
type Failure struct {
	ProviderID string
	Model      string
	Status     int
	ErrorCode  string
	ErrorText  string
}

// ValidActor reports whether actor names a plugin as the plugin host stamps it.
func ValidActor(actor string) bool { return actorPattern.MatchString(actor) }

func validOwner(owner string) bool { return owner == "" || ValidActor(owner) }

// Normalized trims the free-text fields and turns absent lists into empty
// ones, so the stored and listed shape is stable.
func (rule RequestRule) Normalized() RequestRule {
	rule.ID = strings.TrimSpace(rule.ID)
	rule.Name = strings.TrimSpace(rule.Name)
	rule.Match.Models = trimmedList(rule.Match.Models)
	rule.Match.Endpoints = nonNil(rule.Match.Endpoints)
	rule.Effects.Provider = strings.TrimSpace(rule.Effects.Provider)
	rule.Effects.Model = strings.TrimSpace(rule.Effects.Model)
	if rule.Effects.Block != nil {
		block := *rule.Effects.Block
		block.Message = strings.TrimSpace(block.Message)
		rule.Effects.Block = &block
	}
	if rule.Effects.RateLimit != nil {
		limit := *rule.Effects.RateLimit
		rule.Effects.RateLimit = &limit
	}
	if rule.Effects.Budget != nil {
		budget := *rule.Effects.Budget
		rule.Effects.Budget = &budget
	}
	if len(rule.Effects.Headers) == 0 {
		rule.Effects.Headers = nil
	} else {
		headers := make(map[string]string, len(rule.Effects.Headers))
		for name, value := range rule.Effects.Headers {
			headers[name] = value
		}
		rule.Effects.Headers = headers
	}
	if len(rule.Effects.Tags) == 0 {
		rule.Effects.Tags = nil
	} else {
		rule.Effects.Tags = slices.Clone(rule.Effects.Tags)
	}
	return rule
}

// Validate checks a normalized request rule against every contract limit.
func (rule RequestRule) Validate() error {
	if err := validateIdentity(rule.ID, rule.Name, rule.Owner); err != nil {
		return err
	}
	if err := validateGlobs("Model patterns", rule.Match.Models); err != nil {
		return err
	}
	if len(rule.Match.Endpoints) > len(Endpoints) {
		return ValidationError{"Endpoints", "lists too many endpoints"}
	}
	for index, endpoint := range rule.Match.Endpoints {
		if !slices.Contains(Endpoints, endpoint) {
			return ValidationError{"Endpoints", "must name known endpoints"}
		}
		if slices.Contains(rule.Match.Endpoints[:index], endpoint) {
			return ValidationError{"Endpoints", "must not repeat an endpoint"}
		}
	}
	return rule.Effects.validate()
}

func (effects Effects) validate() error {
	if effects.Provider != "" && !validText(effects.Provider, maxProviderRunes) {
		return ValidationError{"Provider", "must be a provider id of 1-64 characters"}
	}
	if effects.Model != "" && !validText(effects.Model, maxModelRunes) {
		return ValidationError{"Model rewrite", "must be 1-200 characters"}
	}
	if block := effects.Block; block != nil {
		if !slices.Contains(blockStatuses, block.Status) {
			return ValidationError{"Block status", "must be 400, 403, 429 or 503"}
		}
		if !validText(block.Message, maxBlockRunes) {
			return ValidationError{"Block message", "must be 1-200 characters of plain text"}
		}
	}
	if limit := effects.RateLimit; limit != nil {
		if limit.Requests < 1 || limit.Requests > maxRateRequests {
			return ValidationError{"Rate limit requests", "must be 1-100000"}
		}
		if limit.WindowSeconds < 1 || limit.WindowSeconds > maxRateWindow {
			return ValidationError{"Rate limit window", "must be 1-86400 seconds"}
		}
	}
	if budget := effects.Budget; budget != nil {
		if budget.Period != BudgetPeriodHour && budget.Period != BudgetPeriodDay {
			return ValidationError{"Budget period", "must be hour or day"}
		}
		if budget.Requests < 0 || budget.Requests > maxBudgetRequests {
			return ValidationError{"Budget requests", "must be 1-10000000"}
		}
		if budget.Tokens < 0 || budget.Tokens > maxBudgetTokens {
			return ValidationError{"Budget tokens", "must be 1-10000000000"}
		}
		if budget.Requests == 0 && budget.Tokens == 0 {
			return ValidationError{"Budget", "must limit requests, tokens or both"}
		}
	}
	if len(effects.Headers) > maxHeaders {
		return ValidationError{"Headers", "are limited to 8"}
	}
	seen := make(map[string]bool, len(effects.Headers))
	for name, value := range effects.Headers {
		if !headerPattern.MatchString(name) || ForbiddenHeader(name) {
			return ValidationError{"Headers", "names must be X- headers other than X-Switchboard-*, X-Forwarded-*, X-Real-IP, X-Stainless-* and credential names (auth, token, key, secret, session, cookie, password, signature)"}
		}
		folded := strings.ToLower(name)
		if seen[folded] {
			return ValidationError{"Headers", "must not repeat a name"}
		}
		seen[folded] = true
		if !validHeaderValue(value) {
			return ValidationError{"Headers", "values must be 1-256 printable ASCII characters"}
		}
	}
	if len(effects.Tags) > maxRuleTags {
		return ValidationError{"Tags", "are limited to 8"}
	}
	for index, tag := range effects.Tags {
		if !ValidTag(tag) {
			return ValidationError{"Tags", "must be lowercase letters, digits, '.', '_' or '-' (1-32)"}
		}
		if slices.Contains(effects.Tags[:index], tag) {
			return ValidationError{"Tags", "must not repeat a tag"}
		}
	}
	return nil
}

// credentialStems mark header names that carry a credential or a session
// (X-Api-Key, X-Goog-Api-Key, X-Auth-Token, ...): a rule must never be able to
// replace the provider's key or plant its own.
var credentialStems = []string{"auth", "token", "key", "secret", "session", "cookie", "password", "signature"}

// ForbiddenHeader reports whether name is one no rule may set: the relay's
// own X-Switchboard-* identity, the proxy-chain headers, the client identity
// presets send (X-Stainless-*) and anything credential-shaped.
func ForbiddenHeader(name string) bool {
	folded := strings.ToLower(name)
	if strings.HasPrefix(folded, switchboardPrefix) || strings.HasPrefix(folded, forwardedPrefix) || folded == realIPHeader || strings.HasPrefix(folded, stainlessPrefix) {
		return true
	}
	for _, stem := range credentialStems {
		if strings.Contains(folded, stem) {
			return true
		}
	}
	return false
}

// ValidTag reports whether tag has the activity tag shape.
func ValidTag(tag string) bool { return tagPattern.MatchString(tag) }

func validHeaderValue(value string) bool {
	if value == "" || len(value) > maxHeaderValue {
		return false
	}
	for index := 0; index < len(value); index++ {
		if value[index] < 0x20 || value[index] > 0x7e {
			return false
		}
	}
	return true
}

// Normalized trims the free-text fields and turns absent lists into empty
// ones.
func (rule RetryRule) Normalized() RetryRule {
	rule.ID = strings.TrimSpace(rule.ID)
	rule.Name = strings.TrimSpace(rule.Name)
	rule.Match.Statuses = nonNil(rule.Match.Statuses)
	rule.Match.Contains = trimmedList(rule.Match.Contains)
	rule.Match.ErrorCodes = trimmedList(rule.Match.ErrorCodes)
	rule.Match.Providers = trimmedList(rule.Match.Providers)
	rule.Match.Models = trimmedList(rule.Match.Models)
	return rule
}

// Validate checks a normalized retry rule against every contract limit.
func (rule RetryRule) Validate() error {
	if err := validateIdentity(rule.ID, rule.Name, rule.Owner); err != nil {
		return err
	}
	match := rule.Match
	if len(match.Statuses) == 0 && len(match.Contains) == 0 && len(match.ErrorCodes) == 0 {
		return ValidationError{"Retry rule match", "needs statuses, contains or errorCodes"}
	}
	if len(match.Statuses) > maxStatuses {
		return ValidationError{"Statuses", "are limited to 16"}
	}
	for index, status := range match.Statuses {
		if status < minErrorStatus || status > maxErrorStatus {
			return ValidationError{"Statuses", "must be 400-599"}
		}
		if slices.Contains(match.Statuses[:index], status) {
			return ValidationError{"Statuses", "must not repeat a status"}
		}
	}
	if len(match.Contains) > maxContains {
		return ValidationError{"Contains", "is limited to 8 entries"}
	}
	for _, text := range match.Contains {
		if utf8.RuneCountInString(text) < minContainsRunes || !validText(text, maxContainsRunes) {
			return ValidationError{"Contains entries", "must be 2-120 characters"}
		}
	}
	if len(match.ErrorCodes) > maxErrorCodes {
		return ValidationError{"Error codes", "are limited to 8"}
	}
	for _, code := range match.ErrorCodes {
		if !errorCodePattern.MatchString(code) {
			return ValidationError{"Error codes", "must be lowercase error codes"}
		}
	}
	if len(match.Providers) > maxGlobs {
		return ValidationError{"Providers", "are limited to 16"}
	}
	for _, provider := range match.Providers {
		if !validText(provider, maxProviderRunes) {
			return ValidationError{"Providers", "must be provider ids of 1-64 characters"}
		}
	}
	if err := validateGlobs("Model patterns", match.Models); err != nil {
		return err
	}
	switch rule.Action {
	case RetryRetry, RetryRotateKey, RetrySwitch, RetryFail:
	default:
		return ValidationError{"Retry action", "must be retry, rotate_key, switch_provider or fail"}
	}
	if rule.MaxAttempts < 1 || rule.MaxAttempts > maxRetryAttempts {
		return ValidationError{"Max attempts", "must be 1-10"}
	}
	if rule.DelayMS < 0 || rule.DelayMS > maxRetryDelayMS {
		return ValidationError{"Delay", "must be 0-60000 ms"}
	}
	return nil
}

// Validate checks a whole set: every rule, the per-kind caps, and ids that
// are unique across both kinds (statistics share one id space).
func (set Set) Validate() error {
	if len(set.RequestRules) > MaxRequestRules {
		return ValidationError{"Request rules", "are limited to 64"}
	}
	if len(set.RetryRules) > MaxRetryRules {
		return ValidationError{"Retry rules", "are limited to 64"}
	}
	ids := make(map[string]bool, len(set.RequestRules)+len(set.RetryRules))
	for _, rule := range set.RequestRules {
		if err := rule.Validate(); err != nil {
			return err
		}
		if ids[rule.ID] {
			return ValidationError{"Rule id", "must be unique"}
		}
		ids[rule.ID] = true
	}
	for _, rule := range set.RetryRules {
		if err := rule.Validate(); err != nil {
			return err
		}
		if ids[rule.ID] {
			return ValidationError{"Rule id", "must be unique"}
		}
		ids[rule.ID] = true
	}
	return nil
}

// Normalized normalizes every rule of the set.
func (set Set) Normalized() Set {
	result := Set{
		RequestRules: make([]RequestRule, 0, len(set.RequestRules)),
		RetryRules:   make([]RetryRule, 0, len(set.RetryRules)),
	}
	for _, rule := range set.RequestRules {
		result.RequestRules = append(result.RequestRules, rule.Normalized())
	}
	for _, rule := range set.RetryRules {
		result.RetryRules = append(result.RetryRules, rule.Normalized())
	}
	return result
}

// Clone returns a deep copy the caller may keep.
func (set Set) Clone() Set { return set.Normalized() }

func validateIdentity(id, name, owner string) error {
	if !ruleIDPattern.MatchString(id) {
		return ValidationError{"Rule id", "must be 1-48 lowercase letters, digits, '_' or '-', starting with a letter or digit"}
	}
	if !validText(name, maxNameRunes) {
		return ValidationError{"Rule name", "must be 1-60 characters"}
	}
	if !validOwner(owner) {
		return ValidationError{"Rule owner", "is invalid"}
	}
	return nil
}

func validateGlobs(field string, globs []string) error {
	if len(globs) > maxGlobs {
		return ValidationError{field, "are limited to 16"}
	}
	for _, glob := range globs {
		if !validText(glob, maxGlobRunes) {
			return ValidationError{field, "must be 1-200 characters"}
		}
	}
	return nil
}

func validText(value string, limit int) bool {
	if value == "" || !utf8.ValidString(value) || utf8.RuneCountInString(value) > limit {
		return false
	}
	for _, character := range value {
		if character < 32 || character == 127 {
			return false
		}
	}
	return true
}

func trimmedList(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		result = append(result, strings.TrimSpace(value))
	}
	return result
}

func nonNil[T any](values []T) []T {
	if values == nil {
		return []T{}
	}
	return slices.Clone(values)
}

// Matches reports whether a request for model on endpoint matches.
func (match RequestMatch) Matches(model, endpoint string) bool {
	if len(match.Endpoints) > 0 && !slices.Contains(match.Endpoints, endpoint) {
		return false
	}
	return matchesAnyGlob(match.Models, model)
}

// Matches reports whether a failed attempt matches.
func (match RetryMatch) Matches(failure Failure) bool {
	if len(match.Statuses) > 0 && !slices.Contains(match.Statuses, failure.Status) {
		return false
	}
	if len(match.ErrorCodes) > 0 && !slices.Contains(match.ErrorCodes, failure.ErrorCode) && !(httpStatusCode(failure.ErrorCode) && slices.ContainsFunc(match.ErrorCodes, httpStatusCode)) {
		return false
	}
	if len(match.Providers) > 0 && !slices.Contains(match.Providers, failure.ProviderID) {
		return false
	}
	if len(match.Contains) > 0 {
		found := false
		for _, text := range match.Contains {
			if strings.Contains(failure.ErrorText, NormalizeErrorText(text)) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return matchesAnyGlob(match.Models, failure.Model)
}

func matchesAnyGlob(globs []string, value string) bool {
	if len(globs) == 0 {
		return true
	}
	for _, glob := range globs {
		if Glob(glob, value) {
			return true
		}
	}
	return false
}

// Glob matches value against pattern case-insensitively over the whole
// string. '*' matches any run of characters; every other character is
// literal.
func Glob(pattern, value string) bool {
	pattern, value = strings.ToLower(pattern), strings.ToLower(value)
	// Greedy match with single-star backtracking: linear in practice and
	// bounded by len(pattern)*len(value) at worst, both capped.
	p, v := 0, 0
	star, mark := -1, 0
	for v < len(value) {
		switch {
		case p < len(pattern) && pattern[p] == '*':
			star, mark = p, v
			p++
		case p < len(pattern) && pattern[p] == value[v]:
			p++
			v++
		case star >= 0:
			p = star + 1
			mark++
			v = mark
		default:
			return false
		}
	}
	for p < len(pattern) && pattern[p] == '*' {
		p++
	}
	return p == len(pattern)
}

// httpStatusCode reports whether code names "the provider answered with an
// error status". The relay asks retry rules with upstream_status, while history
// files the same failure as request_rejected — the code a rule drafted from a
// recent error carries — so either spelling matches the other.
func httpStatusCode(code string) bool { return code == "upstream_status" || code == "request_rejected" }

// NormalizeErrorText is the relay's error-text normalization: lower case,
// with '_' and '-' read as spaces, so "rate_limit" and "rate-limit" match
// "rate limit".
func NormalizeErrorText(text string) string {
	return errorTextReplacer.Replace(strings.ToLower(text))
}

var errorTextReplacer = strings.NewReplacer("_", " ", "-", " ")
