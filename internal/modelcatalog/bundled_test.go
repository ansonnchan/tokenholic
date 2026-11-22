package modelcatalog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ansonnchan/tokenholic/internal/normalize"
)

// bundledRevision pins the embedded data. It changes whenever the snapshot,
// the overrides, or rulesVersion change; update it deliberately in the
// same change so the reprice that follows is visible in review.
const bundledRevision = "2026-11-21-1255fbad1e32"

func TestBundledRevisionIsPinned(t *testing.T) {
	t.Parallel()
	catalog, registry, err := Bundled()
	if err != nil {
		t.Fatal(err)
	}
	if catalog.Revision != bundledRevision || registry.Revision != bundledRevision {
		t.Fatalf("bundled revision = %q/%q, want %q", catalog.Revision, registry.Revision, bundledRevision)
	}
	if registry.EffectiveAt().IsZero() {
		t.Fatal("bundled pricing has no effective date")
	}
	if _, err := NewResolver(catalog); err != nil {
		t.Fatalf("bundled catalog is not resolvable: %v", err)
	}
}

func TestBundledSnapshotMatchesItsPin(t *testing.T) {
	t.Parallel()
	lock, err := os.ReadFile(filepath.Join("data", "litellm.lock.json"))
	if err != nil {
		t.Fatal(err)
	}
	var pin SnapshotPin
	if err := decodeStrict(lock, &pin); err != nil {
		t.Fatal(err)
	}
	if err := pin.Validate(); err != nil {
		t.Fatal(err)
	}
	snapshot, err := DecodeSnapshot(bundledSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Source.Commit != pin.Commit || snapshot.Source.SHA256 != pin.SHA256 || snapshot.Source.Retrieved != pin.Retrieved ||
		strings.Join(snapshot.Source.Providers, ",") != strings.Join(pin.Providers, ",") ||
		strings.Join(snapshot.Source.Modes, ",") != strings.Join(pin.Modes, ",") {
		t.Fatalf("vendored snapshot source %#v differs from the pin %#v; run `make prices`", snapshot.Source, pin)
	}
	notice, err := os.ReadFile(filepath.Join("data", "LICENSE-litellm.txt"))
	if err != nil || !strings.Contains(string(notice), "MIT License") || !strings.Contains(string(notice), "Berri AI") {
		t.Fatalf("vendored data is missing its MIT notice: %v", err)
	}
}

// Every model identifier seen in the maintainer's local Claude and Codex
// records must either price or say why it does not. The identifiers are public
// model names; nothing else from those records appears here.
func TestBundledCatalogAccountsForObservedModels(t *testing.T) {
	t.Parallel()
	catalog, registry, err := Bundled()
	if err != nil {
		t.Fatal(err)
	}
	resolver, err := NewResolver(catalog)
	if err != nil {
		t.Fatal(err)
	}
	million := uint64(1_000_000)
	// One million tokens in each of the five priced categories.
	tokens := normalize.TokenCounts{
		InputUncached: &million, InputCacheRead: &million, InputCacheWrite: func() *uint64 { v := 2 * million; return &v }(),
		InputCacheWrite5m: &million, InputCacheWrite1h: &million, Output: &million,
	}
	tests := []struct {
		observed  string
		evidence  normalize.ModelResolution
		canonical string
		match     MatchKind
		// wantMicroUSD is input + cache read + 5m write + 1h write + output
		// per million tokens, as published by the provider.
		wantMicroUSD uint64
		reason       string
		// noCacheWrites prices a source that reports no cache writes, as
		// Codex does: one million each of input, cache read, and output.
		noCacheWrites bool
	}{
		{observed: "claude-opus-5-5", evidence: normalize.ModelReported, canonical: "claude-opus-5-5", match: MatchExact, wantMicroUSD: 37_200_000},
		{observed: "claude-opus-5-5[1m]", evidence: normalize.ModelReported, canonical: "claude-opus-5-5", match: MatchReviewed, wantMicroUSD: 37_200_000},
		{observed: "claude-fable-5-2", evidence: normalize.ModelReported, canonical: "claude-fable-5-2", match: MatchExact, wantMicroUSD: 74_200_000},
		{observed: "claude-fable-5-1", evidence: normalize.ModelReported, canonical: "claude-fable-5-1", match: MatchExact, wantMicroUSD: 92_750_000},
		{observed: "claude-opus-5", evidence: normalize.ModelReported, canonical: "claude-opus-5", match: MatchExact, wantMicroUSD: 46_750_000},
		{observed: "claude-opus-4-8", evidence: normalize.ModelReported, canonical: "claude-opus-4-8", match: MatchExact, wantMicroUSD: 46_750_000},
		{observed: "claude-sonnet-5-5", evidence: normalize.ModelReported, canonical: "claude-sonnet-5-5", match: MatchExact, wantMicroUSD: 18_600_000},
		// Both charge more above a prompt length. These are their standard
		// cards; TestBundledTierGoldenCosts covers the others.
		{observed: "claude-haiku-5-5", evidence: normalize.ModelReported, canonical: "claude-haiku-5-5", match: MatchExact, wantMicroUSD: 935_000},
		{observed: "gpt-5.6-sol", evidence: normalize.ModelInferred, canonical: "gpt-5.6-sol", match: MatchExact, wantMicroUSD: 24_400_000, noCacheWrites: true},
		{observed: "codex-auto-review", evidence: normalize.ModelInferred, match: MatchUnknown, reason: ReasonRoutingLabel},
		{observed: "auto", evidence: normalize.ModelReported, match: MatchUnknown, reason: ReasonAmbiguousAlias},
		{observed: "default", evidence: normalize.ModelReported, match: MatchUnknown, reason: ReasonAmbiguousAlias},
		{observed: "router", evidence: normalize.ModelReported, match: MatchUnknown, reason: ReasonAmbiguousAlias},
		{observed: "smart", evidence: normalize.ModelReported, match: MatchUnknown, reason: ReasonAmbiguousAlias},
		{observed: "model-released-after-this-build", evidence: normalize.ModelReported, match: MatchUnknown, reason: ReasonUnknownModel},
	}
	for _, tt := range tests {
		t.Run(tt.observed, func(t *testing.T) {
			resolution := resolver.Resolve(tt.observed, tt.evidence)
			if resolution.Match != tt.match || resolution.UnpricedReason != tt.reason {
				t.Fatalf("Resolve() = %#v, want match %q and reason %q", resolution, tt.match, tt.reason)
			}
			if tt.canonical == "" {
				if resolution.CanonicalID != nil || resolution.Quality != normalize.ModelUnknown {
					t.Fatalf("Resolve() = %#v, want unknown", resolution)
				}
			} else if resolution.CanonicalID == nil || *resolution.CanonicalID != tt.canonical {
				t.Fatalf("canonical ID = %v, want %q", resolution.CanonicalID, tt.canonical)
			}
			if tt.reason != "" {
				if resolution.PricingKey != nil {
					t.Fatalf("pricing key = %q, want unpriced (%s)", *resolution.PricingKey, tt.reason)
				}
				return
			}
			if resolution.PricingKey == nil {
				t.Fatal("observed model has neither a price nor an unpriced reason")
			}
			tokens := tokens
			if tt.noCacheWrites {
				tokens.InputCacheWrite, tokens.InputCacheWrite5m, tokens.InputCacheWrite1h = new(uint64), new(uint64), new(uint64)
			}
			estimate, available, err := registry.EstimateTokens(*resolution.PricingKey, tokens)
			if err != nil || !available || estimate.LowerBound || estimate.UnpricedTokens != 0 {
				t.Fatalf("EstimateTokens() = %#v, %t, %v", estimate, available, err)
			}
			if estimate.MicroUSD != tt.wantMicroUSD {
				t.Fatalf("estimate = %d micro-USD, want %d", estimate.MicroUSD, tt.wantMicroUSD)
			}
		})
	}
}

func TestBuildValidatesOverrides(t *testing.T) {
	t.Parallel()
	const priced = `{"id":"model-new","provider":"anthropic","input":1000000,"output":5000000,` +
		`"source_url":"https://platform.claude.com/docs/en/about-claude/pricing","retrieved":"2026-11-21"}`
	const label = `{"id":"client-label","reason":"routing_label","note":"synthetic","source_url":"https://provider.invalid/pricing","retrieved":"2026-11-21"}`
	tests := []struct {
		name      string
		overrides string
		wantErr   string
	}{
		{name: "none", overrides: `{"prices":[],"unpriced":[]}`},
		{name: "cited price and label", overrides: `{"prices":[` + priced + `],"unpriced":[` + label + `]}`},
		{name: "unofficial host", overrides: `{"prices":[` + strings.Replace(priced, "platform.claude.com", "prices.example.com", 1) + `]}`, wantErr: "not an official"},
		{name: "plain HTTP", overrides: `{"prices":[` + strings.Replace(priced, "https://", "http://", 1) + `]}`, wantErr: "HTTPS"},
		{name: "missing retrieval date", overrides: `{"prices":[` + strings.Replace(priced, "2026-11-21", "", 1) + `]}`, wantErr: "retrieval date"},
		{name: "provider without a reviewed host", overrides: `{"prices":[` + strings.Replace(priced, `"anthropic"`, `"elsewhere"`, 1) + `]}`, wantErr: "no reviewed official pricing host"},
		{name: "missing output rate", overrides: `{"prices":[` + strings.Replace(priced, `"output":5000000,`, "", 1) + `]}`, wantErr: "input and output"},
		{name: "unknown field", overrides: `{"prices":[],"guess":true}`, wantErr: "unknown field"},
		{name: "label without a note", overrides: `{"unpriced":[` + strings.Replace(label, `"note":"synthetic",`, "", 1) + `]}`, wantErr: "note"},
		{name: "label that is also a model", overrides: `{"unpriced":[` + strings.Replace(label, "client-label", "model-old", 1) + `]}`, wantErr: "also a catalog model"},
	}
	snapshot := []byte(`{"source":{"repository":"example/prices","path":"prices.json","commit":"` + strings.Repeat("a", 40) +
		`","sha256":"` + strings.Repeat("b", 64) + `","retrieved":"2026-11-20","providers":["anthropic"],"modes":["chat"]},` +
		`"models":[{"id":"model-old","provider":"anthropic","input":2000000,"output":10000000},` +
		`{"id":"model-unpriced","provider":"anthropic","unpriced_reason":"no_published_price"}]}`)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			catalog, registry, err := Build(snapshot, []byte(tt.overrides))
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("Build() error = %v, want substring %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(catalog.Revision, "2026-11-2") || catalog.Revision != registry.Revision {
				t.Fatalf("revision = %q/%q", catalog.Revision, registry.Revision)
			}
		})
	}

	// Any change to either input yields a different revision.
	first, _, err := Build(snapshot, []byte(`{"prices":[],"unpriced":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	second, registry, err := Build(snapshot, []byte(`{"prices":[`+priced+`],"unpriced":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	if first.Revision == second.Revision {
		t.Fatal("an added override did not change the revision")
	}
	if price, found := registry.Lookup("anthropic/model-new"); !found || price.SourceRevision != "retrieved-2026-11-21" {
		t.Fatalf("override price = %#v, %t", price, found)
	}
}

func TestDecodeSnapshotRejectsMalformedData(t *testing.T) {
	t.Parallel()
	source := `"source":{"repository":"example/prices","path":"prices.json","commit":"` + strings.Repeat("a", 40) +
		`","sha256":"` + strings.Repeat("b", 64) + `","retrieved":"2026-11-20","providers":["anthropic"],"modes":["chat"]}`
	tests := map[string]string{
		"branch instead of commit":   strings.Replace(`{`+source+`,"models":[]}`, strings.Repeat("a", 40), "main", 1),
		"unsorted models":            `{` + source + `,"models":[{"id":"b","provider":"p","input":1},{"id":"a","provider":"p","input":1}]}`,
		"duplicate model":            `{` + source + `,"models":[{"id":"a","provider":"p","input":1},{"id":"a","provider":"p","input":1}]}`,
		"neither rates nor reason":   `{` + source + `,"models":[{"id":"a","provider":"p"}]}`,
		"both rates and a reason":    `{` + source + `,"models":[{"id":"a","provider":"p","input":1,"unpriced_reason":"no_published_price"}]}`,
		"tier card without rates":    `{` + source + `,"models":[{"id":"a","provider":"p","input":1,"tiers":{"priority":{}}}]}`,
		"standard as its own tier":   `{` + source + `,"models":[{"id":"a","provider":"p","input":1,"tiers":{"standard":{"input":1}}}]}`,
		"unnamed tier":               `{` + source + `,"models":[{"id":"a","provider":"p","input":1,"tiers":{"turbo":{"input":1}}}]}`,
		"long card, no threshold":    `{` + source + `,"models":[{"id":"a","provider":"p","input":1,"long_prompt":{"input":2}}]}`,
		"threshold, no long card":    `{` + source + `,"models":[{"id":"a","provider":"p","input":1,"prompt_threshold":272000}]}`,
		"threshold off the ladder":   `{` + source + `,"models":[{"id":"a","provider":"p","input":1,"prompt_threshold":128000,"long_prompt":{"input":2}}]}`,
		"cards on an unpriced model": `{` + source + `,"models":[{"id":"a","provider":"p","unpriced_reason":"no_published_price","tiers":{"flex":{"input":1}}}]}`,
		"unknown field":              `{` + source + `,"models":[{"id":"a","provider":"p","input":1,"guess":2}]}`,
		"trailing value":             `{` + source + `,"models":[]} {}`,
		"missing provider on model":  `{` + source + `,"models":[{"id":"a","input":1}]}`,
	}
	for name, data := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeSnapshot([]byte(data)); err == nil {
				t.Fatal("DecodeSnapshot() accepted malformed data")
			}
		})
	}
}

