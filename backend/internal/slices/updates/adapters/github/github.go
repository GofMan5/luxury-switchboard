// Package github answers the update question from the project's own release
// feed. The coordinates are the product's home, not configuration: one named
// place, changed when the repository moves.
package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/updates/domain"
)

const (
	// The project's public home. Update checks read only the latest-release
	// endpoint; nothing about the operator's setup is ever sent.
	releasesURL = "https://api.github.com/repos/GofMan5/swap-provider-url/releases/latest"
	httpTimeout = 6 * time.Second
)

type Client struct {
	http *http.Client
}

func NewClient() *Client {
	return &Client{http: &http.Client{Timeout: httpTimeout}}
}

type releasePayload struct {
	TagName     string `json:"tag_name"`
	HTMLURL     string `json:"html_url"`
	PublishedAt string `json:"published_at"`
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
	if err := json.NewDecoder(http.MaxBytesReader(nil, response.Body, 64*1024)).Decode(&payload); err != nil {
		return domain.Latest{}, errors.New("release feed answered unreadably")
	}
	return domain.Latest{Version: payload.TagName, URL: payload.HTMLURL, PublishedAt: payload.PublishedAt}, nil
}
