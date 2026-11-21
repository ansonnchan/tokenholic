package tui

import (
	"fmt"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ansonnchan/tokenholic/internal/application"
	"github.com/ansonnchan/tokenholic/internal/modelcatalog"
	"github.com/ansonnchan/tokenholic/internal/multiplayer"
	"github.com/ansonnchan/tokenholic/internal/normalize"
	"github.com/ansonnchan/tokenholic/internal/store"
)

func buildPage(section Section, dashboard application.Dashboard) Page {
	switch section {
	case OverviewSection:
		return overviewPage(dashboard)
	case SourcesSection:
		return sourcesPage(dashboard)
	case ModelsSection:
		return modelsPage(dashboard)
	case SettingsSection:
		return settingsPage(dashboard)
	case GroupsSection:
		return Page{Warning: "Group data requires the multiplayer client"}
	default:
		return Page{Warning: "Unknown local view"}
	}
}

func overviewPage(dashboard application.Dashboard) Page {
	if noSupportedSources(dashboard) {
		return noSupportedSourcesPage()
	}
	if summaryEmpty(dashboard.AllTime) && len(dashboard.Diagnostics.AdapterHealth) == 0 {
		return Page{}
	}
	page := Page{Lines: []string{
		"Current period · Today · " + dashboard.Timezone,
		"Total tokens: " + totalTokens(dashboard.Today.Tokens),
		"Input uncached: " + formatValue(dashboard.Today.Tokens.InputUncached),
		"Cache read: " + formatValue(dashboard.Today.Tokens.InputCacheRead),
		"Cache write: " + formatValue(dashboard.Today.Tokens.InputCacheWrite),
		"  5-minute: " + formatValue(dashboard.Today.Tokens.InputCacheWrite5m),
		"  1-hour: " + formatValue(dashboard.Today.Tokens.InputCacheWrite1h),
		"Output: " + formatValue(dashboard.Today.Tokens.Output),
		"Reasoning output: " + formatValue(dashboard.Today.Tokens.ReasoningOutput),
		"API-equivalent estimate: " + formatEstimate(dashboard.Today.APIEstimate),
		"Estimates here are a local preview; the server derives its own.",
		"",
		"Recent periods",
		periodLine("Today", dashboard.Today),
		periodLine("This week", dashboard.Week),
		periodLine("30 days", dashboard.Last30Days),
		periodLine("All time", dashboard.AllTime),
		"",
		"Collection: " + collectionFreshness(dashboard.Diagnostics.LastSuccessfulCollection, dashboard.GeneratedAt),
		fmt.Sprintf("Database: integrity %s · schema %d/%d · %d source checkpoints", integrityText(dashboard.Diagnostics.IntegrityOK), dashboard.Diagnostics.SchemaVersion, dashboard.Diagnostics.SupportedSchemaVersion, dashboard.Diagnostics.CheckpointCount),
		fmt.Sprintf("Pending local buckets: %d", dashboard.Diagnostics.PendingOutbox),
	}}
	if summaryIncomplete(dashboard.Today) {
		page.Warning = "Today includes partial or unavailable values"
	} else if unhealthyAdapters(dashboard.Diagnostics.AdapterHealth) > 0 {
		page.Warning = "One or more local sources need attention"
	}
	return page
}

func sourcesPage(dashboard application.Dashboard) Page {
	health := dashboard.Diagnostics.AdapterHealth
	if len(health) == 0 && len(dashboard.Today.Sources) == 0 {
		return Page{}
	}
	page := Page{Lines: []string{"Adapter health"}}
	for _, record := range health {
		count := "unavailable"
		if record.Health.RecordCount != nil {
			count = formatUint(*record.Health.RecordCount)
		}
		lastSuccess := "never"
		if record.LastSuccessAt != nil {
			lastSuccess = record.LastSuccessAt.UTC().Format(time.RFC3339)
		}
		page.Lines = append(page.Lines, fmt.Sprintf(
			"%s/%s · %s · records %s · last success %s",
			record.Health.Source, record.Health.Surface, record.Health.Status, count, lastSuccess,
		))
	}
	if len(dashboard.Today.Sources) > 0 {
		page.Lines = append(page.Lines, "", "Usage today by source")
		for _, source := range dashboard.Today.Sources {
			page.Lines = append(page.Lines, fmt.Sprintf("%s · %s tokens · API-equivalent estimate %s", source.Name, totalTokens(source.Tokens), formatEstimate(source.APIEstimate)))
		}
	}
	if count := unhealthyAdapters(health); count > 0 {
		page.Warning = fmt.Sprintf("%d local adapter state(s) need attention; run `tokenholic doctor`", count)
	}
	return page
}

