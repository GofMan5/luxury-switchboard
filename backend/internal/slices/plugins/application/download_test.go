package application

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
)

// webTransport answers by URL; an unknown URL is a 404.
type webTransport struct {
	mu    sync.Mutex
	pages map[string]InboundResponse
	asked []string
	err   error
}

func (web *webTransport) Fetch(_ context.Context, request OutboundRequest, maxBody int) (InboundResponse, error) {
	web.mu.Lock()
	defer web.mu.Unlock()
	web.asked = append(web.asked, request.URL)
	if web.err != nil {
		return InboundResponse{}, web.err
	}
	page, ok := web.pages[request.URL]
	if !ok {
		return InboundResponse{Status: 404}, nil
	}
	if len(page.Body) > maxBody+1 {
		page.Body = page.Body[:maxBody+1]
	}
	return page, nil
}

func ok(body string) InboundResponse { return InboundResponse{Status: 200, Body: []byte(body)} }

func newDownloader(t *testing.T, pages map[string]InboundResponse) (*Downloader, *webTransport) {
	t.Helper()
	service, _, _ := newService(t, &memory{}, 1<<20)
	web := &webTransport{pages: pages}
	downloader, err := NewDownloader(service, web)
	if err != nil {
		t.Fatal(err)
	}
	return downloader, web
}

func TestResolveLink(t *testing.T) {
	for typed, want := range map[string]link{
		"https://github.com/acme/tools":                                 {"https://raw.githubusercontent.com/acme/tools/HEAD/", true},
		"github.com/acme/tools.git":                                     {"https://raw.githubusercontent.com/acme/tools/HEAD/", true},
		"https://www.github.com/acme/tools/":                            {"https://raw.githubusercontent.com/acme/tools/HEAD/", true},
		"https://github.com/acme/tools/tree/main/plugins/dusk":          {"https://raw.githubusercontent.com/acme/tools/main/plugins/dusk/", true},
		"https://github.com/acme/tools/tree/v2":                         {"https://raw.githubusercontent.com/acme/tools/v2/", true},
		"https://github.com/acme/tools/blob/main/dist/dusk.json":        {"https://raw.githubusercontent.com/acme/tools/main/dist/dusk.json", false},
		"https://github.com/acme/tools/blob/main/dusk/plugin.json":      {"https://raw.githubusercontent.com/acme/tools/main/dusk/", true},
		"https://github.com/acme/tools/raw/main/dusk.json":              {"https://raw.githubusercontent.com/acme/tools/main/dusk.json", false},
		"https://github.com/acme/tools/releases/download/v1/dusk.json":  {"https://github.com/acme/tools/releases/download/v1/dusk.json", false},
		"https://github.com/acme/tools/releases/latest/download/x.json": {"https://github.com/acme/tools/releases/latest/download/x.json", false},
		"https://raw.githubusercontent.com/acme/tools/main/dusk.json":   {"https://raw.githubusercontent.com/acme/tools/main/dusk.json", false},
		"https://raw.githubusercontent.com/acme/tools/main/plugin.json": {"https://raw.githubusercontent.com/acme/tools/main/", true},
		"  https://plugins.example.com/dusk.json#readme  ":              {"https://plugins.example.com/dusk.json", false},
		"https://plugins.example.com/dusk/?ref=1":                       {"https://plugins.example.com/dusk/", true},
		"https://PLUGINS.example.com.:443/dusk.json?v=2":                {"https://plugins.example.com/dusk.json?v=2", false},
	} {
		got, err := resolveLink(typed)
		if err != nil || got != want {
			t.Errorf("resolveLink(%q) = %+v, %v; want %+v", typed, got, err, want)
		}
	}
	for _, typed := range []string{
		"", "http://github.com/acme/tools", "ftp://example.com/x.json", "https://user:pass@example.com/x.json",
		"https://example.com:8443/x.json", "https://127.0.0.1/x.json", "https://[::1]/x.json", "https://localhost/x.json",
		"https://github.com/acme", "https://github.com/acme/tools/issues/1", "https://github.com/acme/tools/tree",
		"file:///C:/x.json", "javascript:alert(1)", "https://" + strings.Repeat("a", MaxFetchURLBytes) + ".com/",
	} {
		var refused DownloadError
		if _, err := resolveLink(typed); !errors.As(err, &refused) || refused.Code != "invalid_link" {
			t.Errorf("resolveLink(%q) = %v, want invalid_link", typed, err)
		}
	}
}

