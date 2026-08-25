package relayhttp

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// Responses clients disagree with providers about how tools travel.
//
// Codex 0.14x and newer no longer send the documented top-level "tools" array:
// tools arrive inside the request input as an "additional_tools" item holding
// nested "namespace" groups. Providers that implement only the documented schema
// drop that item, so the model receives no tool definitions and leaks raw call
// syntax such as `to=functions.exec {"cmd":"git status"}` into its assistant
// text while the client never sees a tool call.
//
// Freeform ("custom") tools have the mirror problem: providers answer them with
// a documented function_call carrying {"input":"..."} arguments, which the client
// rejects because it declared a freeform tool.
//
// The relay therefore folds every declaration into the documented tools array and
// converts provider answers back into the shape the client declared. Requests
// that already use the documented schema are forwarded byte for byte.
//
// A freeform tool itself is forwarded exactly as declared. Its "format" is the
// grammar the payload has to follow, so rewriting the tool into a documented
// function taking a string throws that contract away: the model then answers
// `apply_patch` with an empty argument string and `exec` with a JSON object
// instead of the patch or the script, every call fails, and the assistant falls
// back to narrating its intentions. Providers that refuse the freeform type are
// served by downgradeFreeformTools once they say so.
const (
	maxToolNamespaceDepth = 8
	maxToolDefinitions    = 512
	maxToolNameBytes      = 64
	maxToolRewriteDepth   = 32
)

type toolCompat struct {
	toClient   map[string]string   // provider tool name -> name declared by the client
	toProvider map[string]string   // name declared by the client -> provider tool name
	freeform   map[string]struct{} // provider tool names the client declared as freeform
}

func (compat toolCompat) empty() bool {
	return len(compat.toClient) == 0 && len(compat.freeform) == 0
}

type declaredTool struct {
	qualified  string
	definition map[string]any
}

func normalizeResponsesTools(method, path, contentType string, body []byte) ([]byte, toolCompat) {
	if method != http.MethodPost || canonicalPath(path) != "/v1/responses" ||
		!strings.Contains(strings.ToLower(contentType), "json") || len(body) == 0 {
		return body, toolCompat{}
	}
	var payload map[string]any
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if decoder.Decode(&payload) != nil {
		return body, toolCompat{}
	}
	// A body may carry input as a bare string. Its tools still have to be read: the
	// compat map is what turns the provider's function_call back into the freeform
	// call the client declared, and without it the client is handed a call shape it
	// has no tool for. Only the input rewriting is skipped.
	input, inputIsList := payload["input"].([]any)
	existing, ok := payload["tools"].([]any)
	if payload["tools"] != nil && !ok {
		return body, toolCompat{}
	}
	kept := make([]any, 0, len(input))
	declared := make([]declaredTool, 0, 8)
	for _, entry := range input {
		item, _ := entry.(map[string]any)
		if item == nil || item["type"] != "additional_tools" {
			kept = append(kept, entry)
			continue
		}
		group, _ := item["tools"].([]any)
		declared = flattenToolNamespaces(group, nil, 0, declared)
	}
	tools, compat := providerToolDefinitions(existing, declared)
	if len(tools) > maxToolDefinitions {
		return body, toolCompat{}
	}
	changed := len(kept) != len(input)
	// Calls kept on the provider side cannot be matched locally, so their replayed
	// outputs must never be treated as orphans.
	_, serverHistory := payload["previous_response_id"].(string)
	kept, itemsChanged := normalizeInputItems(kept, compat, serverHistory)
	if itemsChanged {
		changed = true
	}
	if !changed && compat.empty() {
		return body, toolCompat{}
	}
	if inputIsList {
		payload["input"] = kept
	}
	if len(tools) > 0 {
		payload["tools"] = tools
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return body, toolCompat{}
	}
	return encoded, compat
}

