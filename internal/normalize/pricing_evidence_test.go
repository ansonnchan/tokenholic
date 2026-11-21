package normalize

import (
	"testing"
	"time"
)

func TestPromptBandFor(t *testing.T) {
	t.Parallel()
	tests := []struct {
		tokens uint64
		want   PromptBand
	}{
		{0, PromptBandUpTo100K}, {100_000, PromptBandUpTo100K},
		{100_001, PromptBandUpTo200K}, {200_000, PromptBandUpTo200K},
		{200_001, PromptBandUpTo272K}, {272_000, PromptBandUpTo272K},
		{272_001, PromptBandOver272K}, {^uint64(0), PromptBandOver272K},
	}
	for _, tt := range tests {
		if got := PromptBandFor(tt.tokens); got != tt.want || !got.Valid() {
			t.Errorf("PromptBandFor(%d) = %q, want %q", tt.tokens, got, tt.want)
		}
	}
}

func TestPromptBandSide(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		band      PromptBand
		threshold uint64
		want      PromptSide
	}{
		{"unknown band proves nothing", PromptBandUnknown, 272_000, PromptSideUnproven},
		{"band ending on the threshold is within it", PromptBandUpTo272K, 272_000, PromptSideAtOrBelow},
		{"lower band is within it", PromptBandUpTo100K, 272_000, PromptSideAtOrBelow},
		{"open band above the last limit", PromptBandOver272K, 272_000, PromptSideAbove},
		{"band starting on the threshold is above it", PromptBandUpTo200K, 100_000, PromptSideAbove},
		{"higher bands are above a low threshold", PromptBandOver272K, 100_000, PromptSideAbove},
		{"first band is within a low threshold", PromptBandUpTo100K, 100_000, PromptSideAtOrBelow},
		{"threshold inside a band", PromptBandUpTo200K, 128_000, PromptSideUnproven},
		{"threshold inside the first band", PromptBandUpTo100K, 32_000, PromptSideUnproven},
		{"threshold beyond the ladder", PromptBandOver272K, 512_000, PromptSideUnproven},
		{"bounded band below a threshold beyond the ladder", PromptBandUpTo272K, 512_000, PromptSideAtOrBelow},
		{"name outside the ladder", PromptBand("le_1m"), 272_000, PromptSideUnproven},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.band.Side(tt.threshold); got != tt.want {
				t.Fatalf("Side() = %d, want %d", got, tt.want)
			}
		})
	}
}

// Every ladder limit must separate the bands around it exactly, because a
// vendored price threshold is always one of them.
func TestPromptLadderProvesEachOfItsLimits(t *testing.T) {
	t.Parallel()
	for _, limit := range PromptLadderLimits() {
		if PromptBandFor(limit).Side(limit) != PromptSideAtOrBelow || PromptBandFor(limit+1).Side(limit) != PromptSideAbove {
			t.Errorf("limit %d is not proven by the bands around it", limit)
		}
	}
}

func TestPricingEvidenceValidation(t *testing.T) {
	t.Parallel()
	prompt, input := uint64(12), uint64(12)
	base := Observation{
		Source: SourceClaude, Surface: SurfaceCLI, SourceVersion: "synthetic", SourceRecordID: "synthetic-record",
		OccurredAt: time.Date(2026, 1, 9, 9, 0, 0, 0, time.UTC), ObservedModelID: "model-synthetic",
		ModelResolution: ModelReported, Tokens: TokenCounts{InputUncached: &input},
		CountSemantics: CountIncremental, Measurement: MeasurementReported, AdapterVersion: "1", NormalizationVersion: SchemaVersion,
	}
	tests := []struct {
		name    string
		mutate  func(*Observation)
		wantErr bool
	}{
		{name: "no evidence", mutate: func(*Observation) {}},
		{name: "tier and prompt size", mutate: func(o *Observation) { o.ServiceTier, o.PromptTokens = ServiceTierPriority, &prompt }},
		{name: "unnamed tier", mutate: func(o *Observation) { o.ServiceTier = "turbo" }, wantErr: true},
		{name: "prompt size on a running total", mutate: func(o *Observation) { o.CountSemantics, o.PromptTokens = CountCumulative, &prompt }, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			observation := base
			tt.mutate(&observation)
			if err := observation.Validate(); (err != nil) != tt.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %t", err, tt.wantErr)
			}
		})
	}
	for _, tier := range []ServiceTier{ServiceTierUnknown, ServiceTierStandard, ServiceTierPriority, ServiceTierFlex, ServiceTierBatch, ServiceTierFast} {
		if !tier.Valid() {
			t.Errorf("tier %q is not valid", tier)
		}
	}
}
