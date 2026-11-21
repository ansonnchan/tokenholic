package modelcatalog

import (
	"strings"
	"testing"
	"time"

	"github.com/ansonnchan/tokenholic/internal/normalize"
)

// The synthetic tiered model charges, per million tokens of input / cache
// read / output:
//
//	standard  $4  / $0.40 / $20     above 272K: $8  / $0.80 / $30
//	priority  $8  / $0.80 / $40     above 272K: $16 / $1.60 / $60
//	flex      $2  / $0.20 / $10     above 272K: not published
//
// "flat" has one card at $1 / $0.10 / $5, and "quick" adds a fast card at
// twice that. Every case prices one million tokens of each category.
func TestDeriveSelectsTheProvenRateCard(t *testing.T) {
	t.Parallel()
	deriver := tieredDeriver(t)
	million := uint64(1_000_000)
	tokens := normalize.TokenCounts{
		InputUncached: &million, InputCacheRead: &million, InputCacheWrite: new(uint64),
		InputCacheWrite5m: new(uint64), InputCacheWrite1h: new(uint64), Output: &million,
	}
	tests := []struct {
		name   string
		model  string
		tier   normalize.ServiceTier
		band   normalize.PromptBand
		key    string
		micro  uint64
		reason string
	}{
		{name: "standard, short prompt", model: "tiered", tier: normalize.ServiceTierStandard, band: normalize.PromptBandUpTo100K, key: "provider/tiered", micro: 24_400_000},
		{name: "standard, on the threshold", model: "tiered", tier: normalize.ServiceTierStandard, band: normalize.PromptBandUpTo272K, key: "provider/tiered", micro: 24_400_000},
		{name: "standard, long prompt", model: "tiered", tier: normalize.ServiceTierStandard, band: normalize.PromptBandOver272K, key: "provider/tiered:above-272k", micro: 38_800_000},
		{name: "priority, short prompt", model: "tiered", tier: normalize.ServiceTierPriority, band: normalize.PromptBandUpTo200K, key: "provider/tiered:priority", micro: 48_800_000},
		{name: "priority, long prompt", model: "tiered", tier: normalize.ServiceTierPriority, band: normalize.PromptBandOver272K, key: "provider/tiered:priority:above-272k", micro: 77_600_000},
		{name: "flex, short prompt", model: "tiered", tier: normalize.ServiceTierFlex, band: normalize.PromptBandUpTo100K, key: "provider/tiered:flex", micro: 12_200_000},
		{name: "flex, long prompt has no published card", model: "tiered", tier: normalize.ServiceTierFlex, band: normalize.PromptBandOver272K, reason: ReasonTierRateUnavailable},
		{name: "batch has no published card", model: "tiered", tier: normalize.ServiceTierBatch, band: normalize.PromptBandUpTo100K, reason: ReasonTierRateUnavailable},
		// The decided default: an unproven tier is priced as standard.
		{name: "unknown tier, short prompt", model: "tiered", band: normalize.PromptBandUpTo100K, key: "provider/tiered", micro: 24_400_000},
		{name: "unknown tier, long prompt", model: "tiered", band: normalize.PromptBandOver272K, key: "provider/tiered:above-272k", micro: 38_800_000},
		// Prompt length has no default side.
		{name: "standard, unproven prompt length", model: "tiered", tier: normalize.ServiceTierStandard, reason: ReasonPromptLengthUnproven},
		{name: "priority, unproven prompt length", model: "tiered", tier: normalize.ServiceTierPriority, reason: ReasonPromptLengthUnproven},
		{name: "no evidence at all", model: "tiered", reason: ReasonPromptLengthUnproven},
		// A model with one card needs no evidence, and ignores a band.
		{name: "flat, no evidence", model: "flat", key: "provider/flat", micro: 6_100_000},
		{name: "flat, standard and long prompt", model: "flat", tier: normalize.ServiceTierStandard, band: normalize.PromptBandOver272K, key: "provider/flat", micro: 6_100_000},
		{name: "flat, priority is not published", model: "flat", tier: normalize.ServiceTierPriority, reason: ReasonTierRateUnavailable},
		{name: "quick, fast mode", model: "quick", tier: normalize.ServiceTierFast, key: "provider/quick:fast", micro: 12_200_000},
		{name: "quick, standard", model: "quick", tier: normalize.ServiceTierStandard, key: "provider/quick", micro: 6_100_000},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			derivation, err := deriver.Derive(Usage{
				ObservedModelID: tt.model, ModelEvidence: normalize.ModelReported, Tokens: tokens,
				ServiceTier: tt.tier, PromptBand: tt.band,
			})
			if err != nil {
				t.Fatal(err)
			}
			if derivation.Resolution.CanonicalID == nil || *derivation.Resolution.CanonicalID != tt.model {
				t.Fatalf("canonical = %v, want %q: pricing evidence must never change resolution", derivation.Resolution.CanonicalID, tt.model)
			}
			if tt.reason != "" {
				if derivation.Resolution.PricingKey != nil || derivation.Resolution.UnpricedReason != tt.reason ||
					derivation.EstimateMicroUSD != nil || derivation.EstimateCoverage != CoverageUnavailable ||
					derivation.PricedTokens != 0 || derivation.UnpricedTokens != 3*million {
					t.Fatalf("Derive() = %#v (reason %q), want unpriced for %q", derivation, derivation.Resolution.UnpricedReason, tt.reason)
				}
				return
			}
			if derivation.Resolution.PricingKey == nil || *derivation.Resolution.PricingKey != tt.key || derivation.Resolution.UnpricedReason != "" {
				t.Fatalf("pricing key = %v (reason %q), want %q", derivation.Resolution.PricingKey, derivation.Resolution.UnpricedReason, tt.key)
			}
			if derivation.EstimateMicroUSD == nil || *derivation.EstimateMicroUSD != tt.micro ||
				derivation.EstimateCoverage != CoverageComplete || derivation.PricedTokens != 3*million || derivation.UnpricedTokens != 0 {
				t.Fatalf("estimate = %v %s priced %d unpriced %d, want %d complete", derivation.EstimateMicroUSD,
					derivation.EstimateCoverage, derivation.PricedTokens, derivation.UnpricedTokens, tt.micro)
			}
		})
	}
}

