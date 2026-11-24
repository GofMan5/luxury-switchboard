package relayhttp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	relayapp "github.com/luxuryprivate/switchboard/backend/internal/slices/relay/application"
)

// Verbatim from a strict provider: the whole request dies over one keyword.
const uniqueItemsRefusal = `{"error":{"message":"Invalid schema for 'tools[118].parameters': ` +
	`Unimplemented keys: [\"uniqueItems\"]. Compilation limits apply to all strict schemas in the request.",` +
	`"type":"invalid_request_error","param":"tools[118].parameters","code":null}}`

func TestUnimplementedKeysAreReadFromTheProvidersOwnComplaint(t *testing.T) {
	if keys := unimplementedSchemaKeys([]byte(uniqueItemsRefusal)); !reflect.DeepEqual(keys, []string{"uniqueitems"}) {
		t.Fatalf("the unimplemented key was not read: %v", keys)
	}
	if keys := unimplementedSchemaKeys([]byte(`{"error":{"message":"Unimplemented keys: [\"format\", \"pattern\"]"}}`)); !reflect.DeepEqual(keys, []string{"format", "pattern"}) {
		t.Fatalf("several named keys were not read: %v", keys)
	}
	// Only a complaint that carries the wording counts: anything else is prose.
	for _, unrelated := range []string{
		`{"error":{"message":"context length exceeded","type":"invalid_request_error"}}`,
		`{"error":{"message":"tools[118].parameters.uniqueItems is invalid","type":"invalid_request_error"}}`,
		`{"error":{"message":"uniqueItems must be a boolean","type":"invalid_request_error"}}`,
		``,
	} {
		if keys := unimplementedSchemaKeys([]byte(unrelated)); keys != nil {
			t.Fatalf("an unrelated complaint named keys %v", keys)
		}
	}
}

func TestTheNamedKeyIsRemovedFromEverySchemaAndNothingElseIs(t *testing.T) {
	body := []byte(`{"model":"m","tools":[{"type":"function","name":"find","strict":true,"parameters":{` +
		`"type":"object","properties":{"items":{"type":"array","uniqueItems":true,"items":{"$ref":"#/$defs/row"}},` +
		`"limit":{"type":"integer"}},` +
		`"$defs":{"row":{"type":"object","properties":{"tags":{"type":"array","uniqueItems":true}}}},` +
		`"required":["limit"],"additionalProperties":false}}],` +
		`"text":{"format":{"type":"json_schema","name":"out","strict":true,"schema":{"type":"object","uniqueItems":true}}}}`)
	repaired, changed := repairUnimplementedSchemaKeys(body, unimplementedSchemaKeys([]byte(uniqueItemsRefusal)))
	if !changed {
		t.Fatal("the refused schema was not rewritten")
	}
	var got, want any
	_ = json.Unmarshal(repaired, &got)
	_ = json.Unmarshal([]byte(`{"model":"m","tools":[{"type":"function","name":"find","strict":true,"parameters":{`+
		`"type":"object","properties":{"items":{"type":"array","items":{"$ref":"#/$defs/row"}},`+
		`"limit":{"type":"integer"}},`+
		`"$defs":{"row":{"type":"object","properties":{"tags":{"type":"array"}}}},`+
		`"required":["limit"],"additionalProperties":false}}],`+
		`"text":{"format":{"type":"json_schema","name":"out","strict":true,"schema":{"type":"object"}}}}`), &want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("the repair changed more than the named key:\n got %s\nwant %s", repaired, want)
	}
	// A body without the key is byte-identical, and an empty key list is a no-op.
	untouched := []byte(`{"model":"m","messages":[]}`)
	if out, changed := repairUnimplementedSchemaKeys(untouched, []string{"uniqueitems"}); changed || string(out) != string(untouched) {
		t.Fatalf("a clean body was rewritten: %s", out)
	}
	if out, changed := repairUnimplementedSchemaKeys(body, nil); changed || string(out) != string(body) {
		t.Fatalf("an empty key list rewrote the body: %s", out)
	}
	// An undecodable body is left alone rather than replaced by garbage.
	if out, changed := repairUnimplementedSchemaKeys([]byte(`{"model":`), []string{"uniqueitems"}); changed || string(out) != `{"model":` {
		t.Fatalf("a broken body was rewritten: %s", out)
	}
}

