package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/plugins/domain"
)

type fakeCollections struct {
	mu      sync.Mutex
	files   map[string]CollectionData
	saves   int
	deletes []string
	fail    bool
}

func (store *fakeCollections) Load(_ context.Context, id string) (CollectionData, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	return copyData(store.files[id]), nil
}

func (store *fakeCollections) Save(_ context.Context, id string, data CollectionData) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.fail {
		return errors.New("disk is full")
	}
	store.saves++
	store.files[id] = copyData(data)
	return nil
}

func (store *fakeCollections) Delete(_ context.Context, id string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.deletes = append(store.deletes, id)
	delete(store.files, id)
	return nil
}

func copyData(data CollectionData) CollectionData {
	if data == nil {
		return nil
	}
	copied := CollectionData{}
	for name, documents := range data {
		copied[name] = maps.Clone(documents)
	}
	return copied
}

func newCollections(t *testing.T) (*Service, *Collections, *fakeCollections) {
	t.Helper()
	service, _, _ := newService(t, &memory{}, 1<<20)
	if _, err := service.Install(context.Background(), pkg("acme.notes", "Notes", domain.PermissionStorage), nil, ""); err != nil {
		t.Fatal(err)
	}
	store := &fakeCollections{files: map[string]CollectionData{}}
	collections, err := NewCollections(service, store)
	if err != nil {
		t.Fatal(err)
	}
	return service, collections, store
}

