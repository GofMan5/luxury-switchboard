package application

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/updates/domain"
)

// downloadReleases serves both the checksum file and the installer from
// memory: the service must be judged on what it does with the bytes, not on
// reaching a network.
type downloadReleases struct {
	latest     domain.Latest
	latestErr  error
	checksums  string
	installer  []byte
	openedURLs []string
}

func (releases *downloadReleases) LatestRelease(context.Context) (domain.Latest, error) {
	return releases.latest, releases.latestErr
}

func (releases *downloadReleases) Open(_ context.Context, url string) (io.ReadCloser, int64, error) {
	releases.openedURLs = append(releases.openedURLs, url)
	switch url {
	case "https://example.test/SHA256SUMS.txt":
		return io.NopCloser(strings.NewReader(releases.checksums)), int64(len(releases.checksums)), nil
	case "https://example.test/setup.exe":
		return io.NopCloser(bytes.NewReader(releases.installer)), int64(len(releases.installer)), nil
	default:
		return nil, 0, errors.New("no such asset")
	}
}

// installFixture points the service's staging directory at a temp dir instead
// of the operator's real user data, on every platform the app builds for:
// Windows reads LOCALAPPDATA with an uppercase directory name, everything
// else the XDG user config root with the lowercase one.
func installFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	directory := "provider-switchboard"
	if runtime.GOOS == "windows" {
		directory = "ProviderSwitchboard"
		t.Setenv("LOCALAPPDATA", root)
	} else {
		t.Setenv("XDG_CONFIG_HOME", root)
	}
	return filepath.Join(root, directory, "update")
}

func installerRelease(version string, payload []byte, digest string) *downloadReleases {
	return &downloadReleases{
		latest: domain.Latest{
			Version: version, URL: "https://example.test/release",
			Installer: &domain.Asset{Name: "Luxury-Switchboard-" + version + "-windows-x64-setup.exe", URL: "https://example.test/setup.exe", Size: int64(len(payload))},
			Checksums: &domain.Asset{Name: "SHA256SUMS.txt", URL: "https://example.test/SHA256SUMS.txt"},
		},
		checksums: digest + "  Luxury-Switchboard-" + version + "-windows-x64-setup.exe\n",
		installer: payload,
	}
}

