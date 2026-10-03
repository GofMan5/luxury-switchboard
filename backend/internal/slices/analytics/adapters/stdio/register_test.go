package analyticsstdio_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	platform "github.com/luxuryprivate/switchboard/backend/internal/platform/stdio"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/analytics/adapters/jsonfile"
	analyticsstdio "github.com/luxuryprivate/switchboard/backend/internal/slices/analytics/adapters/stdio"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/analytics/application"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/analytics/domain"
)

type fakeFacts struct {
	grouped []domain.GroupedRow
	failing bool
}

func (facts *fakeFacts) Grouped(context.Context, int64) ([]domain.GroupedRow, error) {
	if facts.failing {
		return nil, context.DeadlineExceeded
	}
	return facts.grouped, nil
}

func (facts *fakeFacts) Latencies(context.Context, int64) ([]domain.LatencySample, error) {
	return nil, nil
}

func (facts *fakeFacts) Daily(context.Context, int64) ([]domain.DailyRow, error) {
	return nil, nil
}

func (facts *fakeFacts) Errors(context.Context, int64) ([]domain.ErrorRow, error) {
	return nil, nil
}

// Commands are driven as real protocol frames rather than through the handler
// map, so what the assertions see is exactly what the desktop shell receives.
func exchange(t *testing.T, service *application.Service, commands ...string) []map[string]any {
	t.Helper()
	var input strings.Builder
	for index, command := range commands {
		input.WriteString(`{"v":1,"id":"req` + strconv.Itoa(index) + `","type":"command",` + command + "}\n")
	}
	var output strings.Builder
	server := platform.NewServer(strings.NewReader(input.String()), &output, 1)
	analyticsstdio.Register(server, service)
	if err := server.Serve(context.Background()); err != nil {
		t.Fatalf("serve: %v", err)
	}
	var results []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(output.String()), "\n") {
		frame := make(map[string]any)
		decoder := json.NewDecoder(strings.NewReader(line))
		decoder.UseNumber()
		if decoder.Decode(&frame) != nil {
			t.Fatalf("unreadable frame: %s", line)
		}
		if frame["type"] == "result" {
			results = append(results, frame)
		}
	}
	return results
}

func payloadOf(t *testing.T, frame map[string]any) map[string]any {
	t.Helper()
	if frame["ok"] != true {
		t.Fatalf("command failed: %+v", frame)
	}
	payload, ok := frame["payload"].(map[string]any)
	if !ok {
		t.Fatalf("command did not answer with an object: %+v", frame)
	}
	return payload
}

func number(t *testing.T, value any) float64 {
	t.Helper()
	parsed, ok := value.(json.Number)
	if !ok {
		t.Fatalf("expected a number, got %#v", value)
	}
	count, err := parsed.Float64()
	if err != nil {
		t.Fatal(err)
	}
	return count
}

func TestTheReportTravelsInOneProtocolFrame(t *testing.T) {
	grouped := make([]domain.GroupedRow, 0, 200)
	for index := range 200 {
		grouped = append(grouped, domain.GroupedRow{
			ProviderID: "p" + strconv.Itoa(index%50), Model: strings.Repeat("m", 20) + strconv.Itoa(index),
			TokenVolume: domain.TokenVolume{Requests: 3, Completed: 2, TotalTokens: 100},
		})
	}
	service := application.NewService(&fakeFacts{grouped: grouped}, jsonfile.New(filepath.Join(t.TempDir(), "prices.json")), nil)
	frames := exchange(t, service, `"method":"analytics.report","payload":{"period":"24h"}`)
	if len(frames) != 1 {
		t.Fatalf("the report did not answer: %+v", frames)
	}
	report := payloadOf(t, frames[0])
	providers, _ := report["providers"].([]any)
	if len(providers) != 50 {
		t.Fatalf("providers were not bounded: %d", len(providers))
	}
	if report["period"] != "24h" {
		t.Fatalf("the period did not travel: %v", report["period"])
	}
}

func TestPricesRoundTripThroughTheProtocol(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prices.json")
	service := application.NewService(&fakeFacts{}, jsonfile.New(path), nil)
	frames := exchange(t, service,
		`"method":"analytics.prices.set","payload":{"model":"gpt-6-astra","input":1.25,"cachedInput":0.125,"output":10,"reasoning":10}`,
		`"method":"analytics.prices.get"`,
	)
	if len(frames) != 2 {
		t.Fatalf("commands did not answer: %d frames", len(frames))
	}
	prices, _ := payloadOf(t, frames[0])["prices"].([]any)
	if len(prices) != 1 || entryModel(t, prices[0]) != "gpt-6-astra" {
		t.Fatalf("the catalog did not answer with the saved model: %v", prices)
	}
	// The set answer and the get answer agree, and both agree with what the
	// next process will load from disk.
	fresh := application.NewService(&fakeFacts{}, jsonfile.New(path), nil)
	again := exchange(t, fresh, `"method":"analytics.prices.get"`)
	pricesAgain, _ := payloadOf(t, again[0])["prices"].([]any)
	if len(pricesAgain) != 1 || entryModel(t, pricesAgain[0]) != "gpt-6-astra" {
		t.Fatalf("the catalog did not survive a restart: %v", pricesAgain)
	}
}

