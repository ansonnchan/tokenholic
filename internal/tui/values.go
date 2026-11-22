package tui

import (
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/ansonnchan/tokenholic/internal/modelcatalog"
	"github.com/ansonnchan/tokenholic/internal/normalize"
	"github.com/ansonnchan/tokenholic/internal/numfmt"
	"github.com/ansonnchan/tokenholic/internal/store"
	"github.com/ansonnchan/tokenholic/internal/tui/theme"
)

// This file turns stored values into display text. The one rule it exists to
// keep: a value that is not known renders as the unavailable glyph, and a
// partial one as a lower bound. Only a known number renders as a number.

// tokensText is a compact token count: "4.27B", a lower bound "≥4.27B", or
// the unavailable glyph.
func tokensText(g theme.Glyphs, value store.AggregateValue) string {
	if value.Value == nil {
		return g.Unavailable
	}
	return lowerBound(g, value.State) + numfmt.Tokens(*value.Value)
}

// exactText is a full token count with separators, for category detail.
func exactText(g theme.Glyphs, value store.AggregateValue) string {
	if value.Value == nil {
		return g.Unavailable
	}
	return lowerBound(g, value.State) + numfmt.Exact(*value.Value)
}

// estimateText is an API-equivalent estimate in dollars. It carries no EST
// label; the column or tile it sits in does.
func estimateText(g theme.Glyphs, value store.AggregateValue) string {
	if value.Value == nil {
		return g.Unavailable
	}
	return lowerBound(g, value.State) + numfmt.USD(*value.Value)
}

func lowerBound(g theme.Glyphs, state store.Availability) string {
	if state == store.AvailabilityPartial {
		return g.AtLeast
	}
	return ""
}

// pricedText is the priced share of a total, or the unavailable glyph when
// there are no tokens to take a share of.
func pricedText(g theme.Glyphs, coverage store.Coverage) string {
	if coverage.PricedBasisPoints == nil {
		return g.Unavailable
	}
	return numfmt.Percent(*coverage.PricedBasisPoints)
}

// shareText is part as a share of total, both known, cut to one decimal.
func shareText(g theme.Glyphs, part, total store.AggregateValue) string {
	if part.Value == nil || total.Value == nil || *total.Value == 0 {
		return g.Unavailable
	}
	share := new(big.Int).Mul(new(big.Int).SetUint64(*part.Value), big.NewInt(10_000))
	share.Quo(share, new(big.Int).SetUint64(*total.Value))
	if share.Sign() == 0 && *part.Value > 0 {
		// Too small to show, but not nothing.
		return numfmt.Percent(1)
	}
	return numfmt.Percent(int(min(share.Int64(), 10_000)))
}

// isEmpty reports whether a summary has no usage at all, as opposed to usage
// whose counts are unknown.
func isEmpty(summary store.UsageSummary) bool {
	state := summary.Tokens.Total().State
	return state == "" || state == store.AvailabilityEmpty
}

// age describes how long ago something happened in plain words.
func age(then *time.Time, now time.Time) string {
	if then == nil {
		return "never"
	}
	elapsed := now.Sub(*then)
	switch {
	case elapsed < time.Minute:
		return "just now"
	case elapsed < time.Hour:
		return fmt.Sprintf("%dm ago", int(elapsed.Minutes()))
	case elapsed < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(elapsed.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(elapsed.Hours()/24))
	}
}

// until describes how far away something is: "in 23h", "in 3d".
func until(remaining time.Duration) string {
	switch {
	case remaining <= 0:
		return "now"
	case remaining < time.Hour:
		return fmt.Sprintf("in %dm", max(int(remaining.Minutes()), 1))
	case remaining < 48*time.Hour:
		return fmt.Sprintf("in %dh", int(remaining.Hours()))
	default:
		return fmt.Sprintf("in %dd", int(remaining.Hours()/24))
	}
}

// dayLabel is a short uppercase date such as "NOV 21".
func dayLabel(day time.Time) string {
	return strings.ToUpper(day.Format("Jan 2"))
}

// modelName is the canonical model when this build resolved one, and the
// observed identifier otherwise.
func modelName(model store.ModelBreakdown) string {
	if model.CanonicalModelID != nil && *model.CanonicalModelID != "" {
		return *model.CanonicalModelID
	}
	if model.ObservedModelID == "" {
		return "(no model recorded)"
	}
	return model.ObservedModelID
}

