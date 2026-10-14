package keypoolstdio

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	platform "github.com/luxuryprivate/switchboard/backend/internal/platform/stdio"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/keypool/application"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/keypool/domain"
)

type fixtureRepository struct{ keys []domain.Key }

func (r *fixtureRepository) Load(context.Context) ([]domain.Key, error) { return r.keys, nil }
func (r *fixtureRepository) Save(_ context.Context, keys []domain.Key) error {
	r.keys = keys
	return nil
}

func TestManagementKeyListNeverSilentlyHidesTail(t *testing.T) {
	for _, count := range []int{2, 2000} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			keys := make([]domain.Key, 0, count)
			for index := range count {
				key, err := domain.NewKey(domain.Params{ProviderID: "fixture", Label: strings.Repeat("界", 80), Secret: fmt.Sprintf("synthetic-%d", index), Priority: index})
				if err != nil {
					t.Fatal(err)
				}
				keys = append(keys, key)
			}
			manager, err := application.NewManager(application.NewScheduler(10), &fixtureRepository{keys: keys}, map[string]application.Rate{"fixture": {}}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := manager.Load(context.Background()); err != nil {
				t.Fatal(err)
			}
			var output strings.Builder
			server := platform.NewServer(strings.NewReader("{\"v\":1,\"id\":\"req1\",\"type\":\"command\",\"method\":\"keys.list\",\"payload\":{\"providerId\":\"fixture\"}}\n"), &output, 1)
			Register(server, manager, nil)
			if err := server.Serve(context.Background()); err != nil {
				t.Fatal(err)
			}
			var frame struct {
				OK      bool `json:"ok"`
				Payload struct {
					Keys      []domain.PublicKey `json:"keys"`
					Available int                `json:"available"`
				} `json:"payload"`
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			if err := json.Unmarshal([]byte(strings.TrimSpace(output.String())), &frame); err != nil {
				t.Fatal(err)
			}
			if count == 2 {
				if !frame.OK || len(frame.Payload.Keys) != count || frame.Payload.Available != count {
					t.Fatalf("ordinary list failed: %+v", frame)
				}
			} else if frame.OK || frame.Error.Code != "key_pool_too_large" {
				t.Fatalf("management list silently truncated: ok=%t shown=%d available=%d error=%s", frame.OK, len(frame.Payload.Keys), frame.Payload.Available, frame.Error.Code)
			}
			if output.Len() > platform.MaxFrameBytes {
				t.Fatalf("list exceeded protocol frame: %d", output.Len())
			}
		})
	}
}
