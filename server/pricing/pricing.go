// Package pricing is the server's authority for model resolution and
// API-equivalent estimates.
//
// Clients upload what they observed: a model identifier, how the source
// attributed it, token counts, and any pricing evidence the source proved. Everything concluded from that evidence is
// derived here from the bundled catalog and stored beside the raw counts, so
// a catalog or price update can re-derive history without a client.
package pricing

import (
	"errors"

	"github.com/ansonnchan/tokenholic/internal/modelcatalog"
	"github.com/ansonnchan/tokenholic/internal/normalize"
	protocol "github.com/ansonnchan/tokenholic/internal/sync"
)

// Usage is the client evidence one derivation depends on. It is the same
// whether it arrives in an upload or is read back from storage for repricing.
type Usage struct {
	ObservedModelID string
	ModelEvidence   string
	// ServiceTier and PromptBand are the bucket's pricing evidence; empty
	// means the client did not prove it.
	ServiceTier string
	PromptBand  string
	Tokens      protocol.Tokens
}

// Derived holds every server-owned column of a usage bucket.
//
// PricedTokens and UnpricedTokens sum to the bucket's known total tokens.
// PricingRevision names the catalog revision behind all of these values,
// including an unknown model or an unavailable estimate.
type Derived struct {
	CanonicalModelID *string
	ModelProvider    *string
	PricingKey       *string
	ModelResolution  string
	ModelMatch       string
	UnpricedReason   *string
	EstimateMicroUSD *uint64
	EstimateState    string
	PricedTokens     uint64
	UnpricedTokens   uint64
	PricingRevision  string
}

// Engine derives server-owned values with one catalog revision.
type Engine struct {
	deriver *modelcatalog.Deriver
}

// NewEngine wraps the catalog revision the server treats as authoritative.
func NewEngine(deriver *modelcatalog.Deriver) (*Engine, error) {
	if deriver == nil {
		return nil, errors.New("initialize server pricing: a model deriver is required")
	}
	return &Engine{deriver: deriver}, nil
}

// Revision identifies the active catalog and prices.
func (e *Engine) Revision() string {
	return e.deriver.Revision()
}

// Derive resolves the model and prices the token totals of one bucket. It
// depends only on its arguments and the engine's revision, so deriving the
// same usage twice yields identical values.
func (e *Engine) Derive(usage Usage) (Derived, error) {
	tokens := usage.Tokens
	partial := false
	for _, value := range []protocol.Value{tokens.InputUncached, tokens.InputCacheRead, tokens.InputCacheWrite, tokens.Output} {
		partial = partial || value.State == "partial"
	}
	derivation, err := e.deriver.Derive(modelcatalog.Usage{
		ObservedModelID: usage.ObservedModelID, ModelEvidence: normalize.ModelResolution(usage.ModelEvidence),
		Tokens: normalize.TokenCounts{
			InputUncached: tokens.InputUncached.Value, InputCacheRead: tokens.InputCacheRead.Value,
			InputCacheWrite: tokens.InputCacheWrite.Value, InputCacheWrite5m: tokens.InputCacheWrite5m.Value,
			InputCacheWrite1h: tokens.InputCacheWrite1h.Value, Output: tokens.Output.Value,
		},
		ServiceTier: normalize.ServiceTier(usage.ServiceTier), PromptBand: normalize.PromptBand(usage.PromptBand),
		Partial: partial,
	})
	if err != nil {
		return Derived{}, err
	}
	derived := Derived{
		CanonicalModelID: derivation.Resolution.CanonicalID, ModelProvider: derivation.Resolution.Provider,
		PricingKey: derivation.Resolution.PricingKey, ModelResolution: string(derivation.Resolution.Quality),
		ModelMatch: string(derivation.Resolution.Match), EstimateMicroUSD: derivation.EstimateMicroUSD,
		EstimateState: string(derivation.EstimateCoverage), PricedTokens: derivation.PricedTokens,
		UnpricedTokens: derivation.UnpricedTokens, PricingRevision: derivation.Revision,
	}
	if reason := derivation.Resolution.UnpricedReason; reason != "" {
		derived.UnpricedReason = &reason
	}
	return derived, nil
}
