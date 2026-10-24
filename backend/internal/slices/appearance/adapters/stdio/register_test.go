package appearancestdio

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	platform "github.com/luxuryprivate/switchboard/backend/internal/platform/stdio"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/appearance/application"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/appearance/domain"
)

type memory struct {
	saved   *domain.Appearance
	loadErr error
	fail    bool
}

func (store *memory) Load(context.Context) (domain.Appearance, bool, error) {
	if store.saved == nil {
		return domain.Appearance{}, false, store.loadErr
	}
	return *store.saved, true, store.loadErr
}

func (store *memory) Save(_ context.Context, appearance domain.Appearance) error {
	if store.fail {
		return errors.New("disk is full")
	}
	store.saved = &appearance
	return nil
}

type frame struct {
	Type    string             `json:"type"`
	ID      string             `json:"id"`
	Topic   string             `json:"topic"`
	OK      bool               `json:"ok"`
	Payload json.RawMessage    `json:"payload"`
	Error   *platform.RPCError `json:"error"`
}

func serve(t *testing.T, service *application.Service, method, payload string) (frame, []frame) {
	t.Helper()
	request := `{"v":1,"id":"test","type":"command","method":"` + method + `"`
	if payload != "" {
		request += `,"payload":` + payload
	}
	var output bytes.Buffer
	server := platform.NewServer(strings.NewReader(request+"}\n"), &output, 1)
	Register(server, service)
	if err := server.Serve(context.Background()); err != nil {
		t.Fatal(err)
	}
	var result frame
	var events []frame
	for _, line := range strings.Split(output.String(), "\n") {
		var parsed frame
		if json.Unmarshal([]byte(line), &parsed) != nil {
			continue
		}
		if parsed.Type == "event" {
			events = append(events, parsed)
		} else if parsed.ID == "test" {
			result = parsed
		}
	}
	if result.ID == "" {
		t.Fatalf("missing result: %s", output.String())
	}
	return result, events
}

func loaded(t *testing.T, store *memory) *application.Service {
	t.Helper()
	service, err := application.NewService(store)
	if err != nil {
		t.Fatal(err)
	}
	_ = service.Load(context.Background())
	return service
}

func TestGetAnswersDefaultsWithEveryField(t *testing.T) {
	result, _ := serve(t, loaded(t, &memory{}), "appearance.get", "")
	if !result.OK {
		t.Fatalf("get failed: %+v", result.Error)
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(result.Payload, &fields)
	for _, name := range []string{"theme", "accent", "density", "textSize", "uiFont", "monoFont", "radius", "tokens", "customCss"} {
		if _, ok := fields[name]; !ok {
			t.Errorf("appearance.get omits %s: %s", name, result.Payload)
		}
	}
	if _, ok := fields["motion"]; ok || string(fields["tokens"]) != "{}" {
		t.Fatalf("unexpected payload: %s", result.Payload)
	}
}

func TestUpdateAnswersAndAnnouncesTheNormalizedAppearance(t *testing.T) {
	store := &memory{}
	result, events := serve(t, loaded(t, store), "appearance.update", `{"theme":"paper","accent":"#ABCDEF","tokens":{"--bg":"#fff"}}`)
	if !result.OK {
		t.Fatalf("update failed: %+v", result.Error)
	}
	var answered domain.Appearance
	_ = json.Unmarshal(result.Payload, &answered)
	if answered.Accent != "#abcdef" || answered.Density != domain.DefaultDensity || store.saved == nil || store.saved.Theme != "paper" {
		t.Fatalf("unexpected answer %+v, saved %+v", answered, store.saved)
	}
	if len(events) != 1 || events[0].Topic != "appearance.changed" || !bytes.Equal(events[0].Payload, result.Payload) {
		t.Fatalf("unexpected events: %+v", events)
	}
}

func TestUpdateRefusalsUseTheContractCodes(t *testing.T) {
	for name, test := range map[string]struct {
		store   *memory
		payload string
		code    string
	}{
		"unknown field":  {&memory{}, `{"motion":"reduced"}`, "invalid_appearance"},
		"wrong type":     {&memory{}, `{"textSize":"13"}`, "invalid_appearance"},
		"invalid value":  {&memory{}, `{"density":"cozy"}`, "invalid_appearance"},
		"hostile token":  {&memory{}, `{"tokens":{"--bg":"url(https://example.invalid/x)"}}`, "invalid_appearance"},
		"unreadable":     {&memory{loadErr: errors.New("locked")}, `{}`, "secure_storage_unavailable"},
		"failed to save": {&memory{fail: true}, `{}`, "appearance_update_failed"},
	} {
		result, events := serve(t, loaded(t, test.store), "appearance.update", test.payload)
		if result.OK || result.Error == nil || result.Error.Code != test.code || len(events) != 0 {
			t.Errorf("%s: unexpected outcome %+v %+v", name, result.Error, events)
		}
		if result.Error != nil && strings.Contains(result.Error.Message, "example.invalid") {
			t.Errorf("%s: the error echoes the value", name)
		}
	}
}
