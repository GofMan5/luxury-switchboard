package domain

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	MaxSettings                = 24
	MaxSettingKeyBytes         = 32
	MaxSettingLabelRunes       = 64
	MaxSettingDescriptionRunes = 200
	MaxSettingPlaceholderRunes = 64
	// MaxSettingTextRunes is a text setting's length when it declares none.
	MaxSettingTextRunes   = 256
	MaxSettingOptions     = 32
	MaxSettingOptionRunes = 64
	// MaxSecretSettings bounds the secret settings of one package.
	MaxSecretSettings = 8
	// MaxSecretRunes bounds one secret value.
	MaxSecretRunes = 4096
)

const (
	SettingText   = "text"
	SettingNumber = "number"
	SettingToggle = "toggle"
	SettingSelect = "select"
	// SettingSecret is a value the owner enters and the plugin never reads:
	// net:fetch substitutes it for {{secret:KEY}} in the control plane.
	SettingSecret = "secret"
)

var settingKey = regexp.MustCompile(`^[a-z][A-Za-z0-9_]{0,31}$`)

// Setting is one value the owner can tune and the plugin's script reads. The
// package declares its shape and default; the owner's choice is kept in the
// plugin's storage by the shell.
type Setting struct {
	Key         string `json:"key"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
	Type        string `json:"type"`
	// Default is raw so its JSON type can be checked against Type.
	Default     json.RawMessage `json:"default,omitempty"`
	Placeholder string          `json:"placeholder,omitempty"`
	// MaxLength bounds a text setting; zero means MaxSettingTextRunes.
	MaxLength int             `json:"maxLength,omitempty"`
	Min       *float64        `json:"min,omitempty"`
	Max       *float64        `json:"max,omitempty"`
	Options   []SettingOption `json:"options,omitempty"`
}

type SettingOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// normalized shares nothing with the receiver and writes Default in one
// canonical form, so a package reads back from storage exactly as it was
// installed whatever escaping the storage encoder applied.
func (setting Setting) normalized() Setting {
	setting.Options = slices.Clone(setting.Options)
	setting.Min = cloneNumber(setting.Min)
	setting.Max = cloneNumber(setting.Max)
	setting.Default = canonicalJSON(setting.Default)
	return setting
}

func validateSettings(settings []Setting) error {
	if len(settings) > MaxSettings {
		return ValidationError{"settings", fmt.Sprintf("list more than %d entries", MaxSettings)}
	}
	for index, setting := range settings {
		if err := setting.validate(fmt.Sprintf("settings[%d]", index), settings[:index]); err != nil {
			return err
		}
	}
	if len(SecretKeys(settings)) > MaxSecretSettings {
		return ValidationError{"settings", fmt.Sprintf("list more than %d secret entries", MaxSecretSettings)}
	}
	return nil
}

// SecretKeys returns the keys of the secret settings, in declared order.
func SecretKeys(settings []Setting) []string {
	var keys []string
	for _, setting := range settings {
		if setting.Type == SettingSecret {
			keys = append(keys, setting.Key)
		}
	}
	return keys
}

// ValidSecretValue reports whether value can be saved as a secret: 1 to
// MaxSecretRunes characters of valid UTF-8 without control characters, so
// it can sit in a header value.
func ValidSecretValue(value string) bool {
	count := utf8.RuneCountInString(value)
	if count < 1 || count > MaxSecretRunes || !utf8.ValidString(value) {
		return false
	}
	return !strings.ContainsFunc(value, unicode.IsControl)
}

// validate checks one setting in a fixed order — key, label, description,
// type, attributes the type does not use, the type's own attributes, then
// the default — and reports the first failure.
func (setting Setting) validate(field string, earlier []Setting) error {
	switch {
	case !settingKey.MatchString(setting.Key):
		return ValidationError{field + ".key", fmt.Sprintf("must be a lowercase letter then at most %d letters, digits or underscores", MaxSettingKeyBytes-1)}
	case slices.ContainsFunc(earlier, func(other Setting) bool { return other.Key == setting.Key }):
		return ValidationError{field + ".key", "repeats another setting's key"}
	case !validText(setting.Label, 1, MaxSettingLabelRunes, false):
		return ValidationError{field + ".label", fmt.Sprintf("must be 1-%d characters without control characters", MaxSettingLabelRunes)}
	case !validText(setting.Description, 0, MaxSettingDescriptionRunes, false):
		return ValidationError{field + ".description", fmt.Sprintf("must be at most %d characters without control characters", MaxSettingDescriptionRunes)}
	}
	var used []string
	switch setting.Type {
	case SettingText:
		used = []string{"placeholder", "maxLength"}
	case SettingNumber:
		used = []string{"placeholder", "min", "max"}
	case SettingToggle:
	case SettingSelect:
		used = []string{"options"}
	case SettingSecret:
	default:
		return ValidationError{field + ".type", "must be text, number, toggle, select or secret"}
	}
	for _, attribute := range setting.attributes() {
		if !slices.Contains(used, attribute) {
			return ValidationError{field + "." + attribute, "is not used by a " + setting.Type + " setting"}
		}
	}
	switch setting.Type {
	case SettingText:
		return setting.validateText(field)
	case SettingNumber:
		return setting.validateNumber(field)
	case SettingToggle:
		if _, ok := decodeDefault(setting.Default).(bool); !ok {
			return defaultError(setting, field, "must be true or false")
		}
		return nil
	case SettingSecret:
		if len(setting.Default) != 0 {
			return ValidationError{field + ".default", "does not apply to a secret setting"}
		}
		return nil
	default:
		return setting.validateSelect(field)
	}
}

// attributes names the type-specific attributes the setting carries. Zero
// values are absent: once decoded, "placeholder":"", "maxLength":0,
// "min":null and "options":[] cannot be told from a missing key.
func (setting Setting) attributes() []string {
	var present []string
	if setting.Placeholder != "" {
		present = append(present, "placeholder")
	}
	if setting.MaxLength != 0 {
		present = append(present, "maxLength")
	}
	if setting.Min != nil {
		present = append(present, "min")
	}
	if setting.Max != nil {
		present = append(present, "max")
	}
	if len(setting.Options) > 0 {
		present = append(present, "options")
	}
	return present
}

func (setting Setting) validateText(field string) error {
	limit := MaxSettingTextRunes
	if setting.MaxLength != 0 {
		if setting.MaxLength < 1 || setting.MaxLength > MaxSettingTextRunes {
			return ValidationError{field + ".maxLength", fmt.Sprintf("must be 1-%d", MaxSettingTextRunes)}
		}
		limit = setting.MaxLength
	}
	if err := setting.validatePlaceholder(field); err != nil {
		return err
	}
	if text, ok := decodeDefault(setting.Default).(string); !ok || !validText(text, 0, limit, false) {
		return defaultError(setting, field, "must be text within maxLength without control characters")
	}
	return nil
}

func (setting Setting) validatePlaceholder(field string) error {
	if !validText(setting.Placeholder, 0, MaxSettingPlaceholderRunes, false) {
		return ValidationError{field + ".placeholder", fmt.Sprintf("must be at most %d characters without control characters", MaxSettingPlaceholderRunes)}
	}
	return nil
}

func (setting Setting) validateNumber(field string) error {
	if err := setting.validatePlaceholder(field); err != nil {
		return err
	}
	switch {
	case setting.Min != nil && !finite(*setting.Min):
		return ValidationError{field + ".min", "must be a finite number"}
	case setting.Max != nil && !finite(*setting.Max):
		return ValidationError{field + ".max", "must be a finite number"}
	case setting.Min != nil && setting.Max != nil && *setting.Min > *setting.Max:
		return ValidationError{field + ".max", "must not be less than min"}
	}
	literal, ok := decodeDefault(setting.Default).(json.Number)
	number, err := strconv.ParseFloat(string(literal), 64)
	if !ok || err != nil || !finite(number) ||
		(setting.Min != nil && number < *setting.Min) || (setting.Max != nil && number > *setting.Max) {
		return defaultError(setting, field, "must be a finite number within min and max")
	}
	return nil
}

func (setting Setting) validateSelect(field string) error {
	if len(setting.Options) < 1 || len(setting.Options) > MaxSettingOptions {
		return ValidationError{field + ".options", fmt.Sprintf("must list 1-%d options", MaxSettingOptions)}
	}
	for index, option := range setting.Options {
		path := fmt.Sprintf("%s.options[%d]", field, index)
		switch {
		case !validText(option.Value, 1, MaxSettingOptionRunes, false):
			return ValidationError{path + ".value", fmt.Sprintf("must be 1-%d characters without control characters", MaxSettingOptionRunes)}
		case slices.ContainsFunc(setting.Options[:index], func(other SettingOption) bool { return other.Value == option.Value }):
			return ValidationError{path + ".value", "repeats another option's value"}
		case !validText(option.Label, 1, MaxSettingOptionRunes, false):
			return ValidationError{path + ".label", fmt.Sprintf("must be 1-%d characters without control characters", MaxSettingOptionRunes)}
		}
	}
	value, ok := decodeDefault(setting.Default).(string)
	if !ok || !slices.ContainsFunc(setting.Options, func(option SettingOption) bool { return option.Value == value }) {
		return defaultError(setting, field, "must be one of the option values")
	}
	return nil
}

// defaultError tells a missing default apart from one of the wrong shape.
func defaultError(setting Setting, field, reason string) error {
	if len(setting.Default) == 0 {
		return ValidationError{field + ".default", "is required"}
	}
	return ValidationError{field + ".default", reason}
}

// decodeDefault returns the default as a string, bool, json.Number, nil for
// JSON null, or nil when it is not one JSON value.
func decodeDefault(raw json.RawMessage) any {
	if !json.Valid(raw) {
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if decoder.Decode(&value) != nil {
		return nil
	}
	return value
}

// canonicalJSON re-encodes one JSON value compactly without HTML escaping.
// Anything that is not one JSON value is copied unchanged for Validate to
// refuse.
func canonicalJSON(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		// An absent default stays absent; an empty non-nil RawMessage would
		// not even encode.
		return nil
	}
	value := decodeDefault(raw)
	if value == nil && !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return bytes.Clone(raw)
	}
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if encoder.Encode(value) != nil {
		return bytes.Clone(raw)
	}
	return bytes.TrimSuffix(buffer.Bytes(), []byte("\n"))
}

func cloneNumber(value *float64) *float64 {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
}

func finite(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }
