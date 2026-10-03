package tunnelhttp

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	tunnelapp "github.com/luxuryprivate/switchboard/backend/internal/slices/publictunnel/application"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/publictunnel/domain"
	relayapp "github.com/luxuryprivate/switchboard/backend/internal/slices/relay/application"
)

type fakeBans map[string]bool

func (bans fakeBans) Banned(ip string) bool { return bans[ip] }

func bannedGateway(t *testing.T, dispatcher *fakeDispatcher, activity tunnelapp.ClientActivity, bans tunnelapp.Bans) *Gateway {
	t.Helper()
	gateway, err := NewGateway(
		domain.Config{Token: testToken},
		fakeRoutes{[]domain.Route{{PublicModel: "public-gpt", UpstreamModel: "private-gpt", ProviderID: "private-provider"}}},
		fakeMarkers{}, dispatcher, activity, bans,
	)
	if err != nil {
		t.Fatal(err)
	}
	return gateway
}

func TestBannedClientIsRefusedBeforeReachingTheProvider(t *testing.T) {
	dispatcher := &fakeDispatcher{response: relayapp.DispatchResponse{Status: http.StatusOK, Headers: http.Header{"Content-Type": []string{"application/json"}}, Body: []byte(`{"output":"ok"}`)}}
	activity := &fakeClientActivity{}
	gateway := bannedGateway(t, dispatcher, activity, fakeBans{"203.0.113.7": true})
	request := authorizedRequest(http.MethodPost, "/v1/responses", `{"model":"public-gpt"}`)
	request.Header.Set("Cf-Connecting-Ip", "203.0.113.7")
	response := httptest.NewRecorder()
	gateway.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("banned client was served: status=%d", response.Code)
	}
	if dispatcher.calls != 0 {
		t.Fatal("banned client reached the provider")
	}
	if response.Body.String() != `{"error":"Request rejected"}` {
		t.Fatalf("ban response is not the neutral rejection: %s", response.Body.String())
	}
	if activity.rejected != 1 || activity.begins != 0 {
		t.Fatalf("a refusal cost a full request lifecycle: rejected=%d begins=%d", activity.rejected, activity.begins)
	}
}

func TestBannedClientCannotEvenListTheModels(t *testing.T) {
	gateway := bannedGateway(t, &fakeDispatcher{}, nil, fakeBans{"203.0.113.7": true})
	request := authorizedRequest(http.MethodGet, "/v1/models", "")
	request.Header.Set("Cf-Connecting-Ip", "203.0.113.7")
	response := httptest.NewRecorder()
	gateway.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden || response.Body.String() != `{"error":"Request rejected"}` {
		t.Fatalf("banned client still saw the published models: status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestUnbannedClientsKeepWorkingWhileABanIsActive(t *testing.T) {
	dispatcher := &fakeDispatcher{response: relayapp.DispatchResponse{Status: http.StatusOK, Headers: http.Header{"Content-Type": []string{"application/json"}}, Body: []byte(`{"output":"ok"}`)}}
	gateway := bannedGateway(t, dispatcher, nil, fakeBans{"203.0.113.7": true})
	request := authorizedRequest(http.MethodPost, "/v1/responses", `{"model":"public-gpt"}`)
	request.Header.Set("Cf-Connecting-Ip", "198.51.100.4")
	response := httptest.NewRecorder()
	gateway.ServeHTTP(response, request)
	if response.Code != http.StatusOK || dispatcher.calls != 1 {
		t.Fatalf("a ban blocked an unrelated client: status=%d calls=%d", response.Code, dispatcher.calls)
	}
}

// The client address comes from the Cloudflare edge, never from the client:
// a self-declared address header would otherwise hand the caller someone
// else's RPM budget, ban record and history.
func TestAClientCannotNameItsOwnAddress(t *testing.T) {
	dispatcher := &fakeDispatcher{response: relayapp.DispatchResponse{Status: http.StatusOK, Headers: http.Header{"Content-Type": []string{"application/json"}}, Body: []byte(`{"output":"ok"}`)}}
	gateway := bannedGateway(t, dispatcher, nil, fakeBans{"203.0.113.7": true})
	request := authorizedRequest(http.MethodPost, "/v1/responses", `{"model":"public-gpt"}`)
	request.RemoteAddr = "203.0.113.7:9000"
	request.Header.Set("X-Tunnel-Client-IP", "198.51.100.4") // forged: must be ignored
	request.Header.Set("Cf-Connecting-Ip", "203.0.113.7")    // the edge's word: must rule
	response := httptest.NewRecorder()
	gateway.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("a forged address header beat the edge's: status=%d", response.Code)
	}
}

// The owner note is governance state. The gateway can only ask whether an address
// is banned, and this pins that no answer can ever carry the note itself.
func TestOwnerNoteNeverReachesAPublicResponse(t *testing.T) {
	const note = "owner-secret-note-7f3a"
	dispatcher := &fakeDispatcher{response: relayapp.DispatchResponse{Status: http.StatusOK, Headers: http.Header{"Content-Type": []string{"application/json"}}, Body: []byte(`{"output":"ok"}`)}}
	gateway := bannedGateway(t, dispatcher, &fakeClientActivity{}, notedBans{banned: map[string]bool{"203.0.113.7": true}, note: note})
	for _, probe := range []struct {
		method, path, body, ip string
	}{
		{http.MethodPost, "/v1/responses", `{"model":"public-gpt"}`, "203.0.113.7"},
		{http.MethodPost, "/v1/responses", `{"model":"public-gpt"}`, "198.51.100.4"},
		{http.MethodGet, "/v1/models", "", "198.51.100.4"},
		{http.MethodGet, "/v1/models", "", "203.0.113.7"},
	} {
		request := authorizedRequest(probe.method, probe.path, probe.body)
		request.Header.Set("Cf-Connecting-Ip", probe.ip)
		response := httptest.NewRecorder()
		gateway.ServeHTTP(response, request)
		if strings.Contains(response.Body.String(), note) {
			t.Fatalf("%s %s leaked the owner note: %s", probe.method, probe.path, response.Body.String())
		}
		for name, values := range response.Result().Header {
			for _, value := range values {
				if strings.Contains(value, note) {
					t.Fatalf("%s leaked the owner note in header %s", probe.path, name)
				}
			}
		}
	}
}

// notedBans carries a note next to the ban decision so a leak would have something
// to leak; the port itself only ever exposes the boolean.
type notedBans struct {
	banned map[string]bool
	note   string
}

func (bans notedBans) Banned(ip string) bool { return bans.banned[ip] }

func TestBanCheckStaysAfterAuthorization(t *testing.T) {
	activity := &fakeClientActivity{}
	gateway := bannedGateway(t, &fakeDispatcher{}, activity, fakeBans{"203.0.113.7": true})
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	request.Header.Set("Cf-Connecting-Ip", "203.0.113.7")
	response := httptest.NewRecorder()
	gateway.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("an unauthenticated request learned about the ban list: status=%d", response.Code)
	}
	if activity.rejected != 0 {
		t.Fatal("an unauthenticated request was attributed to a banned client")
	}
}