// The bundled cards are checked against hand-worked provider prices. Codex
// reports no cache writes, so its cases price one million each of input,
// cache read, and output; Claude cases add one million of each cache write.
func TestBundledTierGoldenCosts(t *testing.T) {
	t.Parallel()
	deriver, err := BundledDeriver()
	if err != nil {
		t.Fatal(err)
	}
	million, two := uint64(1_000_000), uint64(2_000_000)
	codex := normalize.TokenCounts{
		InputUncached: &million, InputCacheRead: &million, InputCacheWrite: new(uint64),
		InputCacheWrite5m: new(uint64), InputCacheWrite1h: new(uint64), Output: &million,
	}
	claude := normalize.TokenCounts{
		InputUncached: &million, InputCacheRead: &million, InputCacheWrite: &two,
		InputCacheWrite5m: &million, InputCacheWrite1h: &million, Output: &million,
	}
	tests := []struct {
		name   string
		model  string
		tokens normalize.TokenCounts
		tier   normalize.ServiceTier
		band   normalize.PromptBand
		micro  uint64
		reason string
	}{
		// gpt-5.6-sol: $4 / $0.40 / $20 standard, doubled for priority and
		// halved for flex; above 272K input doubles and output is 1.5x.
		{name: "codex standard", model: "gpt-5.6-sol", tokens: codex, tier: normalize.ServiceTierStandard, band: normalize.PromptBandUpTo272K, micro: 24_400_000},
		{name: "codex priority", model: "gpt-5.6-sol", tokens: codex, tier: normalize.ServiceTierPriority, band: normalize.PromptBandUpTo200K, micro: 48_800_000},
		{name: "codex flex", model: "gpt-5.6-sol", tokens: codex, tier: normalize.ServiceTierFlex, band: normalize.PromptBandUpTo100K, micro: 12_200_000},
		{name: "codex standard above 272K", model: "gpt-5.6-sol", tokens: codex, tier: normalize.ServiceTierStandard, band: normalize.PromptBandOver272K, micro: 38_800_000},
		{name: "codex priority above 272K", model: "gpt-5.6-sol", tokens: codex, tier: normalize.ServiceTierPriority, band: normalize.PromptBandOver272K, micro: 77_600_000},
		{name: "codex flex above 272K", model: "gpt-5.6-sol", tokens: codex, tier: normalize.ServiceTierFlex, band: normalize.PromptBandOver272K, micro: 19_400_000},
		{name: "codex first turn has no tier", model: "gpt-5.6-sol", tokens: codex, band: normalize.PromptBandUpTo100K, micro: 24_400_000},
		{name: "codex legacy rollout has no prompt size", model: "gpt-5.6-sol", tokens: codex, reason: ReasonPromptLengthUnproven},
		// claude-haiku-5-5: $0.10 + $0.01 + $0.125 + $0.20 + $0.50 up to 100K
		// and five times that above it.
		{name: "haiku up to 100K", model: "claude-haiku-5-5", tokens: claude, tier: normalize.ServiceTierStandard, band: normalize.PromptBandUpTo100K, micro: 935_000},
		{name: "haiku above 100K", model: "claude-haiku-5-5", tokens: claude, tier: normalize.ServiceTierStandard, band: normalize.PromptBandUpTo200K, micro: 4_675_000},
		{name: "haiku without a prompt size", model: "claude-haiku-5-5", tokens: claude, tier: normalize.ServiceTierStandard, reason: ReasonPromptLengthUnproven},
		// claude-opus-5-5: $4 + $0.20 + $5 + $8 + $20 standard; fast mode is
		// twice every rate at any prompt length.
		{name: "opus standard", model: "claude-opus-5-5", tokens: claude, tier: normalize.ServiceTierStandard, band: normalize.PromptBandOver272K, micro: 37_200_000},
		{name: "opus fast mode", model: "claude-opus-5-5", tokens: claude, tier: normalize.ServiceTierFast, band: normalize.PromptBandOver272K, micro: 74_400_000},
		{name: "opus without evidence", model: "claude-opus-5-5", tokens: claude, micro: 37_200_000},
		{name: "opus priority tier has no published rate", model: "claude-opus-5-5", tokens: claude, tier: normalize.ServiceTierPriority, reason: ReasonTierRateUnavailable},
		{name: "opus batch is not vendored", model: "claude-opus-5-5", tokens: claude, tier: normalize.ServiceTierBatch, reason: ReasonTierRateUnavailable},
		// claude-sonnet-5-5 publishes no fast mode.
		{name: "sonnet fast mode is not published", model: "claude-sonnet-5-5", tokens: claude, tier: normalize.ServiceTierFast, reason: ReasonTierRateUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			derivation, err := deriver.Derive(Usage{
				ObservedModelID: tt.model, ModelEvidence: normalize.ModelReported, Tokens: tt.tokens,
				ServiceTier: tt.tier, PromptBand: tt.band,
			})
			if err != nil {
				t.Fatal(err)
			}
			if tt.reason != "" {
				if derivation.EstimateMicroUSD != nil || derivation.Resolution.UnpricedReason != tt.reason {
					t.Fatalf("Derive() estimate %v reason %q, want unpriced for %q", derivation.EstimateMicroUSD, derivation.Resolution.UnpricedReason, tt.reason)
				}
				return
			}
			if derivation.EstimateMicroUSD == nil || *derivation.EstimateMicroUSD != tt.micro || derivation.EstimateCoverage != CoverageComplete {
				t.Fatalf("estimate = %v %s (reason %q), want %d complete", derivation.EstimateMicroUSD, derivation.EstimateCoverage, derivation.Resolution.UnpricedReason, tt.micro)
			}
		})
	}
}

