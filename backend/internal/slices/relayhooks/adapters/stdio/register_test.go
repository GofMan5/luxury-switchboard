package relayhooksstdio

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	platform "github.com/luxuryprivate/switchboard/backend/internal/platform/stdio"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/relayhooks/application"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/relayhooks/domain"
)

type grants struct {
	mu      sync.Mutex
	granted bool
}

func (grants *grants) Granted(pluginID, permission string) bool {
	grants.mu.Lock()
	defer grants.mu.Unlock()
	return grants.granted && pluginID == "acme.tools" && permission == "relay:content"
}

type frame struct {
	ID      string          `json:"id"`
	Type    string          `json:"type"`
	Topic   string          `json:"topic"`
	OK      bool            `json:"ok"`
	Payload json.RawMessage `json:"payload"`
	Error   *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// session runs the protocol over pipes, as the shell does.
type session struct {
	t       *testing.T
	input   *io.PipeWriter
	frames  chan frame
	service *application.Service
	grants  *grants
	next    int
}

func start(t *testing.T) *session {
	t.Helper()
	inputReader, input := io.Pipe()
	outputReader, output := io.Pipe()
	server := platform.NewServer(inputReader, output, 4)
	service := application.NewService()
	granted := &grants{granted: true}
	service.Connect(granted, NewEvents(server))
	Register(server, service)
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan struct{})
	go func() {
		defer close(served)
		_ = server.Serve(ctx)
		_ = output.Close()
	}()
	frames := make(chan frame, 64)
	go func() {
		defer close(frames)
		scanner := bufio.NewScanner(outputReader)
		scanner.Buffer(make([]byte, 64*1024), platform.MaxFrameBytes+3)
		for scanner.Scan() {
			var decoded frame
			if err := json.Unmarshal(scanner.Bytes(), &decoded); err != nil {
				t.Errorf("undecodable frame: %v", err)
				continue
			}
			frames <- decoded
		}
	}()
	t.Cleanup(func() {
		cancel()
		_ = input.Close()
		<-served
		_ = outputReader.Close()
	})
	return &session{t: t, input: input, frames: frames, service: service, grants: granted}
}

func (session *session) send(method, payload string) string {
	session.t.Helper()
	session.next++
	id := fmt.Sprintf("req-%d", session.next)
	line := fmt.Sprintf(`{"v":1,"id":%q,"type":"command","method":%q,"payload":%s}`+"\n", id, method, payload)
	if _, err := io.WriteString(session.input, line); err != nil {
		session.t.Fatalf("write %s: %v", method, err)
	}
	return id
}

// await returns the next frame matching match, skipping others.
func (session *session) await(match func(frame) bool) frame {
	session.t.Helper()
	timeout := time.After(3 * time.Second)
	for {
		select {
		case next, ok := <-session.frames:
			if !ok {
				session.t.Fatal("the protocol stream ended")
			}
			if match(next) {
				return next
			}
		case <-timeout:
			session.t.Fatal("timed out waiting for a frame")
		}
	}
}

func (session *session) call(method, payload string) frame {
	session.t.Helper()
	id := session.send(method, payload)
	return session.await(func(next frame) bool { return next.Type == "result" && next.ID == id })
}

func (session *session) event(topic string) frame {
	session.t.Helper()
	return session.await(func(next frame) bool { return next.Type == "event" && next.Topic == topic })
}

func (session *session) ok(method, payload, want string) {
	session.t.Helper()
	result := session.call(method, payload)
	if !result.OK || string(result.Payload) != want {
		session.t.Fatalf("%s: ok=%v payload=%s error=%+v, want %s", method, result.OK, result.Payload, result.Error, want)
	}
}

func (session *session) fails(method, payload, code string) string {
	session.t.Helper()
	result := session.call(method, payload)
	if result.OK || result.Error == nil || result.Error.Code != code {
		session.t.Fatalf("%s: ok=%v error=%+v, want %s", method, result.OK, result.Error, code)
	}
	return result.Error.Message
}

const chatHook = `{"pluginId":"acme.tools","hookId":"redact","phase":"request","models":["gpt-*"],"endpoints":["chat"]}`

var chatRequest = domain.Request{
	ActivityID: "act-1", Model: "gpt-4o", Endpoint: "chat", Method: "POST", Path: "/v1/chat/completions",
	Bytes: 18, Body: `{"model":"gpt-4o"}`,
}

