package application

import (
	"slices"
	"strings"
	"time"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/rules/domain"
)

// Request is the part of a local relay request request rules match on.
type Request struct {
	Model    string
	Endpoint string
}

// Limit refuses a request: the named rule's rate limit or budget is spent
// until RetryAfter has passed.
type Limit struct {
	RuleName   string
	RetryAfter time.Duration
}

// Outcome is the merged effect of the matching request rules. Block and Limit
// are mutually exclusive; with either set nothing was counted against a
// window.
type Outcome struct {
	Provider string
	Model    string
	Block    *domain.Block
	Limit    *Limit
	Headers  map[string]string
	Tags     []string
	// Budgeted names the admitted rules with a token budget.
	Budgeted []string
}

// RetryDecision is the first matching custom retry rule.
type RetryDecision struct {
	RuleID      string
	Action      string
	MaxAttempts int
	Delay       time.Duration
}

// RuleStats are one rule's in-memory counters. For a request rule the window
// is its budget period when it has a budget, otherwise its rate-limit window;
// retry rules only count hits.
type RuleStats struct {
	Hits           int64      `json:"hits"`
	LastHitAt      *time.Time `json:"lastHitAt,omitempty"`
	WindowRequests int64      `json:"windowRequests"`
	WindowTokens   int64      `json:"windowTokens"`
	WindowResetsAt *time.Time `json:"windowResetsAt,omitempty"`
}

// counter is one rule's hit count and its open windows. The rate window is
// fixed and opens at the first request it admits; the budget window is the
// current local clock hour or day.
type counter struct {
	hits           int64
	lastHit        time.Time
	rateStart      time.Time
	rateRequests   int64
	budgetStart    time.Time
	budgetRequests int64
	budgetTokens   int64
}

// Evaluate merges the enabled request rules matching request, counts their
// hits, and admits the request against every matching rate limit and budget
// at once: all of the windows count it or none do.
func (service *Service) Evaluate(request Request) Outcome {
	service.mu.Lock()
	defer service.mu.Unlock()
	now := service.now()
	matched := matchRequestRules(service.set.RequestRules, request)
	if len(matched) == 0 {
		return Outcome{}
	}
	for _, rule := range matched {
		entry := service.counterLocked(rule.ID)
		entry.hits++
		entry.lastHit = now
	}
	effects := mergeEffects(matched)
	outcome := Outcome{
		Provider: effects.Provider,
		Model:    effects.Model,
		Headers:  effects.Headers,
		Tags:     effects.Tags,
	}
	if effects.Block != nil {
		block := *effects.Block
		outcome.Block = &block
		return outcome
	}
	var refusal *Limit
	for _, rule := range matched {
		wait, refused := service.refusalLocked(rule, now)
		if refused && (refusal == nil || wait > refusal.RetryAfter) {
			refusal = &Limit{RuleName: rule.Name, RetryAfter: wait}
		}
	}
	if refusal != nil {
		outcome.Limit = refusal
		return outcome
	}
	for _, rule := range matched {
		service.admitLocked(rule, now)
		if budget := rule.Effects.Budget; budget != nil && budget.Tokens > 0 {
			outcome.Budgeted = append(outcome.Budgeted, rule.ID)
		}
	}
	return outcome
}