func flattenToolNamespaces(group []any, prefix []string, depth int, declared []declaredTool) []declaredTool {
	if depth > maxToolNamespaceDepth {
		return declared
	}
	for _, entry := range group {
		definition, _ := entry.(map[string]any)
		if definition == nil {
			continue
		}
		name, _ := definition["name"].(string)
		name = strings.TrimSpace(name)
		if definition["type"] == "namespace" {
			nested, _ := definition["tools"].([]any)
			declared = flattenToolNamespaces(nested, namespacePrefix(prefix, name, depth), depth+1, declared)
			continue
		}
		if name == "" || len(declared) >= maxToolDefinitions {
			continue
		}
		declared = append(declared, declaredTool{qualified: strings.Join(append(append([]string(nil), prefix...), name), "."), definition: definition})
	}
	return declared
}

// The "functions" root namespace is implicit in every tool recipient, so it never
// becomes part of a qualified tool name.
func namespacePrefix(prefix []string, name string, depth int) []string {
	if name == "" || (depth == 0 && name == "functions") {
		return prefix
	}
	return append(append([]string(nil), prefix...), name)
}

func providerToolDefinitions(existing []any, declared []declaredTool) ([]any, toolCompat) {
	compat := toolCompat{toClient: map[string]string{}, toProvider: map[string]string{}, freeform: map[string]struct{}{}}
	listed := make(map[string]struct{}, len(existing)+len(declared))
	reserved := make(map[string]struct{}, len(existing)+len(declared))
	for _, entry := range existing {
		if name := definitionName(entry); name != "" {
			listed[name] = struct{}{}
			reserved[name] = struct{}{}
		}
	}
	for _, tool := range declared {
		if providerSafeToolName(tool.qualified) {
			reserved[tool.qualified] = struct{}{}
		}
	}
	tools := make([]any, 0, len(existing)+len(declared))
	for _, entry := range existing {
		definition, _ := entry.(map[string]any)
		if definition == nil {
			tools = append(tools, entry)
			continue
		}
		tools = append(tools, providerToolDefinition(definition, definitionName(entry), compat))
	}
	for _, tool := range declared {
		name := tool.qualified
		// Names that cannot be written as a plain JSON string are left untouched;
		// the provider decides whether to accept them.
		if !providerSafeToolName(name) && plainToolName(name) {
			name = providerToolName(tool.qualified, reserved)
			reserved[name] = struct{}{}
			compat.toClient[name] = tool.qualified
			compat.toProvider[tool.qualified] = name
		}
		if _, duplicate := listed[name]; duplicate {
			continue
		}
		listed[name] = struct{}{}
		tools = append(tools, providerToolDefinition(tool.definition, name, compat))
	}
	return tools, compat
}

// providerToolDefinition renames a tool definition. A freeform tool is recorded
// so provider answers can be matched back to it, but its own definition travels
// untouched: "format" is the grammar its payload must follow.
func providerToolDefinition(definition map[string]any, name string, compat toolCompat) map[string]any {
	if definition["type"] == "custom" {
		compat.freeform[name] = struct{}{}
	}
	if name == definitionName(definition) {
		return definition
	}
	clone := make(map[string]any, len(definition))
	for key, value := range definition {
		clone[key] = value
	}
	clone["name"] = name
	return clone
}

// documentedFreeformTool expresses a freeform tool as a documented function
// taking a single "input" string, which is exactly how providers that do not
// implement freeform tools report their calls back.
func documentedFreeformTool(definition map[string]any) map[string]any {
	clone := make(map[string]any, len(definition)+1)
	for key, value := range definition {
		if key == "format" || key == "parameters" {
			continue
		}
		clone[key] = value
	}
	clone["type"] = "function"
	clone["strict"] = false
	clone["parameters"] = map[string]any{
		"type":                 "object",
		"properties":           map[string]any{"input": map[string]any{"type": "string"}},
		"required":             []any{"input"},
		"additionalProperties": false,
	}
	return clone
}

