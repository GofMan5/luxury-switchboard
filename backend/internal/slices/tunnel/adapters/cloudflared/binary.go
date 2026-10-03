package cloudflared

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	goruntime "runtime"
	"time"

	"github.com/luxuryprivate/switchboard/backend/internal/platform/appdata"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/tunnel/domain"
)

// The connector is pinned by version AND checksum: a tunnel is the one place
// the app runs a downloaded binary, and a silent upgrade of that binary is a
// supply-chain decision nobody made. Bumping the pin is a release note.
const (
	connectorVersion = "2026.9.3"
	connectorURLBase = "https://github.com/cloudflare/cloudflared/releases/download/" + connectorVersion + "/"
)

// connectorSHA256 pins the release artifacts, computed from the published
// binaries themselves. Lowercase, because hex.EncodeToString speaks lowercase.
var connectorSHA256 = map[string]string{
	"windows/amd64": "f096265ec2fcbe9bb6e2d64268db167ced3fcbb83d894bdb9e2fcdb26f2ea7e2",
	"linux/amd64":   "77e26d8d900e0b8469f416239d14b5f296525fdf79fee6f511ef55609e3fbac2",
}

// downloadCeiling bounds the whole download without punishing a slow line:
// the connector is ~35 MB, so ten minutes forgives ~60 KB/s and still refuses
// to hang forever on a connection that answers headers and then goes silent.
const downloadCeiling = 10 * time.Minute

// connectorHTTPClient bounds the handshake, not the download: a 35 MB binary
// on a slow line needs minutes, and a total timeout would cancel exactly the
// machines this is kindest to.
var connectorHTTPClient = &http.Client{
	Timeout: 0,
	Transport: &http.Transport{
		ResponseHeaderTimeout: 30 * time.Second,
		IdleConnTimeout:       30 * time.Second,
	},
}

func connectorFileName() string {
	if goruntime.GOOS == "windows" {
		return "cloudflared.exe"
	}
	return "cloudflared"
}

// hashFile answers whether the on-disk connector still is the pinned build.
// The binary runs the public edge, so an existing file is verified, not
// trusted for existing.
func hashFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func connectorAsset() (string, string, error) {
	platform := goruntime.GOOS + "/" + goruntime.GOARCH
	checksum, ok := connectorSHA256[platform]
	if !ok {
		return "", "", fmt.Errorf("the tunnel connector has no pinned build for %s", platform)
	}
	asset := "cloudflared-" + goruntime.GOOS + "-amd64"
	if goruntime.GOOS == "windows" {
		asset += ".exe"
	}
	return connectorURLBase + asset, checksum, nil
}

// ensureConnector returns the verified binary's path, downloading it on first
// use. The download lands beside the target and is renamed only after the
// checksum matches, so a cut connection never leaves a half binary behind.
func (runtime *Runtime) ensureConnector(ctx context.Context) (string, error) {
	root, err := appdata.Root()
	if err != nil {
		return "", err
	}
	binDir := filepath.Join(root, "bin")
	target := filepath.Join(binDir, connectorFileName())
	url, checksum, err := connectorAsset()
	if err != nil {
		return "", err
	}
	if info, err := os.Stat(target); err == nil && !info.IsDir() {
		if existing, hashErr := hashFile(target); hashErr == nil && existing == checksum {
			return target, nil
		}
		// A connector that is not the pinned build is replaced, not run.
		_ = os.Remove(target)
	}
	runtime.emit(domain.StateInstalling, "", "Downloading the tunnel connector (one-time, ~35 MB)…")
	if err := os.MkdirAll(binDir, 0o700); err != nil {
		return "", err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	downloadCtx, cancel := context.WithTimeout(ctx, downloadCeiling)
	defer cancel()
	response, err := connectorHTTPClient.Do(request.WithContext(downloadCtx))
	if err != nil {
		return "", errors.New("the tunnel connector could not be downloaded")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("the tunnel connector download answered %d", response.StatusCode)
	}
	temp, err := os.CreateTemp(binDir, "cloudflared-*.part")
	if err != nil {
		return "", err
	}
	tempPath := temp.Name()
	hasher := sha256.New()
	_, copyErr := io.Copy(io.MultiWriter(temp, hasher), io.LimitReader(response.Body, 256*1024*1024))
	closeErr := temp.Close()
	if copyErr != nil || closeErr != nil {
		_ = os.Remove(tempPath)
		return "", errors.New("the tunnel connector download was interrupted")
	}
	if got := hex.EncodeToString(hasher.Sum(nil)); got != checksum {
		_ = os.Remove(tempPath)
		return "", errors.New("the tunnel connector failed its checksum; nothing was installed")
	}
	if err := os.Chmod(tempPath, 0o700); err != nil {
		_ = os.Remove(tempPath)
		return "", err
	}
	if err := os.Rename(tempPath, target); err != nil {
		_ = os.Remove(tempPath)
		return "", err
	}
	return target, nil
}