func TestCollectionsStoreAndAnswer(t *testing.T) {
	service, collections, store := newCollections(t)
	ctx := context.Background()
	const id = "acme.notes"
	if doc, err := collections.Get(ctx, id, "notes", "a"); err != nil || doc != nil {
		t.Fatalf("absent document: %s %v", doc, err)
	}
	if err := collections.Put(ctx, id, "notes", "a", json.RawMessage(`{ "n": 1 }`)); err != nil {
		t.Fatal(err)
	}
	if err := collections.Put(ctx, id, "notes", "a", json.RawMessage(`{"n":1}`)); err != nil || store.saves != 1 {
		t.Fatalf("an unchanged put was saved again: %d %v", store.saves, err)
	}
	if doc, err := collections.Get(ctx, id, "notes", "a"); err != nil || string(doc) != `{"n":1}` {
		t.Fatalf("stored document: %s %v", doc, err)
	}
	for _, docID := range []string{"b", "c"} {
		if err := collections.Put(ctx, id, "notes", docID, json.RawMessage(`"x"`)); err != nil {
			t.Fatal(err)
		}
	}
	if err := collections.Put(ctx, id, "tags", "t", json.RawMessage(`true`)); err != nil {
		t.Fatal(err)
	}
	stats, err := collections.Stats(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(stats)
	if string(encoded) != `{"collections":[{"name":"notes","count":3,"bytes":40},{"name":"tags","count":1,"bytes":13}],"bytes":53,"limitBytes":2097152}` {
		t.Fatalf("stats %s", encoded)
	}
	if deleted, err := collections.Delete(ctx, id, "notes", "b"); err != nil || !deleted {
		t.Fatalf("delete: %v %v", deleted, err)
	}
	if deleted, err := collections.Delete(ctx, id, "notes", "b"); err != nil || deleted {
		t.Fatalf("delete again: %v %v", deleted, err)
	}
	if cleared, err := collections.Clear(ctx, id, "tags"); err != nil || cleared != 1 || store.files[id]["tags"] != nil {
		t.Fatalf("clear one: %d %v", cleared, err)
	}
	if cleared, err := collections.Clear(ctx, id, ""); err != nil || cleared != 2 || store.files[id] != nil {
		t.Fatalf("clear all: %d %v %v", cleared, err, store.files)
	}

	// Refusals: bad addresses, an absent plugin, and a store that fails
	// leaves memory as it was.
	for _, bad := range [][2]string{{"Notes", "a"}, {"notes", "a/b"}, {"", "a"}} {
		if err := collections.Put(ctx, id, bad[0], bad[1], json.RawMessage(`1`)); err == nil {
			t.Fatalf("address %v was accepted", bad)
		}
	}
	if err := collections.Put(ctx, id, "notes", "a", json.RawMessage(`null`)); err == nil {
		t.Fatal("a null document was stored")
	}
	if _, err := collections.Get(ctx, "acme.none", "notes", "a"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("absent plugin: %v", err)
	}
	store.fail = true
	// A disk that refuses the write is a failed save, not a full store.
	if err := collections.Put(ctx, id, "notes", "z", json.RawMessage(`1`)); err == nil || errors.Is(err, ErrStorageFull) {
		t.Fatalf("a failed save: %v", err)
	}
	if doc, _ := collections.Get(ctx, id, "notes", "z"); doc != nil {
		t.Fatal("a document the store refused was kept in memory")
	}
	store.fail = false

	// Removing the plugin deletes its file.
	if err := collections.Put(ctx, id, "notes", "a", json.RawMessage(`1`)); err != nil {
		t.Fatal(err)
	}
	if err := service.Remove(ctx, id); err != nil {
		t.Fatal(err)
	}
	if store.files[id] != nil || store.deletes[len(store.deletes)-1] != id {
		t.Fatalf("the data outlived the plugin: %v", store.files)
	}
	if _, loaded := collections.loaded[id]; loaded {
		t.Fatal("the data stayed in memory")
	}
}

func TestCollectionLimits(t *testing.T) {
	_, collections, _ := newCollections(t)
	ctx := context.Background()
	const id = "acme.notes"
	for index := range domain.MaxCollections {
		if err := collections.Put(ctx, id, fmt.Sprintf("c%d", index), "a", json.RawMessage(`1`)); err != nil {
			t.Fatal(err)
		}
	}
	if err := collections.Put(ctx, id, "one-more", "a", json.RawMessage(`1`)); !errors.Is(err, ErrStorageFull) {
		t.Fatalf("a 17th collection: %v", err)
	}
	if err := collections.Put(ctx, id, "c0", "b", json.RawMessage(`1`)); err != nil {
		t.Fatalf("a document in an existing collection: %v", err)
	}
	if _, err := collections.Clear(ctx, id, ""); err != nil {
		t.Fatal(err)
	}

	big := json.RawMessage(`"` + strings.Repeat("x", domain.MaxDocumentBytes-3) + `"`)
	if err := collections.Put(ctx, id, "big", "over", json.RawMessage(`"`+strings.Repeat("x", domain.MaxDocumentBytes)+`"`)); err == nil {
		t.Fatal("an oversized document was stored")
	}
	collections.writes.now = func() time.Time { return time.Unix(0, 0) }
	filled := 0
	for ; ; filled++ {
		err := collections.Put(ctx, id, "big", fmt.Sprintf("d%03d", filled), big)
		if errors.Is(err, ErrStorageFull) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	stats, _ := collections.Stats(ctx, id)
	if filled != domain.MaxCollectionBytes/domain.DocumentCost("d000", big) || stats.Bytes > domain.MaxCollectionBytes {
		t.Fatalf("filled %d documents, %d bytes", filled, stats.Bytes)
	}

	// The write limit: the 301st write in a minute is refused, reads are not.
	_, collections, _ = newCollections(t)
	now := time.Unix(0, 0)
	collections.writes.now = func() time.Time { return now }
	for index := range CollectionWritesPerMinute {
		if err := collections.Put(ctx, id, "notes", "a", json.RawMessage(fmt.Sprint(index))); err != nil {
			t.Fatal(err)
		}
	}
	if err := collections.Put(ctx, id, "notes", "a", json.RawMessage(`1`)); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("write %d: %v", CollectionWritesPerMinute+1, err)
	}
	if _, err := collections.Get(ctx, id, "notes", "a"); err != nil {
		t.Fatalf("a read under the write limit: %v", err)
	}
	now = now.Add(time.Minute)
	if err := collections.Put(ctx, id, "notes", "a", json.RawMessage(`1`)); err != nil {
		t.Fatalf("after a minute: %v", err)
	}
}

func TestCollectionListPages(t *testing.T) {
	_, collections, _ := newCollections(t)
	ctx := context.Background()
	const id = "acme.notes"
	for _, docID := range []string{"a1", "a2", "a3", "b1", "b2"} {
		if err := collections.Put(ctx, id, "notes", docID, json.RawMessage(`{"id":"`+docID+`"}`)); err != nil {
			t.Fatal(err)
		}
	}
	ids := func(list DocumentList) string {
		var names []string
		for _, item := range list.Items {
			names = append(names, item.ID)
		}
		return strings.Join(names, ",") + "|" + list.Next
	}
	for _, test := range []struct {
		prefix, after string
		limit         int
		want          string
	}{
		{"", "", 0, "a1,a2,a3,b1,b2|"},
		{"", "", 2, "a1,a2|a2"},
		{"", "a2", 2, "a3,b1|b1"},
		{"", "b1", 2, "b2|"},
		{"a", "", 0, "a1,a2,a3|"},
		{"b", "a3", 1, "b1|b1"},
		{"c", "", 0, "|"},
	} {
		list, err := collections.List(ctx, id, "notes", test.prefix, test.after, test.limit)
		if err != nil || ids(list) != test.want {
			t.Errorf("list(%q, %q, %d) = %s %v, want %s", test.prefix, test.after, test.limit, ids(list), err, test.want)
		}
	}
	if list, _ := collections.List(ctx, id, "empty", "", "", 0); list.Items == nil {
		t.Fatal("an empty list must encode as []")
	}
	for _, limit := range []int{-1, MaxCollectionList + 1} {
		if _, err := collections.List(ctx, id, "notes", "", "", limit); err == nil {
			t.Fatalf("limit %d was accepted", limit)
		}
	}

	// A page stops at the response budget even under its limit.
	service, _, _ := newService(t, &memory{}, 4096)
	if _, err := service.Install(ctx, pkg(id, "Notes", domain.PermissionStorage), nil, ""); err != nil {
		t.Fatal(err)
	}
	small, err := NewCollections(service, &fakeCollections{files: map[string]CollectionData{}})
	if err != nil {
		t.Fatal(err)
	}
	doc := json.RawMessage(`"` + strings.Repeat("x", 1000) + `"`)
	for index := range 8 {
		if err := small.Put(ctx, id, "notes", fmt.Sprint(index), doc); err != nil {
			t.Fatal(err)
		}
	}
	list, err := small.List(ctx, id, "notes", "", "", 0)
	if err != nil || len(list.Items) != 3 || list.Next != "2" {
		t.Fatalf("a budgeted page: %d items, next %q, %v", len(list.Items), list.Next, err)
	}
	encoded, _ := json.Marshal(list)
	if len(encoded) > 4096 {
		t.Fatalf("the page is %d bytes", len(encoded))
	}
}
