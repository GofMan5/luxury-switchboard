// Package domain holds the rules for owner-installed plugins: what a package
// may declare, which permissions the owner may grant it, and the bounded
// key-value storage it keeps. It knows nothing of the transport that delivers
// a package or the file that keeps it.
package domain

import (
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/luxuryprivate/switchboard/backend/internal/platform/cssguard"
)

const (
	SchemaVersion = 1

	MaxPlugins          = 24
	MaxPermissions      = 16
	MaxNameRunes        = 64
	MaxVersionBytes     = 32
	MaxAuthorRunes      = 64
	MaxDescriptionRunes = 280
	MaxThemeNameRunes   = 48
	MaxThemeTokens      = 96
	MaxCSSBytes         = 64 * 1024
	MaxScriptBytes      = 128 * 1024

	MaxStorageKeys       = 64
	MaxStorageValueBytes = 8 * 1024
	// MaxStorageBytes bounds the sum of every key and value a plugin keeps.
	MaxStorageBytes = 64 * 1024
)

const (
	PermissionTheme     = "ui:theme"
	PermissionCSS       = "ui:css"
	PermissionPages     = "ui:pages"
	PermissionStatus    = "ui:status"
	PermissionCommands  = "ui:commands"
	PermissionNotify    = "ui:notify"
	PermissionRead      = "control:read"
	PermissionWrite     = "control:write"
	PermissionSensitive = "control:sensitive"
	PermissionStorage   = "storage"
)

var knownPermissions = []string{
	PermissionTheme, PermissionCSS, PermissionPages, PermissionStatus, PermissionCommands,
	PermissionNotify, PermissionRead, PermissionWrite, PermissionSensitive, PermissionStorage,
}

// scriptPermissions only mean something to code: declaring one without a
// script is a package that promises behavior it cannot have.
var scriptPermissions = []string{
	PermissionPages, PermissionStatus, PermissionCommands, PermissionNotify,
	PermissionRead, PermissionWrite, PermissionSensitive, PermissionStorage,
}

var (
	pluginID   = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,62}[a-z0-9]$`)
	version    = regexp.MustCompile(`^[0-9A-Za-z.+-]{1,32}$`)
	storageKey = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,64}$`)
)

// ErrStorageFull is a storage write that would break a storage bound.
var ErrStorageFull = errors.New("plugin storage is full")

// ValidationError names the field that failed in text that is safe to show:
// it never repeats the rejected value.
type ValidationError struct {
	Field  string
	Reason string
}

func (err ValidationError) Error() string { return err.Field + " " + err.Reason }

type Theme struct {
	Name   string            `json:"name"`
	Scheme string            `json:"scheme"`
	Tokens map[string]string `json:"tokens"`
}

// Package is what the owner installs. It is immutable once installed: an
// upgrade replaces it whole.
type Package struct {
	Schema      int      `json:"schema"`
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Version     string   `json:"version"`
	Author      string   `json:"author,omitempty"`
	Description string   `json:"description,omitempty"`
	Permissions []string `json:"permissions"`
	Theme       *Theme   `json:"theme,omitempty"`
	CSS         string   `json:"css,omitempty"`
	Script      string   `json:"script,omitempty"`
}

// Plugin is an installed package with the owner's decisions about it.
type Plugin struct {
	Package     Package
	Enabled     bool
	Granted     []string
	InstalledAt time.Time
	UpdatedAt   time.Time
	Storage     map[string]string
}

// ValidID reports whether id can name a plugin.
func ValidID(id string) bool { return pluginID.MatchString(id) }

// Normalized returns a copy whose absent lists and maps are empty, sharing no
// mutable state with the receiver.
func (pkg Package) Normalized() Package {
	pkg.Permissions = cloneList(pkg.Permissions)
	if pkg.Theme != nil {
		theme := *pkg.Theme
		theme.Tokens = cloneMap(theme.Tokens)
		pkg.Theme = &theme
	}
	return pkg
}

func (pkg Package) Validate() error {
	switch {
	case pkg.Schema != SchemaVersion:
		return ValidationError{"schema", fmt.Sprintf("must be %d", SchemaVersion)}
	case !ValidID(pkg.ID):
		return ValidationError{"id", "must be 3-64 lowercase letters, digits, dots or dashes"}
	case !validText(pkg.Name, 1, MaxNameRunes, false):
		return ValidationError{"name", fmt.Sprintf("must be 1-%d characters without control characters", MaxNameRunes)}
	case !version.MatchString(pkg.Version):
		return ValidationError{"version", fmt.Sprintf("must be 1-%d letters, digits, dots, plus or dash", MaxVersionBytes)}
	case !validText(pkg.Author, 0, MaxAuthorRunes, false):
		return ValidationError{"author", fmt.Sprintf("must be at most %d characters without control characters", MaxAuthorRunes)}
	case !validText(pkg.Description, 0, MaxDescriptionRunes, true):
		return ValidationError{"description", fmt.Sprintf("must be at most %d characters without control characters", MaxDescriptionRunes)}
	case len(pkg.Script) > MaxScriptBytes:
		return ValidationError{"script", fmt.Sprintf("is larger than %d bytes", MaxScriptBytes)}
	}
	if err := validatePermissions(pkg.Permissions); err != nil {
		return err
	}
	if pkg.Theme != nil {
		if err := pkg.Theme.validate(); err != nil {
			return err
		}
	}
	if err := cssguard.CheckStylesheet(pkg.CSS, MaxCSSBytes); err != nil {
		return ValidationError{"css", err.Error()}
	}
	declared := pkg.Permissions
	switch {
	case pkg.Theme != nil && !slices.Contains(declared, PermissionTheme):
		return ValidationError{"theme", "requires the ui:theme permission"}
	case pkg.CSS != "" && !slices.Contains(declared, PermissionCSS):
		return ValidationError{"css", "requires the ui:css permission"}
	case pkg.Script == "" && slices.ContainsFunc(declared, isScriptPermission):
		return ValidationError{"permissions", "declare script capabilities without a script"}
	}
	return nil
}

