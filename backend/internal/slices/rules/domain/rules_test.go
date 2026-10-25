package domain

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func validRequestRule() RequestRule {
	return RequestRule{ID: "route-a", Name: "Route A", Enabled: true}.Normalized()
}

func validRetryRule() RetryRule {
	return RetryRule{
		ID: "retry-a", Name: "Retry A", Enabled: true,
		Match:  RetryMatch{Statuses: []int{503}},
		Action: RetryRetry, MaxAttempts: 2, DelayMS: 0,
	}.Normalized()
}

func TestGlobMatchesCaseInsensitivelyOverTheWholeValue(t *testing.T) {
	for _, test := range []struct {
		pattern, value string
		want           bool
	}{
		{"gpt-4o", "GPT-4o", true},
		{"gpt-4o", "gpt-4o-mini", false},
		{"gpt-*", "gpt-4o-mini", true},
		{"*mini", "gpt-4o-mini", true},
		{"*mini", "gpt-4o-mini-high", false},
		{"*", "", true},
		{"*", "anything", true},
		{"claude-*-sonnet*", "Claude-3-5-Sonnet-latest", true},
		{"claude-*-sonnet*", "claude-opus", false},
		{"a*b*c", "abc", true},
		{"a*b*c", "aXbYbZc", true},
		{"a*b*c", "acb", false},
		{"**", "x", true},
		{"a?b", "aXb", false},
		{"a?b", "a?b", true},
		{"", "", true},
		{"", "x", false},
	} {
		if got := Glob(test.pattern, test.value); got != test.want {
			t.Errorf("Glob(%q, %q) = %v, want %v", test.pattern, test.value, got, test.want)
		}
	}
}

func TestRequestMatchIsAConjunctionOfItsNonEmptyFields(t *testing.T) {
	match := RequestMatch{Models: []string{"gpt-*", "o3"}, Endpoints: []string{EndpointChat, EndpointResponses}}
	for _, test := range []struct {
		model, endpoint string
		want            bool
	}{
		{"gpt-4o", EndpointChat, true},
		{"o3", EndpointResponses, true},
		{"o3", EndpointEmbeddings, false},
		{"claude", EndpointChat, false},
	} {
		if got := match.Matches(test.model, test.endpoint); got != test.want {
			t.Errorf("Matches(%q, %q) = %v", test.model, test.endpoint, got)
		}
	}
	if !(RequestMatch{}).Matches("anything", EndpointOther) {
		t.Fatal("an empty match must match every request")
	}
}

func TestRetryMatchAndsFieldsAndOrsValues(t *testing.T) {
	match := RetryMatch{
		Statuses:   []int{429, 503},
		Contains:   []string{"Rate_Limit", "overloaded"},
		ErrorCodes: []string{"upstream_status"},
		Providers:  []string{"fixture-a"},
		Models:     []string{"gpt-*"},
	}
	base := Failure{ProviderID: "fixture-a", Model: "gpt-4o", Status: 503, ErrorCode: "upstream_status", ErrorText: NormalizeErrorText("Server Overloaded, try later")}
	if !match.Matches(base) {
		t.Fatal("a failure matching every field did not match")
	}
	limited := base
	limited.ErrorText = NormalizeErrorText("rate-limit reached")
	if !match.Matches(limited) {
		t.Fatal("contains must read '_' and '-' as spaces on both sides")
	}
	for name, mutate := range map[string]func(*Failure){
		"status":   func(failure *Failure) { failure.Status = 500 },
		"text":     func(failure *Failure) { failure.ErrorText = "bad gateway" },
		"code":     func(failure *Failure) { failure.ErrorCode = "transport" },
		"provider": func(failure *Failure) { failure.ProviderID = "fixture-b" },
		"model":    func(failure *Failure) { failure.Model = "claude" },
	} {
		failure := base
		mutate(&failure)
		if match.Matches(failure) {
			t.Errorf("a different %s still matched", name)
		}
	}
	transport := RetryMatch{ErrorCodes: []string{"transport"}}
	if !transport.Matches(Failure{ErrorCode: "transport"}) {
		t.Fatal("an empty statuses list must match a transport failure")
	}
	// History files an error status as request_rejected; the relay asks with
	// upstream_status. A rule drafted from history must still fire.
	drafted := RetryMatch{ErrorCodes: []string{"request_rejected"}}
	if !drafted.Matches(Failure{Status: 503, ErrorCode: "upstream_status"}) {
		t.Fatal("request_rejected must match an upstream error status")
	}
	if drafted.Matches(Failure{ErrorCode: "transport"}) || transport.Matches(Failure{Status: 503, ErrorCode: "upstream_status"}) {
		t.Fatal("the status alias must not reach transport failures")
	}
}

