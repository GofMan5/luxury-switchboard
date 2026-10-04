package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/luxuryprivate/switchboard/backend/internal/platform/appdata"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/updates/domain"
)

// Releases answers "what is the newest published release" — one HTTP call away
// behind the port, so the service never names a host.
type Releases interface {
	LatestRelease(ctx context.Context) (domain.Latest, error)
	// Open downloads one asset and answers its byte stream and total.
	Open(ctx context.Context, url string) (io.ReadCloser, int64, error)
}

// InstallProgress is the report an in-flight download owes its window.
type InstallProgress struct {
	Phase    string `json:"phase"`
	Received int64  `json:"received"`
	Total    int64  `json:"total"`
	Percent  int    `json:"percent"`
}

// InstallResult is where a verified installer waits for the shell to run it.
type InstallResult struct {
	Path    string `json:"path"`
	Version string `json:"version"`
}

// CheckResult is what the shell shows: the running version, the newest one,
// and whether the question could even be asked. An unreachable release feed is
// not an error state — the app works fine without knowing.
type CheckResult struct {
	Current   string `json:"current"`
	Latest    string `json:"latest"`
	URL       string `json:"url"`
	Newer     bool   `json:"newer"`
	Reachable bool   `json:"reachable"`
	CheckedAt string `json:"checkedAt"`
}

// cacheTTL keeps one launch from asking more than once and a long session from
// asking more than twice a day: release feeds answer with rate limits, and the
// answer is not urgent enough to pay them.
const cacheTTL = 6 * time.Hour

type Service struct {
	current  string
	releases Releases
	now      func() time.Time
	mu       sync.Mutex
	cached   *CheckResult
	cachedAt time.Time
	// installMu serializes downloads: two "Update now" clicks are one
	// download, not a race over the same .part file.
	installMu sync.Mutex
}

func NewService(current string, releases Releases) *Service {
	return &Service{current: current, releases: releases, now: time.Now}
}

func (service *Service) Check(ctx context.Context) CheckResult {
	service.mu.Lock()
	if service.cached != nil && service.now().Sub(service.cachedAt) < cacheTTL {
		cached := *service.cached
		service.mu.Unlock()
		return cached
	}
	service.mu.Unlock()

	result := CheckResult{Current: service.current, CheckedAt: service.now().UTC().Format(time.RFC3339)}
	if service.releases != nil {
		if latest, err := service.releases.LatestRelease(ctx); err == nil && latest.Version != "" {
			result.Reachable = true
			result.Latest = latest.Version
			result.URL = latest.URL
			result.Newer = domain.NewerThan(latest.Version, service.current)
		}
	}
	service.mu.Lock()
	service.cached = &result
	service.cachedAt = service.now()
	service.mu.Unlock()
	return result
}

var (
	// ErrAlreadyCurrent names the non-error: there is nothing to install.
	ErrAlreadyCurrent = errors.New("this build is the newest release")
	// ErrNoSelfUpdate names the platform truth: the release ships no
	// installer this app can hand to the shell, and the release page is the
	// honest path.
	ErrNoSelfUpdate = errors.New("the release ships no self-update for this platform")
)