// Grant validates the permissions the owner grants pkg and returns them in the
// package's declared order. Nothing outside the declaration can be granted.
func (pkg Package) Grant(granted []string) ([]string, error) {
	if len(granted) > len(pkg.Permissions) {
		return nil, ValidationError{"granted", "includes permissions the plugin does not declare"}
	}
	for index, permission := range granted {
		if !slices.Contains(pkg.Permissions, permission) {
			return nil, ValidationError{"granted", "includes permissions the plugin does not declare"}
		}
		if slices.Contains(granted[:index], permission) {
			return nil, ValidationError{"granted", "lists a permission twice"}
		}
	}
	ordered := make([]string, 0, len(granted))
	for _, permission := range pkg.Permissions {
		if slices.Contains(granted, permission) {
			ordered = append(ordered, permission)
		}
	}
	return ordered, nil
}

// Validate checks a restored plugin as a whole.
func (plugin Plugin) Validate() error {
	if err := plugin.Package.Validate(); err != nil {
		return err
	}
	if granted, err := plugin.Package.Grant(plugin.Granted); err != nil || !slices.Equal(granted, plugin.Granted) {
		return ValidationError{"granted", "is not an ordered subset of the declared permissions"}
	}
	if plugin.InstalledAt.IsZero() || plugin.UpdatedAt.IsZero() || plugin.UpdatedAt.Before(plugin.InstalledAt) {
		return ValidationError{"timestamps", "are missing or out of order"}
	}
	return CheckStorage(plugin.Storage)
}

// ValidStorageKey reports whether key may name a storage entry.
func ValidStorageKey(key string) bool { return storageKey.MatchString(key) }

// CheckStorage validates a whole storage map. Bounds breaches are
// ErrStorageFull; malformed keys are a ValidationError.
func CheckStorage(values map[string]string) error {
	if len(values) > MaxStorageKeys {
		return ErrStorageFull
	}
	total := 0
	for key, value := range values {
		if !ValidStorageKey(key) {
			return ValidationError{"key", "must be 1-64 letters, digits, dots, colons, underscores or dashes"}
		}
		if len(value) > MaxStorageValueBytes {
			return ErrStorageFull
		}
		total += len(key) + len(value)
	}
	if total > MaxStorageBytes {
		return ErrStorageFull
	}
	return nil
}

// WithStorage returns a copy of values with key set, or removed when value is
// nil, after checking the result against the storage bounds.
func WithStorage(values map[string]string, key string, value *string) (map[string]string, error) {
	if !ValidStorageKey(key) {
		return nil, ValidationError{"key", "must be 1-64 letters, digits, dots, colons, underscores or dashes"}
	}
	next := cloneMap(values)
	if value == nil {
		delete(next, key)
		return next, nil
	}
	next[key] = *value
	if err := CheckStorage(next); err != nil {
		return nil, err
	}
	return next, nil
}

func (theme Theme) validate() error {
	switch {
	case !validText(theme.Name, 1, MaxThemeNameRunes, false):
		return ValidationError{"theme.name", fmt.Sprintf("must be 1-%d characters without control characters", MaxThemeNameRunes)}
	case theme.Scheme != "dark" && theme.Scheme != "light":
		return ValidationError{"theme.scheme", "must be dark or light"}
	}
	if err := cssguard.CheckTokens(theme.Tokens, MaxThemeTokens); err != nil {
		return ValidationError{"theme.tokens", err.Error()}
	}
	return nil
}

func validatePermissions(permissions []string) error {
	if len(permissions) > MaxPermissions {
		return ValidationError{"permissions", fmt.Sprintf("list more than %d entries", MaxPermissions)}
	}
	for index, permission := range permissions {
		if !slices.Contains(knownPermissions, permission) {
			return ValidationError{"permissions", "include an unknown permission"}
		}
		if slices.Contains(permissions[:index], permission) {
			return ValidationError{"permissions", "list a permission twice"}
		}
	}
	return nil
}

func isScriptPermission(permission string) bool {
	return slices.Contains(scriptPermissions, permission)
}

// validText bounds a display string by runes. A required value must not be
// blank; control characters are refused, except newlines where allowed.
func validText(value string, minRunes, maxRunes int, newlines bool) bool {
	count := utf8.RuneCountInString(value)
	if count < minRunes || count > maxRunes || !utf8.ValidString(value) {
		return false
	}
	if minRunes > 0 && strings.TrimSpace(value) == "" {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) && !(newlines && character == '\n') {
			return false
		}
	}
	return true
}

func cloneList(values []string) []string {
	if values == nil {
		return []string{}
	}
	return slices.Clone(values)
}

func cloneMap(values map[string]string) map[string]string {
	if values == nil {
		return map[string]string{}
	}
	return maps.Clone(values)
}
