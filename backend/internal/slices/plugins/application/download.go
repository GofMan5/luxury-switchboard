package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/plugins/domain"
)

const (
	// MaxDownloadFileBytes bounds each file a link download reads; the
	// package rules check the script and stylesheet budgets afterwards.
	MaxDownloadFileBytes = 256 * 1024
	MaxDownloadRedirects = 5
	DownloadTimeout      = 30 * time.Second
	githubRaw            = "raw.githubusercontent.com"
	downloadUserAgent    = "LuxurySwitchboard/%s (plugin download)"
)

// DownloadError refuses or fails plugins.download. Message is about the
// owner's own link and safe to show them; Code is "invalid_link" when the
// link itself cannot be used and "download_failed" otherwise.
type DownloadError struct {
	Code    string
	Message string
}

func (err DownloadError) Error() string { return err.Message }

func badLink(message string) error { return DownloadError{"invalid_link", message} }

func failedDownload(format string, args ...any) error {
	return DownloadError{"download_failed", fmt.Sprintf(format, args...)}
}

// DownloadResult is a package fetched from a link, not yet installed: the
// shell reviews it like a pasted one. Source is where it was read from.
type DownloadResult struct {
	Package json.RawMessage `json:"package"`
	Source  string          `json:"source"`
}

// Downloader fetches a plugin package from a link the owner typed: a packed
// package file, or a source folder (plugin.json, main.js, style.css) — a
// GitHub repository, folder or file link is turned into its raw address.
// It installs nothing.
type Downloader struct {
	plugins   *Service
	transport FetchTransport
}

func NewDownloader(plugins *Service, transport FetchTransport) (*Downloader, error) {
	if plugins == nil || transport == nil {
		return nil, errors.New("plugin download needs the plugins service and a transport")
	}
	return &Downloader{plugins: plugins, transport: transport}, nil
}

// link is a resolved download address: a package file, or a folder whose
// base ends in "/".
type link struct {
	url    string
	folder bool
}