// normalizeInputItems rewrites replayed conversation history so it matches the
// tool definitions the provider now sees: earlier calls carry the same names, and
// every call carries exactly one string output. Clients may report an extra output
// for the same call (a background notification) or content-part arrays, which
// documented providers reject outright.
func normalizeInputItems(input []any, compat toolCompat, serverHistory bool) ([]any, bool) {
	changed := false
	calls := make(map[string]struct{}, len(input)/2+1)
	outputs := make(map[string]map[string]any, len(input)/2+1)
	kept := make([]any, 0, len(input))
	for _, entry := range input {
		item, _ := entry.(map[string]any)
		if item == nil {
			kept = append(kept, entry)
			continue
		}
		callID, _ := item["call_id"].(string)
		switch kind, _ := item["type"].(string); kind {
		case "function_call", "custom_tool_call":
			name, _ := item["name"].(string)
			if provider, aliased := compat.toProvider[name]; aliased {
				item["name"] = provider
				changed = true
			}
			if callID != "" {
				calls[callID] = struct{}{}
			}
		case "function_call_output", "custom_tool_call_output":
			if text, converted := toolOutputText(item["output"]); converted {
				item["output"] = text
				changed = true
			}
			if callID == "" {
				break
			}
			if _, called := calls[callID]; !called && !serverHistory {
				changed = true
				continue
			}
			if previous := outputs[callID]; previous != nil {
				appendToolOutput(previous, item)
				changed = true
				continue
			}
			outputs[callID] = item
		}
		kept = append(kept, entry)
	}
	return kept, changed
}

// toolOutputText flattens the content-part arrays some clients send into the
// documented output string.
func toolOutputText(value any) (string, bool) {
	switch value := value.(type) {
	case []any:
		parts := make([]string, 0, len(value))
		for _, entry := range value {
			switch part := entry.(type) {
			case string:
				parts = append(parts, part)
			case map[string]any:
				if text, ok := part["text"].(string); ok {
					parts = append(parts, text)
					continue
				}
				if encoded, err := json.Marshal(part); err == nil {
					parts = append(parts, string(encoded))
				}
			}
		}
		return strings.Join(parts, "\n"), true
	case map[string]any:
		if text, ok := value["text"].(string); ok {
			return text, true
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			return "", false
		}
		return string(encoded), true
	}
	return "", false
}

func appendToolOutput(target, extra map[string]any) {
	text, _ := extra["output"].(string)
	if text == "" {
		return
	}
	previous, _ := target["output"].(string)
	if previous == "" {
		target["output"] = text
		return
	}
	target["output"] = previous + "\n" + text
}

// downgradeFreeformTools answers a provider that rejected the freeform tool type
// by re-expressing every freeform tool as a documented function, and by rewriting
// the replayed freeform history to match. It is only reached after such a refusal,
// so a provider that implements freeform tools never loses their grammar.
func downgradeFreeformTools(body []byte) ([]byte, bool) {
	if len(body) == 0 {
		return body, false
	}
	var payload map[string]any
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if decoder.Decode(&payload) != nil {
		return body, false
	}
	tools, _ := payload["tools"].([]any)
	freeform := make(map[string]struct{}, len(tools))
	for index, entry := range tools {
		definition, _ := entry.(map[string]any)
		if definition == nil || definition["type"] != "custom" {
			continue
		}
		if name := definitionName(definition); name != "" {
			freeform[name] = struct{}{}
		}
		tools[index] = documentedFreeformTool(definition)
	}
	if len(freeform) == 0 {
		return body, false
	}
	input, _ := payload["input"].([]any)
	for _, entry := range input {
		item, _ := entry.(map[string]any)
		if item == nil {
			continue
		}
		switch item["type"] {
		case "custom_tool_call":
			name, _ := item["name"].(string)
			if _, declared := freeform[name]; !declared {
				continue
			}
			text, _ := item["input"].(string)
			encoded, err := json.Marshal(map[string]any{"input": text})
			if err != nil {
				continue
			}
			item["type"] = "function_call"
			item["arguments"] = string(encoded)
			delete(item, "input")
		case "custom_tool_call_output":
			item["type"] = "function_call_output"
			delete(item, "name")
		}
	}
	downgraded, err := json.Marshal(payload)
	if err != nil {
		return body, false
	}
	return downgraded, true
}

