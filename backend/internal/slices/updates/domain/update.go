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

// InstallerSuffix names the release asset a platform self-updates through.
// It is the one naming contract the release scripts, the feed reader and
// the staging service all share: NSIS setups on Windows, AppImages on
// Linux, dmg bundles on macOS. A pair the line does not build answers
// empty, which reads as "no self-update for this platform" and leaves the
// release page the honest path.
func InstallerSuffix(goos, goarch string) string {
	switch goos + "/" + goarch {
	case "windows/amd64":
		return "-windows-x64-setup.exe"
	case "windows/arm64":
		return "-windows-arm64-setup.exe"
	case "linux/amd64":
		return "-linux-x86_64.AppImage"
	case "linux/arm64":
		return "-linux-aarch64.AppImage"
	case "darwin/amd64":
		return "-macos-x64.dmg"
	case "darwin/arm64":
		return "-macos-arm64.dmg"
	}
	return ""
}

// IsParkedInstaller says whether a file in the update staging directory is
// one of the installer shapes this line parks there on any platform it
// builds for. The staging pruner sweeps by shape, not by the current
// platform's exact suffix, so a superseded candidate left by an older
// build or another platform's hand never survives a sweep; a file that is
// no installer at all is left alone.
func IsParkedInstaller(name string) bool {
	for _, suffix := range [...]string{"-setup.exe", ".AppImage", ".dmg"} {
		if strings.HasSuffix(name, suffix) {
			return true
		}
	}
	return false
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