func TestRegisterUnregisterReset(t *testing.T) {
	session := start(t)
	session.ok("relayhooks.register", chatHook, `{"registered":true}`)
	session.ok("relayhooks.register", `{"pluginId":"acme.tools","hookId":"seen","phase":"response","models":[],"endpoints":[]}`, `{"registered":true}`)
	session.ok("relayhooks.unregister", `{"pluginId":"acme.tools","hookId":"redact"}`, `{"removed":1}`)
	session.ok("relayhooks.unregister", `{"pluginId":"acme.tools","hookId":"redact"}`, `{"removed":0}`)
	session.ok("relayhooks.register", chatHook, `{"registered":true}`)
	session.ok("relayhooks.unregister", `{"pluginId":"acme.tools"}`, `{"removed":2}`)
	session.ok("relayhooks.register", chatHook, `{"registered":true}`)
	session.ok("relayhooks.reset", `{}`, `{"removed":1}`)
}

func TestRegisterRefusals(t *testing.T) {
	session := start(t)
	message := session.fails("relayhooks.register", `{"pluginId":"acme.tools","hookId":"Bad","phase":"request"}`, "invalid_argument")
	if !strings.Contains(message, "hookId") {
		t.Fatalf("message does not name the field: %q", message)
	}
	session.fails("relayhooks.register", `{"pluginId":"acme.tools","hookId":"h","phase":"request","timeoutMs":99}`, "invalid_argument")
	session.fails("relayhooks.register", `{"pluginId":"acme.tools","hookId":"h","phase":"request","endpoints":["tunnel"]}`, "invalid_argument")
	session.fails("relayhooks.register", `{"pluginId":"acme.tools","hookId":"h","phase":"request","extra":1}`, "invalid_argument")
	session.fails("relayhooks.register", `{"pluginId":"other.one","hookId":"h","phase":"request"}`, "permission_denied")
	session.ok("relayhooks.register", `{"pluginId":"acme.tools","hookId":"a","phase":"request"}`, `{"registered":true}`)
	session.ok("relayhooks.register", `{"pluginId":"acme.tools","hookId":"b","phase":"request"}`, `{"registered":true}`)
	session.fails("relayhooks.register", `{"pluginId":"acme.tools","hookId":"c","phase":"request"}`, "limit")
	session.fails("relayhooks.unregister", `{"pluginId":"","hookId":""}`, "invalid_argument")
	session.fails("relayhooks.reset", `{"all":true}`, "invalid_argument")
	session.grants.mu.Lock()
	session.grants.granted = false
	session.grants.mu.Unlock()
	session.fails("relayhooks.register", `{"pluginId":"acme.tools","hookId":"a","phase":"request"}`, "permission_denied")
}

