package relayhttp

import (
	"bytes"
	"encoding/json"
	"sync"
)

// providerAdjustments is the request shape one provider has been measured to
// accept. Every entry here was learned the expensive way — a request the
// provider answered 400, and the repair that made the retry succeed — so the
// fields carry the exact transformations the repairs already make, nothing
// new: applied ahead of the first attempt they only move the repair before
// the probe instead of after it.
type providerAdjustments struct {
	// developerRole rewrites "developer" messages to "system": chat-only
	// upstreams validate roles against the classic five (measured: "developer
	// is not one of ['system', 'assistant', 'user', 'tool', 'function']").
	developerRole bool
	// maxTokens renames max_tokens to max_completion_tokens.
	maxTokens bool
	// dropFields lists top-level parameters the provider refuses.
	dropFields []string
	// efforts is the provider's accepted reasoning-effort list, verbatim from
	// its own complaint; empty means the effort was never refused.
	efforts []string
}

// repairMemo remembers, per provider, which request repairs a successful
// exchange needed. The first request to a provider still pays for its own
// probe — the provider's 400 is the only honest source of what it accepts —
// but every request after the successful retry sends the shape that already
// worked. The chat-endpoint discovery caches exactly this way, and for the
// same reason: one probe, then memory. Nothing persists across restarts, so a
// provider that changes its API is re-learned in one request, not stuck on a
// stale shape.
type repairMemo struct {
	mu          sync.Mutex
	adjustments map[string]providerAdjustments
}

func newRepairMemo() *repairMemo {
	return &repairMemo{adjustments: make(map[string]providerAdjustments, 8)}
}

// merge folds what one exchange learned into the provider's record. The
// caller only invokes this after the request finally succeeded, so a failure
// chain teaches nothing — a provider that is simply down does not get its
// parameters "repaired" out of the next request.
func (memo *repairMemo) merge(providerID string, learned providerAdjustments) {
	if !learned.developerRole && !learned.maxTokens && len(learned.dropFields) == 0 && len(learned.efforts) == 0 {
		return
	}
	memo.mu.Lock()
	defer memo.mu.Unlock()
	known := memo.adjustments[providerID]
	if learned.developerRole {
		known.developerRole = true
	}
	if learned.maxTokens {
		known.maxTokens = true
	}
	for _, field := range learned.dropFields {
		if !containsString(known.dropFields, field) {
			known.dropFields = append(known.dropFields, field)
		}
	}
	if len(learned.efforts) > 0 {
		known.efforts = learned.efforts
	}
	memo.adjustments[providerID] = known
}

// apply rewrites a request body into the shape this provider already
// accepted. It is deliberately a no-op on a body that carries none of the
// learned fields: a Responses-shaped body has no messages array or
// max_tokens, so a shared relay reuses one memo without dialect damage.
func (memo *repairMemo) apply(providerID string, body []byte) []byte {
	memo.mu.Lock()
	known := memo.adjustments[providerID]
	memo.mu.Unlock()
	if !known.developerRole && !known.maxTokens && len(known.dropFields) == 0 && len(known.efforts) == 0 {
		return body
	}
	var payload map[string]any
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if decoder.Decode(&payload) != nil || payload == nil {
		return body
	}
	changed := false
	if known.developerRole {
		changed = rewriteDeveloperRoles(payload) || changed
	}
	if known.maxTokens {
		if tokens, carried := payload["max_tokens"]; carried {
			delete(payload, "max_tokens")
			payload["max_completion_tokens"] = tokens
			changed = true
		}
	}
	for _, field := range known.dropFields {
		if _, carried := payload[field]; carried {
			delete(payload, field)
			changed = true
		}
	}
	if len(known.efforts) > 0 {
		changed = alignReasoningEffort(payload, known.efforts) || changed
	}
	if !changed {
		return body
	}
	repaired, err := json.Marshal(payload)
	if err != nil {
		return body
	}
	return repaired
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// rewriteDeveloperRoles rewrites "developer" messages to "system" in place
// and reports whether anything changed. Both the reactive repair and the
// memo's proactive pass walk the same messages array; one function keeps the
// two from drifting apart.
func rewriteDeveloperRoles(payload map[string]any) bool {
	messages, _ := payload["messages"].([]any)
	changed := false
	for _, entry := range messages {
		message, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		if role, _ := message["role"].(string); role == "developer" {
			message["role"] = "system"
			changed = true
		}
	}
	return changed
}

// alignReasoningEffort maps the request's reasoning_effort onto the list the
// provider named in its own complaint: nearest accepted rank at or below the
// ask, the cheapest offered when the ask sits under everything, dropped when
// the ask or the list cannot be ranked. The second return says whether the
// body changed.
func alignReasoningEffort(payload map[string]any, accepted []string) bool {
	requested, _ := payload["reasoning_effort"].(string)
	if requested == "" {
		return false
	}
	rankable := rankableEfforts(accepted)
	if containsString(rankable, requested) {
		return false
	}
	requestedRank, known := reasoningEffortRanks[requested]
	if !known {
		delete(payload, "reasoning_effort")
		return true
	}
	if len(rankable) == 0 {
		delete(payload, "reasoning_effort")
		return true
	}
	replacement, changed := nearestReasoningEffort(requestedRank, rankable)
	if !changed {
		return false
	}
	payload["reasoning_effort"] = replacement
	return true
}
