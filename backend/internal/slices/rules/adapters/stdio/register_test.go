package rulesstdio

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	platform "github.com/luxuryprivate/switchboard/backend/internal/platform/stdio"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/rules/application"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/rules/domain"
)

type memory struct {
	saved   *domain.Set
	loadErr error
	saveErr error
}

func (store *memory) Load(context.Context) (domain.Set, bool, error) {
	if store.loadErr != nil || store.saved == nil {
		return domain.Set{}, false, store.loadErr
	}
	return store.saved.Clone(), true, nil
}

func (store *memory) Save(_ context.Context, set domain.Set) error {
	if store.saveErr != nil {
		return store.saveErr
	}
	copied := set.Clone()
	store.saved = &copied
	return nil
}

type history struct {
	records []application.FailureRecord
	err     error
}

func (value history) RecentFailures(context.Context) ([]application.FailureRecord, error) {
	return value.records, value.err
}

type frame struct {
	Type    string             `json:"type"`
	ID      string             `json:"id"`
	Topic   string             `json:"topic"`
	OK      bool               `json:"ok"`
	Payload json.RawMessage    `json:"payload"`
	Error   *platform.RPCError `json:"error"`
}

func newService(t *testing.T, store *memory, failures application.FailureHistory) *application.Service {
	t.Helper()
	service, err := application.NewService(store, failures)
	if err != nil {
		t.Fatal(err)
	}
	_ = service.Load(context.Background())
	return service
}

func serve(t *testing.T, service *application.Service, method, payload string) (frame, []frame) {
	t.Helper()
	request := `{"v":1,"id":"test","type":"command","method":"` + method + `"`
	if payload != "" {
		request += `,"payload":` + payload
	}
	var output bytes.Buffer
	server := platform.NewServer(strings.NewReader(request+"}\n"), &output, 1)
	Register(server, service)
	if err := server.Serve(context.Background()); err != nil {
		t.Fatal(err)
	}
	var result frame
	var events []frame
	for _, line := range strings.Split(output.String(), "\n") {
		var parsed frame
		if json.Unmarshal([]byte(line), &parsed) != nil {
			continue
		}
		if parsed.Type == "event" {
			events = append(events, parsed)
		} else if parsed.ID == "test" {
			result = parsed
		}
	}
	return result, events
}

func expectError(t *testing.T, result frame, code string) {
	t.Helper()
	if result.OK || result.Error == nil || result.Error.Code != code {
		t.Fatalf("expected %s, got ok=%v error=%+v payload=%s", code, result.OK, result.Error, result.Payload)
	}
}

func decode[T any](t *testing.T, result frame) T {
	t.Helper()
	if !result.OK {
		t.Fatalf("command failed: %+v", result.Error)
	}
	var value T
	if err := json.Unmarshal(result.Payload, &value); err != nil {
		t.Fatalf("payload %s: %v", result.Payload, err)
	}
	return value
}

const chatRule = `{"rule":{"id":"route","name":"Route","enabled":true,"match":{"models":["gpt-*"],"endpoints":["chat"]},"effects":{"provider":"fixture-a","tags":["team.a"]}}`

func TestSaveListDeleteAndChangedEvents(t *testing.T) {
	store := &memory{}
	service := newService(t, store, nil)
	result, events := serve(t, service, "rules.saveRequestRule", chatRule+`}`)
	saved := decode[domain.RequestRule](t, result)
	if saved.ID != "route" || saved.Owner != "" || len(events) != 1 || events[0].Topic != "rules.changed" || string(events[0].Payload) != "{}" {
		t.Fatalf("unexpected save: %+v events=%+v", saved, events)
	}
	result, events = serve(t, service, "rules.saveRetryRule", `{"rule":{"id":"overloaded","name":"Overloaded","enabled":true,"match":{"statuses":[503],"contains":["overloaded"]},"action":"retry","maxAttempts":2,"delayMs":500}}`)
	if retry := decode[domain.RetryRule](t, result); retry.Action != "retry" || retry.Match.ErrorCodes == nil || len(events) != 1 {
		t.Fatalf("unexpected retry save: %+v", retry)
	}

	result, events = serve(t, service, "rules.list", "")
	if !strings.Contains(string(result.Payload), `"requestRules":[{"id":"route"`) || !strings.Contains(string(result.Payload), `"retryRules":[{"id":"overloaded"`) || len(events) != 0 {
		t.Fatalf("unexpected list: %s", result.Payload)
	}
	if strings.Contains(string(result.Payload), "null") {
		t.Fatalf("the list carries null: %s", result.Payload)
	}

	result, _ = serve(t, service, "rules.deleteRequestRule", `{"id":"route"}`)
	if string(result.Payload) != `{"deleted":true}` {
		t.Fatalf("unexpected delete: %s", result.Payload)
	}
	result, _ = serve(t, service, "rules.deleteRetryRule", `{"id":"overloaded"}`)
	if string(result.Payload) != `{"deleted":true}` {
		t.Fatalf("unexpected delete: %s", result.Payload)
	}
	result, events = serve(t, service, "rules.deleteRetryRule", `{"id":"overloaded"}`)
	expectError(t, result, "rule_not_found")
	if len(events) != 0 {
		t.Fatal("a failed delete announced a change")
	}
	result, _ = serve(t, service, "rules.list", "{}")
	if string(result.Payload) != `{"requestRules":[],"retryRules":[]}` {
		t.Fatalf("unexpected empty list: %s", result.Payload)
	}
}

