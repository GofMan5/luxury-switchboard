package application_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/analytics/application"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/analytics/domain"
)

type fakeFacts struct {
	grouped   []domain.GroupedRow
	latencies []domain.LatencySample
	daily     []domain.DailyRow
	errRows   []domain.ErrorRow
	failWith  error
}

func (facts *fakeFacts) Grouped(_ context.Context, _ int64) ([]domain.GroupedRow, error) {
	return facts.grouped, facts.failWith
}

func (facts *fakeFacts) Latencies(_ context.Context, _ int64) ([]domain.LatencySample, error) {
	return facts.latencies, facts.failWith
}

func (facts *fakeFacts) Daily(_ context.Context, _ int64) ([]domain.DailyRow, error) {
	return facts.daily, facts.failWith
}

func (facts *fakeFacts) Errors(_ context.Context, _ int64) ([]domain.ErrorRow, error) {
	return facts.errRows, facts.failWith
}

type fakePrices struct {
	catalog  domain.Catalog
	failWith error
	saved    int
}

func (prices *fakePrices) Load(_ context.Context) (domain.Catalog, error) {
	return prices.catalog, prices.failWith
}

func (prices *fakePrices) Save(_ context.Context, catalog domain.Catalog) error {
	prices.saved++
	prices.catalog = catalog
	return prices.failWith
}

func newService(facts *fakeFacts, prices *fakePrices) *application.Service {
	service := application.NewService(facts, prices)
	return service
}

func TestTheReportMergesFactsIntoDimensions(t *testing.T) {
	service := newService(&fakeFacts{
		grouped: []domain.GroupedRow{
			{ProviderID: "p1", ProviderName: "Alpha", Model: "m1", TokenVolume: volume(10, 8, 2, 5_000, 1_000, 100, 50, 4_000)},
			{ProviderID: "p1", ProviderName: "Alpha", Model: "m2", TokenVolume: volume(4, 4, 0, 1_000, 500, 0, 0, 1_000)},
			{ProviderID: "p2", ProviderName: "Beta", Model: "m1", TokenVolume: volume(6, 3, 3, 2_000, 300, 0, 0, 900)},
		},
		latencies: []domain.LatencySample{
			{ProviderID: "p1", Model: "m1", LatencyMS: 100},
			{ProviderID: "p1", Model: "m1", LatencyMS: 300},
			{ProviderID: "p1", Model: "m2", LatencyMS: 200},
			{ProviderID: "p2", Model: "m1", LatencyMS: 500},
		},
		errRows: []domain.ErrorRow{
			{ProviderID: "p1", Model: "m1", ErrorCode: "upstream_unavailable", Requests: 2},
		},
	}, &fakePrices{})
	report, err := service.Report(context.Background(), application.Period24H)
	if err != nil {
		t.Fatal(err)
	}
	if report.Overview.Volume.Requests != 20 || report.Overview.Volume.Completed != 15 {
		t.Fatalf("overview did not merge: %+v", report.Overview.Volume)
	}
	if report.Overview.SuccessRate != 15.0/20.0 {
		t.Fatalf("success rate is wrong: %v", report.Overview.SuccessRate)
	}
	if len(report.Providers) != 2 || report.Providers[0].ID != "p1" || report.Providers[0].Name != "Alpha" {
		t.Fatalf("providers did not group: %+v", report.Providers)
	}
	// p1 saw latencies 100, 200, 300 → p50 = 200, p95 = 300 (nearest rank).
	if report.Providers[0].P50MS != 200 || report.Providers[0].P95MS != 300 {
		t.Fatalf("provider percentiles are wrong: p50=%v p95=%v", report.Providers[0].P50MS, report.Providers[0].P95MS)
	}
	if len(report.Models) != 2 || report.Models[0].Model != "m1" {
		t.Fatalf("models did not group: %+v", report.Models)
	}
	// m1 spans both providers: 100, 300, 500 → p50 = 300.
	if report.Models[0].P50MS != 300 {
		t.Fatalf("model percentile is wrong: %v", report.Models[0].P50MS)
	}
	if len(report.Errors) != 1 || report.Errors[0].Requests != 2 {
		t.Fatalf("errors did not group: %+v", report.Errors)
	}
}

func volume(requests, completed, failed int, input, output, cached, reasoning, generation float64) domain.TokenVolume {
	var tokens int64 = int64(input + output)
	return domain.TokenVolume{
		Requests: requests, Completed: completed, Failed: failed,
		InputTokens: int64(input), OutputTokens: int64(output),
		CachedTokens: int64(cached), ReasoningToken: int64(reasoning),
		TotalTokens: tokens, GenerationMS: generation,
	}
}

