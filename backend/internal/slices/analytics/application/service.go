package application

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/analytics/domain"
)

// Period reuses the history slice's periods so both workspaces answer the
// same "since when" question with the same words.
type Period string

const (
	Period24H Period = "24h"
	Period48H Period = "48h"
	Period72H Period = "72h"
	PeriodAll Period = "all"
)

func ValidPeriod(period Period) bool {
	switch period {
	case Period24H, Period48H, Period72H, PeriodAll:
		return true
	default:
		return false
	}
}

func (period Period) Since(now time.Time) int64 {
	switch period {
	case Period24H:
		return now.Add(-24 * time.Hour).UnixMilli()
	case Period48H:
		return now.Add(-48 * time.Hour).UnixMilli()
	case Period72H:
		return now.Add(-72 * time.Hour).UnixMilli()
	default:
		return 0
	}
}

// Facts is the aggregated history the report is built from. It is a port so
// the SQLite details stay behind the application boundary.
type Facts interface {
	Grouped(ctx context.Context, since int64) ([]domain.GroupedRow, error)
	Latencies(ctx context.Context, since int64) ([]domain.LatencySample, error)
	Daily(ctx context.Context, since int64) ([]domain.DailyRow, error)
	Errors(ctx context.Context, since int64) ([]domain.ErrorRow, error)
}

// PriceStore persists the catalog. Prices are not secrets: they are market
// rates, stored in the clear next to the rest of the operator's settings.
type PriceStore interface {
	Load(ctx context.Context) (domain.Catalog, error)
	Save(ctx context.Context, catalog domain.Catalog) error
}

// ProviderNames resolves the current display name for each provider id.
// History rows carry the name the provider had when the request ran; a rename
// must not leave reports reading the old name forever.
type ProviderNames interface {
	Names(ctx context.Context) (map[string]string, error)
}

// Service answers "what happened and what did it cost".
type Service struct {
	facts  Facts
	prices PriceStore
	names  ProviderNames
	now    func() time.Time
	// The catalog is read-modify-write: without the lock, two concurrent
	// price edits race on Load-then-Save and one silently loses. The shell
	// can issue commands concurrently, so the service owns the merge.
	writeMu sync.Mutex
}

func NewService(facts Facts, prices PriceStore, names ProviderNames) *Service {
	return &Service{facts: facts, prices: prices, names: names, now: time.Now}
}

// maxDimensionRows bounds each breakdown list. The report travels in one
// protocol frame: hundreds of distinct model names would not fit and would
// not be read either — the tail past the top rows is noise by definition.
const maxDimensionRows = 60

// latencySampleCap bounds how many completed latencies are read to compute
// percentiles. Retention keeps a month; even a busy relay stays far below
// this, and the report degrades to exact-but-fewer samples rather than
// refusing.
const latencySampleCap = 100_000

// ErrUnavailable is the honest answer when the history database could not
// open: analytics without facts is a dashboard of zeros, and zeros read as
// measurements.
var ErrUnavailable = errors.New("analytics is unavailable")

func (service *Service) Report(ctx context.Context, period Period) (domain.Report, error) {
	if !ValidPeriod(period) {
		return domain.Report{}, errors.New("invalid analytics period")
	}
	if service.facts == nil {
		return domain.Report{}, ErrUnavailable
	}
	since := period.Since(service.now())
	grouped, err := service.facts.Grouped(ctx, since)
	if err != nil {
		return domain.Report{}, err
	}
	latencies, err := service.facts.Latencies(ctx, since)
	if err != nil {
		return domain.Report{}, err
	}
	daily, err := service.facts.Daily(ctx, since)
	if err != nil {
		return domain.Report{}, err
	}
	errorRows, err := service.facts.Errors(ctx, since)
	if err != nil {
		return domain.Report{}, err
	}
	catalog, err := service.prices.Load(ctx)
	if err != nil {
		return domain.Report{}, err
	}
	// Current names are a courtesy layer over the recorded ones: a resolver
	// outage degrades to what history wrote, never to a failed report.
	liveNames := map[string]string{}
	if service.names != nil {
		if resolved, nameErr := service.names.Names(ctx); nameErr == nil {
			liveNames = resolved
		}
	}
	if len(latencies) > latencySampleCap {
		latencies = latencies[:latencySampleCap]
	}
	return domain.Report{
		Period:         string(period),
		GeneratedAt:    service.now(),
		Overview:       buildOverview(grouped, latencies, errorRows, catalog),
		Providers:      buildProviders(grouped, latencies, errorRows, catalog, liveNames),
		Models:         buildModels(grouped, latencies, errorRows, catalog),
		Daily:          buildDaily(daily, catalog),
		Errors:         buildErrors(errorRows),
		UnpricedModels: unpricedModels(grouped, catalog),
	}, nil
}