// stripEncryptedReasoning removes the reasoning history a Responses client
// replays, together with the include entry that asks for it.
//
// Encrypted reasoning is sealed for the account that produced it. The relay
// rotates keys per attempt and providers pool accounts of their own, so the very
// next turn is usually presented to a different account, which cannot open the
// blob and refuses the whole request. The first turn of a conversation carries no
// reasoning and always succeeds; every follow-up dies, which is why an agent
// appears to run one tool and then start narrating instead of acting.
//
// Dropping it costs the model its private chain of thought from earlier turns; the
// summaries, the tool calls and their outputs all survive, so the turn continues.
// That is strictly better than a turn that never happens.
func stripEncryptedReasoning(body []byte) ([]byte, bool) {
	if !bytes.Contains(body, []byte("encrypted_content")) {
		return body, false
	}
	var payload map[string]any
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if decoder.Decode(&payload) != nil {
		return body, false
	}
	input, ok := payload["input"].([]any)
	if !ok {
		return body, false
	}
	kept := make([]any, 0, len(input))
	for _, entry := range input {
		item, _ := entry.(map[string]any)
		if item != nil && item["type"] == "reasoning" {
			if _, sealed := item["encrypted_content"]; sealed {
				continue
			}
		}
		kept = append(kept, entry)
	}
	if len(kept) == len(input) {
		return body, false
	}
	payload["input"] = kept
	if include, listed := payload["include"].([]any); listed {
		remaining := make([]any, 0, len(include))
		for _, entry := range include {
			if name, _ := entry.(string); name == "reasoning.encrypted_content" {
				continue
			}
			remaining = append(remaining, entry)
		}
		payload["include"] = remaining
	}
	stripped, err := json.Marshal(payload)
	if err != nil {
		return body, false
	}
	return stripped, true
}

func definitionName(entry any) string {
	definition, _ := entry.(map[string]any)
	if definition == nil {
		return ""
	}
	name, _ := definition["name"].(string)
	return name
}

// providerToolName derives a documented tool name (letters, digits, "_" and "-",
// at most 64 bytes) that stays stable for the same qualified name.
func providerToolName(qualified string, reserved map[string]struct{}) string {
	alias := strings.Map(func(character rune) rune {
		if providerSafeToolRune(character) {
			return character
		}
		return '_'
	}, strings.ReplaceAll(qualified, ".", "__"))
	if _, clash := reserved[alias]; !clash && len(alias) <= maxToolNameBytes {
		return alias
	}
	digest := sha256.Sum256([]byte(qualified))
	suffix := "_" + hex.EncodeToString(digest[:4])
	if len(alias)+len(suffix) > maxToolNameBytes {
		alias = alias[:maxToolNameBytes-len(suffix)]
	}
	return alias + suffix
}

func providerSafeToolName(value string) bool {
	if value == "" || len(value) > maxToolNameBytes {
		return false
	}
	for _, character := range value {
		if !providerSafeToolRune(character) {
			return false
		}
	}
	return true
}

func providerSafeToolRune(character rune) bool {
	return character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' ||
		character >= '0' && character <= '9' || character == '_' || character == '-'
}

// plainToolName keeps aliasing reversible: names needing JSON escaping or holding
// control characters are never renamed.
func plainToolName(value string) bool {
	if value == "" || len(value) > 4*maxToolNameBytes {
		return false
	}
	for _, character := range value {
		if character < 32 || character == 127 || character == '"' || character == '\\' {
			return false
		}
	}
	return true
}