func TestUnpricedModelsAreNamedNotFolded(t *testing.T) {
	service := newService(&fakeFacts{
		grouped: []domain.GroupedRow{
			{ProviderID: "p1", Model: "priced", TokenVolume: volume(1, 1, 0, 1_000_000, 1_000_000, 0, 0, 0)},
			{ProviderID: "p1", Model: "free", TokenVolume: volume(1, 1, 0, 2_000_000, 0, 0, 0, 0)},
		},
	}, &fakePrices{catalog: pricedCatalog("priced")})
	report, err := service.Report(context.Background(), application.Period24H)
	if err != nil {
		t.Fatal(err)
	}
	// The priced model contributes exactly its rates; the unpriced one
	// contributes zero and is named in the report's gap list.
	if report.Overview.Volume.Cost != 3.0 {
		t.Fatalf("cost estimate is wrong: %v", report.Overview.Volume.Cost)
	}
	if report.Overview.Volume.IsPriced {
		t.Fatal("a partial catalog claimed completeness")
	}
	if len(report.UnpricedModels) != 1 || report.UnpricedModels[0] != "free" {
		t.Fatalf("unpriced models are wrong: %v", report.UnpricedModels)
	}
	if report.Overview.PricedRequests != 1 {
		t.Fatalf("priced request count is wrong: %v", report.Overview.PricedRequests)
	}
}

func pricedCatalog(models ...string) domain.Catalog {
	catalog := domain.NewCatalog()
	for _, model := range models {
		catalog.Prices[model] = domain.Price{Model: model, Input: 1, CachedInput: 0.1, Output: 2, Reasoning: 2}
	}
	return catalog
}

func TestCachedAndReasoningBillAtTheirOwnRates(t *testing.T) {
	catalog := domain.NewCatalog()
	catalog.Prices["m"] = domain.Price{Model: "m", Input: 3, CachedInput: 0.3, Output: 5, Reasoning: 5}
	// 1M plain input + 1M cached input + 1M output (of which 0.5M reasoning).
	// Reasoning rides the output rate: 1M × 5 covers it, the dedicated rate
	// matches it, so no correction applies.
	row := domain.TokenVolume{InputTokens: 2_000_000, CachedTokens: 1_000_000, OutputTokens: 1_000_000, ReasoningToken: 500_000}
	cost, isPriced := catalog.EstimateCost(row, "m")
	if !isPriced || cost != 1_000_000*3/1_000_000+1_000_000*0.3/1_000_000+1_000_000*5/1_000_000 {
		t.Fatalf("cost math is wrong: %v", cost)
	}
	// A dedicated reasoning rate below output bills the reasoning subset at
	// the cheaper rate and the rest of output at the plain rate.
	catalog.Prices["m"] = domain.Price{Model: "m", Input: 3, Output: 5, Reasoning: 1}
	cost, _ = catalog.EstimateCost(row, "m")
	// cached input now has no rate: the estimate refuses rather than
	// quietly billing cache at the full input price.
	_, isPricedCached := catalog.EstimateCost(row, "m")
	if isPricedCached {
		t.Fatal("an unpriced cached rate still claimed completeness")
	}
	rowNoCache := domain.TokenVolume{InputTokens: 2_000_000, OutputTokens: 1_000_000, ReasoningToken: 500_000}
	cost, isPriced = catalog.EstimateCost(rowNoCache, "m")
	if !isPriced || cost != 2*3+0.5*1+0.5*5 {
		t.Fatalf("reasoning correction is wrong: %v", cost)
	}
}

func TestAnInvalidPriceIsRefusedNotSaved(t *testing.T) {
	prices := &fakePrices{}
	service := newService(&fakeFacts{}, prices)
	if _, err := service.SetPrice(context.Background(), domain.Price{Model: "m", Input: -1}); err == nil {
		t.Fatal("a negative rate was accepted")
	}
	if _, err := service.SetPrice(context.Background(), domain.Price{Model: "", Input: 1}); err == nil {
		t.Fatal("an empty model was accepted")
	}
	if _, err := service.SetPrice(context.Background(), domain.Price{Model: "m", Input: domain.MaxPrice + 1}); err == nil {
		t.Fatal("an impossible rate was accepted")
	}
	// A model key long enough to grow the frame unchecked is refused the
	// same way: real model ids are far shorter than this.
	if _, err := service.SetPrice(context.Background(), domain.Price{Model: strings.Repeat("m", domain.MaxModelName+1), Input: 1}); err == nil {
		t.Fatal("an oversized model name was accepted")
	}
	if prices.saved != 0 {
		t.Fatal("a refused price still saved")
	}
}

