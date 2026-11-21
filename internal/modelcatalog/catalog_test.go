package modelcatalog

import (
	"testing"

	"github.com/ansonnchan/tokenholic/internal/normalize"
)

func TestResolverMatchesExactAliasReviewedInferredAndUnknown(t *testing.T) {
	t.Parallel()
	resolver := syntheticResolver(t, "catalog-r1")
	tests := []struct {
		name      string
		observed  string
		evidence  normalize.ModelResolution
		match     MatchKind
		quality   normalize.ModelResolution
		canonical string
	}{
		{name: "exact", observed: "model-canonical", evidence: normalize.ModelReported, match: MatchExact, quality: normalize.ModelReported, canonical: "model-canonical"},
		{name: "pinned exact", observed: "model-canonical-20261117", evidence: normalize.ModelReported, match: MatchExact, quality: normalize.ModelReported, canonical: "model-canonical"},
		{name: "alias", observed: "model-latest", evidence: normalize.ModelReported, match: MatchAlias, quality: normalize.ModelResolved, canonical: "model-canonical"},
		{name: "reviewed", observed: "source-model-label", evidence: normalize.ModelReported, match: MatchReviewed, quality: normalize.ModelResolved, canonical: "model-canonical"},
		{name: "inferred", observed: "model-canonical", evidence: normalize.ModelInferred, match: MatchExact, quality: normalize.ModelInferred, canonical: "model-canonical"},
		{name: "unknown", observed: "new-model", evidence: normalize.ModelReported, match: MatchUnknown, quality: normalize.ModelUnknown},
		{name: "ambiguous routing alias", observed: "auto", evidence: normalize.ModelReported, match: MatchUnknown, quality: normalize.ModelUnknown},
		{name: "uncertain source stays unknown", observed: "model-canonical", evidence: normalize.ModelUnknown, match: MatchUnknown, quality: normalize.ModelUnknown},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			resolution := resolver.Resolve(test.observed, test.evidence)
			if resolution.Match != test.match || resolution.Quality != test.quality || resolution.CatalogRevision != "catalog-r1" {
				t.Fatalf("Resolve() = %#v", resolution)
			}
			if test.canonical == "" {
				if resolution.CanonicalID != nil || resolution.PricingKey != nil {
					t.Fatalf("unknown resolution has canonical metadata: %#v", resolution)
				}
			} else if resolution.CanonicalID == nil || *resolution.CanonicalID != test.canonical {
				t.Fatalf("canonical ID = %v, want %q", resolution.CanonicalID, test.canonical)
			}
		})
	}
}

