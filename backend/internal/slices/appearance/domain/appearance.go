// Package domain holds the owner's appearance choices: which theme the shell
// wears, the density and type it uses, and the bounded token and stylesheet
// overrides layered on top. Motion is deliberately absent — the settings
// slice's animationsEnabled already owns it.
package domain

import (
	"errors"
	"maps"
	"regexp"
	"slices"
	"strings"

	"github.com/luxuryprivate/switchboard/backend/internal/platform/cssguard"
)

const (
	DefaultTheme    = "graphite"
	DefaultDensity  = "comfortable"
	DefaultTextSize = 13
	DefaultUIFont   = "plex"
	DefaultMonoFont = "plex"
	DefaultRadius   = "default"

	// MaxTokens bounds the token overrides; MaxCustomCSSBytes the stylesheet.
	MaxTokens         = 96
	MaxCustomCSSBytes = 32 * 1024
)

// themeID admits builtin and plugin themes alike ("graphite", "plugin:acme.dusk"):
// unknown ids are accepted because the frontend falls back to the default when
// the theme they name is not installed.
var themeID = regexp.MustCompile(`^[a-z0-9][a-z0-9:._-]{0,95}$`)

var accentColor = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

// ValidationError names the field that failed in text that is safe to show:
// it never repeats the rejected value.
type ValidationError struct {
	Field  string
	Reason string
}

func (err ValidationError) Error() string { return err.Field + " " + err.Reason }

type Appearance struct {
	Theme     string            `json:"theme"`
	Accent    string            `json:"accent"`
	Density   string            `json:"density"`
	TextSize  int               `json:"textSize"`
	UIFont    string            `json:"uiFont"`
	MonoFont  string            `json:"monoFont"`
	Radius    string            `json:"radius"`
	Tokens    map[string]string `json:"tokens"`
	CustomCSS string            `json:"customCss"`
}

func Defaults() Appearance {
	return Appearance{
		Theme:    DefaultTheme,
		Density:  DefaultDensity,
		TextSize: DefaultTextSize,
		UIFont:   DefaultUIFont,
		MonoFont: DefaultMonoFont,
		Radius:   DefaultRadius,
		Tokens:   map[string]string{},
	}
}

// Normalized fills empty and zero fields with their defaults, lowercases the
// accent and copies the token map so the result shares nothing with the input.
// Any other value is left for Validate to judge.
func (appearance Appearance) Normalized() Appearance {
	defaults := Defaults()
	if appearance.Theme == "" {
		appearance.Theme = defaults.Theme
	}
	appearance.Accent = strings.ToLower(appearance.Accent)
	if appearance.Density == "" {
		appearance.Density = defaults.Density
	}
	if appearance.TextSize == 0 {
		appearance.TextSize = defaults.TextSize
	}
	if appearance.UIFont == "" {
		appearance.UIFont = defaults.UIFont
	}
	if appearance.MonoFont == "" {
		appearance.MonoFont = defaults.MonoFont
	}
	if appearance.Radius == "" {
		appearance.Radius = defaults.Radius
	}
	if appearance.Tokens == nil {
		appearance.Tokens = map[string]string{}
	} else {
		appearance.Tokens = maps.Clone(appearance.Tokens)
	}
	return appearance
}

// Validate judges a normalized appearance.
func (appearance Appearance) Validate() error {
	switch {
	case !themeID.MatchString(appearance.Theme):
		return ValidationError{"theme", "must be a lowercase theme id"}
	case appearance.Accent != "" && !accentColor.MatchString(appearance.Accent):
		return ValidationError{"accent", "must be empty or #rrggbb"}
	case !oneOf(appearance.Density, "compact", "comfortable", "spacious"):
		return ValidationError{"density", "must be compact, comfortable or spacious"}
	case appearance.TextSize < 12 || appearance.TextSize > 14:
		return ValidationError{"textSize", "must be 12, 13 or 14"}
	case !oneOf(appearance.UIFont, "plex", "geist", "system"):
		return ValidationError{"uiFont", "must be plex, geist or system"}
	case !oneOf(appearance.MonoFont, "plex", "jetbrains", "system"):
		return ValidationError{"monoFont", "must be plex, jetbrains or system"}
	case !oneOf(appearance.Radius, "sharp", "default", "round"):
		return ValidationError{"radius", "must be sharp, default or round"}
	}
	if err := cssguard.CheckTokens(appearance.Tokens, MaxTokens); err != nil {
		return ValidationError{"tokens", err.Error()}
	}
	if err := cssguard.CheckStylesheet(appearance.CustomCSS, MaxCustomCSSBytes); err != nil {
		return ValidationError{"customCss", err.Error()}
	}
	return nil
}

// IsValidation reports whether err is a rule the input broke, as opposed to a
// storage or runtime failure.
func IsValidation(err error) bool {
	var validation ValidationError
	return errors.As(err, &validation)
}

func oneOf(value string, allowed ...string) bool { return slices.Contains(allowed, value) }