// restoreClientToolCalls converts provider tool calls back into the shape the
// client declared and repairs streaming item lifecycles. SSE framing, comments
// and unrelated events stay untouched. Dialects without Responses items and
// without renamed tools are returned as they arrived.
func restoreClientToolCalls(body []byte, compat toolCompat, path string, eventStream bool) []byte {
	responses := canonicalPath(path) == "/v1/responses"
	if len(body) == 0 || (compat.empty() && !(responses && eventStream)) {
		return body
	}
	// Most streams need no repair at all; one scan for the only two triggers keeps
	// them from being split and reassembled for nothing.
	if compat.empty() && !bytes.Contains(body, []byte("response.output_item.added")) && !bytes.Contains(body, []byte("response.output_text.delta")) {
		return body
	}
	if !eventStream {
		if rewritten, changed := rewriteToolPayload(body, compat, nil); changed {
			return rewritten
		}
		return body
	}
	freeformItems := make(map[string]struct{})
	announcedItems := make(map[string]struct{})
	announcedParts := make(map[string]struct{})
	blocks := bytes.Split(bytes.ReplaceAll(body, []byte("\r\n"), []byte("\n")), []byte("\n\n"))
	rebuilt := make([][]byte, 0, len(blocks)+4)
	changedAny := false
	for _, block := range blocks {
		lines := bytes.Split(block, []byte("\n"))
		// W3C joins every `data:` line of one event with a newline, and a provider is
		// free to split its JSON that way. Reading them one at a time parsed as
		// nothing, so such a block skipped every repair below: the item stayed
		// announced as `completed` and the client dropped all of its deltas, the tool
		// kept its provider-side alias so the client rejected a call it never
		// declared, and the prologue was synthesized a second time because the
		// announcement it duplicates went unrecognised. sseInspector already reads
		// these events the same way.
		data := -1
		payload := []byte(nil)
		spacing := []byte(nil)
		merged := false
		for position, line := range lines {
			if !bytes.HasPrefix(line, []byte("data:")) {
				continue
			}
			fragment := bytes.TrimPrefix(line, []byte("data:"))
			if data < 0 {
				data = position
				if bytes.HasPrefix(fragment, []byte(" ")) {
					spacing, fragment = []byte(" "), fragment[1:]
				}
				payload = fragment
				continue
			}
			// A newline, because that is the separator the spec names and the one the
			// client will have used. Joining without it would parse a split that falls
			// inside a string literal, which the client discards as malformed - and the
			// relay would then synthesize events for content nobody is going to read.
			merged = true
			payload = append(append(payload, '\n'), bytes.TrimPrefix(fragment, []byte(" "))...)
		}
		if data < 0 {
			rebuilt = append(rebuilt, block)
			continue
		}
		if prologue := missingStreamPrologue(payload, announcedItems, announcedParts); prologue != nil {
			rebuilt = append(rebuilt, prologue...)
			changedAny = true
		}
		rewritten, changed := rewriteToolPayload(payload, compat, freeformItems)
		if !changed {
			rebuilt = append(rebuilt, block)
			continue
		}
		lines[data] = append(append(append([]byte(nil), []byte("data:")...), spacing...), rewritten...)
		// The rewrite is one marshalled object with no newline in it, so the
		// continuation lines it was assembled from are now part of that one line.
		if merged {
			joined := make([][]byte, 0, len(lines))
			for position, line := range lines {
				if position != data && bytes.HasPrefix(line, []byte("data:")) {
					continue
				}
				joined = append(joined, line)
			}
			lines = joined
		}
		if eventType := payloadType(rewritten); eventType != "" {
			for position, line := range lines {
				if bytes.HasPrefix(line, []byte("event:")) {
					lines[position] = []byte("event: " + eventType)
				}
			}
		}
		rebuilt = append(rebuilt, bytes.Join(lines, []byte("\n")))
		changedAny = true
	}
	if !changedAny {
		return body
	}
	return bytes.Join(rebuilt, []byte("\n\n"))
}

