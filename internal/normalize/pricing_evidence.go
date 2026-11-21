package normalize

// ServiceTier is the provider processing tier a request ran at, as the source
// recorded it. Providers price tiers differently, so it is evidence a price
// depends on; it is never a catalog result.
type ServiceTier string

const (
	// ServiceTierUnknown means the source did not prove a tier. It is not the
	// standard tier.
	ServiceTierUnknown ServiceTier = ""
	// ServiceTierStandard is the provider's default processing tier.
	ServiceTierStandard ServiceTier = "standard"
	// ServiceTierPriority is faster processing billed above the standard rate.
	ServiceTierPriority ServiceTier = "priority"
	// ServiceTierFlex is slower processing billed below the standard rate.
	ServiceTierFlex ServiceTier = "flex"
	// ServiceTierBatch is asynchronous batch processing.
	ServiceTierBatch ServiceTier = "batch"
	// ServiceTierFast is a provider's faster-output mode on the standard tier.
	ServiceTierFast ServiceTier = "fast"
)

// Valid reports whether the tier is unknown or one this contract names.
func (t ServiceTier) Valid() bool {
	switch t {
	case ServiceTierUnknown, ServiceTierStandard, ServiceTierPriority, ServiceTierFlex, ServiceTierBatch, ServiceTierFast:
		return true
	default:
		return false
	}
}

// PromptBand places one request's prompt size on a fixed ladder of the
// lengths at which providers change their per-token prices. A usage bucket
// carries the band instead of a size so that every request in it sits on the
// same side of any threshold on the ladder.
//
// The ladder is part of the upload contract and independent of any catalog: a
// client never needs to know which model has which threshold.
type PromptBand string

const (
	// PromptBandUnknown means the request's prompt size was not proven.
	PromptBandUnknown PromptBand = ""
	// PromptBandUpTo100K is a prompt of at most 100,000 tokens.
	PromptBandUpTo100K PromptBand = "le_100k"
	// PromptBandUpTo200K is a prompt above 100,000 and at most 200,000 tokens.
	PromptBandUpTo200K PromptBand = "le_200k"
	// PromptBandUpTo272K is a prompt above 200,000 and at most 272,000 tokens.
	PromptBandUpTo272K PromptBand = "le_272k"
	// PromptBandOver272K is a prompt above 272,000 tokens.
	PromptBandOver272K PromptBand = "gt_272k"
)

// promptLadder lists each bounded band with its inclusive upper limit, in
// ascending order. Appending a limit splits only the open-ended last band.
var promptLadder = []struct {
	band  PromptBand
	limit uint64
}{
	{PromptBandUpTo100K, 100_000},
	{PromptBandUpTo200K, 200_000},
	{PromptBandUpTo272K, 272_000},
}

// PromptBandFor returns the band holding a request with promptTokens of input,
// cached or not.
func PromptBandFor(promptTokens uint64) PromptBand {
	for _, step := range promptLadder {
		if promptTokens <= step.limit {
			return step.band
		}
	}
	return PromptBandOver272K
}

// Valid reports whether the band is unknown or on the ladder.
func (b PromptBand) Valid() bool {
	if b == PromptBandUnknown || b == PromptBandOver272K {
		return true
	}
	for _, step := range promptLadder {
		if b == step.band {
			return true
		}
	}
	return false
}

// PromptSide says where a band sits relative to a price threshold.
type PromptSide int

const (
	// PromptSideUnproven means the band is unknown or straddles the threshold.
	PromptSideUnproven PromptSide = iota
	// PromptSideAtOrBelow means every prompt in the band is within the threshold.
	PromptSideAtOrBelow
	// PromptSideAbove means every prompt in the band exceeds the threshold.
	PromptSideAbove
)

// Side reports whether every prompt in the band is within threshold tokens,
// every prompt exceeds it, or the band cannot prove either. A threshold that
// is not on the ladder leaves the band containing it unproven.
func (b PromptBand) Side(threshold uint64) PromptSide {
	if b == PromptBandUnknown {
		return PromptSideUnproven
	}
	var above uint64 // exclusive lower limit of the band
	for _, step := range promptLadder {
		if b == step.band {
			switch {
			case step.limit <= threshold:
				return PromptSideAtOrBelow
			case above >= threshold:
				return PromptSideAbove
			default:
				return PromptSideUnproven
			}
		}
		above = step.limit
	}
	if b == PromptBandOver272K && above >= threshold {
		return PromptSideAbove
	}
	return PromptSideUnproven
}

// PromptLadderLimits returns the ladder's thresholds in ascending order. A
// price threshold outside this list cannot be proven by any band.
func PromptLadderLimits() []uint64 {
	limits := make([]uint64, 0, len(promptLadder))
	for _, step := range promptLadder {
		limits = append(limits, step.limit)
	}
	return limits
}