// The happy path: a newer release's installer lands in the staging directory
// with the exact bytes the release's own checksum file names, and the download
// reported itself along the way.
func TestInstallParksAVerifiedInstaller(t *testing.T) {
	updateDir := installFixture(t)
	payload := bytes.Repeat([]byte("installer-bytes"), 64)
	digest := digestOf(payload)
	releases := installerRelease("v1.0.99", payload, digest)
	service := NewService("1.0.40", releases)

	var phases []string
	result, err := service.Install(context.Background(), func(report InstallProgress) {
		if report.Phase == "ready" {
			phases = append(phases, report.Phase)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Version != "v1.0.99" {
		t.Fatalf("wrong release: %+v", result)
	}
	if result.Path != filepath.Join(updateDir, "Luxury-Switchboard-v1.0.99-windows-x64-setup.exe") {
		t.Fatalf("wrong parking spot: %s", result.Path)
	}
	written, err := os.ReadFile(result.Path)
	if err != nil || !bytes.Equal(written, payload) {
		t.Fatalf("the parked installer is not the release's bytes: %v", err)
	}
	if _, err := os.Stat(result.Path + ".part"); !os.IsNotExist(err) {
		t.Fatal("the partial file survived a successful install")
	}
	if len(phases) != 1 {
		t.Fatalf("the ready phase was not reported: %v", phases)
	}
}

// A checksum mismatch is a refusal that leaves nothing behind: no half file,
// no "ready" report, no path the shell could be told to run.
func TestATamperedInstallerIsRefusedAndLeavesNothing(t *testing.T) {
	updateDir := installFixture(t)
	payload := []byte("not what the release signed")
	releases := installerRelease("v1.0.99", payload, "0000000000000000000000000000000000000000000000000000000000000000")
	service := NewService("1.0.40", releases)
	if _, err := service.Install(context.Background(), func(InstallProgress) {}); err == nil {
		t.Fatal("a tampered installer was accepted")
	}
	entries, readErr := os.ReadDir(updateDir)
	if readErr != nil || len(entries) != 0 {
		t.Fatalf("a refused install left files behind: %v %v", entries, readErr)
	}
}

// Only a newer release installs; and while one download holds the floor, a
// second waits instead of racing it over the same .part file.
func TestInstallNeedsANewerReleaseAndRefusesToRunTwice(t *testing.T) {
	installFixture(t)
	payload := []byte("installer")
	releases := installerRelease("v1.0.40", payload, "x")
	service := NewService("1.0.40", releases)
	if _, err := service.Install(context.Background(), func(InstallProgress) {}); !errors.Is(err, ErrAlreadyCurrent) {
		t.Fatalf("the current release was still installed: %v", err)
	}

	releases = installerRelease("v1.0.41", payload, digestOf(payload))
	release := make(chan struct{})
	stalled := &blockingReleases{releases: releases, release: release}
	service = NewService("1.0.40", stalled)
	first := make(chan error, 1)
	go func() {
		_, err := service.Install(context.Background(), func(InstallProgress) {})
		first <- err
	}()
	// Give the first install time to take the floor.
	time.Sleep(50 * time.Millisecond)
	second := make(chan error, 1)
	go func() {
		_, err := service.Install(context.Background(), func(InstallProgress) {})
		second <- err
	}()
	select {
	case err := <-second:
		t.Fatalf("a second install ran while the first held the floor: %v", err)
	case <-time.After(150 * time.Millisecond):
	}
	close(release)
	if err := <-first; err != nil {
		t.Fatalf("the first install did not finish: %v", err)
	}
	if err := <-second; err != nil {
		t.Fatalf("the second install did not finish after the first: %v", err)
	}
}

type blockingReleases struct {
	releases *downloadReleases
	release  chan struct{}
}

// blockingReleases stalls the installer stream until released, so the lock a
// running download holds is observable instead of hoped for.
func digestOf(payload []byte) string {
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

func (releases *blockingReleases) LatestRelease(ctx context.Context) (domain.Latest, error) {
	return releases.releases.LatestRelease(ctx)
}

func (releases *blockingReleases) Open(ctx context.Context, url string) (io.ReadCloser, int64, error) {
	// The checksum file must read through for the flow to reach the installer.
	if url == "https://example.test/SHA256SUMS.txt" {
		return releases.releases.Open(ctx, url)
	}
	<-releases.release
	return releases.releases.Open(ctx, url)
}

// A release with no installer asset for this platform says so instead of
// pretending the page is a download.
func TestInstallAdmitsWhenTheReleaseCannotSelfUpdate(t *testing.T) {
	installFixture(t)
	releases := &downloadReleases{latest: domain.Latest{Version: "v1.0.99"}}
	service := NewService("1.0.40", releases)
	if _, err := service.Install(context.Background(), func(InstallProgress) {}); !errors.Is(err, ErrNoSelfUpdate) {
		t.Fatalf("a page-only release was installed anyway: %v", err)
	}
}

// A superseded installer is pruned the moment a newer verified one lands: one
// candidate at a time is all the flow promises.
func TestASupersededInstallerIsPruned(t *testing.T) {
	updateDir := installFixture(t)
	payload := []byte("installer")
	digest := digestOf(payload)
	releases := installerRelease("v1.0.99", payload, digest)
	service := NewService("1.0.40", releases)
	first, err := service.Install(context.Background(), func(InstallProgress) {})
	if err != nil {
		t.Fatal(err)
	}
	releases = installerRelease("v1.0.100", payload, digest)
	service = NewService("1.0.40", releases)
	if _, err := service.Install(context.Background(), func(InstallProgress) {}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(first.Path); !os.IsNotExist(err) {
		t.Fatal("the older installer survived a newer verified one")
	}
	entries, _ := os.ReadDir(updateDir)
	if len(entries) != 1 {
		t.Fatalf("the update directory holds more than one candidate: %d", len(entries))
	}
}
