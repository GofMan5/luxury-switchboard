package domain

import "testing"

func TestNewerThanComparesTheReleaseLineNumerically(t *testing.T) {
	for _, testCase := range []struct {
		latest, current string
		newer           bool
	}{
		{"1.0.39", "1.0.38", true},
		{"v1.0.39", "1.0.38", true},
		{"1.0.38", "1.0.38", false},
		{"1.0.37", "1.0.38", false},
		// Numeric, not lexicographic: "1.0.9" sorts after "1.0.10" as text.
		{"1.0.10", "1.0.9", true},
		{"1.0.9", "1.0.10", false},
		// A malformed tag is never an upgrade prompt.
		{"latest", "1.0.38", false},
		{"1.0.38-beta", "1.0.37", false},
		{"", "1.0.38", false},
	} {
		if got := NewerThan(testCase.latest, testCase.current); got != testCase.newer {
			t.Errorf("NewerThan(%q, %q) = %v, want %v", testCase.latest, testCase.current, got, testCase.newer)
		}
	}
}