func TestAnImpossiblePriceIsRefusedByCode(t *testing.T) {
	service := application.NewService(&fakeFacts{}, jsonfile.New(filepath.Join(t.TempDir(), "prices.json")), nil)
	frames := exchange(t, service, `"method":"analytics.prices.set","payload":{"model":"m","input":99999999}`)
	if len(frames) != 1 || frames[0]["ok"] != false {
		t.Fatalf("an impossible rate was accepted: %+v", frames)
	}
	errorCode := frameError(t, frames[0])["code"]
	if errorCode != "invalid_price" {
		t.Fatalf("the refusal carries the wrong code: %v", errorCode)
	}
}

func TestAFailedReportAnswersAnalyticsQueryFailed(t *testing.T) {
	service := application.NewService(&fakeFacts{failing: true}, jsonfile.New(filepath.Join(t.TempDir(), "prices.json")), nil)
	frames := exchange(t, service, `"method":"analytics.report","payload":{"period":"24h"}`)
	if len(frames) != 1 || frames[0]["ok"] != false || frameError(t, frames[0])["code"] != "analytics_query_failed" {
		t.Fatalf("a failed query did not refuse by code: %+v", frames)
	}
}

func TestAnUnknownPeriodIsInvalidPayload(t *testing.T) {
	service := application.NewService(&fakeFacts{}, jsonfile.New(filepath.Join(t.TempDir(), "prices.json")), nil)
	frames := exchange(t, service, `"method":"analytics.report","payload":{"period":"forever"}`)
	if len(frames) != 1 || frames[0]["ok"] != false || frameError(t, frames[0])["code"] != "invalid_payload" {
		t.Fatalf("an unknown period was accepted: %+v", frames)
	}
}

// A broken catalog file must not blame the operator's rates: the code says
// the catalog itself failed, not that a number was wrong.
func TestABrokenCatalogRefusesByItsOwnCode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prices.json")
	if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	service := application.NewService(&fakeFacts{}, jsonfile.New(path), nil)
	frames := exchange(t, service,
		`"method":"analytics.prices.set","payload":{"model":"m","input":1}`,
		`"method":"analytics.prices.get"`,
	)
	if len(frames) != 2 {
		t.Fatalf("commands did not answer: %d frames", len(frames))
	}
	for _, frame := range frames {
		if frame["ok"] != false || frameError(t, frame)["code"] != "price_catalog_failed" {
			t.Fatalf("a broken catalog was misreported: %+v", frame)
		}
	}
}

func TestAPricedReportCarriesTheEstimate(t *testing.T) {
	grouped := []domain.GroupedRow{{
		ProviderID: "p1", ProviderName: "Alpha", Model: "gpt-6-astra",
		TokenVolume: domain.TokenVolume{Requests: 1, Completed: 1, InputTokens: 1_000_000, OutputTokens: 1_000_000, TotalTokens: 2_000_000},
	}}
	service := application.NewService(&fakeFacts{grouped: grouped}, jsonfile.New(filepath.Join(t.TempDir(), "prices.json")), nil)
	frames := exchange(t, service,
		`"method":"analytics.prices.set","payload":{"model":"gpt-6-astra","input":1,"cachedInput":0.1,"output":2}`,
		`"method":"analytics.report","payload":{"period":"24h"}`,
	)
	report := payloadOf(t, frames[1])
	overview := report["overview"].(map[string]any)
	volume := overview["volume"].(map[string]any)
	if number(t, volume["cost"]) != 3.0 {
		t.Fatalf("the estimate did not travel: %v", volume["cost"])
	}
	models, _ := report["models"].([]any)
	first := models[0].(map[string]any)
	modelVolume := first["volume"].(map[string]any)
	if number(t, modelVolume["cost"]) != 3.0 {
		t.Fatalf("the model row did not carry its estimate: %v", modelVolume["cost"])
	}
	unpriced, _ := report["unpricedModels"].([]any)
	if len(unpriced) != 0 {
		t.Fatalf("a fully priced report still named gaps: %v", unpriced)
	}
}

func frameError(t *testing.T, frame map[string]any) map[string]any {
	t.Helper()
	err, ok := frame["error"].(map[string]any)
	if !ok {
		t.Fatalf("a failure frame carries no error: %+v", frame)
	}
	return err
}

func entryModel(t *testing.T, entry any) string {
	t.Helper()
	price, ok := entry.(map[string]any)
	if !ok {
		t.Fatalf("a catalog entry is not an object: %#v", entry)
	}
	model, _ := price["model"].(string)
	return model
}
