package domain

import (
	"runtime"
	"testing"
)

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

// The six platforms the release builds for must each name a distinct
// installer, and a pair outside that map must answer empty, because an
// empty suffix is the "no self-update, see the release page" answer.
func TestTheInstallerSuffixNamesEveryPlatformTheReleaseBuilds(t *testing.T) {
	for _, testCase := range []struct {
		goos, goarch string
		suffix       string
	}{
		{"windows", "amd64", "-windows-x64-setup.exe"},
		{"windows", "arm64", "-windows-arm64-setup.exe"},
		{"linux", "amd64", "-linux-x86_64.AppImage"},
		{"linux", "arm64", "-linux-aarch64.AppImage"},
		{"darwin", "amd64", "-macos-x64.dmg"},
		{"darwin", "arm64", "-macos-arm64.dmg"},
	} {
		if got := InstallerSuffix(testCase.goos, testCase.goarch); got != testCase.suffix {
			t.Errorf("InstallerSuffix(%q, %q) = %q, want %q", testCase.goos, testCase.goarch, got, testCase.suffix)
		}
	}
	for _, unknown := range [][2]string{{"plan9", "amd64"}, {"linux", "386"}, {"", ""}} {
		if got := InstallerSuffix(unknown[0], unknown[1]); got != "" {
			t.Errorf("InstallerSuffix(%q, %q) = %q, want empty", unknown[0], unknown[1], got)
		}
	}
	// The running platform is one of the six, or this code never shipped.
	if InstallerSuffix(runtime.GOOS, runtime.GOARCH) == "" {
		t.Errorf("the running pair %s/%s has no installer suffix", runtime.GOOS, runtime.GOARCH)
	}
}

func TestIsParkedInstallerRecognizesEveryShapeOnEveryPlatform(t *testing.T) {
	for _, parked := range [...]string{
		"Luxury-Switchboard-1.0.44-windows-x64-setup.exe",
		"Luxury-Switchboard-1.0.44-windows-arm64-setup.exe",
		"Luxury-Switchboard-1.0.44-linux-x86_64.AppImage",
		"Luxury-Switchboard-1.0.44-linux-aarch64.AppImage",
		"Luxury-Switchboard-1.0.44-macos-x64.dmg",
		"Luxury-Switchboard-1.0.44-macos-arm64.dmg",
	} {
		if !IsParkedInstaller(parked) {
			t.Errorf("IsParkedInstaller(%q) = false, want true", parked)
		}
	}
	for _, other := range [...]string{"setup.exe", "notes.txt", "SHA256SUMS.txt", ".part", "installer.dmgz"} {
		if IsParkedInstaller(other) {
			t.Errorf("IsParkedInstaller(%q) = true, want false", other)
		}
	}
}