func TestDownloadAssemblesAFolder(t *testing.T) {
	base := "https://raw.githubusercontent.com/acme/tools/HEAD/"
	downloader, web := newDownloader(t, map[string]InboundResponse{
		base + "plugin.json": ok("\xef\xbb\xbf" + `{"schema":1,"id":"acme.dusk","name":"Dusk","version":"1.0.0","permissions":["ui:css"]}`),
		base + "style.css":   ok(":root{}"),
	})
	result, err := downloader.Download(context.Background(), "https://github.com/acme/tools")
	if err != nil {
		t.Fatal(err)
	}
	var pkg map[string]any
	if err := json.Unmarshal(result.Package, &pkg); err != nil {
		t.Fatal(err)
	}
	if pkg["id"] != "acme.dusk" || pkg["css"] != ":root{}" || pkg["script"] != nil || result.Source != base {
		t.Fatalf("result = %s from %s", result.Package, result.Source)
	}
	if len(web.asked) != 3 || !strings.HasPrefix(web.asked[1]+web.asked[2], base) {
		t.Fatalf("asked %v", web.asked)
	}
	for _, asked := range web.asked {
		if !strings.HasPrefix(asked, base) {
			t.Fatalf("left the folder: %v", web.asked)
		}
	}
}

func TestDownloadFollowsHTTPSRedirects(t *testing.T) {
	asset := "https://github.com/acme/tools/releases/download/v1/dusk.json"
	stored := "https://objects.example.com/store/dusk.json?sig=fixture"
	downloader, _ := newDownloader(t, map[string]InboundResponse{
		asset:  {Status: 302, Headers: map[string][]string{"Location": {stored}}},
		stored: ok(`{"schema":1,"id":"acme.dusk"}`),
	})
	result, err := downloader.Download(context.Background(), asset)
	if err != nil || !strings.Contains(string(result.Package), "acme.dusk") || result.Source != asset {
		t.Fatalf("result = %s, %v", result.Package, err)
	}
	loop := "https://example.com/a.json"
	downloader, _ = newDownloader(t, map[string]InboundResponse{loop: {Status: 301, Headers: map[string][]string{"Location": {"/a.json"}}}})
	failedWith(t, downloader, loop, "redirected too many times")
	downloader, _ = newDownloader(t, map[string]InboundResponse{loop: {Status: 302, Headers: map[string][]string{"Location": {"http://example.com/a.json"}}}})
	failedWith(t, downloader, loop, "must be public HTTPS")
	downloader, _ = newDownloader(t, map[string]InboundResponse{loop: {Status: 302, Headers: map[string][]string{"Location": {"https://10.0.0.1/a.json"}}}})
	failedWith(t, downloader, loop, "must be public HTTPS")
}

func TestDownloadRefusals(t *testing.T) {
	folder := "https://example.com/dusk/"
	for name, test := range map[string]struct {
		pages map[string]InboundResponse
		link  string
		want  string
	}{
		"nothing":       {nil, "https://example.com/dusk.json", "Nothing was found"},
		"no manifest":   {nil, folder, "No plugin.json"},
		"not an object": {map[string]InboundResponse{"https://example.com/x.json": ok(`[1]`)}, "https://example.com/x.json", "expected a JSON object"},
		"html page":     {map[string]InboundResponse{"https://example.com/x": ok(`<html>`)}, "https://example.com/x", "expected a JSON object"},
		"server error":  {map[string]InboundResponse{"https://example.com/x.json": {Status: 500}}, "https://example.com/x.json", "HTTP 500"},
		"too large":     {map[string]InboundResponse{"https://example.com/x.json": ok(strings.Repeat(" ", MaxDownloadFileBytes+1))}, "https://example.com/x.json", "larger than 256 KiB"},
		"bad utf-8":     {map[string]InboundResponse{folder + "plugin.json": ok(`{}`), folder + "main.js": ok("\xff")}, folder, "main.js is not valid UTF-8"},
		"carries css":   {map[string]InboundResponse{folder + "plugin.json": ok(`{"css":"x"}`)}, folder, `may not carry "css"`},
		"manifest list": {map[string]InboundResponse{folder + "plugin.json": ok(`null`)}, folder, "must hold a JSON object"},
	} {
		t.Run(name, func(t *testing.T) {
			downloader, _ := newDownloader(t, test.pages)
			failedWith(t, downloader, test.link, test.want)
		})
	}
	downloader, web := newDownloader(t, nil)
	web.err = ErrBlockedAddress
	failedWith(t, downloader, "https://example.com/x.json", "private or local")
	web.err = ErrFetchTimeout
	failedWith(t, downloader, "https://example.com/x.json", "timed out")
	web.err = ErrNetwork
	failedWith(t, downloader, "https://example.com/x.json", "download failed")
}

func TestDownloadFitsTheFrame(t *testing.T) {
	service, _, _ := newService(t, &memory{}, 4096)
	web := &webTransport{pages: map[string]InboundResponse{"https://example.com/x.json": ok(`{"script":"` + strings.Repeat("x", 8192) + `"}`)}}
	downloader, _ := NewDownloader(service, web)
	failedWith(t, downloader, "https://example.com/x.json", "larger than a plugin")
}

func failedWith(t *testing.T, downloader *Downloader, link, want string) {
	t.Helper()
	_, err := downloader.Download(context.Background(), link)
	var failed DownloadError
	if !errors.As(err, &failed) || failed.Code != "download_failed" || !strings.Contains(failed.Message, want) {
		t.Fatalf("Download(%q) = %v, want download_failed containing %q", link, err, want)
	}
}
