package relayhttp

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	keyapp "github.com/luxuryprivate/switchboard/backend/internal/slices/keypool/application"
	keydomain "github.com/luxuryprivate/switchboard/backend/internal/slices/keypool/domain"
	relaykeypool "github.com/luxuryprivate/switchboard/backend/internal/slices/relay/adapters/keypool"
	relayapp "github.com/luxuryprivate/switchboard/backend/internal/slices/relay/application"
)

func TestRealKeyPoolRotatesPastEveryRejectedCredential(t *testing.T) {
	for _, healthy := range []bool{true, false} {
		t.Run(map[bool]string{true: "third key succeeds", false: "all keys rejected once"}[healthy], func(t *testing.T) {
			scheduler := keyapp.NewScheduler(10)
			keys := []keydomain.Key{}
			for i, secret := range []string{"fixture-first", "fixture-second", "fixture-third"} {
				key, err := keydomain.NewKey(keydomain.Params{ProviderID: "fixture", Label: secret, Secret: secret, Priority: i})
				if err != nil {
					t.Fatal(err)
				}
				keys = append(keys, key)
			}
			if err := scheduler.Configure("fixture", 0, 0, keys); err != nil {
				t.Fatal(err)
			}
			seen := []string{}
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				secret := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
				seen = append(seen, secret)
				w.Header().Set("Content-Type", "application/json")
				if !healthy || secret != "fixture-third" {
					w.WriteHeader(http.StatusUnauthorized)
					_, _ = w.Write([]byte(`{"error":{"message":"Invalid key"}}`))
					return
				}
				_, _ = w.Write([]byte(`{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"served"}]}]}`))
			}))
			defer upstream.Close()
			base, _ := url.Parse(upstream.URL)
			server := NewServer("127.0.0.1:0", Dependencies{
				Routes:      fixedRoute{route: relayapp.Route{ProviderID: "fixture", BaseURL: base, AuthMode: "bearer", Format: "responses"}},
				Credentials: relaykeypool.NewSource(scheduler), Config: Config{RetryBase: time.Millisecond, RetryMax: time.Millisecond},
			})
			r := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"fixture-model","input":"hi"}`))
			r.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			server.ServeHTTP(w, r)
			want := http.StatusUnauthorized
			if healthy {
				want = http.StatusOK
			}
			if w.Code != want || !slices.Equal(seen, []string{"fixture-first", "fixture-second", "fixture-third"}) {
				t.Fatalf("status=%d attempts=%v; every priority key must get one real chance", w.Code, seen)
			}
			for index, state := range scheduler.Snapshot("fixture") {
				if state.StartsInWindow != 1 {
					t.Fatalf("key %d starts=%d", index, state.StartsInWindow)
				}
				if (!healthy || index < 2) && (state.AuthStreak != 1 || state.CooldownMS == 0) {
					t.Fatalf("rejected credential lost its own verdict: %+v", state)
				}
			}
		})
	}
}
