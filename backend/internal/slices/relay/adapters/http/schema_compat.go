package relayhttp

import (
	"bytes"
	"encoding/json"
	"strings"
)

// Strict providers compile every tool schema in the request against a fixed
// subset of JSON Schema and refuse the whole request over a key they do not
// implement ("Unimplemented keys: [\"uniqueItems\"]. Compilation limits apply
// to all strict schemas in the request."). The refusal is about one key, so the
// repair removes exactly the keys the provider named, from every schema they
// appear in, and nothing else. A schema that keeps working is never rewritten.
//
// The named keys are read from the provider's own complaint rather than from a
// list of ours: the set of unimplemented keys differs between providers and
// changes without notice, and a wrong guess would weaken validation on a
// provider that does enforce the key.
const maxSchemaRepairKeys = 16

// unimplementedSchemaKeys reads the keys a provider named as unimplemented.
// It only answers for a complaint that actually carries that wording, so an
// unrelated 400 never strips anything. The keys are matched case-insensitively
// because the complaint is lowercased on arrival while the request is not.
func unimplementedSchemaKeys(errorBody []byte) []string {
	if len(errorBody) == 0 {
		return nil
	}
	// The complaint rides a JSON envelope, so decode it first: inside the
	// envelope the list is written `[\"uniqueItems\"]`, and a quote split on
	// the raw bytes reads the escapes as part of the key.
	var envelope struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
		Message string `json:"message"`
	}
	_ = json.Unmarshal(errorBody, &envelope)
	text := strings.ToLower(envelope.Error.Message + " " + envelope.Message)
	if !strings.Contains(text, "unimplemented key") {
		return nil
	}
	keys := make([]string, 0, 4)
	// The list follows the wording directly. A bracket anywhere earlier in the
	// complaint (`tools[118].parameters`) is a path index, not the list.
	position := strings.Index(text, "unimplemented key")
	for position >= 0 {
		open := strings.Index(text[position:], "[")
		if open < 0 {
			break
		}
		open += position
		close := strings.Index(text[open:], "]")
		if close < 0 {
			break
		}
		close += open
		for _, field := range strings.FieldsFunc(text[open+1:close], func(character rune) bool {
			return character == ',' || character == '"' || character == '\'' || character == ' ' || character == '\t'
		}) {
			if schemaKey(field) && !containsString(keys, field) {
				keys = append(keys, field)
			}
		}
		next := strings.Index(text[close+1:], "unimplemented key")
		if next < 0 {
			break
		}
		position = close + 1 + next
	}
	if len(keys) == 0 || len(keys) > maxSchemaRepairKeys {
		return nil
	}
	return keys
}

// matchesSchemaKey reports whether a request key is the named keyword,
// ignoring case. The complaint is lowercased on arrival; the request is not.
func matchesSchemaKey(requestKey, named string) bool {
	return strings.EqualFold(requestKey, named)
}

// schemaKey accepts the shape a JSON Schema keyword can have: letters, digits
// and a few punctuation marks. Anything longer or stranger is prose the
// complaint quoted, and removing a key by that name would be a guess.
func schemaKey(value string) bool {
	if len(value) == 0 || len(value) > 64 {
		return false
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' ||
			character == '_' || character == '-' || character == '$' || character == ':' {
			continue
		}
		return false
	}
	return true
}

// repairUnimplementedSchemaKeys removes the named keys from every schema in
// the request and reports whether anything changed. The walk is bounded so a
// hostile nesting cannot turn one repair into unbounded work.
func repairUnimplementedSchemaKeys(body []byte, keys []string) ([]byte, bool) {
	if len(body) == 0 || len(keys) == 0 {
		return body, false
	}
	var payload map[string]any
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if decoder.Decode(&payload) != nil {
		return body, false
	}
	changed := false
	for _, key := range keys {
		changed = removeSchemaKey(payload, key, 0) || changed
	}
	if !changed {
		return body, false
	}
	repaired, err := json.Marshal(payload)
	if err != nil {
		return body, false
	}
	return repaired, true
}

// removeSchemaKey deletes key from every object reachable from value. It does
// not distinguish a schema object from any other object on purpose: the key
// only ever reaches this walk because the provider refused the request over
// it, so wherever it sits — parameters, a nested definition, a response
// format — it is the thing the provider will refuse again.
func removeSchemaKey(value any, key string, depth int) bool {
	if depth > maxToolRewriteDepth {
		return false
	}
	var changed bool
	switch value := value.(type) {
	case map[string]any:
		for name := range value {
			if matchesSchemaKey(name, key) {
				delete(value, name)
				changed = true
			}
		}
		for _, child := range value {
			changed = removeSchemaKey(child, key, depth+1) || changed
		}
	case []any:
		for _, child := range value {
			changed = removeSchemaKey(child, key, depth+1) || changed
		}
	}
	return changed
}
