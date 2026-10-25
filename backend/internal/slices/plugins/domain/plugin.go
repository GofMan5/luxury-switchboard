// Package domain holds the rules for owner-installed plugins: what a package
// may declare, which permissions the owner may grant it, and the bounded
// key-value storage it keeps. It knows nothing of the transport that delivers
// a package or the file that keeps it.
package domain

import (
	"errors"
	"fmt"
	"maps"
	"net/url"
	"regexp"
	"slices"
	"strconv"
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
	MaxHomepageBytes    = 200

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
	PermissionClipboard = "ui:clipboard"
	PermissionRead      = "control:read"
	PermissionWrite     = "control:write"
	PermissionSensitive = "control:sensitive"
	PermissionStorage   = "storage"
)

var knownPermissions = []string{
	PermissionTheme, PermissionCSS, PermissionPages, PermissionStatus, PermissionCommands,
	PermissionNotify, PermissionClipboard, PermissionRead, PermissionWrite, PermissionSensitive, PermissionStorage,
}

// scriptPermissions only mean something to code: declaring one without a
// script is a package that promises behavior it cannot have.
var scriptPermissions = []string{
	PermissionPages, PermissionStatus, PermissionCommands, PermissionNotify, PermissionClipboard,
	PermissionRead, PermissionWrite, PermissionSensitive, PermissionStorage,
}

var (
	pluginID      = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,62}[a-z0-9]$`)
	version       = regexp.MustCompile(`^[0-9A-Za-z.+-]{1,32}$`)
	appVersion    = regexp.MustCompile(`^[0-9]{1,4}\.[0-9]{1,4}\.[0-9]{1,4}$`)
	storageKey    = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,64}$`)
	addressPrefix = "https://"
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
	Schema      int    `json:"schema"`
	ID          string `json:"id"`
	Name        string `json:"name"`
	Version     string `json:"version"`
	Author      string `json:"author,omitempty"`
	Description string `json:"description,omitempty"`
	// Homepage is where the owner can read about the plugin; the shell only
	// ever opens it on the owner's click.
	Homepage string `json:"homepage,omitempty"`
	// MinAppVersion is the oldest app release the plugin runs on.
	MinAppVersion string    `json:"minAppVersion,omitempty"`
	Permissions   []string  `json:"permissions"`
	Theme         *Theme    `json:"theme,omitempty"`
	CSS           string    `json:"css,omitempty"`
	Script        string    `json:"script,omitempty"`
	Settings      []Setting `json:"settings,omitempty"`
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
// mutable state with the receiver. Setting defaults are re-encoded compactly
// so a stored package reads back byte for byte.
func (pkg Package) Normalized() Package {
	pkg.Permissions = cloneList(pkg.Permissions)
	if pkg.Theme != nil {
		theme := *pkg.Theme
		theme.Tokens = cloneMap(theme.Tokens)
		pkg.Theme = &theme
	}
	if pkg.Settings != nil {
		settings := make([]Setting, len(pkg.Settings))
		for index, setting := range pkg.Settings {
			settings[index] = setting.normalized()
		}
		pkg.Settings = settings
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
	case !validHomepage(pkg.Homepage):
		return ValidationError{"homepage", fmt.Sprintf("must be an https:// address of at most %d characters", MaxHomepageBytes)}
	case pkg.MinAppVersion != "" && !ValidAppVersion(pkg.MinAppVersion):
		return ValidationError{"minAppVersion", "must be three dot-separated numbers of 1-4 digits"}
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
	if err := validateSettings(pkg.Settings); err != nil {
		return err
	}
	declared := pkg.Permissions
	switch {
	case pkg.Theme != nil && !slices.Contains(declared, PermissionTheme):
		return ValidationError{"theme", "requires the ui:theme permission"}
	case pkg.CSS != "" && !slices.Contains(declared, PermissionCSS):
		return ValidationError{"css", "requires the ui:css permission"}
	case pkg.Script == "" && slices.ContainsFunc(declared, isScriptPermission):
		return ValidationError{"permissions", "declare script capabilities without a script"}
	case pkg.Script == "" && len(pkg.Settings) > 0:
		return ValidationError{"settings", "need a script to read them"}
	}
	return nil
}

// validHomepage accepts nothing or an absolute https address with a host and
// no credentials. The literal prefix refuses the forms a URL parser would
// still read as https, such as an upper-case scheme or no slashes.
func validHomepage(value string) bool {
	if value == "" {
		return true
	}
	if len(value) > MaxHomepageBytes || !utf8.ValidString(value) || !strings.HasPrefix(value, addressPrefix) ||
		strings.ContainsFunc(value, func(character rune) bool { return unicode.IsSpace(character) || unicode.IsControl(character) }) {
		return false
	}
	address, err := url.Parse(value)
	return err == nil && address.Scheme == "https" && address.Hostname() != "" && address.User == nil
}

// Listed returns a copy of a refused stored package in which each listed
// field that breaks its own rule is blank, the name falling back to the id,
// so the record can still be shown to the owner and removed. The caller has
// checked the id.
func (pkg Package) Listed() Package {
	listed := pkg.Normalized()
	if !validText(listed.Name, 1, MaxNameRunes, false) {
		listed.Name = listed.ID
	}
	if !version.MatchString(listed.Version) {
		listed.Version = ""
	}
	if !validText(listed.Author, 0, MaxAuthorRunes, false) {
		listed.Author = ""
	}
	if !validText(listed.Description, 0, MaxDescriptionRunes, true) {
		listed.Description = ""
	}
	if !validHomepage(listed.Homepage) {
		listed.Homepage = ""
	}
	if !ValidAppVersion(listed.MinAppVersion) {
		listed.MinAppVersion = ""
	}
	if validatePermissions(listed.Permissions) != nil {
		listed.Permissions = []string{}
	}
	return listed
}

// ValidAppVersion reports whether value names a release as three
// dot-separated numbers of one to four digits.
func ValidAppVersion(value string) bool { return appVersion.MatchString(value) }

// NeedsNewerApp reports whether pkg declares a minimum release later than
// running, comparing the three numbers in order. running must be a valid app
// version.
func (pkg Package) NeedsNewerApp(running string) bool {
	if pkg.MinAppVersion == "" {
		return false
	}
	required, current := releaseNumbers(pkg.MinAppVersion), releaseNumbers(running)
	return slices.Compare(required, current) > 0
}

func releaseNumbers(value string) []int {
	numbers := make([]int, 0, 3)
	for _, part := range strings.Split(value, ".") {
		number, _ := strconv.Atoi(part)
		numbers = append(numbers, number)
	}
	return numbers
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

// WithStorageChanges returns a copy of values with every change applied at
// once — a nil value removes its key — after first removing every key when
// clear is set, then checks the result against the storage bounds. A batch
// lists at most MaxStorageKeys keys, and at least one unless it clears.
func WithStorageChanges(values map[string]string, changes map[string]*string, clear bool) (map[string]string, error) {
	if len(changes) > MaxStorageKeys || (len(changes) == 0 && !clear) {
		return nil, ValidationError{"values", fmt.Sprintf("must list at most %d keys, and at least one unless clearing", MaxStorageKeys)}
	}
	next := map[string]string{}
	if !clear {
		next = cloneMap(values)
	}
	for key, value := range changes {
		if !ValidStorageKey(key) {
			return nil, ValidationError{"key", "must be 1-64 letters, digits, dots, colons, underscores or dashes"}
		}
		if value == nil {
			delete(next, key)
		} else {
			next[key] = *value
		}
	}
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