func modelsPage(dashboard application.Dashboard) Page {
	if noSupportedSources(dashboard) {
		return noSupportedSourcesPage()
	}
	models := append([]store.ModelBreakdown(nil), dashboard.AllTime.Models...)
	if len(models) == 0 {
		return Page{}
	}
	sort.SliceStable(models, func(i, j int) bool {
		return knownTokenTotal(models[i].Tokens).Cmp(knownTokenTotal(models[j].Tokens)) > 0
	})
	page := Page{Lines: []string{"All-time model usage"}}
	for _, model := range models {
		name := model.ObservedModelID + " (canonical unavailable)"
		if model.CanonicalModelID != nil {
			name = *model.CanonicalModelID
			if *model.CanonicalModelID != model.ObservedModelID {
				name += " · observed " + model.ObservedModelID
			}
		}
		page.Lines = append(page.Lines,
			fmt.Sprintf("%s · attribution %s", name, model.Resolution),
			fmt.Sprintf("  %s tokens · API-equivalent estimate %s", totalTokens(model.Tokens), formatEstimate(model.APIEstimate)),
		)
		if reason := unpricedReasonText(model.UnpricedReason); reason != "" {
			label := "  no estimate: "
			if model.APIEstimate.Value != nil {
				// Some of the model's usage priced; the reason covers the rest.
				label = "  partly unpriced: "
			}
			page.Lines = append(page.Lines, label+reason)
		}
		if model.Resolution == normalize.ModelUnknown || model.Resolution == normalize.ModelInferred {
			page.Warning = "Some model attribution is inferred or unavailable"
		}
	}
	return page
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

func noSupportedSources(dashboard application.Dashboard) bool {
	if !summaryEmpty(dashboard.AllTime) || len(dashboard.Diagnostics.AdapterHealth) == 0 {
		return false
	}
	for _, record := range dashboard.Diagnostics.AdapterHealth {
		if record.Health.Status != normalize.HealthNotFound {
			return false
		}
	}
	return true
}

func noSupportedSourcesPage() Page {
	return Page{
		EmptyMessage: "No supported coding-agent usage found.",
		Lines: []string{
			"Supported now: Claude Code/Desktop coding sessions and Codex CLI/Desktop.",
			"Install or use a supported coding agent, then press r to scan again.",
			"Run `tokenholic doctor` to check source detection.",
		},
	}
}

func settingsPage(dashboard application.Dashboard) Page {
	return Page{Lines: []string{
		"Local mode",
		"No account, server, group, or synchronization is required.",
		"",
		"Privacy",
		"tokenholic stores normalized usage counts only.",
		"Never stored: prompts, responses, source code, command text, or credentials.",
		"Never stored: paths, project names, repository names, or branch names.",
		"",
		"Controls",
		"1–5 switch views · tab/arrows navigate · r collect and refresh · ? help · q quit",
		"",
		fmt.Sprintf("Timezone: %s", dashboard.Timezone),
		fmt.Sprintf("Database schema: %d (supported %d)", dashboard.Diagnostics.SchemaVersion, dashboard.Diagnostics.SupportedSchemaVersion),
		"Collection freshness: " + collectionFreshness(dashboard.Diagnostics.LastSuccessfulCollection, dashboard.GeneratedAt),
		"Run `tokenholic doctor` for detailed, path-redacted diagnostics.",
	}}
}

func groupPage(snapshot multiplayer.Snapshot) Page {
	if len(snapshot.Groups) == 0 {
		return Page{Lines: []string{
			"No private groups yet.",
			"Press c to create one or i to join with an invite code.",
			"New groups use this device's IANA timezone.",
		}}
	}
	groupIndex := 0
	for index, group := range snapshot.Groups {
		if group.ID == snapshot.Selected.ID {
			groupIndex = index
			break
		}
	}
	page := Page{Lines: []string{
		fmt.Sprintf("Group %d/%d · %s · %s · %s", groupIndex+1, len(snapshot.Groups), snapshot.Selected.Name, snapshot.Selected.Role, snapshot.Selected.Timezone),
		"j/k switch group · p period · m metric · c create · i join",
	}}
	if snapshot.Selected.Role == "owner" {
		page.Lines = append(page.Lines, "Create an invite with `tokenholic group invite <name>`.")
	}
	if snapshot.Board == nil {
		return page
	}
	board := snapshot.Board
	page.Lines = append(page.Lines,
		fmt.Sprintf("Period: %s · Metric: %s", periodLabel(board.Period), metricLabel(board.Metric)),
		fmt.Sprintf("Window: %s · generated %s", windowLabel(board.Window), board.GeneratedAt.UTC().Format(time.RFC3339)),
	)
	for _, row := range board.Rows {
		if !row.CurrentUser {
			continue
		}
		standing := "unranked"
		if row.Rank != nil {
			standing = fmt.Sprintf("#%d of %d", *row.Rank, len(board.Rows))
		}
		page.Lines = append(page.Lines, fmt.Sprintf("Your standing: %s · %s", standing, formatRemoteValue(row.Value, board.Metric)))
		break
	}
	page.Lines = append(page.Lines, "", "Leaderboard")
	if len(board.Rows) == 0 {
		page.Lines = append(page.Lines, "No active members to rank.")
		return page
	}
	for _, row := range board.Rows {
		rank := "--"
		if row.Rank != nil {
			rank = fmt.Sprintf("#%d", *row.Rank)
		}
		name := row.DisplayName
		if row.CurrentUser {
			name += " (you)"
		}
		page.Lines = append(page.Lines, fmt.Sprintf(
			"%s %s · %s · last synced %s",
			rank, name, formatRemoteValue(row.Value, board.Metric), collectionFreshness(row.LastSyncedAt, board.GeneratedAt),
		))
		context := make([]string, 0, 2)
		if row.MostUsedModel != nil {
			context = append(context, fmt.Sprintf("model %s (%s)", row.MostUsedModel.ModelID, row.MostUsedModel.Resolution))
		}
		if len(row.SourceMix) > 0 {
			sources := make([]string, 0, len(row.SourceMix))
			for _, source := range row.SourceMix {
				sources = append(sources, source.Source+" "+formatRemoteValue(source.TotalTokens, multiplayer.MetricTotalTokens))
			}
			context = append(context, "sources "+strings.Join(sources, ", "))
		}
		if len(context) > 0 {
			page.Lines = append(page.Lines, "  "+strings.Join(context, " · "))
		}
		limitations := row.Limitations
		if limitations.UnknownModelBuckets > 0 || limitations.InferredModelBuckets > 0 || limitations.APIEstimateUnavailableBuckets > 0 {
			page.Lines = append(page.Lines, fmt.Sprintf(
				"  coverage: unknown model %d · inferred model %d · API estimate unavailable %d",
				limitations.UnknownModelBuckets, limitations.InferredModelBuckets, limitations.APIEstimateUnavailableBuckets,
			))
			page.Warning = "Some leaderboard rows have model or API-estimate limitations"
		}
		if row.Value.State == multiplayer.AvailabilityPartial || row.Value.State == multiplayer.AvailabilityUnavailable {
			page.Warning = "Some leaderboard values are partial or unavailable and remain unranked"
		}
	}
	return page
}

func periodLabel(period multiplayer.Period) string {
	switch period {
	case multiplayer.PeriodToday:
		return "Today"
	case multiplayer.PeriodWeek:
		return "This week"
	case multiplayer.PeriodLast30Days:
		return "30 days"
	case multiplayer.PeriodAllTime:
		return "All time"
	default:
		return "Unknown"
	}
}

func metricLabel(metric multiplayer.Metric) string {
	switch metric {
	case multiplayer.MetricTotalTokens:
		return "Tokens"
	case multiplayer.MetricOutput:
		return "Output"
	case multiplayer.MetricAPIEstimate:
		return "API estimate"
	default:
		return "Unknown"
	}
}

func windowLabel(window multiplayer.Window) string {
	if window.Start == nil {
		return "all synchronized history"
	}
	return window.Start.UTC().Format(time.RFC3339) + " to " + window.End.UTC().Format(time.RFC3339)
}

func formatRemoteValue(value multiplayer.Value, metric multiplayer.Metric) string {
	if value.Value == nil {
		switch value.State {
		case multiplayer.AvailabilityEmpty:
			return "no data"
		default:
			return "unavailable"
		}
	}
	formatted := formatDigits(*value.Value)
	if metric == multiplayer.MetricAPIEstimate {
		formatted = formatMicroUSD(*value.Value) + " (estimate)"
	} else if metric == multiplayer.MetricOutput {
		formatted += " output tokens"
	} else {
		formatted += " tokens"
	}
	if value.State == multiplayer.AvailabilityPartial {
		formatted += " (partial; unranked)"
	}
	return formatted
}

func formatMicroUSD(value string) string {
	micros, ok := new(big.Int).SetString(value, 10)
	if !ok || micros.Sign() < 0 {
		return "unavailable"
	}
	dollars, remainder := new(big.Int), new(big.Int)
	dollars.QuoRem(micros, big.NewInt(1_000_000), remainder)
	fraction := remainder.String()
	fraction = strings.Repeat("0", 6-len(fraction)) + fraction
	return "$" + formatDigits(dollars.String()) + "." + fraction
}

func summaryEmpty(summary store.UsageSummary) bool {
	values := breakdownValues(summary.Tokens)
	for _, value := range values {
		if value.State != "" && value.State != store.AvailabilityEmpty {
			return false
		}
	}
	return true
}

func summaryIncomplete(summary store.UsageSummary) bool {
	for _, value := range append(breakdownValues(summary.Tokens), summary.APIEstimate) {
		if value.State == store.AvailabilityPartial || value.State == store.AvailabilityUnavailable {
			return true
		}
	}
	return false
}

func breakdownValues(tokens store.TokenBreakdown) []store.AggregateValue {
	return []store.AggregateValue{tokens.InputUncached, tokens.InputCacheRead, tokens.InputCacheWrite, tokens.Output, tokens.ReasoningOutput}
}

func totalValues(tokens store.TokenBreakdown) []store.AggregateValue {
	return []store.AggregateValue{tokens.InputUncached, tokens.InputCacheRead, tokens.InputCacheWrite, tokens.Output}
}

func totalTokens(tokens store.TokenBreakdown) string {
	values := totalValues(tokens)
	seen, known, incomplete := false, false, false
	total := new(big.Int)
	for _, value := range values {
		if value.State == "" || value.State == store.AvailabilityEmpty {
			continue
		}
		seen = true
		if value.Value != nil {
			known = true
			total.Add(total, new(big.Int).SetUint64(*value.Value))
		}
		if value.State != store.AvailabilityComplete {
			incomplete = true
		}
	}
	switch {
	case !seen:
		return "no data"
	case !known:
		return "unavailable"
	case incomplete:
		return formatDigits(total.String()) + " (partial)"
	default:
		return formatDigits(total.String())
	}
}

func knownTokenTotal(tokens store.TokenBreakdown) *big.Int {
	total := new(big.Int)
	for _, value := range totalValues(tokens) {
		if value.Value != nil {
			total.Add(total, new(big.Int).SetUint64(*value.Value))
		}
	}
	return total
}

func formatValue(value store.AggregateValue) string {
	switch value.State {
	case store.AvailabilityEmpty:
		return "no data"
	case store.AvailabilityUnavailable:
		return "unavailable"
	case store.AvailabilityPartial:
		if value.Value == nil {
			return "unavailable"
		}
		return formatUint(*value.Value) + " (partial)"
	default:
		if value.Value == nil {
			return "unavailable"
		}
		return formatUint(*value.Value)
	}
}

func formatEstimate(value store.AggregateValue) string {
	if value.State == store.AvailabilityEmpty {
		return "no data"
	}
	if value.Value == nil {
		return "unavailable"
	}
	dollars, micros := *value.Value/1_000_000, *value.Value%1_000_000
	result := fmt.Sprintf("$%s.%06d (estimate)", formatUint(dollars), micros)
	if value.State == store.AvailabilityPartial {
		result += " (partial)"
	}
	return result
}

func periodLine(label string, summary store.UsageSummary) string {
	return fmt.Sprintf("%s · %s tokens · API-equivalent estimate %s", label, totalTokens(summary.Tokens), formatEstimate(summary.APIEstimate))
}

func collectionFreshness(last *time.Time, now time.Time) string {
	if last == nil {
		return "never"
	}
	age := now.Sub(*last)
	if age < 0 {
		age = 0
	}
	if age < time.Minute {
		return "just now"
	}
	if age < time.Hour {
		return fmt.Sprintf("%dm ago", int(age.Minutes()))
	}
	return fmt.Sprintf("%dh ago · %s", int(age.Hours()), last.UTC().Format(time.RFC3339))
}

func unhealthyAdapters(records []store.StoredAdapterHealth) int {
	count := 0
	for _, record := range records {
		switch record.Health.Status {
		case normalize.HealthPermissionDenied, normalize.HealthUnsupported, normalize.HealthDegraded, normalize.HealthError:
			count++
		}
	}
	return count
}

func integrityText(ok bool) string {
	if ok {
		return "ok"
	}
	return "failed"
}

func formatUint(value uint64) string {
	return formatDigits(strconv.FormatUint(value, 10))
}

func formatDigits(value string) string {
	if len(value) <= 3 {
		return value
	}
	var groups []string
	for len(value) > 3 {
		index := len(value) - 3
		groups = append(groups, value[index:])
		value = value[:index]
	}
	groups = append(groups, value)
	for left, right := 0, len(groups)-1; left < right; left, right = left+1, right-1 {
		groups[left], groups[right] = groups[right], groups[left]
	}
	return strings.Join(groups, ",")
}