func TestNormalizedTrimsAndNeverLeavesNilLists(t *testing.T) {
	rule := RequestRule{ID: " route ", Name: "  Route  ", Match: RequestMatch{Models: []string{" gpt-* "}}}.Normalized()
	if rule.ID != "route" || rule.Name != "Route" || rule.Match.Models[0] != "gpt-*" || rule.Match.Endpoints == nil {
		t.Fatalf("unexpected normalization: %+v", rule)
	}
	encoded, _ := json.Marshal(rule)
	text := string(encoded)
	if !strings.Contains(text, `"endpoints":[]`) || !strings.Contains(text, `"effects":{}`) || strings.Contains(text, "null") {
		t.Fatalf("unexpected JSON: %s", text)
	}
	retry, _ := json.Marshal(RetryRule{ID: "r"}.Normalized())
	if strings.Contains(string(retry), "null") {
		t.Fatalf("retry rule JSON carries null: %s", retry)
	}
}

func TestNullEffectsDecodeAsAbsent(t *testing.T) {
	var rule RequestRule
	if err := json.Unmarshal([]byte(`{"id":"a","name":"A","enabled":true,"match":{"models":null,"endpoints":null},"effects":{"block":null,"headers":null,"tags":null,"provider":null}}`), &rule); err != nil {
		t.Fatal(err)
	}
	if err := rule.Normalized().Validate(); err != nil {
		t.Fatalf("null fields must read as absent: %v", err)
	}
}

func TestNormalizedCopiesSoTheCallerCannotMutateTheStoredRule(t *testing.T) {
	original := RequestRule{ID: "a", Name: "A", Effects: Effects{
		Block: &Block{Status: 403, Message: "no"}, Headers: map[string]string{"X-A": "1"}, Tags: []string{"t"},
	}}
	copied := original.Normalized()
	copied.Effects.Block.Message = "changed"
	copied.Effects.Headers["X-A"] = "2"
	copied.Effects.Tags[0] = "u"
	if original.Effects.Block.Message != "no" || original.Effects.Headers["X-A"] != "1" || original.Effects.Tags[0] != "t" {
		t.Fatal("Normalized shares memory with its input")
	}
}

