package domain

import (
	"strconv"
	"strings"
)

// Asset is one downloadable file of a release.
type Asset struct {
	Name string `json:"name"`
	URL  string `json:"url"`
	Size int64  `json:"size"`
}

// Latest is one published release: the version tag, the page to open, when it
// shipped, and the files it ships.
type Latest struct {
	Version     string `json:"version"`
	URL         string `json:"url"`
	PublishedAt string `json:"publishedAt"`
	// Installer is the release's setup asset for this platform; Checksums is
	// the file that carries its expected digest. Both are nil when the
	// release ships no self-update for this platform, and the honest answer
	// is the release page, not a download.
	Installer *Asset `json:"installer,omitempty"`
	Checksums *Asset `json:"checksums,omitempty"`
}

// NewerThan compares two release lines numerically. The line is forever
// 1.0.x, but the comparison stays honest for any three-number tag; a tag that
// does not parse is never "newer", because a guessed upgrade is worse than a
// missed one.
func NewerThan(latest, current string) bool {
	next, okNext := parseVersion(latest)
	now, okNow := parseVersion(current)
	if !okNext || !okNow {
		return false
	}
	for index := range 3 {
		if next[index] != now[index] {
			return next[index] > now[index]
		}
	}
	return false
}

func parseVersion(tag string) ([3]int, bool) {
	var version [3]int
	tag = strings.TrimPrefix(strings.TrimSpace(tag), "v")
	parts := strings.Split(tag, ".")
	if len(parts) < 2 || len(parts) > 3 {
		return version, false
	}
	for index, part := range parts {
		// A suffix like "1.0.38-beta" is not a release this line ships.
		value, err := strconv.Atoi(part)
		if err != nil || value < 0 {
			return version, false
		}
		version[index] = value
	}
	if len(parts) == 2 {
		version[2] = 0
	}
	return version, true
}
