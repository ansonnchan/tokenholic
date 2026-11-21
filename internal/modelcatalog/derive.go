package modelcatalog

import (
	"errors"
	"fmt"

	"github.com/ansonnchan/tokenholic/internal/normalize"
)

// Coverage says how much of a derived estimate is backed by evidence.
type Coverage string

const (
	// CoverageComplete means every known billable token was priced at a
	// proven rate and no billable category was missing.
	CoverageComplete Coverage = "complete"
	// CoveragePartial means the estimate is a lower bound.
	CoveragePartial Coverage = "partial"
	// CoverageUnavailable means no token could be priced.
	CoverageUnavailable Coverage = "unavailable"
)

// Derivation is everything one catalog revision says about a usage bucket:
// which model it resolves to, what it would cost at standard API rates, and
// how many of its tokens that estimate covers.
//
// PricedTokens and UnpricedTokens always sum to the bucket's known total, so
// cost coverage can be computed without re-reading the token categories.
type Derivation struct {
	Resolution       Resolution
	EstimateMicroUSD *uint64
	EstimateCoverage Coverage
	PricedTokens     uint64
	UnpricedTokens   uint64
	Revision         string
}

// Deriver applies one catalog and price revision. The API uses it as the
// authority for stored usage; the CLI uses the same code for its offline
// preview, so the two agree whenever their bundled revisions match.
type Deriver struct {
	resolver *Resolver
	pricing  *PricingRegistry
}

// NewDeriver pairs a catalog with the price registry of the same revision.
func NewDeriver(catalog Catalog, pricing *PricingRegistry) (*Deriver, error) {
	if pricing == nil {
		return nil, errors.New("model derivation requires a pricing registry")
	}
	if catalog.Revision != pricing.Revision {
		return nil, fmt.Errorf("catalog revision %q and pricing revision %q differ", catalog.Revision, pricing.Revision)
	}
	resolver, err := NewResolver(catalog)
	if err != nil {
		return nil, err
	}
	return &Deriver{resolver: resolver, pricing: pricing}, nil
}

// BundledDeriver returns the deriver for the data embedded in this build.
func BundledDeriver() (*Deriver, error) {
	catalog, pricing, err := Bundled()
	if err != nil {
		return nil, err
	}
	return NewDeriver(catalog, pricing)
}

// Revision identifies the catalog and prices behind every derivation.
func (d *Deriver) Revision() string {
	return d.pricing.Revision
}

// Usage is the evidence one derivation depends on: what the source observed
// about a bucket and nothing concluded from it.
type Usage struct {
	ObservedModelID string
	ModelEvidence   normalize.ModelResolution
	// Tokens holds the bucket totals. nil categories are unavailable, not zero.
	Tokens normalize.TokenCounts
	// ServiceTier and PromptBand select among a model's rate cards. Empty
	// values mean the source did not prove them.
	ServiceTier normalize.ServiceTier
	PromptBand  normalize.PromptBand
	// Partial reports that some billable category sums only part of the
	// bucket, which makes any estimate a lower bound even when every known
	// token has a rate.
	Partial bool
}

// Derive resolves the observed model, selects the rate card the pricing
// evidence proves, and prices the bucket's token totals. When no card can be
// proven the bucket resolves but stays unpriced, with the reason and without a
// pricing key.
func (d *Deriver) Derive(usage Usage) (Derivation, error) {
	derivation := Derivation{
		Resolution:       d.resolver.Resolve(usage.ObservedModelID, usage.ModelEvidence),
		EstimateCoverage: CoverageUnavailable, Revision: d.pricing.Revision,
	}
	total, err := usage.Tokens.KnownTotal()
	if err != nil {
		return Derivation{}, err
	}
	if total != nil {
		derivation.UnpricedTokens = *total
	}
	if derivation.Resolution.PricingKey == nil {
		return derivation, nil
	}
	card, reason, proven := d.pricing.Select(*derivation.Resolution.PricingKey, usage.ServiceTier, usage.PromptBand)
	if !proven {
		derivation.Resolution.PricingKey, derivation.Resolution.UnpricedReason = nil, reason
		return derivation, nil
	}
	// The stored key names the exact card applied, not only the model.
	derivation.Resolution.PricingKey = stringPointer(card.Key)
	estimate, available, err := d.pricing.EstimateTokens(card.Key, usage.Tokens)
	if err != nil {
		return Derivation{}, err
	}
	if !available {
		return derivation, nil
	}
	value := estimate.MicroUSD
	derivation.EstimateMicroUSD = &value
	derivation.PricedTokens, derivation.UnpricedTokens = estimate.PricedTokens, estimate.UnpricedTokens
	derivation.EstimateCoverage = CoverageComplete
	if estimate.LowerBound || usage.Partial {
		derivation.EstimateCoverage = CoveragePartial
	}
	return derivation, nil
}