// attributionText names how a model was attributed. Inferred and unknown are
// said outright; they are never dressed up as resolved.
func attributionText(resolution normalize.ModelResolution) string {
	if resolution == "" {
		return string(normalize.ModelUnknown)
	}
	return string(resolution)
}

// unpricedReasonText explains a missing estimate in plain language. Unknown
// codes fall back to the code so a newer catalog reason is still visible.
func unpricedReasonText(reason string) string {
	switch reason {
	case "":
		return ""
	case modelcatalog.ReasonUnknownModel:
		return "model is not in this version's price list"
	case modelcatalog.ReasonModelUnavailable:
		return "the source did not record a model"
	case modelcatalog.ReasonAmbiguousAlias, modelcatalog.ReasonRoutingLabel:
		return "a routing label that names no single model"
	case modelcatalog.ReasonPromptLengthUnproven:
		return "price depends on prompt length, which the source did not record"
	case modelcatalog.ReasonTierRateUnavailable:
		return "no reviewed price for this service tier"
	case modelcatalog.ReasonNoPublishedPrice:
		return "no reviewed price for this model"
	default:
		return reason
	}
}

// sourceName and surfaceName are display names for the adapter vocabulary.
// An identifier this build does not know is shown as it is.
func sourceName(source string) string {
	switch normalize.Source(source) {
	case normalize.SourceClaude:
		return "Claude"
	case normalize.SourceCodex:
		return "Codex"
	case normalize.SourceCopilot:
		return "Copilot CLI"
	case normalize.SourceGemini:
		return "Gemini CLI"
	case normalize.SourceGrok:
		return "Grok Build"
	case normalize.SourceOpenCode:
		return "OpenCode"
	case normalize.SourceQwen:
		return "Qwen Code"
	default:
		return source
	}
}

// byDay is how a day-level source's coverage is named beside it: its usage
// is known as one total per UTC day, not per request.
const byDay = "by day"

func surfaceName(surface string) string {
	switch normalize.Surface(surface) {
	case normalize.SurfaceCLI:
		return "CLI"
	case normalize.SurfaceDesktop:
		return "Desktop"
	case normalize.SurfaceDesktopCode:
		return "Desktop Code"
	case normalize.SurfaceDesktopAgent:
		return "Desktop Agent"
	case normalize.SurfaceDesktopCowork:
		return "Desktop Cowork"
	case normalize.SurfaceSharedLocal:
		return "CLI and Desktop"
	default:
		return surface
	}
}

// healthText names an adapter state in words.
func healthText(status normalize.HealthStatus) string {
	switch status {
	case normalize.HealthDetected:
		return "detected"
	case normalize.HealthNotFound:
		return "not found"
	case normalize.HealthPermissionDenied:
		return "no permission"
	case normalize.HealthUnsupported:
		return "unsupported"
	case normalize.HealthDegraded:
		return "no records"
	case normalize.HealthError:
		return "error"
	case normalize.HealthUnavailable:
		return "unavailable"
	default:
		return string(status)
	}
}

// needsAttention reports whether an adapter state is one the user can act on.
func needsAttention(status normalize.HealthStatus) bool {
	switch status {
	case normalize.HealthPermissionDenied, normalize.HealthUnsupported, normalize.HealthDegraded, normalize.HealthError:
		return true
	default:
		return false
	}
}

func unhealthyAdapters(records []store.StoredAdapterHealth) int {
	count := 0
	for _, record := range records {
		if needsAttention(record.Health.Status) {
			count++
		}
	}
	return count
}

// noSupportedSources reports a machine where every adapter looked and found
// nothing, which deserves instructions rather than an empty dashboard.
func noSupportedSources(summary store.UsageSummary, health []store.StoredAdapterHealth) bool {
	if !isEmpty(summary) || len(health) == 0 {
		return false
	}
	for _, record := range health {
		if record.Health.Status != normalize.HealthNotFound {
			return false
		}
	}
	return true
}

// cleanLine replaces control characters so remote or stored text can never
// move the cursor or change the terminal's state.
func cleanLine(line string) string {
	return strings.Map(func(character rune) rune {
		if character < 0x20 || character == 0x7f || character >= 0x80 && character < 0xa0 {
			return ' '
		}
		return character
	}, line)
}