// Install downloads the newest release's installer, verifies it against the
// release's own checksum file, and parks it for the shell to run. Every byte
// is hashed on the way in; the file on disk is the file the release signed.
func (service *Service) Install(ctx context.Context, progress func(InstallProgress)) (InstallResult, error) {
	service.installMu.Lock()
	defer service.installMu.Unlock()

	// The cached check is a cheap early refuse, not the verdict: the feed is
	// re-read below, and the fresh answer is the one acted on.
	result := service.Check(ctx)
	if !result.Newer {
		return InstallResult{}, ErrAlreadyCurrent
	}
	if service.releases == nil {
		return InstallResult{}, ErrNoSelfUpdate
	}
	latest, err := service.releases.LatestRelease(ctx)
	if err != nil {
		return InstallResult{}, errors.New("the release feed could not be read")
	}
	// The fresh release is the release that gets installed, and it must
	// still be an upgrade: a yanked or replaced release under a cached
	// "newer" answer would otherwise download and launch a downgrade wearing
	// a "Verified" badge.
	if !domain.NewerThan(latest.Version, service.current) {
		return InstallResult{}, ErrAlreadyCurrent
	}
	if latest.Installer == nil || latest.Checksums == nil {
		return InstallResult{}, ErrNoSelfUpdate
	}
	expected, err := service.expectedChecksum(ctx, latest.Checksums, latest.Installer.Name)
	if err != nil {
		return InstallResult{}, err
	}

	root, err := appdata.Root()
	if err != nil {
		return InstallResult{}, err
	}
	directory := filepath.Join(root, "update")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return InstallResult{}, err
	}
	// The asset name comes from the project's own feed, but a name is still
	// a path component: only the basename, only this shape.
	name := filepath.Base(latest.Installer.Name)
	if strings.ContainsAny(name, `/\`) || !strings.HasSuffix(name, "-setup.exe") {
		return InstallResult{}, errors.New("the installer asset has no recognizable name")
	}
	final := filepath.Join(directory, name)
	part := final + ".part"

	body, total, err := service.releases.Open(ctx, latest.Installer.URL)
	if err != nil {
		return InstallResult{}, errors.New("the installer could not be downloaded")
	}
	defer body.Close()
	file, err := os.Create(part)
	if err != nil {
		return InstallResult{}, err
	}
	hasher := sha256.New()
	progress(InstallProgress{Phase: "downloading"})
	// The feed names the asset's size: a stream that outgrows it is lying,
	// and a lying stream does not get to fill the disk. The absolute cap
	// catches a feed that lies small.
	const absoluteCeiling = 512 * 1024 * 1024
	limit := latest.Installer.Size
	if limit <= 0 || limit > absoluteCeiling {
		limit = absoluteCeiling
	}
	var received int64
	buffer := make([]byte, 128*1024)
	for {
		read, readErr := body.Read(buffer)
		if read > 0 {
			received += int64(read)
			if received > limit {
				file.Close()
				os.Remove(part)
				return InstallResult{}, errors.New("the download outgrew the size the release named")
			}
			if _, writeErr := file.Write(buffer[:read]); writeErr != nil {
				file.Close()
				os.Remove(part)
				return InstallResult{}, errors.New("the installer could not be written")
			}
			hasher.Write(buffer[:read])
			percent := 0
			if total > 0 {
				percent = int(100 * received / total)
				if percent > 100 {
					percent = 100
				}
			}
			progress(InstallProgress{Phase: "downloading", Received: received, Total: total, Percent: percent})
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			file.Close()
			os.Remove(part)
			return InstallResult{}, errors.New("the download was interrupted")
		}
	}
	if err := file.Close(); err != nil {
		os.Remove(part)
		return InstallResult{}, errors.New("the installer could not be written")
	}

	progress(InstallProgress{Phase: "verifying", Received: received, Total: total, Percent: 100})
	if got := hex.EncodeToString(hasher.Sum(nil)); got != expected {
		os.Remove(part)
		return InstallResult{}, errors.New("the installer failed its checksum; nothing was installed")
	}
	// A second install of the same version rewrites its own verified file: on
	// Windows a rename does not replace an existing target, and the old bytes
	// are ours anyway.
	os.Remove(final)
	if err := os.Rename(part, final); err != nil {
		os.Remove(part)
		return InstallResult{}, err
	}
	// Older verified installers are leftovers of a superseded update: the
	// operator just moved on, and stale tens-of-megabytes files are not
	// evidence worth keeping.
	service.pruneSuperseded(directory, name)
	progress(InstallProgress{Phase: "ready", Received: received, Total: total, Percent: 100})
	return InstallResult{Path: final, Version: latest.Version}, nil
}

// expectedChecksum reads the release's SHA256SUMS and answers the digest named
// for this installer. A release without a line for the asset is a release
// that cannot self-update: the page is the path.
func (service *Service) expectedChecksum(ctx context.Context, checksums *domain.Asset, installerName string) (string, error) {
	body, _, err := service.releases.Open(ctx, checksums.URL)
	if err != nil {
		return "", errors.New("the checksum file could not be read")
	}
	defer body.Close()
	raw, err := io.ReadAll(io.LimitReader(body, 64*1024))
	if err != nil {
		return "", errors.New("the checksum file could not be read")
	}
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		if strings.TrimSpace(fields[1]) == installerName {
			return strings.ToLower(strings.TrimSpace(fields[0])), nil
		}
	}
	return "", errors.New("the release names no checksum for the installer")
}

// pruneSuperseded removes other verified installers, and any orphaned
// partial file a crashed download left: one candidate at a time is all the
// flow promises, and a .part nobody owns is tens of dead megabytes.
func (service *Service) pruneSuperseded(directory, keep string) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasSuffix(name, ".part") || (name != keep && strings.HasSuffix(name, "-setup.exe")) {
			os.Remove(filepath.Join(directory, name))
		}
	}
}
