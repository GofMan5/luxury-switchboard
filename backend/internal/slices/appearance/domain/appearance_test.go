package domain

import (
	"fmt"
	"strings"
	"testing"
)

func TestDefaultsAreValid(t *testing.T) {
	if err := Defaults().Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestNormalizedFillsGapsAndSharesNothing(t *testing.T) {
	tokens := map[string]string{"--bg": "#000000"}
	normalized := Appearance{Accent: "#ABCDEF", Tokens: tokens}.Normalized()
	want := Defaults()
	if normalized.Theme != want.Theme || normalized.Density != want.Density || normalized.TextSize != want.TextSize ||
		normalized.UIFont != want.UIFont || normalized.MonoFont != want.MonoFont || normalized.Radius != want.Radius {
		t.Fatalf("defaults were not filled: %+v", normalized)
	}
	if normalized.Accent != "#abcdef" {
		t.Fatalf("accent was not lowercased: %q", normalized.Accent)
	}
	normalized.Tokens["--bg"] = "#ffffff"
	if tokens["--bg"] != "#000000" {
		t.Fatal("normalized appearance shares its token map with the input")
	}
	if empty := (Appearance{}).Normalized(); empty.Tokens == nil || empty.Accent != "" {
		t.Fatalf("an empty appearance normalized badly: %+v", empty)
	}
}

func TestValidAppearanceIsAccepted(t *testing.T) {
	appearance := Appearance{
		Theme: "plugin:acme.dusk", Accent: "#3b82f6", Density: "compact", TextSize: 14,
		UIFont: "geist", MonoFont: "jetbrains", Radius: "round",
		Tokens:    map[string]string{"--bg": "#0b0d10", "--text-muted": "rgb(140 150 160)"},
		CustomCSS: ".sidebar { border-right: 1px solid var(--border); }",
	}.Normalized()
	if err := appearance.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestEachFieldRejectsItsInvalidValues(t *testing.T) {
	tooManyTokens := map[string]string{}
	for index := range MaxTokens + 1 {
		tooManyTokens[fmt.Sprintf("--t%d", index)] = "1"
	}
	for field, mutate := range map[string]func(*Appearance){
		"theme":     func(a *Appearance) { a.Theme = "Graphite" },
		"accent":    func(a *Appearance) { a.Accent = "red" },
		"density":   func(a *Appearance) { a.Density = "cozy" },
		"textSize":  func(a *Appearance) { a.TextSize = 15 },
		"uiFont":    func(a *Appearance) { a.UIFont = "comic" },
		"monoFont":  func(a *Appearance) { a.MonoFont = "courier" },
		"radius":    func(a *Appearance) { a.Radius = "pill" },
		"tokens":    func(a *Appearance) { a.Tokens = map[string]string{"--bg": "url(x)"} },
		"customCss": func(a *Appearance) { a.CustomCSS = "@import 'x';" },
	} {
		appearance := Defaults()
		mutate(&appearance)
		err := appearance.Normalized().Validate()
		validation, ok := err.(ValidationError)
		if !ok || validation.Field != field || !IsValidation(err) {
			t.Errorf("%s: unexpected error %v", field, err)
		}
	}
	appearance := Defaults()
	appearance.Tokens = tooManyTokens
	if len(tooManyTokens) != MaxTokens+1 || appearance.Validate() == nil {
		t.Fatal("too many tokens were accepted")
	}
	appearance = Defaults()
	appearance.CustomCSS = strings.Repeat("a", MaxCustomCSSBytes+1)
	if appearance.Validate() == nil {
		t.Fatal("an oversized stylesheet was accepted")
	}
	appearance.CustomCSS = strings.Repeat("a", MaxCustomCSSBytes)
	if err := appearance.Validate(); err != nil {
		t.Fatalf("a stylesheet at the limit was refused: %v", err)
	}
}