func (service *Service) Prices(ctx context.Context) (domain.Catalog, error) {
	if service.prices == nil {
		return domain.Catalog{}, ErrUnavailable
	}
	return service.prices.Load(ctx)
}

func (service *Service) SetPrice(ctx context.Context, price domain.Price) (domain.Catalog, error) {
	if service.prices == nil {
		return domain.Catalog{}, ErrUnavailable
	}
	if err := price.Validate(); err != nil {
		return domain.Catalog{}, err
	}
	service.writeMu.Lock()
	defer service.writeMu.Unlock()
	current, err := service.prices.Load(ctx)
	if err != nil {
		return domain.Catalog{}, err
	}
	price.UpdatedAt = service.now()
	updated := current.Set(price)
	if err := service.prices.Save(ctx, updated); err != nil {
		return domain.Catalog{}, err
	}
	return updated, nil
}

func (service *Service) RemovePrice(ctx context.Context, model string) (domain.Catalog, error) {
	if service.prices == nil {
		return domain.Catalog{}, ErrUnavailable
	}
	service.writeMu.Lock()
	defer service.writeMu.Unlock()
	current, err := service.prices.Load(ctx)
	if err != nil {
		return domain.Catalog{}, err
	}
	updated := current.Remove(model)
	if err := service.prices.Save(ctx, updated); err != nil {
		return domain.Catalog{}, err
	}
	return updated, nil
}

// accumulator folds grouped rows, latency samples and error counts into one
// dimension's numbers. Percentiles need the whole sample list; everything
// else is a running sum.
type accumulator struct {
	volume     domain.TokenVolume
	latency    []float64
	latencySum float64
	errors     map[string]int
}

func newAccumulator() *accumulator {
	return &accumulator{errors: map[string]int{}}
}

func (state *accumulator) add(volume domain.TokenVolume) {
	state.volume.Requests += volume.Requests
	state.volume.Completed += volume.Completed
	state.volume.Failed += volume.Failed
	state.volume.Cancelled += volume.Cancelled
	state.volume.Retries += volume.Retries
	state.volume.InputTokens += volume.InputTokens
	state.volume.OutputTokens += volume.OutputTokens
	state.volume.CachedTokens += volume.CachedTokens
	state.volume.ReasoningToken += volume.ReasoningToken
	state.volume.TotalTokens += volume.TotalTokens
	state.volume.GenerationMS += volume.GenerationMS
}

func (state *accumulator) addLatency(latencyMS float64) {
	state.latency = append(state.latency, latencyMS)
	state.latencySum += latencyMS
}

func (state *accumulator) finish() (domain.TokenVolume, []domain.ErrorCount) {
	sort.Float64s(state.latency)
	state.volume.Cost = 0
	state.volume.IsPriced = false
	errs := make([]domain.ErrorCount, 0, len(state.errors))
	for code, count := range state.errors {
		errs = append(errs, domain.ErrorCount{ErrorCode: code, Requests: count})
	}
	sort.Slice(errs, func(i, j int) bool {
		if errs[i].Requests != errs[j].Requests {
			return errs[i].Requests > errs[j].Requests
		}
		return errs[i].ErrorCode < errs[j].ErrorCode
	})
	return state.volume, errs
}

func priceVolume(catalog domain.Catalog, volume domain.TokenVolume, model string) domain.TokenVolume {
	volume.Cost, volume.IsPriced = catalog.EstimateCost(volume, model)
	return volume
}

func buildOverview(grouped []domain.GroupedRow, latencies []domain.LatencySample, errorRows []domain.ErrorRow, catalog domain.Catalog) domain.Overview {
	total := newAccumulator()
	perModel := map[string]*accumulator{}
	for _, row := range grouped {
		total.add(row.TokenVolume)
		state, ok := perModel[row.Model]
		if !ok {
			state = newAccumulator()
			perModel[row.Model] = state
		}
		state.add(row.TokenVolume)
	}
	for _, sample := range latencies {
		total.addLatency(sample.LatencyMS)
	}
	volume, _ := total.finish()
	volume = priceOverviewVolume(catalog, perModel)
	priced := 0
	for model, state := range perModel {
		if _, isPriced := catalog.EstimateCost(state.volume, model); isPriced {
			priced += state.volume.Completed
		}
	}
	errorCounts := map[string]int{}
	for _, row := range errorRows {
		errorCounts[row.ErrorCode] += row.Requests
	}
	errs := make([]domain.ErrorCount, 0, len(errorCounts))
	for code, count := range errorCounts {
		errs = append(errs, domain.ErrorCount{ErrorCode: code, Requests: count})
	}
	sort.Slice(errs, func(i, j int) bool { return errs[i].Requests > errs[j].Requests })
	top := ""
	if len(errs) > 0 {
		top = errs[0].ErrorCode
	}
	return domain.Overview{
		Volume:          volume,
		SuccessRate:     volume.SuccessRate(),
		P50MS:           domain.PercentileMS(total.latency, 0.50),
		P95MS:           domain.PercentileMS(total.latency, 0.95),
		TokensPerSecond: volume.TokensPerSecond(),
		PricedRequests:  priced,
		TopErrorCode:    top,
	}
}

