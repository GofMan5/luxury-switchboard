package probe

import (
	providerdomain "github.com/luxuryprivate/switchboard/backend/internal/slices/providers/domain"
	"net/http/httptest"
	"testing"
)

func TestExplicitBearerIsNotReinterpretedByCatalogDialect(t *testing.T) {
	for _, mode := range []providerdomain.AuthMode{providerdomain.AuthBearer, providerdomain.AuthAuto, providerdomain.AuthAPIKey} {
		t.Run(string(mode), func(t *testing.T) {
			request := httptest.NewRequest("GET", "http://127.0.0.1/catalog", nil)
			applyAuth(request, providerdomain.Provider{AuthMode: mode, Dialect: providerdomain.DialectAnthropic}, "fixture-secret")
			want := "fixture-secret|"
			if mode == providerdomain.AuthBearer {
				want = "|Bearer fixture-secret"
			}
			if got := request.Header.Get("X-Api-Key") + "|" + request.Header.Get("Authorization"); got != want {
				t.Fatalf("explicit profile auth differs from relay: got=%q want=%q", got, want)
			}
		})
	}
}