func TestReorderAnswersTheNewOrder(t *testing.T) {
	service := newService(t, &memory{}, nil)
	for _, id := range []string{"a", "b"} {
		serve(t, service, "rules.saveRequestRule", `{"rule":{"id":"`+id+`","name":"`+id+`","enabled":true}}`)
		serve(t, service, "rules.saveRetryRule", `{"rule":{"id":"r`+id+`","name":"`+id+`","enabled":true,"match":{"statuses":[500]},"action":"fail","maxAttempts":1}}`)
	}
	result, _ := serve(t, service, "rules.reorderRequestRules", `{"ids":["b","a"]}`)
	if ordered := decode[struct {
		RequestRules []domain.RequestRule `json:"requestRules"`
	}](t, result); ordered.RequestRules[0].ID != "b" {
		t.Fatalf("unexpected order: %s", result.Payload)
	}
	result, _ = serve(t, service, "rules.reorderRetryRules", `{"ids":["rb","ra"]}`)
	if ordered := decode[struct {
		RetryRules []domain.RetryRule `json:"retryRules"`
	}](t, result); ordered.RetryRules[0].ID != "rb" {
		t.Fatalf("unexpected order: %s", result.Payload)
	}
	result, _ = serve(t, service, "rules.reorderRequestRules", `{"ids":["a"]}`)
	expectError(t, result, "invalid_rule")
	result, _ = serve(t, service, "rules.reorderRequestRules", `{"ids":["a","b"],"actor":"plugin:fixture-one"}`)
	expectError(t, result, "permission_denied")
}

func TestActorsOwnTheirRules(t *testing.T) {
	service := newService(t, &memory{}, nil)
	result, _ := serve(t, service, "rules.saveRequestRule", strings.TrimSuffix(chatRule, `}`)+`},"actor":"plugin:fixture-one"}`)
	if saved := decode[domain.RequestRule](t, result); saved.Owner != "plugin:fixture-one" {
		t.Fatalf("unexpected owner: %+v", saved)
	}
	result, _ = serve(t, service, "rules.saveRequestRule", strings.TrimSuffix(chatRule, `}`)+`},"actor":"plugin:fixture-two"}`)
	expectError(t, result, "permission_denied")
	result, _ = serve(t, service, "rules.deleteRequestRule", `{"id":"route","actor":"plugin:fixture-two"}`)
	expectError(t, result, "permission_denied")
	result, _ = serve(t, service, "rules.deleteRequestRule", `{"id":"route","actor":"not-a-plugin"}`)
	expectError(t, result, "invalid_rule")
}

func TestMalformedAndInvalidPayloads(t *testing.T) {
	service := newService(t, &memory{}, nil)
	for method, payload := range map[string]string{
		"rules.list":                `{"extra":1}`,
		"rules.saveRequestRule":     `{"actor":"plugin:fixture-one"}`,
		"rules.saveRetryRule":       `{"rule":{"id":"a","surprise":true}}`,
		"rules.deleteRequestRule":   `{"id":7}`,
		"rules.reorderRetryRules":   `{"ids":"a"}`,
		"rules.stats":               `[]`,
		"rules.test":                `{"model":"m"}`,
		"rules.recentErrors":        `{"limit":"ten"}`,
		"rules.reorderRequestRules": `{"ids":[],"order":[]}`,
	} {
		result, _ := serve(t, service, method, payload)
		expectError(t, result, "invalid_payload")
	}
	result, _ := serve(t, service, "rules.saveRequestRule", `{"rule":{"id":"a","name":"A","effects":{"headers":{"X-Switchboard-Key":"secret-marker"}}}}`)
	expectError(t, result, "invalid_rule")
	if strings.Contains(result.Error.Message, "secret-marker") {
		t.Fatalf("the refusal echoed the value: %s", result.Error.Message)
	}
}

