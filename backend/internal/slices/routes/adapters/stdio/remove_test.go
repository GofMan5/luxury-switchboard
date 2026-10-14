package routestdio

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	platform "github.com/luxuryprivate/switchboard/backend/internal/platform/stdio"
	routeapp "github.com/luxuryprivate/switchboard/backend/internal/slices/routes/application"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/routes/domain"
)

type routeMemory struct{ rows []domain.Assignment }

func (store *routeMemory) Load(context.Context) ([]domain.Assignment, error) {
	return slices.Clone(store.rows), nil
}
func (store *routeMemory) Save(_ context.Context, rows []domain.Assignment) error {
	store.rows = slices.Clone(rows)
	return nil
}

type routeProviders struct{}

func (routeProviders) Exists(string) bool { return true }

func TestProviderScopedRemovalKeepsTheSiblingChainAndWholeDeleteStillDeletesIt(t *testing.T) {
	store := &routeMemory{rows: []domain.Assignment{
		{Target: domain.TargetRelay, PublicModel: "shared", UpstreamModel: "a", ProviderID: "a", Enabled: true},
		{Target: domain.TargetRelay, PublicModel: "shared", UpstreamModel: "b", ProviderID: "b", Enabled: true, Priority: 1},
		{Target: domain.TargetTunnel, PublicModel: "shared", UpstreamModel: "a", ProviderID: "a", Enabled: true},
	}}
	service, _ := routeapp.NewService(store, routeProviders{})
	if err := service.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	call := func(method, fields string) platform.Response {
		t.Helper()
		var output bytes.Buffer
		server := platform.NewServer(strings.NewReader(`{"v":1,"id":"test","type":"command","method":"`+method+`","payload":`+fields+"}\n"), &output, 1)
		Register(server, service)
		if err := server.Serve(context.Background()); err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(output.String(), "\n") {
			var result platform.Response
			if json.Unmarshal([]byte(line), &result) == nil && result.ID == "test" {
				return result
			}
		}
		t.Fatalf("missing result: %s", output.String())
		return platform.Response{}
	}
	fields := `{"target":"relay","publicModel":"shared","providerId":"a"}`
	if result := call("routes.remove", fields); !result.OK {
		t.Fatalf("provider removal failed: %+v", result)
	}
	if rows := service.List(domain.TargetRelay); len(rows) != 1 || rows[0].ProviderID != "b" {
		t.Fatalf("sibling chain lost: %+v", rows)
	}
	if len(store.rows) != 2 || len(service.List(domain.TargetTunnel)) != 1 {
		t.Fatalf("other target/durable rows disturbed: %+v", store.rows)
	}
	if result := call("routes.remove", fields); !result.OK {
		t.Fatalf("idempotent removal failed: %+v", result)
	}
	if result := call("routes.remove", `{"target":"relay","publicModel":"shared","providerId":" "}`); result.OK {
		t.Fatal("invalid provider accepted")
	}
	if result := call("routes.delete", `{"target":"relay","publicModel":"shared"}`); !result.OK {
		t.Fatalf("whole deletion failed: %+v", result)
	}
	if len(service.List(domain.TargetRelay)) != 0 || len(store.rows) != 1 {
		t.Fatal("whole model delete no longer removes chain")
	}
}