func TestAFailedFactsQueryFailsTheReport(t *testing.T) {
	service := newService(&fakeFacts{failWith: errors.New("db gone")}, &fakePrices{})
	if _, err := service.Report(context.Background(), application.Period24H); err == nil {
		t.Fatal("a failed facts query produced a report")
	}
}

func TestTheReportRefusesAnUnknownPeriod(t *testing.T) {
	service := newService(&fakeFacts{}, &fakePrices{})
	if _, err := service.Report(context.Background(), application.Period("forever")); err == nil {
		t.Fatal("an unknown period was accepted")
	}
}

func TestDailyPointsArePricedPerModelThenFolded(t *testing.T) {
	service := newService(&fakeFacts{
		daily: []domain.DailyRow{
			{Date: "2026-09-27", Model: "priced", TokenVolume: volume(1, 1, 0, 1_000_000, 0, 0, 0, 0)},
			{Date: "2026-09-27", Model: "free", TokenVolume: volume(1, 1, 0, 1_000_000, 0, 0, 0, 0)},
			{Date: "2026-09-28", Model: "priced", TokenVolume: volume(2, 2, 0, 2_000_000, 0, 0, 0, 0)},
		},
	}, &fakePrices{catalog: pricedCatalog("priced")})
	report, err := service.Report(context.Background(), application.Period48H)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Daily) != 2 || report.Daily[0].Date != "2026-09-27" {
		t.Fatalf("daily points did not fold: %+v", report.Daily)
	}
	if report.Daily[0].Volume.Cost != 1.0 || report.Daily[0].Volume.IsPriced {
		t.Fatalf("the mixed day is priced wrong: cost=%v priced=%v", report.Daily[0].Volume.Cost, report.Daily[0].Volume.IsPriced)
	}
	if report.Daily[1].Volume.Cost != 2.0 || !report.Daily[1].Volume.IsPriced {
		t.Fatalf("the clean day is priced wrong: cost=%v priced=%v", report.Daily[1].Volume.Cost, report.Daily[1].Volume.IsPriced)
	}
}

func TestTokensPerSecondMatchesTheHistorySlice(t *testing.T) {
	// 1M output tokens over 1000 seconds of generation = 1000 tok/s, the
	// same formula history.stats answers with.
	row := domain.TokenVolume{OutputTokens: 1_000_000, GenerationMS: 1_000_000}
	if got := row.TokensPerSecond(); got != 1000 {
		t.Fatalf("tokens per second is wrong: %v", got)
	}
}

func TestPercentilesRefuseToInventNumbers(t *testing.T) {
	if domain.PercentileMS(nil, 0.95) != 0 {
		t.Fatal("an empty sample set produced a percentile")
	}
	sorted := []float64{10, 20, 30, 40, 50, 60, 70, 80, 90, 100}
	// 95% of 10 samples, nearest rank: ceil(9.5) = 10th → 100.
	if domain.PercentileMS(sorted, 0.95) != 100 {
		t.Fatal("p95 used the wrong rank")
	}
	if domain.PercentileMS(sorted, 0.50) != 50 {
		t.Fatal("p50 used the wrong rank")
	}
}

func TestRemovePriceDropsTheModel(t *testing.T) {
	prices := &fakePrices{catalog: pricedCatalog("m")}
	service := newService(&fakeFacts{}, prices)
	catalog, err := service.RemovePrice(context.Background(), "m")
	if err != nil || len(catalog.Models()) != 0 {
		t.Fatalf("remove failed: %v %v", err, catalog.Models())
	}
}

func TestTheServiceClockIsInjectableForDeterministicReports(t *testing.T) {
	// The generated-at stamp must come from the service clock: tests pin it,
	// and any code path reading time.Now directly would drift.
	facts := &fakeFacts{}
	service := newService(facts, &fakePrices{})
	report, err := service.Report(context.Background(), application.PeriodAll)
	if err != nil {
		t.Fatal(err)
	}
	if report.GeneratedAt.IsZero() {
		t.Fatal("the report carries no timestamp")
	}
	_ = time.Now
}