func TestPricingRegistryRejectsInconsistentRateCards(t *testing.T) {
	t.Parallel()
	card := func(key, model string, tier normalize.ServiceTier, long bool, threshold uint64) Price {
		rate := uint64(1_000_000)
		return Price{
			Key: key, Provider: "provider", Model: model, ServiceTier: tier, LongPrompt: long, PromptThreshold: threshold,
			Rate: Rate{InputUncached: &rate}, SourceURL: "https://provider.invalid/pricing", SourceRevision: "synthetic",
			EffectiveDate: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		}
	}
	tests := []struct {
		name    string
		prices  []Price
		wantErr string
	}{
		{name: "tier card without a standard card", prices: []Price{card("provider/a:priority", "provider/a", normalize.ServiceTierPriority, false, 0)}, wantErr: "no standard rate card"},
		{name: "two cards for one tier", prices: []Price{
			card("provider/a", "provider/a", normalize.ServiceTierStandard, false, 0),
			card("provider/a:again", "provider/a", normalize.ServiceTierStandard, false, 0),
		}, wantErr: "duplicates a rate card"},
		{name: "cards disagree on the threshold", prices: []Price{
			card("provider/a", "provider/a", normalize.ServiceTierStandard, false, 272_000),
			card("provider/a:above", "provider/a", normalize.ServiceTierStandard, true, 100_000),
		}, wantErr: "prompt threshold"},
		{name: "long card without a threshold", prices: []Price{card("provider/a", "provider/a", normalize.ServiceTierStandard, true, 0)}, wantErr: "without a threshold"},
		{name: "unnamed tier", prices: []Price{card("provider/a", "provider/a", "turbo", false, 0)}, wantErr: "service tier"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := NewPricingRegistry("synthetic-r1", tt.prices); err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("NewPricingRegistry() error = %v, want substring %q", err, tt.wantErr)
			}
		})
	}
}

