package application

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"strings"
	"sync"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/plugins/domain"
)

const (
	CollectionWritesPerMinute = 300
	DefaultCollectionList     = 50
	MaxCollectionList         = 200
)

// ErrRateLimited refuses a plugin that wrote too often within a minute.
var ErrRateLimited = errors.New("plugin is writing too often")

// CollectionData is one plugin's collections: name to document id to the
// stored document.
type CollectionData map[string]map[string]json.RawMessage

// CollectionStore keeps each plugin's collections in a file of its own.
type CollectionStore interface {
	// Load returns the plugin's collections; a missing file is empty.
	Load(ctx context.Context, id string) (CollectionData, error)
	// Save replaces the plugin's collections atomically.
	Save(ctx context.Context, id string, data CollectionData) error
	// Delete removes the plugin's file; a missing file is not an error.
	Delete(ctx context.Context, id string) error
}

type Document struct {
	ID  string          `json:"id"`
	Doc json.RawMessage `json:"doc"`
}

type DocumentList struct {
	Items []Document `json:"items"`
	Next  string     `json:"next,omitempty"`
}

type CollectionStats struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
	Bytes int    `json:"bytes"`
}

type CollectionUsage struct {
	Collections []CollectionStats `json:"collections"`
	Bytes       int               `json:"bytes"`
	LimitBytes  int               `json:"limitBytes"`
}

// Collections is the document store behind switchboard.db. Each plugin's
// data is loaded on first use and saved whole after every write; a failed
// save leaves both the file and memory as they were.
type Collections struct {
	plugins *Service
	store   CollectionStore
	writes  *limiter
	// ponytail: one lock across every plugin, held while a file is saved;
	// per-plugin locks if several busy plugins ever wait on each other.
	mu     sync.Mutex
	loaded map[string]CollectionData
}

func NewCollections(plugins *Service, store CollectionStore) (*Collections, error) {
	if plugins == nil || store == nil {
		return nil, errors.New("plugin collections need the plugins service and a store")
	}
	collections := &Collections{plugins: plugins, store: store, writes: newLimiter(CollectionWritesPerMinute, 0), loaded: map[string]CollectionData{}}
	plugins.OnRemoved(collections.drop)
	return collections, nil
}

func (collections *Collections) Get(ctx context.Context, id, collection, docID string) (json.RawMessage, error) {
	if err := checkAddress(collection, docID); err != nil {
		return nil, err
	}
	collections.mu.Lock()
	defer collections.mu.Unlock()
	data, err := collections.data(ctx, id)
	if err != nil {
		return nil, err
	}
	return data[collection][docID], nil
}

func (collections *Collections) Put(ctx context.Context, id, collection, docID string, raw json.RawMessage) error {
	if err := checkAddress(collection, docID); err != nil {
		return err
	}
	doc, err := domain.Document(raw)
	if err != nil {
		return err
	}
	return collections.write(ctx, id, func(data CollectionData) (CollectionData, bool, error) {
		documents, exists := data[collection]
		if !exists && len(data) >= domain.MaxCollections {
			return nil, false, ErrStorageFull
		}
		if previous, stored := documents[docID]; stored && string(previous) == string(doc) {
			return data, false, nil
		}
		next := maps.Clone(data)
		next[collection] = maps.Clone(documents)
		if next[collection] == nil {
			next[collection] = map[string]json.RawMessage{}
		}
		next[collection][docID] = doc
		if usage(next).Bytes > domain.MaxCollectionBytes {
			return nil, false, ErrStorageFull
		}
		return next, true, nil
	})
}

func (collections *Collections) Delete(ctx context.Context, id, collection, docID string) (bool, error) {
	if err := checkAddress(collection, docID); err != nil {
		return false, err
	}
	deleted := false
	err := collections.write(ctx, id, func(data CollectionData) (CollectionData, bool, error) {
		if _, stored := data[collection][docID]; !stored {
			return data, false, nil
		}
		next := maps.Clone(data)
		next[collection] = maps.Clone(data[collection])
		delete(next[collection], docID)
		if len(next[collection]) == 0 {
			delete(next, collection)
		}
		deleted = true
		return next, true, nil
	})
	return deleted && err == nil, err
}

// Clear removes one collection, or every collection when name is empty, and
// returns how many documents went.
func (collections *Collections) Clear(ctx context.Context, id, collection string) (int, error) {
	if collection != "" && !domain.ValidCollectionName(collection) {
		return 0, domain.ValidationError{Field: "collection", Reason: "must match ^[a-z0-9][a-z0-9_-]{0,31}$"}
	}
	cleared := 0
	err := collections.write(ctx, id, func(data CollectionData) (CollectionData, bool, error) {
		next := CollectionData{}
		if collection != "" {
			next = maps.Clone(data)
			delete(next, collection)
		}
		for name, documents := range data {
			if _, kept := next[name]; !kept {
				cleared += len(documents)
			}
		}
		return next, cleared > 0, nil
	})
	if err != nil {
		return 0, err
	}
	return cleared, nil
}

