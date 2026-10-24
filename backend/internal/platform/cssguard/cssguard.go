// Package cssguard holds the two style rules the appearance and plugins slices
// share: design tokens that become CSS custom properties, and whole stylesheets
// that are injected into the WebView. Both are owner-supplied text crossing
// into a style context, so the rules are deliberately narrow — a token value
// can never close its declaration or pull in a resource, and a stylesheet can
// never import another or break out of its <style> element.
//
// The functions are pure: errors describe the rule that failed in safe text
// and never echo the rejected value.
package cssguard

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// MaxTokenValueBytes bounds one token value.
const MaxTokenValueBytes = 120

var tokenName = regexp.MustCompile(`^--[a-z][a-z0-9-]{0,47}$`)

// forbiddenValueBytes could end a declaration, open a block or tag, or escape
// the value; control characters (newline included) never belong in one.
const forbiddenValueBytes = ";{}<>\\`"

var forbiddenValueFragments = []string{"url(", "expression(", "@import", "javascript:"}

// CheckTokens validates a token map against limit entries. Names are checked in
// sorted order so the same map always reports the same problem.
func CheckTokens(tokens map[string]string, limit int) error {
	if len(tokens) > limit {
		return fmt.Errorf("has more than %d tokens", limit)
	}
	names := make([]string, 0, len(tokens))
	for name := range tokens {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		if !tokenName.MatchString(name) {
			return errors.New("has a token whose name is not --lowercase-words")
		}
		if err := checkTokenValue(tokens[name]); err != nil {
			// The name matched the pattern above, so it is safe to repeat.
			return fmt.Errorf("token %s %w", name, err)
		}
	}
	return nil
}

func checkTokenValue(value string) error {
	if value == "" || len(value) > MaxTokenValueBytes {
		return fmt.Errorf("must be 1-%d bytes", MaxTokenValueBytes)
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f || strings.ContainsRune(forbiddenValueBytes, character) {
			return errors.New("contains a forbidden character")
		}
	}
	lower := strings.ToLower(value)
	for _, fragment := range forbiddenValueFragments {
		if strings.Contains(lower, fragment) {
			return fmt.Errorf("must not contain %s", fragment)
		}
	}
	return nil
}

// CheckStylesheet validates a stylesheet of at most limit bytes.
func CheckStylesheet(css string, limit int) error {
	if len(css) > limit {
		return fmt.Errorf("is larger than %d bytes", limit)
	}
	lower := strings.ToLower(css)
	if strings.Contains(lower, "@import") {
		return errors.New("must not contain @import")
	}
	if strings.Contains(lower, "</style") {
		return errors.New("must not contain </style")
	}
	return nil
}