func TestResolverAliasRules(t *testing.T) {
	t.Parallel()
	resolver, err := NewResolver(Catalog{
		Revision:     "catalog-r1",
		AliasRules:   AliasRules{ContextMarkers: []string{"[1m]"}, DatedSnapshots: true},
		Unresolvable: []Unresolvable{{ID: "auto", Reason: ReasonAmbiguousAlias}, {ID: "review-label", Reason: ReasonRoutingLabel}},
		Models: []Model{
			{CanonicalID: "model-a", Provider: "provider", PricingKey: "provider/model-a", Aliases: []string{"model-a-latest"}},
			{CanonicalID: "model-a-20260101", Provider: "provider", PricingKey: "provider/model-a-20260101"},
			{CanonicalID: "model-tiered", Provider: "provider", UnpricedReason: ReasonNoPublishedPrice},
			{CanonicalID: "model-unlisted", Provider: "provider"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		observed  string
		canonical string
		match     MatchKind
		quality   normalize.ModelResolution
		priced    bool
		reason    string
	}{
		{observed: "model-a", canonical: "model-a", match: MatchExact, quality: normalize.ModelReported, priced: true},
		{observed: "model-a[1m]", canonical: "model-a", match: MatchReviewed, quality: normalize.ModelResolved, priced: true},
		{observed: "model-a-20260315", canonical: "model-a", match: MatchAlias, quality: normalize.ModelResolved, priced: true},
		{observed: "model-a-2026-03-15", canonical: "model-a", match: MatchAlias, quality: normalize.ModelResolved, priced: true},
		{observed: "model-a-20260315[1m]", canonical: "model-a", match: MatchReviewed, quality: normalize.ModelResolved, priced: true},
		// A dated ID the catalog lists exactly wins over the date rule.
		{observed: "model-a-20260101", canonical: "model-a-20260101", match: MatchExact, quality: normalize.ModelReported, priced: true},
		{observed: "model-a-20260101[1m]", canonical: "model-a-20260101", match: MatchReviewed, quality: normalize.ModelResolved, priced: true},
		// A rule never strengthens an alias and never drops an unpriced reason.
		{observed: "model-a-latest[1m]", canonical: "model-a", match: MatchReviewed, quality: normalize.ModelResolved, priced: true},
		{observed: "model-tiered[1m]", canonical: "model-tiered", match: MatchReviewed, quality: normalize.ModelResolved, reason: ReasonNoPublishedPrice},
		{observed: "model-unlisted", canonical: "model-unlisted", match: MatchExact, quality: normalize.ModelReported, reason: ReasonNoPublishedPrice},
		// Suffixes that are not a calendar date or a listed marker are not stripped.
		{observed: "model-a-20261399", match: MatchUnknown, quality: normalize.ModelUnknown, reason: ReasonUnknownModel},
		{observed: "model-a-1234", match: MatchUnknown, quality: normalize.ModelUnknown, reason: ReasonUnknownModel},
		{observed: "model-a[2m]", match: MatchUnknown, quality: normalize.ModelUnknown, reason: ReasonUnknownModel},
		{observed: "model-a-mini", match: MatchUnknown, quality: normalize.ModelUnknown, reason: ReasonUnknownModel},
		{observed: "model", match: MatchUnknown, quality: normalize.ModelUnknown, reason: ReasonUnknownModel},
		{observed: "[1m]", match: MatchUnknown, quality: normalize.ModelUnknown, reason: ReasonUnknownModel},
		// Routing labels stay unknown, with or without a suffix.
		{observed: "auto", match: MatchUnknown, quality: normalize.ModelUnknown, reason: ReasonAmbiguousAlias},
		{observed: "auto[1m]", match: MatchUnknown, quality: normalize.ModelUnknown, reason: ReasonAmbiguousAlias},
		{observed: "review-label", match: MatchUnknown, quality: normalize.ModelUnknown, reason: ReasonRoutingLabel},
		{observed: "", match: MatchUnknown, quality: normalize.ModelUnknown, reason: ReasonModelUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.observed, func(t *testing.T) {
			resolution := resolver.Resolve(tt.observed, normalize.ModelReported)
			if resolution.Match != tt.match || resolution.Quality != tt.quality || resolution.UnpricedReason != tt.reason {
				t.Fatalf("Resolve() = %#v", resolution)
			}
			if (resolution.PricingKey != nil) != tt.priced {
				t.Fatalf("pricing key = %v, want priced=%t", resolution.PricingKey, tt.priced)
			}
			if tt.canonical == "" {
				if resolution.CanonicalID != nil {
					t.Fatalf("canonical ID = %q, want unknown", *resolution.CanonicalID)
				}
			} else if resolution.CanonicalID == nil || *resolution.CanonicalID != tt.canonical {
				t.Fatalf("canonical ID = %v, want %q", resolution.CanonicalID, tt.canonical)
			}
		})
	}
}

func TestResolverWithoutAliasRulesMatchesExactly(t *testing.T) {
	t.Parallel()
	resolver := syntheticResolver(t, "catalog-r1")
	for _, observed := range []string{"model-canonical[1m]", "model-canonical-20260315"} {
		if resolution := resolver.Resolve(observed, normalize.ModelReported); resolution.Match != MatchUnknown {
			t.Fatalf("Resolve(%q) = %#v, want unknown without alias rules", observed, resolution)
		}
	}
}

func TestCatalogRevisionIsExplicit(t *testing.T) {
	t.Parallel()
	first := syntheticResolver(t, "catalog-r1")
	second := syntheticResolver(t, "catalog-r2")
	if first.Resolve("model-canonical", normalize.ModelReported).CatalogRevision ==
		second.Resolve("model-canonical", normalize.ModelReported).CatalogRevision {
		t.Fatal("catalog revisions are indistinguishable")
	}
}

func TestNewResolverRejectsAmbiguousOrDuplicateMappings(t *testing.T) {
	t.Parallel()
	tests := []Catalog{
		{Revision: "r1", AmbiguousAliases: []string{"auto"}, Models: []Model{{CanonicalID: "one", Provider: "provider", Aliases: []string{"auto"}}}},
		{Revision: "r1", Models: []Model{{CanonicalID: "one", Provider: "provider", Aliases: []string{"shared"}}, {CanonicalID: "two", Provider: "provider", Aliases: []string{"shared"}}}},
	}
	for _, catalog := range tests {
		if _, err := NewResolver(catalog); err == nil {
			t.Fatalf("NewResolver(%#v) succeeded", catalog)
		}
	}
}

func TestPricingRegistryChecksumAndEffectiveDateAreStable(t *testing.T) {
	t.Parallel()
	registry := syntheticPricing(t)
	first, err := registry.Checksum()
	if err != nil {
		t.Fatal(err)
	}
	second, err := registry.Checksum()
	if err != nil {
		t.Fatal(err)
	}
	if first != second || first == [32]byte{} || registry.EffectiveAt().IsZero() {
		t.Fatalf("pricing checksum/effective date = %x/%x/%v", first, second, registry.EffectiveAt())
	}
}

func syntheticResolver(t *testing.T, revision string) *Resolver {
	t.Helper()
	resolver, err := NewResolver(Catalog{
		Revision: revision, AmbiguousAliases: []string{"auto", "router"},
		Models: []Model{{
			CanonicalID: "model-canonical", Provider: "provider", PricingKey: "provider/model",
			ExactIDs: []string{"model-canonical-20261117"}, Aliases: []string{"model-latest"},
			ReviewedMapping: []string{"source-model-label"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return resolver
}

func rate(microUSDPerMillion uint64) *uint64 {
	return &microUSDPerMillion
}
