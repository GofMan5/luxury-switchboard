package pluginsstdio

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	platform "github.com/luxuryprivate/switchboard/backend/internal/platform/stdio"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/plugins/application"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/plugins/domain"
)

type secretSetPayload struct {
	ID  string `json:"id"`
	Key string `json:"key"`
	// Value is raw so an absent value is told apart from an explicit null,
	// which clears the secret.
	Value json.RawMessage `json:"value"`
}

type documentPayload struct {
	ID         string          `json:"id"`
	Collection string          `json:"collection"`
	DocID      string          `json:"docId"`
	Doc        json.RawMessage `json:"doc"`
}

type listPayload struct {
	ID         string `json:"id"`
	Collection string `json:"collection"`
	Prefix     string `json:"prefix"`
	After      string `json:"after"`
	Limit      int    `json:"limit"`
}

type clearPayload struct {
	ID         string `json:"id"`
	Collection string `json:"collection"`
}

type linkPayload struct {
	Path string `json:"path"`
}

func registerSecrets(server *platform.Server, service *application.Service) {
	server.Handle("plugins.secret.set", func(ctx context.Context, payload json.RawMessage) (any, error) {
		var request secretSetPayload
		if platform.DecodePayload(payload, &request) != nil || !domain.ValidID(request.ID) || len(request.Value) == 0 {
			return nil, invalid("Plugin secret write is malformed")
		}
		var value *string
		if platform.DecodePayload(request.Value, &value) != nil {
			return nil, invalid("Plugin secret value must be a string or null")
		}
		secrets, err := service.SetSecret(ctx, request.ID, request.Key, value)
		if err != nil {
			return nil, failure(err)
		}
		return map[string]any{"secrets": secrets}, nil
	})
}

// RegisterFetch serves plugins.fetch. Its refusals carry the neutral fetch
// codes, never the address or a value.
func RegisterFetch(server *platform.Server, fetcher *application.Fetcher) {
	server.Handle("plugins.fetch", func(ctx context.Context, payload json.RawMessage) (any, error) {
		var request application.FetchRequest
		if platform.DecodePayload(payload, &request) != nil || !domain.ValidID(request.ID) {
			return nil, invalid("Plugin fetch request is malformed")
		}
		result, err := fetcher.Fetch(ctx, request)
		var refused application.FetchError
		if errors.As(err, &refused) {
			return nil, platform.MethodError{Code: refused.Code, Message: refused.Message}
		}
		if err != nil {
			return nil, failure(err)
		}
		return result, nil
	})
}