func (downloader *Downloader) Download(ctx context.Context, raw string) (DownloadResult, error) {
	target, err := resolveLink(raw)
	if err != nil {
		return DownloadResult{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, DownloadTimeout)
	defer cancel()
	var pkg json.RawMessage
	if target.folder {
		pkg, err = downloader.folder(ctx, target.url)
	} else {
		pkg, err = downloader.file(ctx, target.url)
	}
	if err != nil {
		return DownloadResult{}, err
	}
	result := DownloadResult{Package: pkg, Source: target.url}
	if ok, err := downloader.plugins.fits(result); err != nil || !ok {
		return DownloadResult{}, failedDownload("The package is larger than a plugin can be.")
	}
	return result, nil
}

func (downloader *Downloader) file(ctx context.Context, address string) (json.RawMessage, error) {
	body, found, err := downloader.get(ctx, address)
	switch {
	case err != nil:
		return nil, err
	case !found:
		return nil, failedDownload("Nothing was found at that link.")
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(body, &object) != nil || object == nil {
		return nil, failedDownload("The link does not hold a plugin package: expected a JSON object. Link to a package file or to a folder with plugin.json.")
	}
	return body, nil
}

func (downloader *Downloader) folder(ctx context.Context, base string) (json.RawMessage, error) {
	manifest, found, err := downloader.get(ctx, base+"plugin.json")
	switch {
	case err != nil:
		return nil, err
	case !found:
		return nil, failedDownload("No plugin.json was found at that link. Link to a plugin's folder, or to its package file.")
	}
	files := domain.SourceFiles{Manifest: manifest}
	for name, into := range map[string]*[]byte{"main.js": &files.Script, "style.css": &files.CSS} {
		text, found, err := downloader.get(ctx, base+name)
		if err != nil {
			return nil, err
		}
		if found {
			*into = text
		}
	}
	pkg, problems := domain.AssembleSource(files)
	if len(problems) > 0 {
		return nil, failedDownload("%s", strings.Join(problems, "\n"))
	}
	return pkg, nil
}

// get reads one file as strict UTF-8 without a byte-order mark, following
// at most MaxDownloadRedirects https redirects. found is false on 404.
func (downloader *Downloader) get(ctx context.Context, address string) (body []byte, found bool, err error) {
	name := path.Base(address)
	for range MaxDownloadRedirects + 1 {
		response, err := downloader.transport.Fetch(ctx, OutboundRequest{
			Method:  http.MethodGet,
			URL:     address,
			Headers: map[string]string{"User-Agent": fmt.Sprintf(downloadUserAgent, downloader.plugins.appVersion), "Accept": "application/json, text/plain, */*"},
		}, MaxDownloadFileBytes)
		switch {
		case errors.Is(err, ErrBlockedAddress):
			return nil, false, failedDownload("That address is private or local; plugins download only from public HTTPS addresses.")
		case errors.Is(err, ErrFetchTimeout), errors.Is(ctx.Err(), context.DeadlineExceeded):
			return nil, false, failedDownload("The download timed out.")
		case err != nil:
			return nil, false, failedDownload("The download failed; check the link and your connection.")
		}
		switch status := response.Status; {
		case status >= 300 && status < 400 && status != http.StatusNotModified:
			next, ok := redirect(address, response.Headers)
			if !ok {
				return nil, false, failedDownload("The link redirected to an address plugins cannot download from (it must be public HTTPS).")
			}
			address = next
			continue
		case status == http.StatusNotFound:
			return nil, false, nil
		case status != http.StatusOK:
			return nil, false, failedDownload("Downloading %s answered HTTP %d.", name, status)
		case len(response.Body) > MaxDownloadFileBytes:
			return nil, false, failedDownload("%s is larger than 256 KiB.", name)
		case !utf8.Valid(response.Body):
			return nil, false, failedDownload("%s is not valid UTF-8.", name)
		}
		return domain.TrimBOM(response.Body), true, nil
	}
	return nil, false, failedDownload("The link redirected too many times.")
}

func redirect(from string, headers map[string][]string) (string, bool) {
	location := http.Header(headers).Get("Location")
	base, err := url.Parse(from)
	if location == "" || err != nil {
		return "", false
	}
	next, err := base.Parse(location)
	if err != nil {
		return "", false
	}
	resolved, err := checkedURL(next.String())
	return resolved, err == nil
}

// checkedURL accepts a public https URL with no credentials and the default
// port, and drops its fragment. The transport refuses private addresses
// when it dials.
func checkedURL(raw string) (string, error) {
	if len(raw) > MaxFetchURLBytes {
		return "", badLink("The link is too long.")
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Opaque != "" || parsed.Host == "" {
		return "", badLink("That is not a link. Paste an https:// address.")
	}
	if parsed.Scheme != "https" {
		return "", badLink("Plugins download only over HTTPS.")
	}
	if parsed.User != nil {
		return "", badLink("Links may not carry a user name or password.")
	}
	if port := parsed.Port(); port != "" && port != "443" {
		return "", badLink("Links must use the standard HTTPS port.")
	}
	host := strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
	if domain.HostProblem(host) != "" {
		return "", badLink("Links must name a public host, not an address.")
	}
	parsed.Host = host
	parsed.Fragment = ""
	return parsed.String(), nil
}

// resolveLink turns what the owner typed into a download address. GitHub
// pages become raw files: a repository is its default branch's root folder,
// /tree/<ref>/<dir> a folder, /blob/<ref>/<file> that file (plugin.json
// means its folder). Release assets are fetched as they are. Elsewhere a
// link ending in "/" or plugin.json is a folder and anything else a file.
// A branch name containing "/" cannot be told from a folder in a GitHub
// page link; link to the raw file instead.
func resolveLink(raw string) (link, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return link{}, badLink("Paste a link to a plugin.")
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	checked, err := checkedURL(raw)
	if err != nil {
		return link{}, err
	}
	parsed, _ := url.Parse(checked)
	if parsed.Host == "www.github.com" {
		parsed.Host = "github.com"
	}
	if parsed.Host == "github.com" {
		return githubLink(parsed)
	}
	return byPath(parsed), nil
}

func githubLink(parsed *url.URL) (link, error) {
	segments := strings.FieldsFunc(parsed.Path, func(r rune) bool { return r == '/' })
	unusable := badLink("Use a GitHub link to a repository, a folder, a file or a release asset.")
	if len(segments) < 2 {
		return link{}, unusable
	}
	owner, repo := segments[0], strings.TrimSuffix(segments[1], ".git")
	rest := segments[2:]
	raw := func(ref string, parts ...string) *url.URL {
		return &url.URL{Scheme: "https", Host: githubRaw, Path: "/" + strings.Join(append([]string{owner, repo, ref}, parts...), "/")}
	}
	switch {
	case len(rest) == 0:
		return link{url: raw("HEAD").String() + "/", folder: true}, nil
	case rest[0] == "tree" && len(rest) >= 2:
		return link{url: strings.TrimSuffix(raw(rest[1], rest[2:]...).String(), "/") + "/", folder: true}, nil
	case (rest[0] == "blob" || rest[0] == "raw") && len(rest) >= 3:
		return byPath(raw(rest[1], rest[2:]...)), nil
	case rest[0] == "releases" && len(rest) >= 3 && slices.Contains(rest[1:len(rest)-1], "download"):
		parsed.RawQuery = ""
		return link{url: parsed.String()}, nil
	}
	return link{}, unusable
}

func byPath(parsed *url.URL) link {
	switch {
	case strings.HasSuffix(parsed.Path, "/"):
		folder := *parsed
		folder.RawQuery = ""
		return link{url: folder.String(), folder: true}
	case path.Base(parsed.Path) == "plugin.json":
		folder := *parsed
		folder.Path = path.Dir(parsed.Path) + "/"
		folder.RawPath = ""
		folder.RawQuery = ""
		return link{url: folder.String(), folder: true}
	}
	return link{url: parsed.String()}
}