// priceOverviewVolume sums each model's estimated cost into the overview's
// volume. Unlike a single model's row, the total is a blend, so IsPriced
// reports whether every token in the period had a rate, and a partial
// catalog still yields the sum of the priced part.
func priceOverviewVolume(catalog domain.Catalog, perModel map[string]*accumulator) domain.TokenVolume {
	var volume domain.TokenVolume
	allPriced := len(perModel) > 0
	for model, state := range perModel {
		volume = addVolumes(volume, state.volume)
		cost, isPriced := catalog.EstimateCost(state.volume, model)
		volume.Cost += cost
		if !isPriced {
			allPriced = false
		}
	}
	volume.IsPriced = allPriced
	return volume
}

func addVolumes(left, right domain.TokenVolume) domain.TokenVolume {
	left.Requests += right.Requests
	left.Completed += right.Completed
	left.Failed += right.Failed
	left.Cancelled += right.Cancelled
	left.Retries += right.Retries
	left.InputTokens += right.InputTokens
	left.OutputTokens += right.OutputTokens
	left.CachedTokens += right.CachedTokens
	left.ReasoningToken += right.ReasoningToken
	left.TotalTokens += right.TotalTokens
	left.GenerationMS += right.GenerationMS
	return left
}

func buildProviders(grouped []domain.GroupedRow, latencies []domain.LatencySample, errorRows []domain.ErrorRow, catalog domain.Catalog, liveNames map[string]string) []domain.ProviderStats {
	states := map[string]*accumulator{}
	names := map[string]string{}
	for _, row := range grouped {
		state, ok := states[row.ProviderID]
		if !ok {
			state = newAccumulator()
			states[row.ProviderID] = state
		}
		state.add(row.TokenVolume)
		if row.ProviderName != "" {
			names[row.ProviderID] = row.ProviderName
		}
	}
	// The current name wins over every recorded one; the recorded name is the
	// fallback for providers that no longer exist.
	for id, live := range liveNames {
		if live != "" {
			names[id] = live
		}
	}
	for _, sample := range latencies {
		if state, ok := states[sample.ProviderID]; ok {
			state.addLatency(sample.LatencyMS)
		}
	}
	for _, row := range errorRows {
		if state, ok := states[row.ProviderID]; ok {
			state.errors[row.ErrorCode] += row.Requests
		}
	}
	providers := make([]domain.ProviderStats, 0, len(states))
	for id, state := range states {
		volume, errs := state.finish()
		volume = priceBlend(catalog, grouped, id, state)
		providers = append(providers, domain.ProviderStats{
			ID: id, Name: names[id], Volume: volume,
			P50MS:           domain.PercentileMS(state.latency, 0.50),
			P95MS:           domain.PercentileMS(state.latency, 0.95),
			AvgMS:           avg(state.latencySum, len(state.latency)),
			TokensPerSecond: volume.TokensPerSecond(),
			Errors:          errs,
		})
	}
	sort.Slice(providers, func(i, j int) bool {
		return providers[i].Volume.TotalTokens > providers[j].Volume.TotalTokens
	})
	return capList(providers)
}

// priceBlend prices a provider row the same way the overview is priced: each
// (provider, model) sub-row at its own rate, summed. A provider serving one
// model is therefore exact; a provider serving several is the honest sum of
// its parts, not a guess at a blend rate.
func priceBlend(catalog domain.Catalog, grouped []domain.GroupedRow, providerID string, state *accumulator) domain.TokenVolume {
	volume := state.volume
	volume.Cost = 0
	volume.IsPriced = false
	anyRow, allPriced := false, true
	for _, row := range grouped {
		if row.ProviderID != providerID {
			continue
		}
		anyRow = true
		cost, isPriced := catalog.EstimateCost(row.TokenVolume, row.Model)
		volume.Cost += cost
		if !isPriced {
			allPriced = false
		}
	}
	volume.IsPriced = anyRow && allPriced
	return volume
}