// List returns documents of one collection in id order, after the id
// `after` and starting with prefix. A page stops early when the next
// document would not fit the response; Next then names where to resume.
func (collections *Collections) List(ctx context.Context, id, collection, prefix, after string, limit int) (DocumentList, error) {
	switch {
	case !domain.ValidCollectionName(collection):
		return DocumentList{}, domain.ValidationError{Field: "collection", Reason: "must match ^[a-z0-9][a-z0-9_-]{0,31}$"}
	case prefix != "" && !domain.ValidDocumentID(prefix), after != "" && !domain.ValidDocumentID(after):
		return DocumentList{}, domain.ValidationError{Field: "prefix and after", Reason: "must be document ids"}
	case limit == 0:
		limit = DefaultCollectionList
	case limit < 1 || limit > MaxCollectionList:
		return DocumentList{}, domain.ValidationError{Field: "limit", Reason: "must be 1-200"}
	}
	collections.mu.Lock()
	defer collections.mu.Unlock()
	data, err := collections.data(ctx, id)
	if err != nil {
		return DocumentList{}, err
	}
	documents := data[collection]
	list := DocumentList{Items: []Document{}}
	// The envelope and next are at most a few hundred bytes; the budget
	// keeps room for them.
	room := collections.plugins.budget - 512
	for _, docID := range slices.Sorted(maps.Keys(documents)) {
		if docID <= after || !strings.HasPrefix(docID, prefix) {
			continue
		}
		item := Document{ID: docID, Doc: documents[docID]}
		cost := len(item.ID) + len(item.Doc) + 20
		if len(list.Items) > 0 && (len(list.Items) == limit || cost > room) {
			list.Next = list.Items[len(list.Items)-1].ID
			break
		}
		room -= cost
		list.Items = append(list.Items, item)
	}
	return list, nil
}

func (collections *Collections) Stats(ctx context.Context, id string) (CollectionUsage, error) {
	collections.mu.Lock()
	defer collections.mu.Unlock()
	data, err := collections.data(ctx, id)
	if err != nil {
		return CollectionUsage{}, err
	}
	return usage(data), nil
}

func usage(data CollectionData) CollectionUsage {
	result := CollectionUsage{Collections: []CollectionStats{}, LimitBytes: domain.MaxCollectionBytes}
	for name, documents := range data {
		stats := CollectionStats{Name: name, Count: len(documents)}
		for docID, doc := range documents {
			stats.Bytes += domain.DocumentCost(docID, doc)
		}
		result.Bytes += stats.Bytes
		result.Collections = append(result.Collections, stats)
	}
	slices.SortFunc(result.Collections, func(left, right CollectionStats) int { return cmp.Compare(left.Name, right.Name) })
	return result
}

// write applies change to the plugin's data and saves the result when it
// reports a change. Every attempt counts against the write limit.
func (collections *Collections) write(ctx context.Context, id string, change func(CollectionData) (CollectionData, bool, error)) error {
	collections.mu.Lock()
	defer collections.mu.Unlock()
	data, err := collections.data(ctx, id)
	if err != nil {
		return err
	}
	release, admitted := collections.writes.acquire(id)
	if !admitted {
		return ErrRateLimited
	}
	release()
	next, changed, err := change(data)
	if err != nil || !changed {
		return err
	}
	if len(next) == 0 {
		err = collections.store.Delete(ctx, id)
	} else {
		err = collections.store.Save(ctx, id, next)
	}
	if err != nil {
		return errors.Join(ErrStorageFull, err)
	}
	collections.loaded[id] = next
	return nil
}

// data returns the plugin's collections, loading them on first use. Callers
// hold mu.
func (collections *Collections) data(ctx context.Context, id string) (CollectionData, error) {
	if _, err := collections.plugins.lookup(id); err != nil {
		return nil, err
	}
	if data, loaded := collections.loaded[id]; loaded {
		return data, nil
	}
	data, err := collections.store.Load(ctx, id)
	if err != nil {
		return nil, errors.Join(ErrStoreUnavailable, err)
	}
	if err := checkData(data); err != nil {
		return nil, errors.Join(ErrStoreUnavailable, err)
	}
	if data == nil {
		data = CollectionData{}
	}
	collections.loaded[id] = data
	return data, nil
}

// drop forgets and deletes a removed plugin's collections. A file that
// cannot be deleted now is retried by the next removal of that id; until
// then a reinstall would find it, which is the lesser harm than refusing
// the uninstall that already happened.
func (collections *Collections) drop(id string) {
	collections.mu.Lock()
	defer collections.mu.Unlock()
	delete(collections.loaded, id)
	_ = collections.store.Delete(context.Background(), id)
}

func checkAddress(collection, docID string) error {
	if !domain.ValidCollectionName(collection) {
		return domain.ValidationError{Field: "collection", Reason: "must match ^[a-z0-9][a-z0-9_-]{0,31}$"}
	}
	if !domain.ValidDocumentID(docID) {
		return domain.ValidationError{Field: "docId", Reason: "must match ^[A-Za-z0-9._:-]{1,128}$"}
	}
	return nil
}

// checkData refuses a stored file this version would not have written.
func checkData(data CollectionData) error {
	if len(data) > domain.MaxCollections {
		return errors.New("stored collections exceed the collection limit")
	}
	for name, documents := range data {
		if !domain.ValidCollectionName(name) || len(documents) == 0 {
			return errors.New("stored collections are invalid")
		}
		for docID, doc := range documents {
			stored, err := domain.Document(doc)
			if !domain.ValidDocumentID(docID) || err != nil || string(stored) != string(doc) {
				return errors.New("stored collections are invalid")
			}
		}
	}
	if usage(data).Bytes > domain.MaxCollectionBytes {
		return errors.New("stored collections exceed the size limit")
	}
	return nil
}