// The repair rides the existing 400 ladder: the provider refuses once, the
// relay removes exactly the key it named, and the retry succeeds. The memo
// then sends the clean shape first, so the next request costs one attempt.
func TestAnUnimplementedSchemaKeyIsRemovedAndThenRemembered(t *testing.T) {
	var attempts int
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		attempts++
		if strings.Contains(string(body), "uniqueItems") {
			writer.Header().Set("Content-Type", "application/json")
			writer.WriteHeader(http.StatusBadRequest)
			_, _ = writer.Write([]byte(uniqueItemsRefusal))
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"id":"resp_1","object":"response","status":"completed","output":[]}`))
	}))
	defer upstream.Close()
	parsed, _ := url.Parse(upstream.URL)
	server := NewServer("127.0.0.1:0", Dependencies{
		Routes:      fixedRoute{route: relayapp.Route{ProviderID: "strict", BaseURL: parsed, AuthMode: "passthrough"}},
		Credentials: &credentialSource{values: []string{""}},
	})
	request := []byte(`{"model":"m","stream":false,"tools":[{"type":"function","name":"find","strict":true,` +
		`"parameters":{"type":"object","properties":{"items":{"type":"array","uniqueItems":true}},"required":[]}}],` +
		`"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`)

	for round, wantAttempts := range []int{2, 1} {
		before := attempts
		req := httptest.NewRequest(http.MethodPost, "http://relay/v1/responses", strings.NewReader(string(request)))
		req.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		server.ServeHTTP(response, req)
		if response.Code != http.StatusOK {
			t.Fatalf("round %d: status=%d body=%s", round, response.Code, response.Body.String())
		}
		if got := attempts - before; got != wantAttempts {
			t.Fatalf("round %d: expected %d attempts, saw %d", round, wantAttempts, got)
		}
	}
	// The learned key is persisted with the rest of the shape.
	profile := server.repairs.snapshot()["strict"]
	if !containsString(profile.SchemaKeys, "uniqueitems") {
		t.Fatalf("the refused schema key was not memorised: %+v", profile)
	}
}

// A refusal that names something the request does not carry must not add
// attempts of its own: the repair reports no change, and the request ends on
// whatever plain 400 ceiling the server is configured with. The same server
// answering an unrelated 400 is the control, so the test does not pin a
// particular retry number.
func TestASchemaRefusalForAnAbsentKeyAddsNoRetries(t *testing.T) {
	configured := Config{RetryBase: time.Millisecond, RetryMax: time.Millisecond, PermanentAttempts: 3}
	run := func(complaint string) (int, int) {
		t.Helper()
		var attempts int
		upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			attempts++
			writer.Header().Set("Content-Type", "application/json")
			writer.WriteHeader(http.StatusBadRequest)
			_, _ = writer.Write([]byte(complaint))
		}))
		defer upstream.Close()
		parsed, _ := url.Parse(upstream.URL)
		server := NewServer("127.0.0.1:0", Dependencies{
			Routes:      fixedRoute{route: relayapp.Route{ProviderID: "strict", BaseURL: parsed, AuthMode: "passthrough"}},
			Credentials: &credentialSource{values: []string{""}},
			Config:      configured,
		})
		dispatched, _ := server.Dispatch(context.Background(), relayapp.DispatchRequest{
			Method: http.MethodPost, Path: "/v1/responses", ProviderID: "strict", UpstreamModel: "m",
			Headers: http.Header{"Content-Type": []string{"application/json"}},
			Body:    []byte(`{"model":"m","input":"hi"}`),
		})
		return attempts, dispatched.Status
	}
	seen, status := run(uniqueItemsRefusal)
	// The control is the same complaint with the list emptied, so both runs
	// classify identically and differ only in whether the repair has a key to
	// remove. An unrelated wording would not be a fair control: the ladder
	// classifies complaints by prose and spends a different budget on each.
	control, _ := run(`{"error":{"message":"Invalid schema for 'tools[118].parameters': Compilation limits apply to all strict schemas in the request.","type":"invalid_request_error"}}`)
	if seen != control {
		t.Fatalf("an unrepairable schema refusal changed the attempt count: %d with the named key, %d without", seen, control)
	}
	if status != http.StatusBadRequest {
		t.Fatalf("the provider's verdict did not reach the client: status=%d", status)
	}
}
