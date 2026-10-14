package cloudflared

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAdvertisedPlatformsHavePinnedConnectorCoverage(t *testing.T) {
	// Exact CI release target matrix, not merely whatever happens to have a pin.
	for _, platform := range []string{"windows/amd64", "windows/arm64", "linux/amd64", "linux/arm64", "darwin/amd64", "darwin/arm64"} {
		goos, goarch, _ := strings.Cut(platform, "/")
		url, checksum, err := connectorAssetFor(goos, goarch)
		if err != nil || url == "" || len(checksum) != 64 {
			t.Fatalf("advertised %s lacks pinned connector: %s %s %v", platform, url, checksum, err)
		}
		if platform == "windows/arm64" && (url != connectorURLBase+"cloudflared-windows-amd64.exe" || checksum != connectorSHA256["windows/amd64"]) {
			t.Fatal("ARM fallback differs from verified x64 pin")
		}
	}
}

func TestARMFallbackRequiresCheckedX64Emulation(t *testing.T) {
	checks := 0
	for _, test := range []struct {
		supported bool
		probeErr  error
		wantErr   bool
	}{
		{true, nil, false}, {false, nil, true}, {true, errors.New("API unavailable"), true},
	} {
		err := checkConnectorPlatform("windows", "arm64", func() (bool, error) { checks++; return test.supported, test.probeErr })
		if (err != nil) != test.wantErr {
			t.Fatalf("support=%v probeErr=%v result=%v", test.supported, test.probeErr, err)
		}
	}
	if checks != 3 {
		t.Fatal("ARM fallback did not query OS support")
	}
	if err := checkConnectorPlatform("windows", "amd64", func() (bool, error) { t.Fatal("native x64 unexpectedly probed emulation"); return false, nil }); err != nil {
		t.Fatal(err)
	}
}

func TestPinnedDownloadDoesNotInstallModifiedBytes(t *testing.T) {
	payload := []byte("offline connector fixture, never executed")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(payload) }))
	defer server.Close()
	target := filepath.Join(t.TempDir(), "connector")
	hash := sha256.Sum256(payload)
	runtime := NewRuntime(&fakeGateway{})
	if err := runtime.downloadVerified(context.Background(), server.URL, strings.Repeat("0", 64), target); err == nil {
		t.Fatal("wrong checksum accepted")
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("refused bytes installed: %v", err)
	}
	parts, _ := filepath.Glob(filepath.Join(filepath.Dir(target), "*.part"))
	if len(parts) != 0 {
		t.Fatal("refused bytes left temporary files")
	}
	if err := runtime.downloadVerified(context.Background(), server.URL, hex.EncodeToString(hash[:]), target); err != nil {
		t.Fatal(err)
	}
	installed, err := os.ReadFile(target)
	if err != nil || string(installed) != string(payload) {
		t.Fatal("verified fixture changed")
	}
}
