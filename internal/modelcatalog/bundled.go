package modelcatalog

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"time"

	"github.com/ansonnchan/tokenholic/internal/normalize"
)

// rulesVersion is part of the bundled revision. Increment it whenever code
// changes what stored usage resolves or prices to with the same data: the
// alias rules in catalog.go or the rate-card selection in pricing.go. The
// server then reprices stored usage under the new rules.
const rulesVersion = "2"

const (
	// ReasonUnknownModel marks an identifier absent from the catalog.
	ReasonUnknownModel = "unknown_model"
	// ReasonModelUnavailable marks usage whose source reported no model.
	ReasonModelUnavailable = "model_unavailable"
	// ReasonAmbiguousAlias marks a routing alias that names no single model.
	ReasonAmbiguousAlias = "ambiguous_routing_alias"
	// ReasonRoutingLabel marks a reviewed client label with no identifiable model.
	ReasonRoutingLabel = "routing_label"
	// ReasonNoPublishedPrice marks a known model with no reviewed price.
	ReasonNoPublishedPrice = "no_published_price"
	// ReasonPromptLengthUnproven marks usage of a model that charges more
	// above a prompt length, without proof of which side the requests fell on.
	ReasonPromptLengthUnproven = "prompt_length_unproven"
	// ReasonTierRateUnavailable marks usage at a proven service tier or
	// prompt length for which no rate is vendored.
	ReasonTierRateUnavailable = "tier_rate_unavailable"
)

//go:embed data/litellm_prices.json
var bundledSnapshot []byte

//go:embed data/overrides.json
var bundledOverrides []byte

// officialPricingHosts lists the provider-owned hosts an override may cite.
// A price copied from an aggregator or a blog is not acceptable evidence.
var officialPricingHosts = map[string][]string{
	"anthropic": {"platform.claude.com", "docs.anthropic.com", "www.anthropic.com"},
	"openai":    {"developers.openai.com", "platform.openai.com", "openai.com"},
}

// Overrides is the reviewed supplement to the vendored snapshot.
type Overrides struct {
	Prices   []PriceOverride    `json:"prices"`
	Unpriced []UnpricedOverride `json:"unpriced"`
}

// PriceOverride prices a model the snapshot lacks or has wrong. Rates are
// micro-US-dollars per million tokens, copied from the cited official page.
type PriceOverride struct {
	ID           string  `json:"id"`
	Provider     string  `json:"provider"`
	Input        *uint64 `json:"input,omitempty"`
	CacheRead    *uint64 `json:"cache_read,omitempty"`
	CacheWrite   *uint64 `json:"cache_write,omitempty"`
	CacheWrite1h *uint64 `json:"cache_write_1h,omitempty"`
	Output       *uint64 `json:"output,omitempty"`
	SourceURL    string  `json:"source_url"`
	Retrieved    string  `json:"retrieved"`
}

// UnpricedOverride records why an observed identifier is deliberately left
// without a model or price, so it is not mistaken for a missing entry.
type UnpricedOverride struct {
	ID        string `json:"id"`
	Reason    string `json:"reason"`
	Note      string `json:"note"`
	SourceURL string `json:"source_url"`
	Retrieved string `json:"retrieved"`
}

// DefaultCatalog returns the model metadata bundled with this build.
func DefaultCatalog() (Catalog, error) {
	catalog, _, err := Bundled()
	return catalog, err
}

// DefaultPricingRegistry returns the standard first-party API prices bundled
// with this build.
func DefaultPricingRegistry() (*PricingRegistry, error) {
	_, registry, err := Bundled()
	return registry, err
}

// Bundled builds the catalog and price registry from the embedded snapshot
// and overrides. Both carry the same content-derived revision.
func Bundled() (Catalog, *PricingRegistry, error) {
	return Build(bundledSnapshot, bundledOverrides)
}

