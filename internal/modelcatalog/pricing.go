package modelcatalog

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"time"

	"github.com/ansonnchan/tokenholic/internal/normalize"
)

const tokensPerMillion = 1_000_000

// Rate is a standard API token price in micro-US-dollars per million tokens.
// InputCacheWrite is the provider's base cache-write rate, which is the
// 5-minute lifetime for Anthropic; InputCacheWrite1h is the 1-hour rate. A nil
// rate means the provider publishes none for that category.
type Rate struct {
	InputUncached     *uint64
	InputCacheRead    *uint64
	InputCacheWrite   *uint64
	InputCacheWrite1h *uint64
	Output            *uint64
}

// Checksum returns a stable digest of the reviewed revision and sorted prices.
func (r *PricingRegistry) Checksum() ([32]byte, error) {
	encoded, err := json.Marshal(struct {
		Revision string  `json:"revision"`
		Prices   []Price `json:"prices"`
	}{Revision: r.Revision, Prices: r.Prices})
	if err != nil {
		return [32]byte{}, fmt.Errorf("encode pricing registry checksum: %w", err)
	}
	return sha256.Sum256(encoded), nil
}

// EffectiveAt returns when the complete revision became effective.
func (r *PricingRegistry) EffectiveAt() time.Time {
	var effective time.Time
	for _, price := range r.Prices {
		if price.EffectiveDate.After(effective) {
			effective = price.EffectiveDate
		}
	}
	return effective
}

// Price is one rate card: what a provider charges for one model at one
// service tier on one side of its prompt-length threshold.
//
// Model groups the cards of one model and is the key of its standard card.
// PromptThreshold is the prompt size, in tokens, above which the provider
// charges the model's LongPrompt cards instead; it is zero when the price does
// not depend on prompt length and is the same on every card of a model.
type Price struct {
	Key             string
	Provider        string
	Model           string
	ServiceTier     normalize.ServiceTier
	LongPrompt      bool
	PromptThreshold uint64
	Rate            Rate
	SourceURL       string
	SourceRevision  string
	EffectiveDate   time.Time
}

// PricingRegistry is an immutable API-equivalent price revision.
type PricingRegistry struct {
	Revision string
	Prices   []Price
	byKey    map[string]Price
	byModel  map[string]*modelCards
}

// modelCards indexes one model's rate cards by the evidence that selects them.
type modelCards struct {
	threshold uint64
	cards     map[cardID]Price
}

type cardID struct {
	tier normalize.ServiceTier
	long bool
}

// EstimateLabel names every cost value tokenholic shows. It is never spending.
const EstimateLabel = "API-equivalent estimate"

// Estimate is explicitly an API-equivalent estimate, not actual spending.
//
// PricedTokens and UnpricedTokens partition the known tokens by whether a rate
// applied. LowerBound is true when the real standard-rate cost may be higher:
// some tokens had no rate, a cache write had an unknown lifetime and was
// priced at the base rate, or a billable category was unavailable.
type Estimate struct {
	MicroUSD        uint64
	PricedTokens    uint64
	UnpricedTokens  uint64
	LowerBound      bool
	PricingRevision string
	Label           string
}

// NewPricingRegistry validates a versioned price set. A price without a Model
// or ServiceTier is its model's standard card.
func NewPricingRegistry(revision string, prices []Price) (*PricingRegistry, error) {
	if revision == "" {
		return nil, errors.New("pricing revision is required")
	}
	registry := &PricingRegistry{
		Revision: revision, Prices: append([]Price(nil), prices...),
		byKey: make(map[string]Price), byModel: make(map[string]*modelCards),
	}
	sort.Slice(registry.Prices, func(i, j int) bool { return registry.Prices[i].Key < registry.Prices[j].Key })
	for index := range registry.Prices {
		price := &registry.Prices[index]
		if price.Key == "" || price.Provider == "" || price.SourceURL == "" || price.SourceRevision == "" || price.EffectiveDate.IsZero() {
			return nil, fmt.Errorf("pricing entry %q is missing source metadata", price.Key)
		}
		if price.Model == "" {
			price.Model = price.Key
		}
		if price.ServiceTier == normalize.ServiceTierUnknown {
			price.ServiceTier = normalize.ServiceTierStandard
		}
		if !price.ServiceTier.Valid() {
			return nil, fmt.Errorf("pricing entry %q names service tier %q", price.Key, price.ServiceTier)
		}
		if price.LongPrompt && price.PromptThreshold == 0 {
			return nil, fmt.Errorf("pricing entry %q is a long-prompt card without a threshold", price.Key)
		}
		if _, exists := registry.byKey[price.Key]; exists {
			return nil, fmt.Errorf("pricing key %q is duplicated", price.Key)
		}
		registry.byKey[price.Key] = *price
		model := registry.byModel[price.Model]
		if model == nil {
			model = &modelCards{threshold: price.PromptThreshold, cards: make(map[cardID]Price)}
			registry.byModel[price.Model] = model
		}
		if model.threshold != price.PromptThreshold {
			return nil, fmt.Errorf("pricing entry %q disagrees with its model's prompt threshold", price.Key)
		}
		id := cardID{tier: price.ServiceTier, long: price.LongPrompt}
		if _, exists := model.cards[id]; exists {
			return nil, fmt.Errorf("pricing entry %q duplicates a rate card of %q", price.Key, price.Model)
		}
		model.cards[id] = *price
	}
	for name, model := range registry.byModel {
		standard, found := model.cards[cardID{tier: normalize.ServiceTierStandard}]
		if !found || standard.Key != name {
			return nil, fmt.Errorf("pricing model %q has no standard rate card under its own key", name)
		}
	}
	return registry, nil
}

