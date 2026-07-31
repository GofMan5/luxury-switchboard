package hub_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	hubhttp "github.com/luxuryprivate/switchboard/backend/internal/hub/adapters/http"
	"github.com/luxuryprivate/switchboard/backend/internal/hub/adapters/jsonfile"
	"github.com/luxuryprivate/switchboard/backend/internal/hub/application"
)

func TestHubControlIsRevisionedNeutralAndGatesImmediately(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tunnels.json")
	state := `{"v":2,"revision":7,"tunnels":[{"id":"tunnel_other","owner":"owner-2","state":"running"},{"id":"tunnel_self","owner":"owner-1","state":"running"}]}`
	if err := os.WriteFile(path, []byte(state), 0o640); err != nil {
		t.Fatal(err)
	}
	service, _ := application.NewService(jsonfile.New(path))
	snapshot, err := service.List(context.Background(), "owner-1")
	if err != nil || len(snapshot.Tunnels) != 2 || snapshot.Tunnels[0].Name != "Ваш коннект" {
		t.Fatalf("neutral owner view failed: %+v err=%v", snapshot, err)
	}
	serialized, _ := json.Marshal(snapshot)
	for _, forbidden := range []string{"tunnel_self", "tunnel_other", "owner-1", "owner-2", "provider"} {
		if strings.Contains(string(serialized), forbidden) {
			t.Fatalf("shared snapshot leaked %q: %s", forbidden, serialized)
		}
	}
	paused, err := service.Control(context.Background(), "owner-1", "pause", 7, 1)
	if err != nil || paused.Revision != 8 || paused.Tunnels[1].State != "paused" {
		t.Fatalf("revisioned mutation failed: %+v err=%v", paused, err)
	}

	gate := hubhttp.NewGate(service)
	allowed := httptest.NewRecorder()
	gate.ServeHTTP(allowed, httptest.NewRequest(http.MethodGet, "/authorize/tunnel_self?cache=1", nil))
	if allowed.Code != http.StatusNoContent || allowed.Header().Get("Cache-Control") != "no-store" || allowed.Header().Get("Server") != "" {
		t.Fatalf("running tunnel gate failed: status=%d headers=%v", allowed.Code, allowed.Header())
	}
	blocked := httptest.NewRecorder()
	gate.ServeHTTP(blocked, httptest.NewRequest(http.MethodGet, "/authorize/tunnel_other", nil))
	if blocked.Code != http.StatusServiceUnavailable || blocked.Body.String() != "{\"error\":\"tunnel_unavailable\"}\n" {
		t.Fatalf("paused tunnel was not blocked: %d %s", blocked.Code, blocked.Body.String())
	}
}