// SpendTokens charges a finished request's tokens to the current budget
// window of each named rule that still has a token budget.
func (service *Service) SpendTokens(ruleIDs []string, tokens int64) {
	if tokens <= 0 || len(ruleIDs) == 0 {
		return
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	now := service.now()
	for _, id := range ruleIDs {
		index := slices.IndexFunc(service.set.RequestRules, func(rule domain.RequestRule) bool { return rule.ID == id })
		if index < 0 {
			continue
		}
		budget := service.set.RequestRules[index].Effects.Budget
		if budget == nil || budget.Tokens == 0 {
			continue
		}
		entry := service.counterLocked(id)
		rollBudget(entry, budget.Period, now)
		entry.budgetTokens += tokens
	}
}

// Retry returns the first enabled custom retry rule matching failure and
// counts its hit.
func (service *Service) Retry(failure domain.Failure) (RetryDecision, bool) {
	failure.ErrorText = domain.NormalizeErrorText(failure.ErrorText)
	service.mu.Lock()
	defer service.mu.Unlock()
	rule, ok := firstRetryRule(service.set.RetryRules, failure)
	if !ok {
		return RetryDecision{}, false
	}
	entry := service.counterLocked(rule.ID)
	entry.hits++
	entry.lastHit = service.now()
	return RetryDecision{
		RuleID:      rule.ID,
		Action:      rule.Action,
		MaxAttempts: rule.MaxAttempts,
		Delay:       time.Duration(rule.DelayMS) * time.Millisecond,
	}, true
}

// Stats returns every current rule's counters, zero-hit rules included.
func (service *Service) Stats() map[string]RuleStats {
	service.mu.Lock()
	defer service.mu.Unlock()
	now := service.now()
	result := make(map[string]RuleStats, len(service.set.RequestRules)+len(service.set.RetryRules))
	for _, rule := range service.set.RequestRules {
		stats := service.hitStatsLocked(rule.ID)
		entry := service.counters[rule.ID]
		switch {
		case rule.Effects.Budget != nil:
			start := periodStart(rule.Effects.Budget.Period, now)
			resets := periodEnd(rule.Effects.Budget.Period, start)
			stats.WindowResetsAt = &resets
			if entry != nil && entry.budgetStart.Equal(start) {
				stats.WindowRequests = entry.budgetRequests
				stats.WindowTokens = entry.budgetTokens
			}
		case rule.Effects.RateLimit != nil && entry != nil && !entry.rateStart.IsZero():
			resets := entry.rateStart.Add(time.Duration(rule.Effects.RateLimit.WindowSeconds) * time.Second)
			if now.Before(resets) {
				stats.WindowRequests = entry.rateRequests
				stats.WindowResetsAt = &resets
			}
		}
		result[rule.ID] = stats
	}
	for _, rule := range service.set.RetryRules {
		result[rule.ID] = service.hitStatsLocked(rule.ID)
	}
	return result
}

func (service *Service) hitStatsLocked(id string) RuleStats {
	entry := service.counters[id]
	if entry == nil || entry.hits == 0 {
		return RuleStats{}
	}
	last := entry.lastHit
	return RuleStats{Hits: entry.hits, LastHitAt: &last}
}

func (service *Service) counterLocked(id string) *counter {
	entry := service.counters[id]
	if entry == nil {
		entry = &counter{}
		service.counters[id] = entry
	}
	return entry
}

// refusalLocked reports whether one more request would exceed rule's rate
// limit or budget, and how long until it would not. It changes nothing.
func (service *Service) refusalLocked(rule domain.RequestRule, now time.Time) (time.Duration, bool) {
	entry := service.counters[rule.ID]
	var wait time.Duration
	refused := false
	if limit := rule.Effects.RateLimit; limit != nil && entry != nil && !entry.rateStart.IsZero() {
		ends := entry.rateStart.Add(time.Duration(limit.WindowSeconds) * time.Second)
		if now.Before(ends) && entry.rateRequests >= limit.Requests {
			refused, wait = true, ends.Sub(now)
		}
	}
	if budget := rule.Effects.Budget; budget != nil && entry != nil {
		start := periodStart(budget.Period, now)
		if entry.budgetStart.Equal(start) {
			spent := (budget.Requests > 0 && entry.budgetRequests >= budget.Requests) ||
				(budget.Tokens > 0 && entry.budgetTokens >= budget.Tokens)
			if spent {
				refused, wait = true, max(wait, periodEnd(budget.Period, start).Sub(now))
			}
		}
	}
	return wait, refused
}

func (service *Service) admitLocked(rule domain.RequestRule, now time.Time) {
	if rule.Effects.RateLimit == nil && rule.Effects.Budget == nil {
		return
	}
	entry := service.counterLocked(rule.ID)
	if limit := rule.Effects.RateLimit; limit != nil {
		if entry.rateStart.IsZero() || !now.Before(entry.rateStart.Add(time.Duration(limit.WindowSeconds)*time.Second)) {
			entry.rateStart, entry.rateRequests = now, 0
		}
		entry.rateRequests++
	}
	if budget := rule.Effects.Budget; budget != nil {
		rollBudget(entry, budget.Period, now)
		entry.budgetRequests++
	}
}

func rollBudget(entry *counter, period string, now time.Time) {
	start := periodStart(period, now)
	if !entry.budgetStart.Equal(start) {
		entry.budgetStart, entry.budgetRequests, entry.budgetTokens = start, 0, 0
	}
}

// periodStart is the start of now's local clock hour or day.
func periodStart(period string, now time.Time) time.Time {
	year, month, day := now.Date()
	if period == domain.BudgetPeriodHour {
		return time.Date(year, month, day, now.Hour(), 0, 0, 0, now.Location())
	}
	return time.Date(year, month, day, 0, 0, 0, 0, now.Location())
}

// periodEnd is the start of the next period. Built from the calendar, so a
// day across a daylight-saving change still ends at the next midnight.
func periodEnd(period string, start time.Time) time.Time {
	year, month, day := start.Date()
	if period == domain.BudgetPeriodHour {
		return time.Date(year, month, day, start.Hour()+1, 0, 0, 0, start.Location())
	}
	return time.Date(year, month, day+1, 0, 0, 0, 0, start.Location())
}

// matchRequestRules returns the enabled rules matching request, in order, up
// to and including the first that stops evaluation.
func matchRequestRules(rules []domain.RequestRule, request Request) []domain.RequestRule {
	var matched []domain.RequestRule
	for _, rule := range rules {
		if !rule.Enabled || !rule.Match.Matches(request.Model, request.Endpoint) {
			continue
		}
		matched = append(matched, rule)
		if rule.Effects.Stop {
			break
		}
	}
	return matched
}

// mergeEffects combines matched rules' effects as the relay applies them:
// provider, model, block, rate limit and budget take the first match;
// headers are first-wins per case-insensitive name; tags are a union capped
// at domain.MaxTags. Stop is not carried.
func mergeEffects(matched []domain.RequestRule) domain.Effects {
	var merged domain.Effects
	seenHeaders := make(map[string]bool)
	for _, rule := range matched {
		effects := rule.Effects
		if merged.Provider == "" {
			merged.Provider = effects.Provider
		}
		if merged.Model == "" {
			merged.Model = effects.Model
		}
		if merged.Block == nil && effects.Block != nil {
			block := *effects.Block
			merged.Block = &block
		}
		if merged.RateLimit == nil && effects.RateLimit != nil {
			limit := *effects.RateLimit
			merged.RateLimit = &limit
		}
		if merged.Budget == nil && effects.Budget != nil {
			budget := *effects.Budget
			merged.Budget = &budget
		}
		for _, name := range sortedKeys(effects.Headers) {
			folded := strings.ToLower(name)
			if seenHeaders[folded] {
				continue
			}
			seenHeaders[folded] = true
			if merged.Headers == nil {
				merged.Headers = make(map[string]string)
			}
			merged.Headers[name] = effects.Headers[name]
		}
		for _, tag := range effects.Tags {
			if len(merged.Tags) < domain.MaxTags && !slices.Contains(merged.Tags, tag) {
				merged.Tags = append(merged.Tags, tag)
			}
		}
	}
	return merged
}

func sortedKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

func firstRetryRule(rules []domain.RetryRule, failure domain.Failure) (domain.RetryRule, bool) {
	for _, rule := range rules {
		if rule.Enabled && rule.Match.Matches(failure) {
			return rule, true
		}
	}
	return domain.RetryRule{}, false
}
