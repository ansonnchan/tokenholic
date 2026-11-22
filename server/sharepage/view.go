package sharepage

import (
	"strconv"
	"strings"
	"unicode"

	"github.com/ansonnchan/tokenholic/internal/numfmt"
	serveraccount "github.com/ansonnchan/tokenholic/server/account"
	serverleaderboard "github.com/ansonnchan/tokenholic/server/leaderboard"
	"github.com/ansonnchan/tokenholic/server/sharecard"
)

// This file turns public API values into the words a share page and its card
// show. It keeps the rule every other surface keeps: a value that is not
// known is the unavailable glyph, a partial one is a lower bound, and only a
// known number is a number. Every estimate is called an estimate.

const (
	unavailable = "—"
	atLeast     = "≥"
	// estimateFootnote is the card's small print wherever an estimate shows.
	estimateFootnote = "Est = API list prices, never spending · self-reported"
	tokensFootnote   = "Usage is self-reported"
	// boardCardRows is how many standings a board card has room for.
	boardCardRows = 3
)

// view is one share page: its words, and the card that illustrates it.
type view struct {
	Title       string
	Description string
	Card        sharecard.Card
}

func bound(value serveraccount.Value) string {
	if value.State == serveraccount.AvailabilityPartial {
		return atLeast
	}
	return ""
}

func tokensText(value serveraccount.Value) string {
	if value.Value == nil {
		return unavailable
	}
	formatted, ok := numfmt.TokensDecimal(*value.Value)
	if !ok {
		return unavailable
	}
	return bound(value) + formatted
}

func estimateText(value serveraccount.Value) string {
	if value.Value == nil {
		return unavailable
	}
	formatted, ok := numfmt.USDDecimal(*value.Value)
	if !ok {
		return unavailable
	}
	return bound(value) + formatted
}

func pricedText(totals serveraccount.PublicTotals) string {
	coverage := serverleaderboard.NewCoverage(totals.PricedTokens, totals.UnpricedTokens)
	if coverage.PricedBasisPoints == nil {
		return unavailable
	}
	return numfmt.Percent(*coverage.PricedBasisPoints)
}

// spoken turns a formatted value into prose: "≥4.26B" reads "at least 4.26B".
func spoken(text string) string {
	if rest, found := strings.CutPrefix(text, atLeast); found {
		return "at least " + rest
	}
	return text
}

// sentence describes one window's totals in the third person.
func sentence(totals serveraccount.PublicTotals, period string) string {
	tokens := tokensText(totals.TotalTokens)
	switch {
	case totals.TotalTokens.State == serveraccount.AvailabilityEmpty:
		return "No coding-agent usage " + period + "."
	case tokens == unavailable:
		return "Token total unknown " + period + "."
	}
	text := capitalize(spoken(tokens)) + " tokens with coding agents " + period + "."
	if estimate := estimateText(totals.APIEstimate); estimate != unavailable {
		text += " " + capitalize(spoken(estimate)) + " at API list prices (an estimate, never spending)."
	} else {
		text += " No estimate: this usage has no known price."
	}
	return text
}

func capitalize(text string) string {
	if text == "" {
		return text
	}
	return strings.ToUpper(text[:1]) + text[1:]
}

// profileView is the share page for a public profile. The headline is the
// last 30 days, as the terminal's share action uses.
func profileView(profile serveraccount.PublicProfile) view {
	login := clean(profile.Login)
	recent, all := profile.Last30Days, profile.AllTime
	note := "All-time  " + tokensText(all.TotalTokens) + " tokens · "
	if estimate := estimateText(all.APIEstimate); estimate != unavailable {
		note += estimate + " est"
	} else {
		note += "no estimate"
	}
	if recent.TotalTokens.State == serveraccount.AvailabilityEmpty {
		note = "No usage in the last 30 days · " + note
	}
	return view{
		Title:       "@" + login + " on tokenholic",
		Description: sentence(recent, "in the last 30 days"),
		Card: sharecard.Card{
			Kind: "Public profile", Title: "@" + login, Section: "Last 30 days",
			Tiles: []sharecard.Tile{
				{Label: "Tokens", Value: tokensText(recent.TotalTokens)},
				{Label: "$ Est", Value: estimateText(recent.APIEstimate)},
				{Label: "Priced", Value: pricedText(recent)},
			},
			Note: note, Footnote: estimateFootnote,
		},
	}
}

var periodNames = map[serverleaderboard.Period]string{
	serverleaderboard.PeriodToday: "Today", serverleaderboard.PeriodWeek: "This week",
	serverleaderboard.PeriodLast30Days: "Last 30 days", serverleaderboard.PeriodAllTime: "All-time",
}

// boardView is the share page for a public group board. Rows keep the
// server's order and ranks; nothing here ranks or reorders.
func boardView(board serverleaderboard.Board) view {
	name := clean(board.GroupName)
	if name == "" {
		name = "Public board"
	}
	estimate := board.Metric == serverleaderboard.MetricAPIEstimate
	metric, footnote, format := "tokens", tokensFootnote, tokensText
	if estimate {
		metric, footnote, format = "$ est", estimateFootnote, estimateText
	}
	period := periodNames[board.Period]
	card := sharecard.Card{Kind: "Public board", Title: name, Section: period + " · " + metric, Footnote: footnote}
	var standings []string
	for index, row := range board.Rows {
		if index == boardCardRows {
			break
		}
		rank, tone := unavailable, sharecard.ToneMuted
		if row.Rank != nil {
			rank, tone = strconv.Itoa(*row.Rank), rankTone(*row.Rank)
		}
		member, value := "@"+clean(row.DisplayName), format(row.Value)
		card.Rows = append(card.Rows, sharecard.Row{Rank: rank, Tone: tone, Name: member, Value: value})
		if row.Rank != nil {
			standings = append(standings, rank+". "+member+" "+spoken(value))
		} else {
			standings = append(standings, member+" "+spoken(value))
		}
	}
	switch {
	case len(board.Rows) == 0:
		card.Note = "No member has a public profile yet"
	case board.RanksHidden:
		card.Note = "Ranks appear once " + strconv.Itoa(board.MinRankedMembers) + " members can be ranked"
	case board.TotalMembers == 1:
		card.Note = "1 public member"
	default:
		card.Note = strconv.Itoa(board.TotalMembers) + " public members"
	}
	description := period + " by total tokens"
	if estimate {
		description = period + " by API-equivalent estimate"
	}
	if len(standings) > 0 {
		description += ": " + strings.Join(standings, ", ") + "."
	} else {
		description += ". " + card.Note + "."
	}
	if estimate {
		description += " Estimates are at API list prices, never spending."
	}
	return view{Title: name + " on tokenholic", Description: description, Card: card}
}

func rankTone(rank int) sharecard.Tone {
	switch rank {
	case 1:
		return sharecard.ToneGold
	case 2:
		return sharecard.ToneSilver
	case 3:
		return sharecard.ToneBronze
	default:
		return sharecard.TonePlain
	}
}

// clean drops control characters from text an account or group owner chose,
// before it is drawn or written into a page.
func clean(text string) string {
	return strings.TrimSpace(strings.Map(func(character rune) rune {
		if unicode.IsControl(character) {
			return -1
		}
		return character
	}, text))
}
