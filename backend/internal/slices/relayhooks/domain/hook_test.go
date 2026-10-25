package domain

import (
	"errors"
	"strings"
	"testing"
)

func validHook() Hook {
	return Hook{PluginID: "acme.tools", HookID: "redact", Phase: PhaseRequest, TimeoutMS: DefaultTimeoutMS}
}

func TestHookValidate(t *testing.T) {
	if err := validHook().Validate(); err != nil {
		t.Fatalf("valid hook refused: %v", err)
	}
	many := make([]string, MaxModels+1)
	for index := range many {
		many[index] = "gpt-*"
	}
	cases := map[string]struct {
		edit  func(*Hook)
		field string
	}{
		"plugin id":        {func(hook *Hook) { hook.PluginID = "Acme" }, "pluginId"},
		"empty hook id":    {func(hook *Hook) { hook.HookID = "" }, "hookId"},
		"hook id dot":      {func(hook *Hook) { hook.HookID = "a.b" }, "hookId"},
		"hook id too long": {func(hook *Hook) { hook.HookID = strings.Repeat("a", 33) }, "hookId"},
		"phase":            {func(hook *Hook) { hook.Phase = "both" }, "phase"},
		"too many models":  {func(hook *Hook) { hook.Models = many }, "models"},
		"blank model":      {func(hook *Hook) { hook.Models = []string{" "} }, "models"},
		"long model":       {func(hook *Hook) { hook.Models = []string{strings.Repeat("m", MaxModelRunes+1)} }, "models"},
		"control model":    {func(hook *Hook) { hook.Models = []string{"gpt\n"} }, "models"},
		"endpoint":         {func(hook *Hook) { hook.Endpoints = []string{"tunnel"} }, "endpoints"},
		"short timeout":    {func(hook *Hook) { hook.TimeoutMS = MinTimeoutMS - 1 }, "timeoutMs"},
		"long timeout":     {func(hook *Hook) { hook.TimeoutMS = MaxTimeoutMS + 1 }, "timeoutMs"},
	}
	for name, test := range cases {
		hook := validHook()
		test.edit(&hook)
		var validation ValidationError
		if err := hook.Validate(); !errors.As(err, &validation) || validation.Field != test.field {
			t.Errorf("%s: got %v, want a %s error", name, err, test.field)
		}
	}
	hook := validHook()
	hook.HookID = strings.Repeat("a", 32)
	hook.Models = []string{strings.Repeat("é", MaxModelRunes)}
	hook.Endpoints = Endpoints
	if err := hook.Validate(); err != nil {
		t.Fatalf("hook at every bound refused: %v", err)
	}
}

func TestHookMatches(t *testing.T) {
	hook := validHook()
	if !hook.Matches("anything", "other") {
		t.Fatal("an empty filter must match everything")
	}
	hook.Models = []string{"gpt-4*", "claude-*-sonnet"}
	hook.Endpoints = []string{"chat", "responses"}
	for _, match := range []struct {
		model, endpoint string
		want            bool
	}{
		{"GPT-4o", "chat", true},
		{"claude-3-5-sonnet", "responses", true},
		{"gpt-4o", "embeddings", false},
		{"gpt-3.5", "chat", false},
		{"claude-3-opus", "chat", false},
	} {
		if got := hook.Matches(match.model, match.endpoint); got != match.want {
			t.Errorf("Matches(%q, %q) = %v, want %v", match.model, match.endpoint, got, match.want)
		}
	}
}

func TestMatchGlob(t *testing.T) {
	for _, match := range []struct {
		pattern, value string
		want           bool
	}{
		{"*", "", true},
		{"*", "gpt", true},
		{"gpt", "GPT", true},
		{"gpt", "gpt-4", false},
		{"gpt*", "gpt-4", true},
		{"*mini", "gpt-4o-mini", true},
		{"*mini", "mini-gpt", false},
		{"a*b*c", "aXbYc", true},
		{"a*b*c", "aXcYb", false},
		{"a*a", "a", false},
		{"a**a", "aa", true},
		{"*-*-*", "x-y", false},
		{"o1*", "o1", true},
	} {
		if got := MatchGlob(match.pattern, match.value); got != match.want {
			t.Errorf("MatchGlob(%q, %q) = %v, want %v", match.pattern, match.value, got, match.want)
		}
	}
}

func TestDecisionValidate(t *testing.T) {
	valid := []Decision{
		{Action: ActionContinue},
		{Action: ActionContinue, Tags: []string{"a", "b.c", "d_e-f"}},
		{Action: ActionRewrite, Model: "gpt-4o", Body: "not json is ignored later"},
		{Action: ActionBlock, Block: &Block{Status: 429, Message: "Slow down"}, Tags: []string{"limited"}},
	}
	for _, decision := range valid {
		if err := decision.Validate(); err != nil {
			t.Errorf("%+v refused: %v", decision, err)
		}
	}
	tooMany := make([]string, MaxTags+1)
	for index := range tooMany {
		tooMany[index] = "t"
	}
	invalid := map[string]Decision{
		"action":        {Action: "allow"},
		"model":         {Action: ActionRewrite, Model: strings.Repeat("m", MaxModelRunes+1)},
		"blank model":   {Action: ActionRewrite, Model: "  "},
		"tags":          {Action: ActionContinue, Tags: tooMany},
		"tag shape":     {Action: ActionContinue, Tags: []string{"Upper"}},
		"missing block": {Action: ActionBlock},
		"status":        {Action: ActionBlock, Block: &Block{Status: 500, Message: "x"}},
		"empty message": {Action: ActionBlock, Block: &Block{Status: 403}},
		"long message":  {Action: ActionBlock, Block: &Block{Status: 403, Message: strings.Repeat("x", MaxBlockMessageRunes+1)}},
		"markup":        {Action: ActionBlock, Block: &Block{Status: 403, Message: "line\nbreak"}},
	}
	for name, decision := range invalid {
		if decision.Validate() == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestUsableBody(t *testing.T) {
	if !UsableBody(`{"model":"x"}`) {
		t.Fatal("small JSON refused")
	}
	if UsableBody("") || UsableBody("{") || UsableBody(`"`+strings.Repeat("x", MaxBodyBytes)+`"`) {
		t.Fatal("empty, invalid or oversized body accepted")
	}
}

func TestMergeTags(t *testing.T) {
	merged := MergeTags(nil, []string{"a", "b", "a"})
	merged = MergeTags(merged, []string{"b", "c"})
	if strings.Join(merged, ",") != "a,b,c" {
		t.Fatalf("merged = %v", merged)
	}
	for index := range 40 {
		merged = MergeTags(merged, []string{strings.Repeat("x", index+1)})
	}
	if len(merged) != MaxActivityTags {
		t.Fatalf("merged %d tags, want %d", len(merged), MaxActivityTags)
	}
}

func TestValidCallID(t *testing.T) {
	if !ValidCallID(strings.Repeat("0a", 16)) || ValidCallID(strings.Repeat("0A", 16)) || ValidCallID("abc") {
		t.Fatal("call id shape is wrong")
	}
}