func TestRequestRuleLimits(t *testing.T) {
	long := func(count int) string { return strings.Repeat("a", count) }
	for name, test := range map[string]struct {
		mutate func(*RequestRule)
		field  string
	}{
		"id empty":          {func(rule *RequestRule) { rule.ID = "" }, "Rule id"},
		"id upper":          {func(rule *RequestRule) { rule.ID = "Route" }, "Rule id"},
		"id leading dash":   {func(rule *RequestRule) { rule.ID = "-route" }, "Rule id"},
		"id too long":       {func(rule *RequestRule) { rule.ID = long(49) }, "Rule id"},
		"name empty":        {func(rule *RequestRule) { rule.Name = "" }, "Rule name"},
		"name too long":     {func(rule *RequestRule) { rule.Name = long(61) }, "Rule name"},
		"name control":      {func(rule *RequestRule) { rule.Name = "a\nb" }, "Rule name"},
		"owner":             {func(rule *RequestRule) { rule.Owner = "operator" }, "Rule owner"},
		"too many models":   {func(rule *RequestRule) { rule.Match.Models = make([]string, 17) }, "Model patterns"},
		"empty model glob":  {func(rule *RequestRule) { rule.Match.Models = []string{""} }, "Model patterns"},
		"long model glob":   {func(rule *RequestRule) { rule.Match.Models = []string{long(201)} }, "Model patterns"},
		"unknown endpoint":  {func(rule *RequestRule) { rule.Match.Endpoints = []string{"audio"} }, "Endpoints"},
		"repeated endpoint": {func(rule *RequestRule) { rule.Match.Endpoints = []string{"chat", "chat"} }, "Endpoints"},
		"provider long":     {func(rule *RequestRule) { rule.Effects.Provider = long(65) }, "Provider"},
		"model long":        {func(rule *RequestRule) { rule.Effects.Model = long(201) }, "Model rewrite"},
		"block status":      {func(rule *RequestRule) { rule.Effects.Block = &Block{Status: 500, Message: "x"} }, "Block status"},
		"block message":     {func(rule *RequestRule) { rule.Effects.Block = &Block{Status: 403} }, "Block message"},
		"block message long": {func(rule *RequestRule) {
			rule.Effects.Block = &Block{Status: 403, Message: long(201)}
		}, "Block message"},
		"rate requests":     {func(rule *RequestRule) { rule.Effects.RateLimit = &RateLimit{Requests: 0, WindowSeconds: 1} }, "Rate limit requests"},
		"rate requests big": {func(rule *RequestRule) { rule.Effects.RateLimit = &RateLimit{Requests: 100_001, WindowSeconds: 1} }, "Rate limit requests"},
		"rate window":       {func(rule *RequestRule) { rule.Effects.RateLimit = &RateLimit{Requests: 1, WindowSeconds: 86_401} }, "Rate limit window"},
		"budget period":     {func(rule *RequestRule) { rule.Effects.Budget = &Budget{Requests: 1, Period: "week"} }, "Budget period"},
		"budget empty":      {func(rule *RequestRule) { rule.Effects.Budget = &Budget{Period: "day"} }, "Budget"},
		"budget requests":   {func(rule *RequestRule) { rule.Effects.Budget = &Budget{Requests: 10_000_001, Period: "day"} }, "Budget requests"},
		"budget tokens":     {func(rule *RequestRule) { rule.Effects.Budget = &Budget{Tokens: 10_000_000_001, Period: "day"} }, "Budget tokens"},
		"budget negative":   {func(rule *RequestRule) { rule.Effects.Budget = &Budget{Requests: -1, Tokens: 5, Period: "day"} }, "Budget requests"},
		"header name":       {func(rule *RequestRule) { rule.Effects.Headers = map[string]string{"Authorization": "x"} }, "Headers"},
		"header switchboard": {func(rule *RequestRule) {
			rule.Effects.Headers = map[string]string{"x-switchboard-provider": "x"}
		}, "Headers"},
		"header forwarded": {func(rule *RequestRule) { rule.Effects.Headers = map[string]string{"X-Forwarded-For": "x"} }, "Headers"},
		"header real ip":   {func(rule *RequestRule) { rule.Effects.Headers = map[string]string{"X-Real-IP": "x"} }, "Headers"},
		"header case dup":  {func(rule *RequestRule) { rule.Effects.Headers = map[string]string{"X-A": "1", "x-a": "2"} }, "Headers"},
		"header value":     {func(rule *RequestRule) { rule.Effects.Headers = map[string]string{"X-A": "line\r\nbreak"} }, "Headers"},
		"header value utf": {func(rule *RequestRule) { rule.Effects.Headers = map[string]string{"X-A": "é"} }, "Headers"},
		"header empty":     {func(rule *RequestRule) { rule.Effects.Headers = map[string]string{"X-A": ""} }, "Headers"},
		"header long":      {func(rule *RequestRule) { rule.Effects.Headers = map[string]string{"X-A": long(257)} }, "Headers"},
		"too many headers": {func(rule *RequestRule) {
			rule.Effects.Headers = map[string]string{}
			for index := range 9 {
				rule.Effects.Headers["X-H"+string(rune('a'+index))] = "v"
			}
		}, "Headers"},
		"tag shape":     {func(rule *RequestRule) { rule.Effects.Tags = []string{"Upper"} }, "Tags"},
		"tag repeat":    {func(rule *RequestRule) { rule.Effects.Tags = []string{"a", "a"} }, "Tags"},
		"too many tags": {func(rule *RequestRule) { rule.Effects.Tags = []string{"a", "b", "c", "d", "e", "f", "g", "h", "i"} }, "Tags"},
	} {
		rule := validRequestRule()
		test.mutate(&rule)
		err := rule.Normalized().Validate()
		var validation ValidationError
		if !errors.As(err, &validation) || validation.Field != test.field {
			t.Errorf("%s: got %v, want a %q refusal", name, err, test.field)
		}
	}
}

