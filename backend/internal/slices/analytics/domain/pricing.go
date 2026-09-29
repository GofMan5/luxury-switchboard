package domain

import (
	"errors"
	"math"
	"sort"
	"time"
)

// Price is what one model costs, per one million tokens, in the operator's
// own currency of account. Providers bill four kinds of token differently:
// plain input, cached input (cheaper), output, and reasoning (normally the
// output rate). Zero means "not set" for each field independently: a model
// can have an output price and no input price, and its estimate then covers
// exactly the output side — no more.
type Price struct {
	Model       string    `json:"model"`
	Input       float64   `json:"input"`
	CachedInput float64   `json:"cachedInput"`
	Output      float64   `json:"output"`
	Reasoning   float64   `json:"reasoning"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// MaxPriceUSD bounds a single per-million rate. There is no rate on the
// market above a thousand dollars per million tokens; anything past that is
// a typo with a comma in the wrong place, and refusing it is cheaper than
// displaying a spend estimate six orders of magnitude wrong.
const MaxPrice = 100_000.0

func (price Price) Validate() error {
	if price.Model == "" {
		return errors.New("model is empty")
	}
	for _, rate := range []float64{price.Input, price.CachedInput, price.Output, price.Reasoning} {
		if math.IsNaN(rate) || rate < 0 || rate > MaxPrice {
			return errors.New("rate is out of range")
		}
	}
	return nil
}

// Catalog is every price the operator set. It starts empty and stays honest:
// an empty catalog means "no cost estimate", never a guess.
type Catalog struct {
	Prices map[string]Price `json:"prices"`
}

func NewCatalog() Catalog { return Catalog{Prices: map[string]Price{}} }

func (catalog Catalog) Get(model string) (Price, bool) {
	price, ok := catalog.Prices[model]
	return price, ok
}

func (catalog Catalog) Set(price Price) Catalog {
	next := NewCatalog()
	for model, existing := range catalog.Prices {
		next.Prices[model] = existing
	}
	next.Prices[price.Model] = price
	return next
}

func (catalog Catalog) Remove(model string) Catalog {
	next := NewCatalog()
	for existing, value := range catalog.Prices {
		if existing != model {
			next.Prices[existing] = value
		}
	}
	return next
}

// Models returns the catalog's model names in stable order.
func (catalog Catalog) Models() []string {
	models := make([]string, 0, len(catalog.Prices))
	for model := range catalog.Prices {
		models = append(models, model)
	}
	sort.Strings(models)
	return models
}

// EstimateCost prices one row's tokens. Each kind is multiplied by its own
// rate; unset rates (zero) contribute nothing. Reasoning tokens are billed
// at the output rate when no dedicated reasoning rate exists, because that
// is how the market bills them. The estimate is only "complete" when every
// kind the row actually consumed has a rate: a row that used cached input
// without a cached rate is priced partially, and IsPriced stays false so the
// total shows the gap instead of hiding it.
func (catalog Catalog) EstimateCost(volume TokenVolume, model string) (cost float64, isPriced bool) {
	price, ok := catalog.Prices[model]
	if !ok {
		return 0, false
	}
	// Billed input splits: the provider charges cached hits at the cached
	// rate and the rest at the plain input rate.
	plainInput := volume.InputTokens - volume.CachedTokens
	if plainInput < 0 {
		plainInput = 0
	}
	if plainInput > 0 {
		if price.Input <= 0 {
			return 0, false
		}
		cost += float64(plainInput) * price.Input / 1_000_000
	}
	if volume.CachedTokens > 0 {
		if price.CachedInput <= 0 {
			return 0, false
		}
		cost += float64(volume.CachedTokens) * price.CachedInput / 1_000_000
	}
	if volume.OutputTokens > 0 {
		if price.Output <= 0 {
			return 0, false
		}
		cost += float64(volume.OutputTokens) * price.Output / 1_000_000
	}
	// Reasoning is a subset of output and is normally billed at the output
	// rate; a dedicated rate only matters when the provider charges it
	// differently.
	if volume.ReasoningToken > 0 && price.Reasoning > 0 {
		cost += float64(volume.ReasoningToken) * (price.Reasoning - price.Output) / 1_000_000
	}
	return cost, true
}
