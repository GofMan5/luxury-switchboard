package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/plugins/domain"
)

// fakeFolders answers Pack from what each folder is set to hold.
type fakeFolders struct {
	mu      sync.Mutex
	folders map[string]folder
	packs   int
}

type folder struct {
	raw      json.RawMessage
	problems []string
}

func (source *fakeFolders) Pack(path string) (json.RawMessage, []string) {
	source.mu.Lock()
	defer source.mu.Unlock()
	source.packs++
	held, exists := source.folders[path]
	if !exists {
		return nil, []string{"plugin.json was not found in the folder."}
	}
	return held.raw, held.problems
}

func (source *fakeFolders) set(path string, value any, problems ...string) {
	raw, _ := json.Marshal(value)
	source.mu.Lock()
	defer source.mu.Unlock()
	source.folders[path] = folder{raw: raw, problems: problems}
}

func devPath(name string) string {
	path, _ := filepath.Abs(filepath.Join(string(filepath.Separator), "dev", name))
	return path
}

func newDevLinks(t *testing.T) (*Service, *DevLinks, *fakeFolders, *[][]Summary, *memory) {
	t.Helper()
	store := &memory{}
	service, _, notifications := newService(t, store, 1<<20)
	source := &fakeFolders{folders: map[string]folder{}}
	links, err := NewDevLinks(service, source)
	if err != nil {
		t.Fatal(err)
	}
	return service, links, source, notifications, store
}

