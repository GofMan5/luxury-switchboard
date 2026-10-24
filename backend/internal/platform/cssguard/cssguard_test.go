package cssguard

import (
	"strings"
	"testing"
)

func TestTokensAcceptOrdinaryValues(t *testing.T) {
	tokens := map[string]string{
		"--bg":           "#101418",
		"--accent-2":     "rgb(10 20 30 / 50%)",
		"--font-display": `"IBM Plex Sans", system-ui`,
		"--shadow":       "0 1px 2px color-mix(in srgb, black 20%, transparent)",
	}
	if err := CheckTokens(tokens, 4); err != nil {
		t.Fatal(err)
	}
	if err := CheckTokens(map[string]string{}, 0); err != nil {
		t.Fatal(err)
	}
}

func TestTokensRefuseEveryEscape(t *testing.T) {
	for name, tokens := range map[string]map[string]string{
		"too many":          {"--a": "1", "--b": "2"},
		"bad name":          {"--Bad": "1"},
		"no dashes":         {"color": "red"},
		"long name":         {"--" + strings.Repeat("a", 49): "1"},
		"empty value":       {"--a": ""},
		"long value":        {"--a": strings.Repeat("a", MaxTokenValueBytes+1)},
		"semicolon":         {"--a": "red; background: url(x)"},
		"brace":             {"--a": "red}body{color:red"},
		"angle":             {"--a": "</style>"},
		"backslash":         {"--a": `\75rl(x)`},
		"backtick":          {"--a": "`x`"},
		"newline":           {"--a": "red\nblue"},
		"tab":               {"--a": "red\tblue"},
		"delete":            {"--a": "red\x7f"},
		"url":               {"--a": "URL(https://example.invalid/beacon)"},
		"expression":        {"--a": "expression(alert)"},
		"import":            {"--a": "@IMPORT x"},
		"javascript scheme": {"--a": "JavaScript:alert"},
	} {
		if err := CheckTokens(tokens, 1); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

func TestTokenErrorsNeverEchoTheValue(t *testing.T) {
	secret := "url(https://example.invalid/private-beacon)"
	err := CheckTokens(map[string]string{"--a": secret}, 1)
	if err == nil || strings.Contains(err.Error(), "example.invalid") {
		t.Fatalf("error leaks the value: %v", err)
	}
}

func TestStylesheetBounds(t *testing.T) {
	if err := CheckStylesheet(".a { color: red; background: url(data:image/png;base64,AA==) }", 1024); err != nil {
		t.Fatal(err)
	}
	if err := CheckStylesheet("", 0); err != nil {
		t.Fatal(err)
	}
	for name, css := range map[string]string{
		"oversized": strings.Repeat("a", 17),
		"import":    "@Import url(x);",
		"breakout":  ".a{}</STYLE ><script>",
	} {
		if err := CheckStylesheet(css, 16); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}
