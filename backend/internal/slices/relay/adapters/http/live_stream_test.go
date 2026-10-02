package relayhttp

import (
	"net/http"
	"net/http/httptest"
	"testing"

	guardrailrelay "github.com/luxuryprivate/switchboard/backend/internal/slices/guardrails/adapters/relay"
	guardrailruleset "github.com/luxuryprivate/switchboard/backend/internal/slices/guardrails/adapters/ruleset"
	guardrailapp "github.com/luxuryprivate/switchboard/backend/internal/slices/guardrails/application"
	guardraildomain "github.com/luxuryprivate/switchboard/backend/internal/slices/guardrails/domain"
)

// The decision itself: a monitor-mode guardrail, a native streaming request,
// and nothing the relay still has to convert — live delivery. Block mode,
// translated dialects, images and non-stream requests keep the buffered
// path, each for its own reason.
func TestCanStreamLiveDecidesByModeAndDialect(t *testing.T) {
	engine, err := guardraildomain.NewEngine(guardrailruleset.RulesJSON, guardrailruleset.BlocklistJSON)
	if err != nil {
		t.Fatal(err)
	}
	monitor, err := guardrailapp.NewInspector(engine, guardraildomain.ModeMonitor, 16)
	if err != nil {
		t.Fatal(err)
	}
	block, err := guardrailapp.NewInspector(engine, guardraildomain.ModeBlock, 16)
	if err != nil {
		t.Fatal(err)
	}
	request := func(path string) *http.Request {
		return httptest.NewRequest(http.MethodPost, "http://relay"+path, nil)
	}
	body := []byte(`{"model":"glm","stream":true,"messages":[]}`)
	translated := true
	native := false

	live := NewServer("127.0.0.1:0", Dependencies{Guardrail: guardrailrelay.New(monitor)})
	if !live.canStreamLive(request("/v1/chat/completions"), body, &native) {
		t.Fatal("a monitor-mode native chat stream was buffered")
	}
	if !live.canStreamLive(request("/chat/completions"), body, &native) {
		t.Fatal("the unprefixed chat path was buffered")
	}

	guarded := NewServer("127.0.0.1:0", Dependencies{Guardrail: guardrailrelay.New(block)})
	if guarded.canStreamLive(request("/v1/chat/completions"), body, &native) {
		t.Fatal("block mode streamed live: the verdict decides whether the client sees a byte at all")
	}
	if live.canStreamLive(request("/v1/responses"), body, &translated) {
		t.Fatal("a translated dialect streamed live: the conversion needs the whole answer")
	}
	if live.canStreamLive(request("/v1/images/generations"), body, &native) {
		t.Fatal("the image bridge streamed live")
	}
	if live.canStreamLive(request("/v1/chat/completions"), []byte(`{"model":"glm","messages":[]}`), &native) {
		t.Fatal("a non-stream request took the live path")
	}
}
