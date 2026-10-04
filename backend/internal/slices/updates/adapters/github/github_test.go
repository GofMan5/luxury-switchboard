package github

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/updates/domain"
)

// feedClient answers the latest-release endpoint from a canned asset list, so
// the client is judged on the names it picks, not on reaching GitHub.
func feedClient(t *testing.T, assetNames ...string) *Client {
	t.Helper()
	var assets strings.Builder
	for index, name := range assetNames {
		if index > 0 {
			assets.WriteString(",")
		}
		assets.WriteString(fmt.Sprintf(`{"name":%q,"browser_download_url":"https://example.test/%s","size":100}`, name, name))
	}
	body := `{"tag_name":"v1.0.45","html_url":"https://example.test/release","published_at":"2026-01-01T00:00:00Z","assets":[` + assets.String() + `]}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return &Client{feedURL: server.URL, http: &http.Client{Timeout: httpTimeout}, download: newDownloadClient()}
}

// foreignSuffix names an installer shape this running platform can never run.
func foreignSuffix() string {
	if runtime.GOOS == "windows" {
		return "-macos-arm64.dmg"
	}
	return "-windows-x64-setup.exe"
}

// A release feed carrying every shape the line builds still hands each
// platform exactly its own installer, and the checksum file alongside it.
func TestLatestReleasePicksThisPlatformsInstaller(t *testing.T) {
	client := feedClient(t,
		"SHA256SUMS.txt",
		"Luxury-Switchboard-1.0.45-windows-x64-setup.exe",
		"Luxury-Switchboard-1.0.45-windows-arm64-setup.exe",
		"Luxury-Switchboard-1.0.45-linux-x86_64.AppImage",
		"Luxury-Switchboard-1.0.45-linux-aarch64.AppImage",
		"Luxury-Switchboard-1.0.45-macos-x64.dmg",
		"Luxury-Switchboard-1.0.45-macos-arm64.dmg",
		"Luxury-Switchboard-1.0.45-linux-x86_64.deb",
		"Luxury-Switchboard-1.0.45-source.zip",
	)
	latest, err := client.LatestRelease(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	suffix := domain.InstallerSuffix(runtime.GOOS, runtime.GOARCH)
	if latest.Installer == nil {
		t.Fatalf("no installer was picked for %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	if !strings.HasSuffix(latest.Installer.Name, suffix) {
		t.Fatalf("picked %q, want this platform's %q", latest.Installer.Name, suffix)
	}
	if latest.Checksums == nil || latest.Checksums.Name != "SHA256SUMS.txt" {
		t.Fatalf("the checksum asset was not recognized: %+v", latest.Checksums)
	}
}

// A release shipping only other platforms' shapes answers with no installer:
// the honest answer the application turns into a no-self-update refusal.
func TestAReleaseWithoutThisPlatformsInstallerHasNone(t *testing.T) {
	client := feedClient(t,
		"SHA256SUMS.txt",
		"Luxury-Switchboard-1.0.45"+foreignSuffix(),
		"Luxury-Switchboard-1.0.45-linux-x86_64.deb",
	)
	latest, err := client.LatestRelease(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if latest.Installer != nil {
		t.Fatalf("a foreign installer was picked as this platform's: %s", latest.Installer.Name)
	}
}

// A download that would leave https is refused rather than followed: the
// bytes an updater fetches never ride a plaintext hop.
func TestOpenRefusesADownloadThatLeavesHttps(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "http://"+r.Host+"/final", http.StatusFound)
			return
		}
		_, _ = w.Write([]byte("bytes"))
	}))
	t.Cleanup(server.Close)
	client := &Client{feedURL: server.URL, http: &http.Client{Timeout: httpTimeout}, download: newDownloadClient()}
	if _, _, err := client.Open(context.Background(), server.URL+"/redirect"); err == nil || !strings.Contains(err.Error(), "a download redirected off https") {
		t.Fatalf("a plaintext redirect was followed: %v", err)
	}
}