// Every bundled model keeps its bare standard key, so usage priced before
// rate cards existed is still found under the key it was stored with.
func TestBundledStandardCardsKeepTheModelKey(t *testing.T) {
	t.Parallel()
	catalog, registry, err := Bundled()
	if err != nil {
		t.Fatal(err)
	}
	tiers := map[normalize.ServiceTier]int{}
	for _, price := range registry.Prices {
		tiers[price.ServiceTier]++
		if (price.Key == price.Model) != (price.ServiceTier == normalize.ServiceTierStandard && !price.LongPrompt) {
			t.Errorf("card %q of %q: only the standard card may use the bare model key", price.Key, price.Model)
		}
	}
	for _, model := range catalog.Models {
		if model.PricingKey == "" {
			continue
		}
		if price, found := registry.Lookup(model.PricingKey); !found || price.ServiceTier != normalize.ServiceTierStandard || price.LongPrompt {
			t.Errorf("model %q: standard card = %#v, %t", model.CanonicalID, price, found)
		}
	}
	for _, tier := range []normalize.ServiceTier{normalize.ServiceTierPriority, normalize.ServiceTierFlex, normalize.ServiceTierFast} {
		if tiers[tier] == 0 {
			t.Errorf("no bundled card for the %q tier", tier)
		}
	}
	if tiers[normalize.ServiceTierBatch] != 0 {
		t.Error("batch cards are deliberately not vendored")
	}
}

func tieredDeriver(t *testing.T) *Deriver {
	t.Helper()
	rate := func(v uint64) *uint64 { return &v }
	card := func(key, model string, tier normalize.ServiceTier, long bool, threshold, input, read, output uint64) Price {
		return Price{
			Key: key, Provider: "provider", Model: model, ServiceTier: tier, LongPrompt: long, PromptThreshold: threshold,
			Rate:      Rate{InputUncached: rate(input), InputCacheRead: rate(read), Output: rate(output)},
			SourceURL: "https://provider.invalid/pricing", SourceRevision: "synthetic", EffectiveDate: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		}
	}
	const tiered, limit = "provider/tiered", 272_000
	registry, err := NewPricingRegistry("synthetic-r1", []Price{
		card(tiered, tiered, normalize.ServiceTierStandard, false, limit, 4_000_000, 400_000, 20_000_000),
		card(tiered+":above-272k", tiered, normalize.ServiceTierStandard, true, limit, 8_000_000, 800_000, 30_000_000),
		card(tiered+":priority", tiered, normalize.ServiceTierPriority, false, limit, 8_000_000, 800_000, 40_000_000),
		card(tiered+":priority:above-272k", tiered, normalize.ServiceTierPriority, true, limit, 16_000_000, 1_600_000, 60_000_000),
		card(tiered+":flex", tiered, normalize.ServiceTierFlex, false, limit, 2_000_000, 200_000, 10_000_000),
		card("provider/flat", "", "", false, 0, 1_000_000, 100_000, 5_000_000),
		card("provider/quick", "provider/quick", normalize.ServiceTierStandard, false, 0, 1_000_000, 100_000, 5_000_000),
		card("provider/quick:fast", "provider/quick", normalize.ServiceTierFast, false, 0, 2_000_000, 200_000, 10_000_000),
	})
	if err != nil {
		t.Fatal(err)
	}
	deriver, err := NewDeriver(Catalog{Revision: "synthetic-r1", Models: []Model{
		{CanonicalID: "tiered", Provider: "provider", PricingKey: tiered},
		{CanonicalID: "flat", Provider: "provider", PricingKey: "provider/flat"},
		{CanonicalID: "quick", Provider: "provider", PricingKey: "provider/quick"},
	}}, registry)
	if err != nil {
		t.Fatal(err)
	}
	return deriver
}