func TestRequestRuleAcceptsTheLimits(t *testing.T) {
	rule := validRequestRule()
	rule.ID = strings.Repeat("a", 48)
	rule.Name = strings.Repeat("é", 60)
	rule.Match.Models = []string{strings.Repeat("m", 200)}
	rule.Match.Endpoints = append([]string(nil), Endpoints...)
	rule.Owner = "plugin:fixture.plugin"
	rule.Effects = Effects{
		Provider:  strings.Repeat("p", 64),
		Model:     strings.Repeat("m", 200),
		Block:     &Block{Status: 429, Message: strings.Repeat("b", 200)},
		RateLimit: &RateLimit{Requests: 100_000, WindowSeconds: 86_400},
		Budget:    &Budget{Requests: 10_000_000, Tokens: 10_000_000_000, Period: BudgetPeriodHour},
		Headers:   map[string]string{"X-" + strings.Repeat("a", 62): strings.Repeat("~", 256)},
		Tags:      []string{"a", "b.c", "d_e", "f-g", "0", strings.Repeat("t", 32), "h", "i"},
		Stop:      true,
	}
	if err := rule.Normalized().Validate(); err != nil {
		t.Fatalf("the documented maximums must validate: %v", err)
	}
}

func TestRetryRuleLimits(t *testing.T) {
	for name, test := range map[string]struct {
		mutate func(*RetryRule)
		field  string
	}{
		"no match":        {func(rule *RetryRule) { rule.Match = RetryMatch{Providers: []string{"a"}} }, "Retry rule match"},
		"status range":    {func(rule *RetryRule) { rule.Match.Statuses = []int{200} }, "Statuses"},
		"status repeat":   {func(rule *RetryRule) { rule.Match.Statuses = []int{503, 503} }, "Statuses"},
		"too many status": {func(rule *RetryRule) { rule.Match.Statuses = make([]int, 17) }, "Statuses"},
		"contains short":  {func(rule *RetryRule) { rule.Match.Contains = []string{" a "} }, "Contains entries"},
		"contains long":   {func(rule *RetryRule) { rule.Match.Contains = []string{strings.Repeat("a", 121)} }, "Contains entries"},
		"contains many": {func(rule *RetryRule) {
			rule.Match.Contains = []string{"aa", "bb", "cc", "dd", "ee", "ff", "gg", "hh", "ii"}
		}, "Contains"},
		"error code":       {func(rule *RetryRule) { rule.Match.ErrorCodes = []string{"Transport"} }, "Error codes"},
		"error codes many": {func(rule *RetryRule) { rule.Match.ErrorCodes = []string{"a", "b", "c", "d", "e", "f", "g", "h", "i"} }, "Error codes"},
		"provider long":    {func(rule *RetryRule) { rule.Match.Providers = []string{strings.Repeat("p", 65)} }, "Providers"},
		"model glob":       {func(rule *RetryRule) { rule.Match.Models = []string{""} }, "Model patterns"},
		"action":           {func(rule *RetryRule) { rule.Action = "wait" }, "Retry action"},
		"attempts low":     {func(rule *RetryRule) { rule.MaxAttempts = 0 }, "Max attempts"},
		"attempts high":    {func(rule *RetryRule) { rule.MaxAttempts = 11 }, "Max attempts"},
		"delay negative":   {func(rule *RetryRule) { rule.DelayMS = -1 }, "Delay"},
		"delay high":       {func(rule *RetryRule) { rule.DelayMS = 60_001 }, "Delay"},
		"name":             {func(rule *RetryRule) { rule.Name = " " }, "Rule name"},
	} {
		rule := validRetryRule()
		test.mutate(&rule)
		err := rule.Normalized().Validate()
		var validation ValidationError
		if !errors.As(err, &validation) || validation.Field != test.field {
			t.Errorf("%s: got %v, want a %q refusal", name, err, test.field)
		}
	}
	for _, action := range []string{RetryRetry, RetryRotateKey, RetrySwitch, RetryFail} {
		rule := validRetryRule()
		rule.Action = action
		rule.MaxAttempts, rule.DelayMS = 10, 60_000
		if err := rule.Validate(); err != nil {
			t.Errorf("%s: %v", action, err)
		}
	}
}