func buildModels(grouped []domain.GroupedRow, latencies []domain.LatencySample, errorRows []domain.ErrorRow, catalog domain.Catalog) []domain.ModelStats {
	states := map[string]*accumulator{}
	lastProvider := map[string]string{}
	for _, row := range grouped {
		state, ok := states[row.Model]
		if !ok {
			state = newAccumulator()
			states[row.Model] = state
		}
		state.add(row.TokenVolume)
		if row.ProviderID != "" {
			lastProvider[row.Model] = row.ProviderID
		}
	}
	for _, sample := range latencies {
		if state, ok := states[sample.Model]; ok {
			state.addLatency(sample.LatencyMS)
		}
	}
	for _, row := range errorRows {
		if state, ok := states[row.Model]; ok {
			state.errors[row.ErrorCode] += row.Requests
		}
	}
	models := make([]domain.ModelStats, 0, len(states))
	for model, state := range states {
		volume, errs := state.finish()
		volume = priceVolume(catalog, volume, model)
		models = append(models, domain.ModelStats{
			Model: model, ProviderName: lastProvider[model], Volume: volume,
			P50MS:           domain.PercentileMS(state.latency, 0.50),
			P95MS:           domain.PercentileMS(state.latency, 0.95),
			AvgMS:           avg(state.latencySum, len(state.latency)),
			TokensPerSecond: volume.TokensPerSecond(),
			Errors:          errs,
		})
	}
	sort.Slice(models, func(i, j int) bool {
		return models[i].Volume.TotalTokens > models[j].Volume.TotalTokens
	})
	return capList(models)
}

// buildDaily folds (day, model) rows into days, pricing each model at its
// own rate before the day's total exists, and reporting a day as priced only
// when every model that served it was priced.
func buildDaily(daily []domain.DailyRow, catalog domain.Catalog) []domain.DailyPoint {
	type dayState struct {
		volume    domain.TokenVolume
		allPriced bool
		any       bool
	}
	days := map[string]*dayState{}
	for _, row := range daily {
		state, ok := days[row.Date]
		if !ok {
			state = &dayState{allPriced: true}
			days[row.Date] = state
		}
		state.volume = addVolumes(state.volume, row.TokenVolume)
		state.any = true
		cost, isPriced := catalog.EstimateCost(row.TokenVolume, row.Model)
		state.volume.Cost += cost
		if !isPriced {
			state.allPriced = false
		}
	}
	points := make([]domain.DailyPoint, 0, len(days))
	for date, state := range days {
		volume := state.volume
		volume.IsPriced = state.any && state.allPriced
		points = append(points, domain.DailyPoint{
			Date: date, Volume: volume,
			SuccessRate:     volume.SuccessRate(),
			TokensPerSecond: volume.TokensPerSecond(),
		})
	}
	sort.Slice(points, func(i, j int) bool { return points[i].Date < points[j].Date })
	return points
}

func buildErrors(errorRows []domain.ErrorRow) []domain.ErrorCount {
	counts := map[string]int{}
	for _, row := range errorRows {
		counts[row.ErrorCode] += row.Requests
	}
	errs := make([]domain.ErrorCount, 0, len(counts))
	for code, count := range counts {
		errs = append(errs, domain.ErrorCount{ErrorCode: code, Requests: count})
	}
	sort.Slice(errs, func(i, j int) bool {
		if errs[i].Requests != errs[j].Requests {
			return errs[i].Requests > errs[j].Requests
		}
		return errs[i].ErrorCode < errs[j].ErrorCode
	})
	return capList(errs)
}

func unpricedModels(grouped []domain.GroupedRow, catalog domain.Catalog) []string {
	seen := map[string]bool{}
	models := make([]string, 0)
	for _, row := range grouped {
		if row.Model == "" || seen[row.Model] {
			continue
		}
		if _, isPriced := catalog.EstimateCost(row.TokenVolume, row.Model); !isPriced {
			seen[row.Model] = true
			models = append(models, row.Model)
		}
	}
	sort.Strings(models)
	if len(models) > maxDimensionRows {
		models = models[:maxDimensionRows]
	}
	return models
}

func avg(sum float64, count int) float64 {
	if count == 0 {
		return 0
	}
	return sum / float64(count)
}

func capList[T any](rows []T) []T {
	if len(rows) > maxDimensionRows {
		return rows[:maxDimensionRows]
	}
	return rows
}