func TestRequestHookRoundTrip(t *testing.T) {
	session := start(t)
	session.ok("relayhooks.register", chatHook, `{"registered":true}`)
	if !session.service.Active(domain.PhaseRequest, "gpt-4o", "chat") {
		t.Fatal("hook is not active")
	}
	decisions := make(chan domain.Decision, 1)
	go func() { decisions <- session.service.Before(context.Background(), chatRequest) }()

	invoke := session.event(TopicInvoke)
	var payload struct {
		CallID   string `json:"callId"`
		PluginID string `json:"pluginId"`
		HookID   string `json:"hookId"`
		Phase    string `json:"phase"`
		Request  struct {
			ActivityID  string `json:"activityId"`
			Model       string `json:"model"`
			Endpoint    string `json:"endpoint"`
			Method      string `json:"method"`
			Path        string `json:"path"`
			Bytes       int64  `json:"bytes"`
			Body        string `json:"body"`
			BodyOmitted bool   `json:"bodyOmitted"`
		} `json:"request"`
	}
	if err := json.Unmarshal(invoke.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if !domain.ValidCallID(payload.CallID) || payload.PluginID != "acme.tools" || payload.HookID != "redact" || payload.Phase != "request" {
		t.Fatalf("invoke = %s", invoke.Payload)
	}
	if payload.Request.Body != chatRequest.Body || payload.Request.Bytes != 18 || payload.Request.Path != chatRequest.Path || payload.Request.BodyOmitted {
		t.Fatalf("invoke request = %s", invoke.Payload)
	}

	session.fails("relayhooks.respond", fmt.Sprintf(`{"callId":%q,"action":"block","block":{"status":500,"message":"x"}}`, strings.Repeat("0", 32)), "invalid_argument")
	session.ok("relayhooks.respond", fmt.Sprintf(`{"callId":%q,"action":"rewrite","model":"gpt-4o-mini","body":"{\"model\":\"gpt-4o-mini\"}","tags":["redacted"]}`, payload.CallID), `{"accepted":true}`)
	session.ok("relayhooks.respond", fmt.Sprintf(`{"callId":%q,"action":"continue"}`, payload.CallID), `{"accepted":false}`)

	select {
	case decision := <-decisions:
		if decision.Action != domain.ActionRewrite || decision.Model != "gpt-4o-mini" || decision.Body != `{"model":"gpt-4o-mini"}` || strings.Join(decision.Tags, ",") != "redacted" {
			t.Fatalf("decision = %+v", decision)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Before did not return")
	}
}

func TestBlockRoundTrip(t *testing.T) {
	session := start(t)
	session.ok("relayhooks.register", chatHook, `{"registered":true}`)
	decisions := make(chan domain.Decision, 1)
	go func() { decisions <- session.service.Before(context.Background(), chatRequest) }()
	var payload struct {
		CallID string `json:"callId"`
	}
	if err := json.Unmarshal(session.event(TopicInvoke).Payload, &payload); err != nil {
		t.Fatal(err)
	}
	session.ok("relayhooks.respond", fmt.Sprintf(`{"callId":%q,"action":"block","block":{"status":403,"message":"Not on this model"}}`, payload.CallID), `{"accepted":true}`)
	decision := <-decisions
	if decision.Action != domain.ActionBlock || decision.Block == nil || *decision.Block != (domain.Block{Status: 403, Message: "Not on this model"}) {
		t.Fatalf("decision = %+v", decision)
	}
}

func TestTimeoutsEmitChanged(t *testing.T) {
	session := start(t)
	session.ok("relayhooks.register", `{"pluginId":"acme.tools","hookId":"slow","phase":"request","timeoutMs":100}`, `{"registered":true}`)
	for range domain.MaxConsecutiveTimeouts {
		if decision := session.service.Before(context.Background(), chatRequest); decision.Action != domain.ActionContinue {
			t.Fatalf("a timeout must continue: %+v", decision)
		}
	}
	changed := session.event(TopicChanged)
	if string(changed.Payload) != `{"pluginId":"acme.tools","hookId":"slow","phase":"request","removed":true,"reason":"timeouts"}` {
		t.Fatalf("changed = %s", changed.Payload)
	}
	session.ok("relayhooks.unregister", `{"pluginId":"acme.tools"}`, `{"removed":0}`)
}

func TestObserveEvent(t *testing.T) {
	session := start(t)
	session.ok("relayhooks.register", `{"pluginId":"acme.tools","hookId":"seen","phase":"response"}`, `{"registered":true}`)
	session.service.After(chatRequest, domain.Response{
		Status: 200, LatencyMS: 120, Tokens: &domain.Tokens{Input: 3, Output: 4, Total: 7}, Body: `{"id":"x"}`,
	})
	observe := session.event(TopicObserve)
	want := `{"pluginId":"acme.tools","hookId":"seen","phase":"response",` +
		`"request":{"activityId":"act-1","model":"gpt-4o","endpoint":"chat","method":"POST","path":"/v1/chat/completions","bytes":18},` +
		`"response":{"status":200,"latencyMs":120,"tokens":{"input":3,"output":4,"total":7},"body":"{\"id\":\"x\"}"}}`
	if string(observe.Payload) != want {
		t.Fatalf("observe =\n%s\nwant\n%s", observe.Payload, want)
	}
}

type recorder struct {
	mu       sync.Mutex
	payloads []any
}

func (recorder *recorder) Emit(_ string, payload any) error {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	recorder.payloads = append(recorder.payloads, payload)
	return nil
}

func TestEventsOmitBodiesOverTheFrameBudget(t *testing.T) {
	emitter := &recorder{}
	events := NewEvents(emitter)
	// Escaping doubles a body of quotes, so a 192 KiB body cannot fit.
	quoted := strings.Repeat(`"`, domain.MaxBodyBytes)
	request := chatRequest
	request.Body = quoted
	omitted, err := events.Invoke(domain.Invocation{CallID: strings.Repeat("a", 32), PluginID: "acme.tools", HookID: "h", Request: request})
	if err != nil || !omitted {
		t.Fatalf("omitted=%v err=%v", omitted, err)
	}
	invoke := emitter.payloads[0].(invokePayload)
	if invoke.Request.Body != nil || !invoke.Request.BodyOmitted || invoke.Request.Bytes != 18 {
		t.Fatalf("invoke request = %+v", invoke.Request)
	}
	if err := events.Observe(domain.Observation{PluginID: "acme.tools", HookID: "o", Request: request, Response: domain.Response{Status: 200, Body: quoted}}); err != nil {
		t.Fatal(err)
	}
	observe := emitter.payloads[1].(observePayload)
	if observe.Response.Body != nil || !observe.Response.BodyOmitted || observe.Request.Body != nil {
		t.Fatalf("observe = %+v", observe)
	}
	// A plain 192 KiB body fits.
	request.Body = strings.Repeat("a", domain.MaxBodyBytes)
	if omitted, _ := events.Invoke(domain.Invocation{CallID: strings.Repeat("b", 32), PluginID: "acme.tools", HookID: "h", Request: request}); omitted {
		t.Fatal("a body within the budget was omitted")
	}
}