// missingStreamPrologue synthesizes the item and content-part announcements some
// providers never send. Clients open a text buffer on those events and discard
// every delta that arrives without one, so the answer would only appear once the
// item completes - if at all.
func missingStreamPrologue(payload []byte, items, parts map[string]struct{}) [][]byte {
	var event struct {
		Type         string      `json:"type"`
		ItemID       string      `json:"item_id"`
		OutputIndex  json.Number `json:"output_index"`
		ContentIndex json.Number `json:"content_index"`
		Item         struct {
			ID string `json:"id"`
		} `json:"item"`
	}
	if json.Unmarshal(payload, &event) != nil {
		return nil
	}
	switch event.Type {
	case "response.output_item.added":
		if event.Item.ID != "" {
			items[event.Item.ID] = struct{}{}
		}
		return nil
	case "response.content_part.added", "response.output_text.done":
		if event.ItemID != "" {
			items[event.ItemID] = struct{}{}
			parts[partKey(event)] = struct{}{}
		}
		return nil
	case "response.output_text.delta":
	default:
		return nil
	}
	if event.ItemID == "" {
		return nil
	}
	prologue := make([][]byte, 0, 2)
	if _, announced := items[event.ItemID]; !announced {
		items[event.ItemID] = struct{}{}
		if block := streamEvent(map[string]any{
			"type":         "response.output_item.added",
			"output_index": numberOrZero(event.OutputIndex),
			"item": map[string]any{
				"id": event.ItemID, "type": "message", "status": "in_progress",
				"role": "assistant", "content": []any{},
			},
		}); block != nil {
			prologue = append(prologue, block)
		}
	}
	if _, announced := parts[partKey(event)]; !announced {
		parts[partKey(event)] = struct{}{}
		if block := streamEvent(map[string]any{
			"type":          "response.content_part.added",
			"item_id":       event.ItemID,
			"output_index":  numberOrZero(event.OutputIndex),
			"content_index": numberOrZero(event.ContentIndex),
			"part":          map[string]any{"type": "output_text", "text": "", "annotations": []any{}},
		}); block != nil {
			prologue = append(prologue, block)
		}
	}
	if len(prologue) == 0 {
		return nil
	}
	return prologue
}

func partKey(event struct {
	Type         string      `json:"type"`
	ItemID       string      `json:"item_id"`
	OutputIndex  json.Number `json:"output_index"`
	ContentIndex json.Number `json:"content_index"`
	Item         struct {
		ID string `json:"id"`
	} `json:"item"`
}) string {
	return event.ItemID + "\x00" + event.ContentIndex.String()
}

func streamEvent(payload map[string]any) []byte {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil
	}
	name, _ := payload["type"].(string)
	return append([]byte("event: "+name+"\ndata: "), encoded...)
}

func numberOrZero(value json.Number) json.Number {
	if value == "" {
		return json.Number("0")
	}
	return value
}

func rewriteToolPayload(payload []byte, compat toolCompat, freeformItems map[string]struct{}) ([]byte, bool) {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	if decoder.Decode(&value) != nil {
		return payload, false
	}
	if !rewriteToolValue(value, compat, freeformItems, 0) {
		return payload, false
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return payload, false
	}
	return encoded, true
}

func rewriteToolValue(value any, compat toolCompat, freeformItems map[string]struct{}, depth int) bool {
	if depth > maxToolRewriteDepth {
		return false
	}
	switch value := value.(type) {
	case map[string]any:
		changed := false
		if depth == 0 && repairAnnouncedItem(value) {
			changed = true
		}
		if rewriteToolCall(value, compat, freeformItems) {
			changed = true
		}
		if rewriteFreeformArgumentEvent(value, freeformItems) {
			changed = true
		}
		for _, child := range value {
			if rewriteToolValue(child, compat, freeformItems, depth+1) {
				changed = true
			}
		}
		return changed
	case []any:
		changed := false
		for _, child := range value {
			if rewriteToolValue(child, compat, freeformItems, depth+1) {
				changed = true
			}
		}
		return changed
	}
	return false
}