// Select returns the rate card that a bucket's pricing evidence selects for
// model, the key of its standard card. When no card can be proven or none is
// vendored, ok is false and reason says why.
//
//   - Service tier: a proven tier selects its own card, and a proven tier with
//     no vendored card stays unpriced. A tier the source did not prove is
//     priced on the standard card. That is a decided default, not evidence:
//     standard is what a provider applies when a request names no tier, and
//     the alternative leaves every first turn of a Codex thread unpriced.
//   - Prompt length: a model with a threshold is priced only when the band
//     proves which side every request fell on. There is no default side.
func (r *PricingRegistry) Select(model string, tier normalize.ServiceTier, band normalize.PromptBand) (price Price, reason string, ok bool) {
	cards, found := r.byModel[model]
	if !found {
		return Price{}, ReasonNoPublishedPrice, false
	}
	if tier == normalize.ServiceTierUnknown {
		tier = normalize.ServiceTierStandard
	}
	long := false
	if cards.threshold > 0 {
		switch band.Side(cards.threshold) {
		case normalize.PromptSideAtOrBelow:
		case normalize.PromptSideAbove:
			long = true
		default:
			return Price{}, ReasonPromptLengthUnproven, false
		}
	}
	price, found = cards.cards[cardID{tier: tier, long: long}]
	if !found {
		return Price{}, ReasonTierRateUnavailable, false
	}
	return price, "", true
}

// Lookup returns one price without implying that local usage incurred a bill.
func (r *PricingRegistry) Lookup(key string) (Price, bool) {
	price, found := r.byKey[key]
	return price, found
}

// EstimateTokens prices the five billable categories: uncached input, cache
// read, base (5-minute) cache write, 1-hour cache write, and output. Reasoning
// output is already inside output and is never priced separately.
//
// A cache write whose lifetime is unknown is priced at the base rate and makes
// the estimate a lower bound. Known tokens without a rate are counted as
// unpriced instead of being dropped or guessed. available is false when the
// key has no price or no known category could be priced.
func (r *PricingRegistry) EstimateTokens(key string, tokens normalize.TokenCounts) (Estimate, bool, error) {
	price, found := r.Lookup(key)
	if !found {
		return Estimate{}, false, nil
	}
	unknownLifetime := tokens.CacheWriteUnknownTTL()
	estimate := Estimate{PricingRevision: r.Revision, Label: EstimateLabel}
	estimate.LowerBound = tokens.InputUncached == nil || tokens.InputCacheRead == nil ||
		tokens.InputCacheWrite == nil || tokens.Output == nil ||
		unknownLifetime != nil && *unknownLifetime > 0
	parts := []struct {
		count *uint64
		rate  *uint64
	}{
		{tokens.InputUncached, price.Rate.InputUncached},
		{tokens.InputCacheRead, price.Rate.InputCacheRead},
		{tokens.InputCacheWrite5m, price.Rate.InputCacheWrite},
		{tokens.InputCacheWrite1h, price.Rate.InputCacheWrite1h},
		{unknownLifetime, price.Rate.InputCacheWrite},
		{tokens.Output, price.Rate.Output},
	}
	total, priced, unpriced := new(big.Int), new(big.Int), new(big.Int)
	available := false
	for _, part := range parts {
		if part.count == nil {
			continue
		}
		count := new(big.Int).SetUint64(*part.count)
		if part.rate == nil {
			unpriced.Add(unpriced, count)
			continue
		}
		available = true
		priced.Add(priced, count)
		total.Add(total, count.Mul(count, new(big.Int).SetUint64(*part.rate)))
	}
	// One floor per estimate keeps sub-micro-dollar remainders from compounding.
	total.Div(total, big.NewInt(tokensPerMillion))
	if !total.IsUint64() || !priced.IsUint64() || !unpriced.IsUint64() {
		return Estimate{}, false, errors.New("API-equivalent estimate overflows uint64")
	}
	estimate.MicroUSD, estimate.PricedTokens, estimate.UnpricedTokens = total.Uint64(), priced.Uint64(), unpriced.Uint64()
	if estimate.UnpricedTokens > 0 {
		estimate.LowerBound = true
	}
	return estimate, available, nil
}