// The day-level agents report models from providers beyond Anthropic and
// OpenAI. A day total proves no prompt size and no service tier, so the
// cases price exactly what such a bucket can prove and leave the rest
// unpriced with its reason. Nothing here is an alias: a name the provider's
// price list does not carry stays unknown.
func TestBundledCatalogPricesDayLevelProviders(t *testing.T) {
	t.Parallel()
	deriver, err := BundledDeriver()
	if err != nil {
		t.Fatal(err)
	}
	million := uint64(1_000_000)
	tests := []struct {
		observed string
		provider string
		// wantMicroUSD is one million each of input, cache read, and output
		// at the standard rate, per the provider's published prices.
		wantMicroUSD uint64
		reason       string
	}{
		// $0.30 input + $0.03 cache read + $2.50 output.
		{observed: "gemini-2.5-flash", provider: "gemini", wantMicroUSD: 2_830_000},
		// $0.75 + $0.075 + $3.75.
		{observed: "gemini-3.8-flash", provider: "gemini", wantMicroUSD: 4_575_000},
		// Priced by prompt length, which a day total cannot prove.
		{observed: "gemini-2.5-pro", provider: "gemini", reason: ReasonPromptLengthUnproven},
		{observed: "grok-4.5", provider: "xai", reason: ReasonPromptLengthUnproven},
		{observed: "grok-code-fast-1", provider: "xai", reason: ReasonPromptLengthUnproven},
		// Grok Build's own model name is not on xAI's price list. It is not
		// assumed to be the model its name resembles.
		{observed: "grok-4.5-build", reason: ReasonUnknownModel},
		// Left out of the snapshot: priced at a prompt length no band proves.
		{observed: "gemini-exp-1206", reason: ReasonUnknownModel},
		// No first-party token price is vendored for these.
		{observed: "qwen3-coder-plus", reason: ReasonUnknownModel},
		{observed: "claude-sonnet-4.5", reason: ReasonUnknownModel},
		// A route through another vendor is never a first-party price.
		{observed: "gemini/gemini-2.5-flash", reason: ReasonUnknownModel},
		{observed: "xai/grok-4.5", reason: ReasonUnknownModel},
	}
	for _, test := range tests {
		t.Run(test.observed, func(t *testing.T) {
			t.Parallel()
			derivation, err := deriver.Derive(Usage{
				ObservedModelID: test.observed, ModelEvidence: normalize.ModelReported,
				Tokens: normalize.TokenCounts{InputUncached: &million, InputCacheRead: &million, Output: &million},
			})
			if err != nil {
				t.Fatal(err)
			}
			if test.provider == "" {
				if derivation.Resolution.CanonicalID != nil || derivation.Resolution.Provider != nil {
					t.Fatalf("resolution = %+v, want an unknown model", derivation.Resolution)
				}
			} else if derivation.Resolution.Provider == nil || *derivation.Resolution.Provider != test.provider ||
				derivation.Resolution.CanonicalID == nil || *derivation.Resolution.CanonicalID != test.observed {
				t.Fatalf("resolution = %+v, want %s from %s", derivation.Resolution, test.observed, test.provider)
			}
			if test.reason != "" {
				if derivation.Resolution.UnpricedReason != test.reason {
					t.Fatalf("unpriced reason = %q, want %q", derivation.Resolution.UnpricedReason, test.reason)
				}
				if derivation.EstimateMicroUSD != nil || derivation.PricedTokens != 0 || derivation.UnpricedTokens != 3*million {
					t.Fatalf("derivation = %+v, want every token unpriced and no estimate", derivation)
				}
				return
			}
			if derivation.EstimateMicroUSD == nil || *derivation.EstimateMicroUSD != test.wantMicroUSD ||
				derivation.PricedTokens != 3*million || derivation.UnpricedTokens != 0 {
				t.Fatalf("derivation = %+v, want %d micro-USD for three million priced tokens", derivation, test.wantMicroUSD)
			}
		})
	}
}
