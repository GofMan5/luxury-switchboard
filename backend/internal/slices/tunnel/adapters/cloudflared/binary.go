package cloudflared

import (
	"archive/tar"
	"compress/gzip"
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
	"strings"
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
// artifacts themselves. Lowercase, because hex.EncodeToString speaks
// lowercase. darwin ships as .tgz archives; the pin covers the archive bytes.
var connectorSHA256 = map[string]string{
	"windows/amd64": "f096265ec2fcbe9bb6e2d64268db167ced3fcbb83d894bdb9e2fcdb26f2ea7e2",
	"linux/amd64":   "77e26d8d900e0b8469f416239d14b5f296525fdf79fee6f511ef55609e3fbac2",
	"linux/arm64":   "aaeb2d7d0da3614634c7e03ab13487a1522c2e79165ed2929cfe23d5e95b326d",
	"darwin/amd64":  "d1155d0837487f261183b15c1eab6c4ebcad9dc49b94675f1524c3564cea3977",
	"darwin/arm64":  "587c2cfb1c230fe36c7fa7727da78be459dae028cabe8c001291999350f07095",
}

// downloadCeiling bounds the whole download without punishing a slow line:
// the connector is ~35 MB, so ten minutes forgives ~60 KB/s and still refuses
// to hang forever on a connection that answers headers and then goes silent.
const downloadCeiling = 10 * time.Minute

// maxConnectorBytes bounds the artifact and the member extracted from it.
const maxConnectorBytes = 256 * 1024 * 1024

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

// assetNameFor answers the release artifact this platform downloads: raw
// binaries everywhere except darwin, whose releases are .tgz archives.
func assetNameFor(goos, goarch string) string {
	if goos == "darwin" {
		return "cloudflared-darwin-" + goarch + ".tgz"
	}
	asset := "cloudflared-" + goos + "-" + goarch
	if goos == "windows" {
		asset += ".exe"
	}
	return asset
}

// connectorAssetFor answers the pinned download for a platform. Split from
// connectorAsset so the naming is testable on every host.
func connectorAssetFor(goos, goarch string) (url string, checksum string, err error) {
	checksum, ok := connectorSHA256[goos+"/"+goarch]
	if !ok {
		return "", "", fmt.Errorf("the tunnel connector has no pinned build for %s/%s", goos, goarch)
	}
	return connectorURLBase + assetNameFor(goos, goarch), checksum, nil
}

func connectorAsset() (string, string, error) {
	return connectorAssetFor(goruntime.GOOS, goruntime.GOARCH)
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

// ensureConnector returns the verified binary's path, downloading it on first
// use. The download lands beside the target and is renamed only after the
// checksum matches, so a cut connection never leaves a half binary behind.
// On darwin the pinned artifact is the release's .tgz; the runnable binary is
// extracted from the verified archive.
func (runtime *Runtime) ensureConnector(ctx context.Context) (string, error) {
	root, err := appdata.Root()
	if err != nil {
		return "", err
	}
	binDir := filepath.Join(root, "bin")
	runnable := filepath.Join(binDir, connectorFileName())
	url, checksum, err := connectorAsset()
	if err != nil {
		return "", err
	}

	if goruntime.GOOS == "darwin" {
		return runtime.ensureDarwinConnector(ctx, binDir, runnable, url, checksum)
	}

	if info, err := os.Stat(runnable); err == nil && !info.IsDir() {
		if existing, hashErr := hashFile(runnable); hashErr == nil && existing == checksum {
			return runnable, nil
		}
		// A connector that is not the pinned build is replaced, not run.
		_ = os.Remove(runnable)
	}
	runtime.emit(domain.StateInstalling, "", "Downloading the tunnel connector (one-time, ~35 MB)…")
	if err := runtime.downloadVerified(ctx, url, checksum, runnable); err != nil {
		return "", err
	}
	return runnable, nil
}

// ensureDarwinConnector keeps the verified .tgz beside the binary it yields.
// The pin covers the archive; the executable is extracted from it, so the
// artifact on disk is always the bytes the release signed.
func (runtime *Runtime) ensureDarwinConnector(ctx context.Context, binDir, runnable, url, checksum string) (string, error) {
	archive := filepath.Join(binDir, "cloudflared.tgz")
	if info, err := os.Stat(archive); err == nil && !info.IsDir() {
		if existing, hashErr := hashFile(archive); hashErr != nil && existing != checksum {
			_ = os.Remove(archive)
		} else if existing == checksum {
			if _, statErr := os.Stat(runnable); statErr == nil {
				return runnable, nil
			}
			if err := extractConnector(archive, runnable); err != nil {
				return "", err
			}
			return runnable, nil
		}
	}
	runtime.emit(domain.StateInstalling, "", "Downloading the tunnel connector (one-time, ~35 MB)…")
	if err := runtime.downloadVerified(ctx, url, checksum, archive); err != nil {
		return "", err
	}
	if err := extractConnector(archive, runnable); err != nil {
		return "", err
	}
	return runnable, nil
}

// extractConnector pulls the single "cloudflared" member out of the verified
// archive. The member name is matched exactly: an archive that ships paths,
// links or anything else than the one binary is refused rather than trusted.
func extractConnector(archive, runnable string) error {
	file, err := os.Open(archive)
	if err != nil {
		return errors.New("the tunnel connector archive could not be read")
	}
	defer file.Close()
	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		return errors.New("the tunnel connector archive could not be read")
	}
	defer gzipReader.Close()
	tarReader := tar.NewReader(gzipReader)
	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			return errors.New("the tunnel connector archive carries no binary")
		}
		if err != nil {
			return errors.New("the tunnel connector archive could not be read")
		}
		if header.Typeflag != tar.TypeReg || filepath.Base(header.Name) != "cloudflared" || strings.ContainsAny(header.Name, `/\`) {
			continue
		}
		if header.Size <= 0 || header.Size > maxConnectorBytes {
			return errors.New("the tunnel connector archive carries an implausible binary")
		}
		temp, err := os.CreateTemp(filepath.Dir(runnable), "cloudflared-*.part")
		if err != nil {
			return err
		}
		tempPath := temp.Name()
		_, copyErr := io.Copy(temp, io.LimitReader(tarReader, maxConnectorBytes))
		closeErr := temp.Close()
		if copyErr != nil || closeErr != nil {
			_ = os.Remove(tempPath)
			return errors.New("the tunnel connector could not be extracted")
		}
		if err := os.Chmod(tempPath, 0o700); err != nil {
			_ = os.Remove(tempPath)
			return err
		}
		if err := os.Rename(tempPath, runnable); err != nil {
			_ = os.Remove(tempPath)
			return err
		}
		return nil
	}
}

// downloadVerified moves one pinned artifact to its final path, hashing as it
// lands: the file that survives is the file the release signed.
func (runtime *Runtime) downloadVerified(ctx context.Context, url, checksum, target string) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	downloadCtx, cancel := context.WithTimeout(ctx, downloadCeiling)
	defer cancel()
	response, err := connectorHTTPClient.Do(request.WithContext(downloadCtx))
	if err != nil {
		return errors.New("the tunnel connector could not be downloaded")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("the tunnel connector download answered %d", response.StatusCode)
	}
	temp, err := os.CreateTemp(filepath.Dir(target), "cloudflared-*.part")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	hasher := sha256.New()
	_, copyErr := io.Copy(io.MultiWriter(temp, hasher), io.LimitReader(response.Body, maxConnectorBytes))
	closeErr := temp.Close()
	if copyErr != nil || closeErr != nil {
		_ = os.Remove(tempPath)
		return errors.New("the tunnel connector download was interrupted")
	}
	if got := hex.EncodeToString(hasher.Sum(nil)); got != checksum {
		_ = os.Remove(tempPath)
		return errors.New("the tunnel connector failed its checksum; nothing was installed")
	}
	if err := os.Chmod(tempPath, 0o700); err != nil {
		_ = os.Remove(tempPath)
		return err
	}
	if err := os.Rename(tempPath, target); err != nil {
		_ = os.Remove(tempPath)
		return err
	}
	return nil
}
