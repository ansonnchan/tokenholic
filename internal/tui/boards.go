package tui

import (
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/ansonnchan/tokenholic/internal/multiplayer"
	"github.com/ansonnchan/tokenholic/internal/tui/widget"
)

// The board area still renders the text layout that predates the design
// system, framed in a panel. The board view replaces this file.

// boardPage is the text content of the board area.
type boardPage struct {
	lines   []string
	warning string
}

// boardState is everything the board area holds between loads.
type boardState struct {
	loading  bool
	loaded   bool
	failure  string
	page     boardPage
	snapshot multiplayer.Snapshot
	groupID  string
	period   multiplayer.Period
	metric   multiplayer.Metric
	input    groupInput
	request  uint64
}

// boardsView frames the group text and the inline create and join prompts.
func (m Model) boardsView(width int) []string {
	t := m.theme
	inner := widget.InnerWidth(width)
	var lines []string
	if action := m.board.input.action; action != "" {
		prompt, value, hint := "Create group name", m.board.input.value, "enter submit "+t.Glyphs.Separator+" esc cancel"
		if action == "join" {
			// An invite code is a secret: never echo it.
			prompt, value = "Join with invite code", strings.Repeat("*", len([]rune(value)))
			hint += " " + t.Glyphs.Separator + " invite code hidden"
		}
		lines = append(lines, t.Accent.Render(widget.Truncate(t, prompt+": "+value+"_", inner)), t.Muted.Render(hint), "")
	}
	switch {
	case m.board.failure != "":
		lines = append(lines, widget.Notice(t, widget.Failure, m.board.failure+". Press r to retry; check TOKENHOLIC_API_URL and `tokenholic claim`.", inner)...)
	case !m.board.loaded:
		lines = append(lines, t.Muted.Render(m.spin()+" Reading private groups"))
	}
	if m.board.page.warning != "" {
		lines = append(lines, widget.Notice(t, widget.Warning, m.board.page.warning, inner)...)
	}
	for _, line := range m.board.page.lines {
		lines = append(lines, widget.Wrap(cleanLine(line), inner)...)
	}
	return widget.Panel(t, "boards", width, lines)
}

func groupPage(snapshot multiplayer.Snapshot) boardPage {
	if len(snapshot.Groups) == 0 {
		return boardPage{lines: []string{
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
	page := boardPage{lines: []string{
		fmt.Sprintf("Group %d/%d · %s · %s · %s", groupIndex+1, len(snapshot.Groups), snapshot.Selected.Name, snapshot.Selected.Role, snapshot.Selected.Timezone),
		"j/k switch group · p period · m metric · c create · i join",
	}}
	if snapshot.Selected.Role == "owner" {
		page.lines = append(page.lines, "Create an invite with `tokenholic group invite <name>`.")
	}
	if snapshot.Board == nil {
		return page
	}
	board := snapshot.Board
	page.lines = append(page.lines,
		fmt.Sprintf("Period: %s · Metric: %s", periodLabel(board.Period), metricLabel(board.Metric)),
		fmt.Sprintf("Window: %s · generated %s", windowLabel(board.Window), board.GeneratedAt.UTC().Format(time.RFC3339)),
		"Movement is "+comparisonLabel(board)+", at the same point in the period.",
	)
	for _, row := range board.Rows {
		if !row.CurrentUser {
			continue
		}
		standing := "unranked"
		if row.Rank != nil {
			standing = fmt.Sprintf("#%d of %d", *row.Rank, len(board.Rows))
		}
		page.lines = append(page.lines, fmt.Sprintf("Your standing: %s · %s", standing, formatRemoteValue(row.Value, board.Metric)))
		break
	}
	page.lines = append(page.lines, "", "Leaderboard")
	if len(board.Rows) == 0 {
		page.lines = append(page.lines, "No active members to rank.")
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
		page.lines = append(page.lines, fmt.Sprintf(
			"%s %s · %s · %s · last synced %s",
			rank, name, formatRemoteValue(row.Value, board.Metric), movementLabel(row), collectionFreshness(row.LastSyncedAt, board.GeneratedAt),
		))
		if row.UnrankedReason == multiplayer.UnrankedCostCoverage {
			page.lines = append(page.lines, fmt.Sprintf(
				"  %s of tokens priced; an estimate is ranked from %s",
				basisPointsLabel(row.Coverage.PricedBasisPoints), basisPointsLabel(&board.CostRankMinPricedBasisPoints),
			))
		}
		if row.Plausibility.DailyTokensAboveLimit {
			page.lines = append(page.lines, "  note: a daily total is above the plausibility limit; usage is self-reported")
		}
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
			page.lines = append(page.lines, "  "+strings.Join(context, " · "))
		}
		limitations := row.Limitations
		if limitations.UnknownModelBuckets > 0 || limitations.InferredModelBuckets > 0 || limitations.APIEstimateUnavailableBuckets > 0 {
			page.lines = append(page.lines, fmt.Sprintf(
				"  coverage: unknown model %d · inferred model %d · API estimate unavailable %d",
				limitations.UnknownModelBuckets, limitations.InferredModelBuckets, limitations.APIEstimateUnavailableBuckets,
			))
			page.warning = "Some leaderboard rows have model or API-estimate limitations"
		}
		if row.Value.State == multiplayer.AvailabilityPartial || row.Value.State == multiplayer.AvailabilityUnavailable {
			page.warning = "Some leaderboard values are lower bounds or unavailable"
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
	} else {
		formatted += " tokens"
	}
	if value.State == multiplayer.AvailabilityPartial {
		// Some usage is missing from the value. Whether the row is ranked is
		// the server's decision and is shown by its rank.
		formatted = "at least " + formatted
	}
	return formatted
}

// comparisonLabel names what rank movement is measured against.
func comparisonLabel(board *multiplayer.Board) string {
	if board.Comparison.Kind == multiplayer.ComparisonDayEarlier {
		return "against 24 hours ago"
	}
	switch board.Period {
	case multiplayer.PeriodToday:
		return "against yesterday"
	case multiplayer.PeriodWeek:
		return "against last week"
	case multiplayer.PeriodLast30Days:
		return "against the prior 30 days"
	default:
		return "against the previous period"
	}
}

// movementLabel describes a row's rank change in words, so it reads the same
// without color or Unicode.
func movementLabel(row multiplayer.Row) string {
	switch {
	case row.Trend.New:
		return "new"
	case row.Trend.RankChange == nil:
		return "no movement to compare"
	case *row.Trend.RankChange > 0:
		return fmt.Sprintf("up %d", *row.Trend.RankChange)
	case *row.Trend.RankChange < 0:
		return fmt.Sprintf("down %d", -*row.Trend.RankChange)
	default:
		return "no change"
	}
}

// basisPointsLabel renders hundredths of a percent, such as 9400 as "94%".
func basisPointsLabel(basisPoints *int) string {
	if basisPoints == nil {
		return "none"
	}
	if *basisPoints%100 == 0 {
		return fmt.Sprintf("%d%%", *basisPoints/100)
	}
	return fmt.Sprintf("%d.%02d%%", *basisPoints/100, *basisPoints%100)
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