func TestSetValidatesCapsAndIDsAcrossKinds(t *testing.T) {
	set := Set{RequestRules: []RequestRule{validRequestRule()}, RetryRules: []RetryRule{validRetryRule()}}
	set.RetryRules[0].ID = set.RequestRules[0].ID
	var validation ValidationError
	if err := set.Validate(); !errors.As(err, &validation) || validation.Field != "Rule id" {
		t.Fatalf("an id shared across kinds validated: %v", err)
	}
	many := Set{}
	for index := range MaxRequestRules + 1 {
		rule := validRequestRule()
		rule.ID = "r" + strings.Repeat("x", index%40) + string(rune('a'+index%26)) + string(rune('a'+index/26))
		many.RequestRules = append(many.RequestRules, rule)
	}
	if err := many.Validate(); !errors.As(err, &validation) || validation.Error() != "Request rules are limited to 64" {
		t.Fatalf("65 request rules validated: %v", err)
	}
}

func TestValidationMessagesNeverEchoTheValue(t *testing.T) {
	rule := validRequestRule()
	rule.Effects.Headers = map[string]string{"X-Secret-Marker": "secret-value\x01"}
	err := rule.Validate()
	if err == nil || strings.Contains(err.Error(), "secret-value") || strings.Contains(err.Error(), "Secret-Marker") {
		t.Fatalf("unexpected message: %v", err)
	}
}

func TestActorsAndForbiddenHeaders(t *testing.T) {
	for actor, want := range map[string]bool{
		"plugin:fixture-plugin": true, "plugin:a.b": true, "plugin:": false, "operator": false, "plugin:Upper": false, "": false,
	} {
		if ValidActor(actor) != want {
			t.Errorf("ValidActor(%q) != %v", actor, want)
		}
	}
	for header, want := range map[string]bool{
		"X-Switchboard-Key": true, "x-forwarded-host": true, "X-REAL-IP": true, "X-Custom": false,
		"X-Api-Key": true, "x-goog-api-key": true, "X-Auth-Token": true, "X-Session-Id": true, "X-Stainless-Lang": true, "X-Team": false,
	} {
		if ForbiddenHeader(header) != want {
			t.Errorf("ForbiddenHeader(%q) != %v", header, want)
		}
	}
}
