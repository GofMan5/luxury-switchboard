package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/rules/domain"
)

// ErrHistoryUnavailable names recentErrors without a readable history:
// unavailable history is not an empty list.
var ErrHistoryUnavailable = errors.New("request history is unavailable")

// TestInput is a dry run of the rules: a request for Model on Endpoint and,
// optionally, a failed attempt of it.
type TestInput struct {
	Model      string
	Endpoint   string
	Status     int
	ErrorText  string
	ErrorCode  string
	ProviderID string
}

// TestResult names the request rules that would match, in order, their
// merged effects, and the retry rule a failure would hit.
type TestResult struct {
	RequestRules []string       `json:"requestRules"`
	Effects      domain.Effects `json:"effects"`
	RetryRule    string         `json:"retryRule,omitempty"`
}

// Test evaluates the rules without counting anything. The retry rules are
// consulted only when the input describes a failure.
func (service *Service) Test(input TestInput) TestResult {
	service.mu.Lock()
	defer service.mu.Unlock()
	matched := matchRequestRules(service.set.RequestRules, Request{Model: input.Model, Endpoint: input.Endpoint})
	result := TestResult{RequestRules: make([]string, 0, len(matched)), Effects: mergeEffects(matched)}
	for _, rule := range matched {
		result.RequestRules = append(result.RequestRules, rule.ID)
	}
	if input.Status != 0 || input.ErrorText != "" || input.ErrorCode != "" {
		failure := domain.Failure{
			ProviderID: input.ProviderID,
			Model:      input.Model,
			Status:     input.Status,
			ErrorCode:  input.ErrorCode,
			ErrorText:  domain.NormalizeErrorText(input.ErrorText),
		}
		if rule, ok := firstRetryRule(service.set.RetryRules, failure); ok {
			result.RetryRule = rule.ID
		}
	}
	return result
}

// FailureRecord is one failed request from history. Detail is the already
// redacted error detail the history stores.
type FailureRecord struct {
	At           time.Time
	Status       int
	ErrorCode    string
	Detail       string
	ProviderID   string
	ProviderName string
	Model        string
}

// FailureHistory reads the most recent failed requests, newest first.
type FailureHistory interface {
	RecentFailures(ctx context.Context) ([]FailureRecord, error)
}

// RecentError is one group of similar recent failures.
type RecentError struct {
	Signature    string    `json:"signature"`
	Status       int       `json:"status"`
	ErrorCode    string    `json:"errorCode"`
	Detail       string    `json:"detail"`
	ProviderName string    `json:"providerName"`
	Model        string    `json:"model"`
	Count        int       `json:"count"`
	LastAt       time.Time `json:"lastAt"`
	CoveredBy    string    `json:"coveredBy,omitempty"`
}

const (
	maxRecentErrors   = 100
	maxRecentDetail   = 200
	transportCode     = "transport"
	signatureByteSize = 8
)

// RecentErrors groups the recent failed requests by status, error code and
// normalized detail, most frequent first, and names the custom retry rule
// that would now handle each. Nothing is counted.
func (service *Service) RecentErrors(ctx context.Context, limit int) ([]RecentError, error) {
	if service.history == nil {
		return nil, ErrHistoryUnavailable
	}
	records, err := service.history.RecentFailures(ctx)
	if err != nil {
		return nil, ErrHistoryUnavailable
	}
	limit = min(max(limit, 1), maxRecentErrors)
	groups := make(map[string]*RecentError)
	order := make([]*RecentError, 0)
	for _, record := range records {
		key := strconv.Itoa(record.Status) + "\x00" + record.ErrorCode + "\x00" + normalizeDetail(record.Detail)
		group := groups[key]
		if group == nil {
			sum := sha256.Sum256([]byte(key))
			group = &RecentError{
				Signature:    hex.EncodeToString(sum[:signatureByteSize]),
				Status:       record.Status,
				ErrorCode:    record.ErrorCode,
				Detail:       truncateRunes(record.Detail, maxRecentDetail),
				ProviderName: record.ProviderName,
				Model:        record.Model,
				LastAt:       record.At.UTC(),
			}
			groups[key] = group
			order = append(order, group)
		}
		group.Count++
		if record.At.After(group.LastAt) {
			group.LastAt = record.At.UTC()
			group.Detail = truncateRunes(record.Detail, maxRecentDetail)
			group.ProviderName, group.Model = record.ProviderName, record.Model
		}
		if group.CoveredBy == "" {
			group.CoveredBy = service.coveredBy(record)
		}
	}
	slices.SortStableFunc(order, func(left, right *RecentError) int {
		if left.Count != right.Count {
			return right.Count - left.Count
		}
		return right.LastAt.Compare(left.LastAt)
	})
	result := make([]RecentError, 0, min(limit, len(order)))
	for _, group := range order[:min(limit, len(order))] {
		result = append(result, *group)
	}
	return result, nil
}

// coveredBy names the enabled retry rule a record's failure would hit now.
// A transport failure is matched with status zero, as the relay asks.
func (service *Service) coveredBy(record FailureRecord) string {
	failure := domain.Failure{
		ProviderID: record.ProviderID,
		Model:      record.Model,
		Status:     record.Status,
		ErrorCode:  record.ErrorCode,
		ErrorText:  domain.NormalizeErrorText(record.Detail),
	}
	if record.ErrorCode == transportCode {
		failure.Status = 0
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	if rule, ok := firstRetryRule(service.set.RetryRules, failure); ok {
		return rule.ID
	}
	return ""
}

// normalizeDetail is the grouping form of an error detail: lower case, digit
// runs read as one '#', whitespace collapsed, so request ids, counts and
// timestamps do not split one failure into many groups.
func normalizeDetail(detail string) string {
	var builder strings.Builder
	builder.Grow(len(detail))
	digits, space := false, false
	for _, character := range strings.ToLower(detail) {
		switch {
		case unicode.IsDigit(character):
			if !digits {
				builder.WriteByte('#')
			}
			digits, space = true, false
		case unicode.IsSpace(character):
			digits = false
			if !space && builder.Len() > 0 {
				builder.WriteByte(' ')
			}
			space = true
		default:
			digits, space = false, false
			builder.WriteRune(character)
		}
	}
	return strings.TrimRight(builder.String(), " ")
}

func truncateRunes(value string, limit int) string {
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	runes := []rune(value)
	return string(runes[:limit])
}