// Build combines a vendored snapshot with reviewed overrides. The revision is
// derived from both inputs and the rules version, so any change to prices
// or resolution produces a new revision without a manual bump.
func Build(snapshotData, overridesData []byte) (Catalog, *PricingRegistry, error) {
	snapshot, err := DecodeSnapshot(snapshotData)
	if err != nil {
		return Catalog{}, nil, err
	}
	var overrides Overrides
	if err := decodeStrict(overridesData, &overrides); err != nil {
		return Catalog{}, nil, fmt.Errorf("decode price overrides: %w", err)
	}
	effective, err := time.Parse(time.DateOnly, snapshot.Source.Retrieved)
	if err != nil {
		return Catalog{}, nil, err
	}
	snapshotURL := snapshot.Source.URL()
	models := make(map[string]Model, len(snapshot.Models))
	prices := make(map[string]Price, len(snapshot.Models))
	for _, entry := range snapshot.Models {
		model := Model{CanonicalID: entry.ID, Provider: entry.Provider, UnpricedReason: entry.UnpricedReason}
		if entry.UnpricedReason == "" {
			model.PricingKey = entry.Provider + "/" + entry.ID
			for _, card := range snapshotCards(entry) {
				card.Model, card.Provider = model.PricingKey, entry.Provider
				card.SourceURL, card.SourceRevision, card.EffectiveDate = snapshotURL, snapshot.Source.Commit, effective
				prices[card.Key] = card
			}
		}
		models[entry.ID] = model
	}
	for _, override := range overrides.Prices {
		retrieved, err := validateCitation(override.ID, override.Provider, override.SourceURL, override.Retrieved)
		if err != nil {
			return Catalog{}, nil, err
		}
		if override.Input == nil || override.Output == nil {
			return Catalog{}, nil, fmt.Errorf("price override %q requires input and output rates", override.ID)
		}
		key := override.Provider + "/" + override.ID
		models[override.ID] = Model{CanonicalID: override.ID, Provider: override.Provider, PricingKey: key}
		prices[key] = Price{
			Key: key, Provider: override.Provider,
			Rate: Rate{
				InputUncached: override.Input, InputCacheRead: override.CacheRead,
				InputCacheWrite: override.CacheWrite, InputCacheWrite1h: override.CacheWrite1h, Output: override.Output,
			},
			SourceURL: override.SourceURL, SourceRevision: "retrieved-" + override.Retrieved, EffectiveDate: retrieved,
		}
		if retrieved.After(effective) {
			effective = retrieved
		}
	}
	catalog := Catalog{
		AliasRules: AliasRules{ContextMarkers: []string{"[1m]"}, DatedSnapshots: true},
		Unresolvable: []Unresolvable{
			{ID: "auto", Reason: ReasonAmbiguousAlias}, {ID: "default", Reason: ReasonAmbiguousAlias},
			{ID: "router", Reason: ReasonAmbiguousAlias}, {ID: "smart", Reason: ReasonAmbiguousAlias},
		},
	}
	for _, override := range overrides.Unpriced {
		if override.ID == "" || override.Reason == "" || override.Note == "" {
			return Catalog{}, nil, fmt.Errorf("unpriced override %q requires an ID, reason, and note", override.ID)
		}
		if _, err := time.Parse(time.DateOnly, override.Retrieved); err != nil {
			return Catalog{}, nil, fmt.Errorf("unpriced override %q retrieval date: %w", override.ID, err)
		}
		if parsed, err := url.Parse(override.SourceURL); err != nil || parsed.Scheme != "https" || parsed.Host == "" {
			return Catalog{}, nil, fmt.Errorf("unpriced override %q requires an HTTPS source URL", override.ID)
		}
		if _, priced := models[override.ID]; priced {
			return Catalog{}, nil, fmt.Errorf("unpriced override %q is also a catalog model", override.ID)
		}
		catalog.Unresolvable = append(catalog.Unresolvable, Unresolvable{ID: override.ID, Reason: override.Reason})
	}
	for _, model := range models {
		catalog.Models = append(catalog.Models, model)
	}
	sort.Slice(catalog.Models, func(i, j int) bool { return catalog.Models[i].CanonicalID < catalog.Models[j].CanonicalID })
	priceList := make([]Price, 0, len(prices))
	for _, price := range prices {
		priceList = append(priceList, price)
	}

	digest := sha256.New()
	for _, part := range [][]byte{[]byte(rulesVersion), snapshotData, overridesData} {
		// Length prefixes keep the three inputs from running into each other.
		fmt.Fprintf(digest, "%d\x00", len(part))
		digest.Write(part)
	}
	catalog.Revision = effective.Format(time.DateOnly) + "-" + hex.EncodeToString(digest.Sum(nil))[:12]
	registry, err := NewPricingRegistry(catalog.Revision, priceList)
	if err != nil {
		return Catalog{}, nil, err
	}
	return catalog, registry, nil
}

// snapshotCards expands one snapshot model into its rate cards. The standard
// card keeps the model's bare key, so usage priced before tiers existed keeps
// its key; every other card appends what selects it.
func snapshotCards(entry SnapshotModel) []Price {
	var threshold uint64
	if entry.PromptThreshold != nil {
		threshold = *entry.PromptThreshold
	}
	base := entry.Provider + "/" + entry.ID
	card := func(tier normalize.ServiceTier, long bool, rates SnapshotRates) Price {
		key := base
		if tier != normalize.ServiceTierStandard {
			key += ":" + string(tier)
		}
		if long {
			key += fmt.Sprintf(":above-%dk", threshold/1000)
		}
		return Price{
			Key: key, ServiceTier: tier, LongPrompt: long, PromptThreshold: threshold,
			Rate: Rate{
				InputUncached: rates.Input, InputCacheRead: rates.CacheRead,
				InputCacheWrite: rates.CacheWrite, InputCacheWrite1h: rates.CacheWrite1h, Output: rates.Output,
			},
		}
	}
	cards := []Price{card(normalize.ServiceTierStandard, false, entry.SnapshotRates)}
	if entry.LongPrompt != nil {
		cards = append(cards, card(normalize.ServiceTierStandard, true, *entry.LongPrompt))
	}
	for name, tier := range entry.Tiers {
		cards = append(cards, card(normalize.ServiceTier(name), false, tier.SnapshotRates))
		if tier.LongPrompt != nil {
			cards = append(cards, card(normalize.ServiceTier(name), true, *tier.LongPrompt))
		}
	}
	return cards
}

// validateCitation enforces that a manual price names its provider's own
// pricing page and the day it was read.
func validateCitation(identifier, provider, source, retrieved string) (time.Time, error) {
	if identifier == "" || provider == "" {
		return time.Time{}, errors.New("price override requires a model ID and provider")
	}
	hosts, known := officialPricingHosts[provider]
	if !known {
		return time.Time{}, fmt.Errorf("price override %q names provider %q, which has no reviewed official pricing host", identifier, provider)
	}
	parsed, err := url.Parse(source)
	if err != nil || parsed.Scheme != "https" {
		return time.Time{}, fmt.Errorf("price override %q requires an HTTPS source URL", identifier)
	}
	official := false
	for _, host := range hosts {
		official = official || parsed.Host == host
	}
	if !official {
		return time.Time{}, fmt.Errorf("price override %q cites %q, which is not an official %s pricing host", identifier, parsed.Host, provider)
	}
	date, err := time.Parse(time.DateOnly, retrieved)
	if err != nil {
		return time.Time{}, fmt.Errorf("price override %q retrieval date: %w", identifier, err)
	}
	return date, nil
}
