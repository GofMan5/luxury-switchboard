// Package github answers the update question from the project's own release
// feed. The coordinates are the product's home, not configuration: one named
// place, changed when the repository moves.
package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"strings"
	"time"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/updates/domain"
)

const (
	// The project's public home. Update checks read only the latest-release
	// endpoint; nothing about the operator's setup is ever sent.
	releasesURL = "https://api.github.com/repos/GofMan5/luxury-switchboard/releases/latest"
	httpTimeout = 6 * time.Second
	// downloadTimeout bounds the whole installer download. The artifact is
	// tens of megabytes; a slow line needs minutes, and a stalled one needs
	// the ceiling, not a hang.
	downloadTimeout = 15 * time.Minute
)

type Client struct {
	http *http.Client
}

func NewClient() *Client {
	return &Client{http: &http.Client{Timeout: httpTimeout}}
}

type assetPayload struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
	Size int64  `json:"size"`
}

type releasePayload struct {
	TagName     string         `json:"tag_name"`
	HTMLURL     string         `json:"html_url"`
	PublishedAt string         `json:"published_at"`
	Assets      []assetPayload `json:"assets"`
}

// installerSuffix is the asset this platform self-updates through. Windows
// ships an NSIS setup the app can hand to the shell; on other platforms the
// release page stays the honest path and the installer asset reads as absent.
func installerSuffix() string {
	if runtime.GOOS == "windows" {
		return "-windows-x64-setup.exe"
	}
	return ""
}

func (client *Client) LatestRelease(ctx context.Context) (domain.Latest, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, releasesURL, nil)
	if err != nil {
		return domain.Latest{}, err
	}
	// GitHub refuses requests without a User-Agent; the version string keeps
	// the feed honest about who asks.
	request.Header.Set("User-Agent", "luxury-switchboard-update-check")
	request.Header.Set("Accept", "application/vnd.github+json")
	response, err := client.http.Do(request)
	if err != nil {
		return domain.Latest{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		// 404 means no release was ever published: not an error worth surfacing,
		// just nothing to tell.
		return domain.Latest{}, fmt.Errorf("release feed answered %d", response.StatusCode)
	}
	var payload releasePayload
	if err := json.NewDecoder(http.MaxBytesReader(nil, response.Body, 512*1024)).Decode(&payload); err != nil {
		return domain.Latest{}, errors.New("release feed answered unreadably")
	}
	latest := domain.Latest{Version: payload.TagName, URL: payload.HTMLURL, PublishedAt: payload.PublishedAt}
	suffix := installerSuffix()
	for _, asset := range payload.Assets {
		switch {
		case asset.Name == "SHA256SUMS.txt":
			latest.Checksums = &domain.Asset{Name: asset.Name, URL: asset.URL, Size: asset.Size}
		case suffix != "" && strings.HasSuffix(asset.Name, suffix):
			latest.Installer = &domain.Asset{Name: asset.Name, URL: asset.URL, Size: asset.Size}
		}
	}
	return latest, nil
}

// Open downloads one asset of the release. The caller reads, hashes and
// reports; this side only moves bytes and names the total.
func (client *Client) Open(ctx context.Context, url string) (io.ReadCloser, int64, error) {
	downloadCtx, cancel := context.WithTimeout(ctx, downloadTimeout)
	request, err := http.NewRequestWithContext(downloadCtx, http.MethodGet, url, nil)
	if err != nil {
		cancel()
		return nil, 0, err
	}
	request.Header.Set("User-Agent", "luxury-switchboard-update-check")
	response, err := client.http.Do(request)
	if err != nil {
		cancel()
		return nil, 0, err
	}
	if response.StatusCode != http.StatusOK {
		response.Body.Close()
		cancel()
		return nil, 0, fmt.Errorf("the download answered %d", response.StatusCode)
	}
	// The context is closed when the caller finishes the body.
	return &cancelingBody{ReadCloser: response.Body, cancel: cancel}, response.ContentLength, nil
}

// cancelingBody releases the request context when the stream ends, so the
// download's own timeout does not outlive the file it was guarding.
type cancelingBody struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (body *cancelingBody) Close() error {
	err := body.ReadCloser.Close()
	body.cancel()
	return err
}