func TestStorageFailuresAreNamed(t *testing.T) {
	unreadable := newService(t, &memory{loadErr: errors.New("locked")}, nil)
	result, _ := serve(t, unreadable, "rules.saveRequestRule", chatRule+`}`)
	expectError(t, result, "secure_storage_unavailable")
	if !strings.Contains(result.Error.Message, "Secret Service") {
		t.Fatalf("unexpected message: %s", result.Error.Message)
	}
	full := newService(t, &memory{saveErr: errors.New("disk is full")}, nil)
	result, events := serve(t, full, "rules.saveRequestRule", chatRule+`}`)
	expectError(t, result, "rules_update_failed")
	if len(events) != 0 || strings.Contains(result.Error.Message, "disk") {
		t.Fatalf("unexpected failure: %+v events=%d", result.Error, len(events))
	}
}

func TestStatsCoverEveryRule(t *testing.T) {
	service := newService(t, &memory{}, nil)
	serve(t, service, "rules.saveRequestRule", `{"rule":{"id":"limited","name":"Limited","enabled":true,"effects":{"rateLimit":{"requests":5,"windowSeconds":60}}}}`)
	service.Evaluate(application.Request{Model: "m", Endpoint: domain.EndpointChat})
	result, _ := serve(t, service, "rules.stats", "")
	stats := decode[struct {
		Rules map[string]struct {
			Hits           int64      `json:"hits"`
			LastHitAt      *time.Time `json:"lastHitAt"`
			WindowRequests int64      `json:"windowRequests"`
			WindowTokens   int64      `json:"windowTokens"`
			WindowResetsAt *time.Time `json:"windowResetsAt"`
		} `json:"rules"`
	}](t, result)
	limited := stats.Rules["limited"]
	if limited.Hits != 1 || limited.LastHitAt == nil || limited.WindowRequests != 1 || limited.WindowResetsAt == nil {
		t.Fatalf("unexpected stats: %s", result.Payload)
	}
}

func TestDryRunClassifiesTheCanonicalPath(t *testing.T) {
	service := newService(t, &memory{}, nil)
	serve(t, service, "rules.saveRequestRule", chatRule+`}`)
	serve(t, service, "rules.saveRetryRule", `{"rule":{"id":"transport","name":"Transport","enabled":true,"match":{"errorCodes":["transport"]},"action":"retry","maxAttempts":1}}`)
	type answer struct {
		RequestRules []string       `json:"requestRules"`
		Effects      domain.Effects `json:"effects"`
		RetryRule    string         `json:"retryRule"`
	}
	for path, matched := range map[string]bool{
		"/v1/chat/completions":         true,
		"V1//Chat/./Completions":       true,
		"/v1/chat/../chat/completions": true,
		"/v1/completions":              false,
		"/v1/embeddings":               false,
	} {
		result, _ := serve(t, service, "rules.test", `{"model":"GPT-4o","path":"`+path+`"}`)
		got := decode[answer](t, result)
		if (len(got.RequestRules) == 1) != matched || (matched && got.Effects.Provider != "fixture-a") {
			t.Errorf("%s: %s", path, result.Payload)
		}
		if got.RequestRules == nil || got.RetryRule != "" || strings.Contains(string(result.Payload), "retryRule") {
			t.Errorf("%s: unexpected shape %s", path, result.Payload)
		}
	}
	result, _ := serve(t, service, "rules.test", `{"model":"","path":"/v1/models","errorCode":"transport"}`)
	if got := decode[answer](t, result); got.RetryRule != "transport" {
		t.Fatalf("a transport failure did not match: %s", result.Payload)
	}
	if service.Stats()["route"].Hits != 0 || service.Stats()["transport"].Hits != 0 {
		t.Fatal("a dry run counted hits")
	}
}

