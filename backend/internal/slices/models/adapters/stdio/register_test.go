package stdio_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	platform "github.com/luxuryprivate/switchboard/backend/internal/platform/stdio"
	modelstdio "github.com/luxuryprivate/switchboard/backend/internal/slices/models/adapters/stdio"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/models/application"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/models/domain"
)

type oneProvider struct{}

func (oneProvider) Get(string) (domain.Provider, bool) {
	return domain.Provider{ID: "echo"}, true
}

type fixedCatalog struct{ models []string }

func (catalog fixedCatalog) Discover(context.Context, domain.Provider) ([]string, error) {
	return catalog.models, nil
}

func (fixedCatalog) Test(_ context.Context, provider domain.Provider, model string) domain.TestResult {
	return domain.TestResult{ProviderID: provider.ID, Model: model, State: "available"}
}

// discover drives the real command through a real protocol server, so what is
// measured is the bytes the desktop shell would read rather than an estimate.
func discover(t *testing.T, models []string) (frame string, code string) {
	t.Helper()
	service, err := application.NewService(oneProvider{}, fixedCatalog{models: models})
	if err != nil {
		t.Fatal(err)
	}
	var output strings.Builder
	server := platform.NewServer(
		strings.NewReader(`{"v":1,"id":"d","type":"command","method":"models.discover","payload":{"providerId":"echo"}}`+"\n"),
		&output, 1,
	)
	modelstdio.Register(server, service)
	if err := server.Serve(context.Background()); err != nil {
		t.Fatal(err)
	}
	frame = strings.TrimSpace(output.String())
	answer := struct {
		OK    bool `json:"ok"`
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}{}
	if json.Unmarshal([]byte(frame), &answer) != nil {
		t.Fatalf("unreadable frame: %s", frame)
	}
	if answer.OK {
		return frame, ""
	}
	return frame, answer.Error.Code
}

func namesOf(count, width int) []string {
	models := make([]string, count)
	for index := range models {
		models[index] = strings.Repeat("m", width)
	}
	return models
}

// The shell reads one line at a time and refuses a frame over MaxFrameBytes by
// breaking its read loop and killing the sidecar. Go answers `response_too_large`
// instead, so nothing crashes - but that code was written for a bug and reads as
// one, and Model Routes would show "Command response is too large" for a catalog
// that is merely big. The count cap does not bound this on its own: measured, 5000
// names of 43 bytes fill 87% of the frame and 5000 of 50 bytes do not fit at all,
// which is an ordinary aggregator catalog rather than a hostile one.
func TestALargeModelCatalogIsRefusedByNameRatherThanByFrameSize(t *testing.T) {
	// The width where it used to break. `response_too_large` here would mean the
	// byte check is gone and the protocol is catching what the domain should.
	frame, code := discover(t, namesOf(5_000, 50))
	if code != "model_catalog_too_large" {
		t.Fatalf("a catalog too big for one frame answered %q: %s", code, truncate(frame))
	}

	// Just as important: a catalog that fits must still be served whole. Refusing
	// early would cost the operator models they can publish today.
	frame, code = discover(t, namesOf(5_000, 43))
	if code != "" {
		t.Fatalf("a catalog that fits was refused as %q", code)
	}
	if len(frame)+1 > platform.MaxFrameBytes {
		t.Fatalf("a served catalog was %d bytes, over the %d the shell accepts", len(frame)+1, platform.MaxFrameBytes)
	}
	var answer struct {
		Payload struct {
			Models []string `json:"models"`
		} `json:"payload"`
	}
	if json.Unmarshal([]byte(frame), &answer) != nil || len(answer.Payload.Models) != 5_000 {
		t.Fatalf("the served catalog lost models: %d of 5000", len(answer.Payload.Models))
	}
	t.Logf("5000 x 43 served in %d bytes of %d (%d%%)", len(frame)+1, platform.MaxFrameBytes, (len(frame)+1)*100/platform.MaxFrameBytes)
}

func truncate(value string) string {
	if len(value) <= 200 {
		return value
	}
	return value[:200] + "…"
}