// repairAnnouncedItem keeps streaming lifecycles usable. Providers announce a new
// streaming item as already completed, or with a null content vector, and clients
// then discard every delta that follows because no item is active for them.
func repairAnnouncedItem(object map[string]any) bool {
	if object["type"] != "response.output_item.added" {
		return false
	}
	item, _ := object["item"].(map[string]any)
	if item == nil {
		return false
	}
	kind, _ := item["type"].(string)
	if kind != "message" && kind != "reasoning" {
		return false
	}
	changed := false
	if item["status"] == "completed" {
		item["status"] = "in_progress"
		changed = true
	}
	if content, ok := item["content"]; !ok || content == nil {
		item["content"] = []any{}
		changed = true
	}
	if kind == "reasoning" {
		if summary, ok := item["summary"]; !ok || summary == nil {
			item["summary"] = []any{}
			changed = true
		}
	}
	return changed
}

func rewriteToolCall(object map[string]any, compat toolCompat, freeformItems map[string]struct{}) bool {
	kind, _ := object["type"].(string)
	if kind != "function_call" && kind != "custom_tool_call" {
		return false
	}
	name, _ := object["name"].(string)
	changed := false
	if _, freeform := compat.freeform[name]; freeform {
		if input, ok := freeformCallInput(object["arguments"]); ok && kind == "function_call" {
			object["type"] = "custom_tool_call"
			object["input"] = input
			delete(object, "arguments")
			changed = true
		}
		if freeformItems != nil {
			if id, _ := object["id"].(string); id != "" {
				freeformItems[id] = struct{}{}
			}
		}
	}
	if client, aliased := compat.toClient[name]; aliased {
		object["name"] = client
		changed = true
	}
	return changed
}

// freeformCallInput unwraps the {"input":"..."} arguments providers report for a
// freeform tool, falling back to the raw arguments text.
func freeformCallInput(arguments any) (string, bool) {
	text, ok := arguments.(string)
	if !ok {
		return "", false
	}
	var wrapper map[string]any
	decoder := json.NewDecoder(strings.NewReader(text))
	decoder.UseNumber()
	if decoder.Decode(&wrapper) == nil && len(wrapper) == 1 {
		if input, ok := wrapper["input"].(string); ok {
			return input, true
		}
	}
	return text, true
}

func rewriteFreeformArgumentEvent(object map[string]any, freeformItems map[string]struct{}) bool {
	if len(freeformItems) == 0 {
		return false
	}
	kind, _ := object["type"].(string)
	suffix, found := strings.CutPrefix(kind, "response.function_call_arguments.")
	if !found {
		return false
	}
	itemID, _ := object["item_id"].(string)
	if _, freeform := freeformItems[itemID]; !freeform {
		return false
	}
	object["type"] = "response.custom_tool_call_input." + suffix
	if arguments, ok := object["arguments"]; ok {
		object["input"] = arguments
		delete(object, "arguments")
	}
	return true
}

func payloadType(payload []byte) string {
	var envelope struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(payload, &envelope) != nil || !safeEventName(envelope.Type) {
		return ""
	}
	return envelope.Type
}

func safeEventName(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, character := range value {
		if !providerSafeToolRune(character) && character != '.' {
			return false
		}
	}
	return true
}

// restoreResponseToolCalls rewrites the buffered upstream payload in place. The
// retry layer already holds the whole response, so this reads from memory and stays
// inside the same ceiling, at the cost of one transient copy while rewriting.
func restoreResponseToolCalls(response *http.Response, compat toolCompat, path string, config Config) {
	if response.Body == nil {
		return
	}
	contentType := strings.ToLower(response.Header.Get("Content-Type"))
	eventStream := strings.Contains(contentType, "event-stream")
	if !eventStream && (compat.empty() || !strings.Contains(contentType, "json")) {
		return
	}
	limit := responseBufferLimit(config)
	buffered, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	response.Body.Close()
	if err == nil && int64(len(buffered)) <= limit {
		buffered = restoreClientToolCalls(buffered, compat, path, eventStream)
	}
	response.Body = io.NopCloser(bytes.NewReader(buffered))
	response.ContentLength = int64(len(buffered))
	if response.Header.Get("Content-Length") != "" {
		response.Header.Set("Content-Length", strconv.Itoa(len(buffered)))
	}
}