func TestDevLinkLoadsAndFollowsTheFolder(t *testing.T) {
	service, links, source, notifications, store := newDevLinks(t)
	ctx := context.Background()
	path := devPath("dusk")
	first := pkg("acme.dusk", "Dusk", domain.PermissionTheme, domain.PermissionStorage)
	source.set(path, first)

	summary, err := links.Link(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if !summary.Enabled || len(summary.Granted) != 0 || summary.Dev == nil || summary.Dev.Path != path || summary.Dev.Problem != "" {
		t.Fatalf("linked %+v", summary)
	}
	if store.plugins[0].Dev == nil || store.plugins[0].Dev.Path != path {
		t.Fatal("the link was not persisted")
	}
	granted := []string{domain.PermissionStorage}
	if _, err := service.Update(ctx, "acme.dusk", nil, &granted); err != nil {
		t.Fatal(err)
	}

	// Unchanged folder: no save, no announcement.
	saves, announced := store.saves, len(*notifications)
	links.poll(ctx)
	if store.saves != saves || len(*notifications) != announced {
		t.Fatal("an unchanged folder was saved again")
	}

	// A change reloads; the new permission arrives ungranted, the granted
	// one stays.
	next := pkg("acme.dusk", "Dusk", domain.PermissionTheme, domain.PermissionStorage, domain.PermissionNetFetch)
	next.Version = "1.1.0"
	next.Network = &domain.Network{Hosts: []string{"api.example.com"}}
	source.set(path, next)
	links.poll(ctx)
	detail, err := service.Get("acme.dusk")
	if err != nil {
		t.Fatal(err)
	}
	if detail.Plugin.Version != "1.1.0" || !slices.Equal(detail.Plugin.Granted, granted) || detail.Plugin.Dev.Problem != "" || !detail.Plugin.Dev.LoadedAt.After(summary.Dev.LoadedAt) {
		t.Fatalf("reloaded %+v %+v", detail.Plugin, detail.Plugin.Dev)
	}
	if len(*notifications) != announced+1 {
		t.Fatal("a reload was not announced")
	}

	// net:fetch survives a reload with the same hosts, not one that adds a
	// host the owner never reviewed.
	fetching := []string{domain.PermissionStorage, domain.PermissionNetFetch}
	if _, err := service.Update(ctx, "acme.dusk", nil, &fetching); err != nil {
		t.Fatal(err)
	}
	same := next
	same.Description = "same hosts"
	source.set(path, same)
	links.poll(ctx)
	if detail, _ = service.Get("acme.dusk"); !slices.Equal(detail.Plugin.Granted, fetching) {
		t.Fatalf("same hosts dropped a grant: %v", detail.Plugin.Granted)
	}
	wider := next
	wider.Network = &domain.Network{Hosts: []string{"api.example.com", "collect.example.net"}}
	source.set(path, wider)
	links.poll(ctx)
	if detail, _ = service.Get("acme.dusk"); !slices.Equal(detail.Plugin.Granted, granted) {
		t.Fatalf("a new host kept net:fetch: %v", detail.Plugin.Granted)
	}
	source.set(path, next)
	links.poll(ctx)
	announced = len(*notifications)

	// A problem keeps the last good version and reports it once.
	broken := next
	broken.Version = "1.2.0"
	broken.Permissions = append(slices.Clone(next.Permissions), "made:up")
	source.set(path, broken)
	links.poll(ctx)
	links.poll(ctx)
	detail, _ = service.Get("acme.dusk")
	if detail.Plugin.Version != "1.1.0" || !strings.Contains(detail.Plugin.Dev.Problem, "unknown permission") || len(*notifications) != announced+1 {
		t.Fatalf("after a problem: %s %q, %d announcements", detail.Plugin.Version, detail.Plugin.Dev.Problem, len(*notifications)-announced)
	}
	source.set(path, next, "style.css could not be read.")
	links.poll(ctx)
	if detail, _ = service.Get("acme.dusk"); detail.Plugin.Dev.Problem != "style.css could not be read." {
		t.Fatalf("a source problem: %q", detail.Plugin.Dev.Problem)
	}

	// Fixed: the problem clears even though the package is the last good one.
	source.set(path, next)
	links.poll(ctx)
	if detail, _ = service.Get("acme.dusk"); detail.Plugin.Dev.Problem != "" || detail.Plugin.Version != "1.1.0" {
		t.Fatalf("after the fix: %q", detail.Plugin.Dev.Problem)
	}

	// A folder that renames the plugin is a problem, not a second plugin.
	renamed := next
	renamed.ID = "acme.other"
	source.set(path, renamed)
	links.poll(ctx)
	list, _ := service.List()
	if detail, _ = service.Get("acme.dusk"); len(list) != 1 || !strings.Contains(detail.Plugin.Dev.Problem, "acme.other") {
		t.Fatalf("a renamed folder: %d plugins, %q", len(list), detail.Plugin.Dev.Problem)
	}

	// Unlink keeps the last version and stops following the folder.
	unlinked, err := links.Unlink(ctx, "acme.dusk")
	if err != nil || unlinked.Dev != nil || unlinked.Version != "1.1.0" {
		t.Fatalf("unlinked %+v %v", unlinked, err)
	}
	packs := source.packs
	links.poll(ctx)
	if source.packs != packs {
		t.Fatal("an unlinked folder was read")
	}
	if _, err := links.Unlink(ctx, "acme.dusk"); !errors.Is(err, ErrNotLinked) {
		t.Fatalf("unlink twice: %v", err)
	}
	if _, err := links.Unlink(ctx, "acme.none"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unlink absent: %v", err)
	}
}

func TestDevLinkRefusals(t *testing.T) {
	service, links, source, _, _ := newDevLinks(t)
	ctx := context.Background()
	var problem DevProblemError
	if _, err := links.Link(ctx, "relative/dir"); err == nil {
		t.Fatal("a relative path was linked")
	}
	if _, err := links.Link(ctx, devPath("missing")); !errors.As(err, &problem) || !strings.Contains(problem.Problem, "plugin.json") {
		t.Fatalf("a missing folder: %v", err)
	}
	source.set(devPath("unknown"), map[string]any{"schema": 1, "id": "acme.x", "surprise": true})
	if _, err := links.Link(ctx, devPath("unknown")); !errors.As(err, &problem) || !strings.Contains(problem.Problem, "surprise") {
		t.Fatalf("an unknown field: %v", err)
	}
	newer := pkg("acme.newer", "Newer", domain.PermissionTheme)
	newer.MinAppVersion = "9.0.0"
	source.set(devPath("newer"), newer)
	if _, err := links.Link(ctx, devPath("newer")); !errors.As(err, &problem) || !strings.Contains(problem.Problem, "9.0.0") {
		t.Fatalf("a newer plugin: %v", err)
	}

	for index := range domain.MaxDevLinks {
		path := devPath(fmt.Sprint(index))
		source.set(path, pkg(fmt.Sprintf("acme.p%d", index), "P", domain.PermissionTheme))
		if _, err := links.Link(ctx, path); err != nil {
			t.Fatal(err)
		}
	}
	source.set(devPath("same"), pkg("acme.p9", "P", domain.PermissionTheme))
	if _, err := links.Link(ctx, devPath("same")); !errors.Is(err, ErrDevLimit) {
		t.Fatalf("a fifth link: %v", err)
	}
	source.set(devPath("0"), pkg("acme.p9", "P", domain.PermissionTheme))
	if _, err := links.Link(ctx, devPath("0")); !errors.Is(err, ErrDevLinked) {
		t.Fatalf("a folder linked twice: %v", err)
	}
	// Relinking the same plugin is not counted against the limit.
	source.set(devPath("0"), pkg("acme.p0", "P", domain.PermissionTheme))
	if _, err := links.Link(ctx, devPath("0")); err != nil {
		t.Fatalf("relink: %v", err)
	}
	// A manual install over a linked plugin ends the link.
	summary, err := service.Install(ctx, pkg("acme.p1", "P", domain.PermissionTheme), nil)
	if err != nil || summary.Dev != nil {
		t.Fatalf("install over a link: %+v %v", summary.Dev, err)
	}
}

func TestDevWatchStopsWithItsContext(t *testing.T) {
	_, links, _, _, _ := newDevLinks(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		links.Watch(ctx)
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Watch did not stop")
	}
}
