package relayhttp

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"sync"

	"github.com/luxuryprivate/switchboard/backend/internal/platform/atomicfile"
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
// same reason: one probe, then memory. When a path is wired, the memo also
// survives restarts: the probe was a full round trip on the first request of
// every launch, and the shape it bought is not a secret worth forgetting.
// A provider that changes its API is re-learned the moment its accepted shape
// stops working: the probe 400 comes back, the ladder repairs reactively,
// and the new shape replaces the stale one.
type repairMemo struct {
	mu          sync.Mutex
	adjustments map[string]providerAdjustments
}

func newRepairMemo() *repairMemo {
	return &repairMemo{adjustments: make(map[string]providerAdjustments, 8)}
}

// RepairProfile is the persisted form of one provider's learned shape. It
// carries no secret — provider ids, field names, and the effort levels a
// provider's own error message named.
type RepairProfile struct {
	DeveloperRole bool     `json:"developerRole"`
	MaxTokens     bool     `json:"maxTokens"`
	DropFields    []string `json:"dropFields,omitempty"`
	Efforts       []string `json:"efforts,omitempty"`
}

// snapshot copies the memo into its persisted form.
func (memo *repairMemo) snapshot() map[string]RepairProfile {
	memo.mu.Lock()
	defer memo.mu.Unlock()
	profiles := make(map[string]RepairProfile, len(memo.adjustments))
	for providerID, adjustments := range memo.adjustments {
		profiles[providerID] = RepairProfile{
			DeveloperRole: adjustments.developerRole,
			MaxTokens:     adjustments.maxTokens,
			DropFields:    append([]string(nil), adjustments.dropFields...),
			Efforts:       append([]string(nil), adjustments.efforts...),
		}
	}
	return profiles
}

// restore loads a persisted snapshot into the memo.
func (memo *repairMemo) restore(profiles map[string]RepairProfile) {
	memo.mu.Lock()
	defer memo.mu.Unlock()
	for providerID, profile := range profiles {
		memo.adjustments[providerID] = providerAdjustments{
			developerRole: profile.DeveloperRole,
			maxTokens:     profile.MaxTokens,
			dropFields:    append([]string(nil), profile.DropFields...),
			efforts:       append([]string(nil), profile.Efforts...),
		}
	}
}

// loadRepairMemo reads the persisted shape. Any failure yields an empty memo:
// the file is an optimization, and the honest fallback is one probe.
func loadRepairMemo(path string) *repairMemo {
	memo := newRepairMemo()
	payload, err := os.ReadFile(path)
	if err != nil {
		return memo
	}
	var profiles map[string]RepairProfile
	if json.Unmarshal(payload, &profiles) != nil {
		return memo
	}
	memo.restore(profiles)
	return memo
}

// persist writes the memo atomically. Rare by construction — once per
// provider — so the whole snapshot per write costs nothing.
func (memo *repairMemo) persist(path string) error {
	payload, err := json.MarshalIndent(memo.snapshot(), "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.Replace(path, payload, 0o600)
}

// merge folds what one exchange learned into the provider's record. The
// caller only invokes this after the request finally succeeded, so a failure
// chain teaches nothing — a provider that is simply down does not get its
// parameters "repaired" out of the next request. It reports whether the
// record changed, so the caller persists only when there is something new.
func (memo *repairMemo) merge(providerID string, learned providerAdjustments) bool {
	if !learned.developerRole && !learned.maxTokens && len(learned.dropFields) == 0 && len(learned.efforts) == 0 {
		return false
	}
	memo.mu.Lock()
	defer memo.mu.Unlock()
	known := memo.adjustments[providerID]
	before := RepairProfile{
		DeveloperRole: known.developerRole, MaxTokens: known.maxTokens,
		DropFields: append([]string(nil), known.dropFields...),
		Efforts:    append([]string(nil), known.efforts...),
	}
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
	after := RepairProfile{
		DeveloperRole: known.developerRole, MaxTokens: known.maxTokens,
		DropFields: append([]string(nil), known.dropFields...),
		Efforts:    append([]string(nil), known.efforts...),
	}
	return after.DeveloperRole != before.DeveloperRole || after.MaxTokens != before.MaxTokens ||
		len(after.DropFields) != len(before.DropFields) || len(after.Efforts) != len(before.Efforts)
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

// ensureStreamUsage asks a chat stream to report its tokens: an
// OpenAI-compatible provider includes usage in a stream ONLY when the request
// says include_usage, so without this the relay's meter has nothing to read
// — the request answers 200-and-healthy with zero tokens on every screen.
// The ask rides the dialect it belongs to: only a chat-path streaming JSON
// body gets it, a client that asked for itself keeps its own options, and a
// Responses-shaped body (no such field in that API) is untouched byte for
// byte.
func ensureStreamUsage(body []byte, contentType, path string) []byte {
	if len(body) == 0 || !strings.Contains(strings.ToLower(contentType), "json") {
		return body
	}
	// Chat only: the legacy completions dialect predates stream_options
	// entirely, and a Responses body has no such field — the ask rides the
	// dialect that defines it.
	if canonical := canonicalPath(path); !chatDialectPath(canonical) {
		return body
	}
	var payload map[string]any
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if decoder.Decode(&payload) != nil || payload == nil {
		return body
	}
	if stream, _ := payload["stream"].(bool); !stream {
		return body
	}
	if options, ok := payload["stream_options"].(map[string]any); ok {
		if include, _ := options["include_usage"].(bool); include {
			return body
		}
		options["include_usage"] = true
	} else {
		payload["stream_options"] = map[string]any{"include_usage": true}
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return body
	}
	return encoded
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