func TestDryRunBounds(t *testing.T) {
	service := newService(t, &memory{}, nil)
	accepted := []string{
		`{"model":"","path":"/"}`,
		`{"model":"` + strings.Repeat("m", 200) + `","path":"/` + strings.Repeat("p", 2047) + `"}`,
		`{"model":"m","path":"/v1/chat/completions","status":599,"errorText":"line one\n\tline two","errorCode":"upstream_status","providerId":"` + strings.Repeat("p", 64) + `"}`,
		`{"model":"m","path":"/v1/chat/completions","status":400,"errorText":"` + strings.Repeat("é", 2048) + `"}`,
		`{"model":"m","path":"/x","errorText":"","errorCode":"","providerId":""}`,
	}
	for _, payload := range accepted {
		if result, _ := serve(t, service, "rules.test", payload); !result.OK {
			t.Errorf("refused %.80s: %+v", payload, result.Error)
		}
	}
	refused := map[string]string{
		"no path":          `{"model":"m","path":""}`,
		"long path":        `{"model":"m","path":"/` + strings.Repeat("p", 2048) + `"}`,
		"control in path":  `{"model":"m","path":"/v1\u0000"}`,
		"long model":       `{"model":"` + strings.Repeat("m", 201) + `","path":"/"}`,
		"control in model": `{"model":"a\nb","path":"/"}`,
		"status zero":      `{"model":"m","path":"/","status":0}`,
		"status 2xx":       `{"model":"m","path":"/","status":200}`,
		"status 600":       `{"model":"m","path":"/","status":600}`,
		"long error text":  `{"model":"m","path":"/","errorText":"` + strings.Repeat("e", 2049) + `"}`,
		"control in text":  `{"model":"m","path":"/","errorText":"a\u0007b"}`,
		"error code shape": `{"model":"m","path":"/","errorCode":"Transport"}`,
		"long error code":  `{"model":"m","path":"/","errorCode":"a` + strings.Repeat("b", 64) + `"}`,
		"long provider":    `{"model":"m","path":"/","providerId":"` + strings.Repeat("p", 65) + `"}`,
		"control provider": `{"model":"m","path":"/","providerId":"a\tb"}`,
	}
	for name, payload := range refused {
		result, _ := serve(t, service, "rules.test", payload)
		if result.OK || result.Error == nil || result.Error.Code != "invalid_payload" {
			t.Errorf("%s: %+v", name, result.Error)
		}
	}
}

func TestRecentErrors(t *testing.T) {
	at := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	records := []application.FailureRecord{
		{At: at, Status: 503, ErrorCode: "upstream_status", Detail: "overloaded", ProviderName: "Fixture A", Model: "gpt-4o"},
		{At: at.Add(time.Minute), Status: 500, ErrorCode: "upstream_status", Detail: "internal", ProviderName: "Fixture A", Model: "gpt-4o"},
		{At: at.Add(2 * time.Minute), Status: 500, ErrorCode: "upstream_status", Detail: "internal", ProviderName: "Fixture A", Model: "gpt-4o"},
	}
	service := newService(t, &memory{}, history{records: records})
	result, _ := serve(t, service, "rules.recentErrors", "")
	got := decode[struct {
		Errors []application.RecentError `json:"errors"`
	}](t, result)
	if len(got.Errors) != 2 || got.Errors[0].Status != 500 || got.Errors[0].Count != 2 || strings.Contains(string(result.Payload), "coveredBy") {
		t.Fatalf("unexpected answer: %s", result.Payload)
	}
	result, _ = serve(t, service, "rules.recentErrors", `{"limit":1}`)
	if got := decode[struct {
		Errors []application.RecentError `json:"errors"`
	}](t, result); len(got.Errors) != 1 {
		t.Fatalf("the limit was ignored: %s", result.Payload)
	}
	for _, limit := range []string{"0", "101", "-1"} {
		result, _ = serve(t, service, "rules.recentErrors", `{"limit":`+limit+`}`)
		expectError(t, result, "invalid_payload")
	}
	empty := newService(t, &memory{}, history{})
	result, _ = serve(t, empty, "rules.recentErrors", "")
	if string(result.Payload) != `{"errors":[]}` {
		t.Fatalf("an empty history must answer an empty list: %s", result.Payload)
	}
	for _, unavailable := range []application.FailureHistory{nil, history{err: errors.New("database is locked")}} {
		result, _ = serve(t, newService(t, &memory{}, unavailable), "rules.recentErrors", "")
		expectError(t, result, "history_unavailable")
		if strings.Contains(result.Error.Message, "locked") {
			t.Fatalf("the cause leaked: %s", result.Error.Message)
		}
	}
}