// RegisterCollections serves the plugins.collection.* document store.
func RegisterCollections(server *platform.Server, collections *application.Collections) {
	decodeDocument := func(payload json.RawMessage, needsDoc bool) (documentPayload, error) {
		var request documentPayload
		if platform.DecodePayload(payload, &request) != nil || !domain.ValidID(request.ID) || needsDoc != (len(request.Doc) > 0) {
			return documentPayload{}, invalid("Plugin collection request is malformed")
		}
		return request, nil
	}
	server.Handle("plugins.collection.get", func(ctx context.Context, payload json.RawMessage) (any, error) {
		request, err := decodeDocument(payload, false)
		if err != nil {
			return nil, err
		}
		doc, err := collections.Get(ctx, request.ID, request.Collection, request.DocID)
		if err != nil {
			return nil, collectionFailure(err)
		}
		return map[string]json.RawMessage{"doc": nullable(doc)}, nil
	})
	server.Handle("plugins.collection.put", func(ctx context.Context, payload json.RawMessage) (any, error) {
		request, err := decodeDocument(payload, true)
		if err != nil {
			return nil, err
		}
		if err := collections.Put(ctx, request.ID, request.Collection, request.DocID, request.Doc); err != nil {
			return nil, collectionFailure(err)
		}
		return map[string]bool{"ok": true}, nil
	})
	server.Handle("plugins.collection.delete", func(ctx context.Context, payload json.RawMessage) (any, error) {
		request, err := decodeDocument(payload, false)
		if err != nil {
			return nil, err
		}
		deleted, err := collections.Delete(ctx, request.ID, request.Collection, request.DocID)
		if err != nil {
			return nil, collectionFailure(err)
		}
		return map[string]bool{"deleted": deleted}, nil
	})
	server.Handle("plugins.collection.list", func(ctx context.Context, payload json.RawMessage) (any, error) {
		var request listPayload
		if platform.DecodePayload(payload, &request) != nil || !domain.ValidID(request.ID) {
			return nil, invalid("Plugin collection list is malformed")
		}
		list, err := collections.List(ctx, request.ID, request.Collection, request.Prefix, request.After, request.Limit)
		if err != nil {
			return nil, collectionFailure(err)
		}
		return list, nil
	})
	server.Handle("plugins.collection.clear", func(ctx context.Context, payload json.RawMessage) (any, error) {
		var request clearPayload
		if platform.DecodePayload(payload, &request) != nil || !domain.ValidID(request.ID) {
			return nil, invalid("Plugin collection clear is malformed")
		}
		cleared, err := collections.Clear(ctx, request.ID, request.Collection)
		if err != nil {
			return nil, collectionFailure(err)
		}
		return map[string]int{"cleared": cleared}, nil
	})
	server.Handle("plugins.collection.stats", func(ctx context.Context, payload json.RawMessage) (any, error) {
		id, err := decodeID(payload)
		if err != nil {
			return nil, err
		}
		stats, err := collections.Stats(ctx, id)
		if err != nil {
			return nil, collectionFailure(err)
		}
		return stats, nil
	})
}

// RegisterDevLinks serves plugins.dev.link and plugins.dev.unlink.
func RegisterDevLinks(server *platform.Server, links *application.DevLinks) {
	server.Handle("plugins.dev.link", func(ctx context.Context, payload json.RawMessage) (any, error) {
		var request linkPayload
		if platform.DecodePayload(payload, &request) != nil {
			return nil, invalid("Plugin folder link is malformed")
		}
		plugin, err := links.Link(ctx, request.Path)
		if err != nil {
			return nil, devFailure(err)
		}
		return plugin, nil
	})
	server.Handle("plugins.dev.unlink", func(ctx context.Context, payload json.RawMessage) (any, error) {
		id, err := decodeID(payload)
		if err != nil {
			return nil, err
		}
		plugin, err := links.Unlink(ctx, id)
		if err != nil {
			return nil, devFailure(err)
		}
		return plugin, nil
	})
}

func nullable(doc json.RawMessage) json.RawMessage {
	if doc == nil {
		return json.RawMessage("null")
	}
	return doc
}

func collectionFailure(err error) error {
	if errors.Is(err, application.ErrRateLimited) {
		return platform.MethodError{Code: "rate_limited", Message: fmt.Sprintf("Plugin wrote more than %d times in a minute; wait and retry", application.CollectionWritesPerMinute)}
	}
	return failure(err)
}

func devFailure(err error) error {
	var problem application.DevProblemError
	switch {
	case errors.As(err, &problem):
		// The owner's own folder's report: what to fix in plugin.json.
		return platform.MethodError{Code: "plugin_dev_problem", Message: problem.Problem}
	case errors.Is(err, application.ErrDevLimit):
		return platform.MethodError{Code: "plugin_dev_limit", Message: fmt.Sprintf("At most %d plugins can be loaded from folders", domain.MaxDevLinks)}
	case errors.Is(err, application.ErrDevLinked):
		return platform.MethodError{Code: "plugin_dev_linked", Message: "That folder is already linked to another plugin"}
	case errors.Is(err, application.ErrNotLinked):
		return platform.MethodError{Code: "plugin_not_linked", Message: "Plugin is not loaded from a folder"}
	}
	return failure(err)
}
