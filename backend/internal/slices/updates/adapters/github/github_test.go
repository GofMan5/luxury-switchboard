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

// feedBody is one full release answer every conditional test below starts
// from: a tag, a page, and this platform's installer.
func feedBody() string {
	return `{"tag_name":"v1.0.45","html_url":"https://example.test/release","published_at":"2026-01-01T00:00:00Z","assets":[{"name":"SHA256SUMS.txt","browser_download_url":"https://example.test/SHA256SUMS.txt","size":100},{"name":"Luxury-Switchboard-1.0.45` + domain.InstallerSuffix(runtime.GOOS, runtime.GOARCH) + `","browser_download_url":"https://example.test/setup","size":100}]}`
}

// The second ask carries the etag of the first and honours a 304 by meaning
// the answer it already has. Minute-cadence checking is affordable only
// because the steady state is exactly this: no body, no parsing, one round
// trip.
func TestASecondAskIsConditionalAndHonoursNotModified(t *testing.T) {
	const etag = `"v1"`
	var hits int
	var seenConditional string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.Header.Get("If-None-Match") == etag {
			seenConditional = etag
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", etag)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(feedBody()))
	}))
	t.Cleanup(server.Close)
	client := &Client{feedURL: server.URL, http: &http.Client{Timeout: httpTimeout}, download: newDownloadClient()}

	first, err := client.LatestRelease(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if first.Version != "v1.0.45" {
		t.Fatalf("first ask answered %q, want v1.0.45", first.Version)
	}
	second, err := client.LatestRelease(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if second.Version != "v1.0.45" || second.URL != first.URL {
		t.Fatalf("the 304 answer was not the cached one: %+v vs %+v", second, first)
	}
	if hits != 2 {
		t.Fatalf("expected exactly two feed hits, saw %d", hits)
	}
	if seenConditional != etag {
		t.Fatal("the second ask never carried the etag it was taught")
	}
}

// A 304 with nothing to mean is refused rather than repeated as a valid
// empty answer: the interface must not turn "you should know this" into "there
// is nothing".
func TestANotModifiedWithoutAnAnswerIsAnError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotModified)
	}))
	t.Cleanup(server.Close)
	client := &Client{feedURL: server.URL, http: &http.Client{Timeout: httpTimeout}, download: newDownloadClient()}
	if _, err := client.LatestRelease(context.Background()); err == nil || !strings.Contains(err.Error(), "not-modified without ever answering") {
		t.Fatalf("a bodyless 304 became a valid answer: %v", err)
	}
}

// A feed that fails after a success does not spend what it taught: the etag
// survives a 404, and the next 304 still means the cached answer. One bad
// minute must not re-download release metadata forever.
func TestTheEtagSurvivesAFailedAsk(t *testing.T) {
	const etag = `"v1"`
	var hits int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		switch {
		case r.Header.Get("If-None-Match") == etag && hits == 3:
			w.WriteHeader(http.StatusNotModified)
		case r.Header.Get("If-None-Match") == etag:
			w.WriteHeader(http.StatusNotFound)
		default:
			w.Header().Set("ETag", etag)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(feedBody()))
		}
	}))
	t.Cleanup(server.Close)
	client := &Client{feedURL: server.URL, http: &http.Client{Timeout: httpTimeout}, download: newDownloadClient()}

	if _, err := client.LatestRelease(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, err := client.LatestRelease(context.Background())
	if err == nil || !strings.Contains(err.Error(), "release feed answered 404") {
		t.Fatalf("the 404 was not reported: %v", err)
	}
	third, err := client.LatestRelease(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if third.Version != "v1.0.45" {
		t.Fatalf("the etag did not survive the 404: %+v", third)
	}
}

// A transport failure keeps the etag for the same reason: it learned nothing
// about the answer it guards. Checked directly under the mutex because from
// outside the only symptom would be the next full download.
func TestATransportFailureKeepsTheEtag(t *testing.T) {
	const etag = `"v1"`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("ETag", etag)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(feedBody()))
	}))
	client := &Client{feedURL: server.URL, http: &http.Client{Timeout: httpTimeout}, download: newDownloadClient()}
	if _, err := client.LatestRelease(context.Background()); err != nil {
		t.Fatal(err)
	}
	server.Close()
	if _, err := client.LatestRelease(context.Background()); err == nil {
		t.Fatal("the transport failure was not reported")
	}
	client.conditionalMu.Lock()
	stored := client.etag
	client.conditionalMu.Unlock()
	if stored != etag {
		t.Fatalf("the etag was lost to a transport failure: %q", stored)
	}
}
