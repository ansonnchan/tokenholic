package sharepage

import (
	"strings"
	"testing"

	serveraccount "github.com/ansonnchan/tokenholic/server/account"
	serverleaderboard "github.com/ansonnchan/tokenholic/server/leaderboard"
	"github.com/ansonnchan/tokenholic/server/sharecard"
)

func totals(tokens, estimate serveraccount.Value, priced, unpriced string) serveraccount.PublicTotals {
	return serveraccount.PublicTotals{TotalTokens: tokens, APIEstimate: estimate, PricedTokens: priced, UnpricedTokens: unpriced}
}

// An unknown or absent value must never become a number, on the card or in
// the words around it.
func TestProfileViewNeverShowsAnUnknownValueAsZero(t *testing.T) {
	t.Parallel()
	complete, partial := serveraccount.AvailabilityComplete, serveraccount.AvailabilityPartial
	empty, unknown := serveraccount.AvailabilityEmpty, serveraccount.AvailabilityUnavailable
	tests := []struct {
		name        string
		recent, all serveraccount.PublicTotals
		tiles       [3]string
		note        string
		description string
	}{
		{
			name:   "known totals",
			recent: totals(value("4263000000", complete), value("1180420000", complete), "4263000000", "0"),
			all:    totals(value("21870000000", complete), value("6204180000", complete), "21870000000", "0"),
			tiles:  [3]string{"4.26B", "$1,180.42", "100%"}, note: "All-time  21.9B tokens · $6,204.18 est",
			description: "4.26B tokens with coding agents in the last 30 days. $1,180.42 at API list prices (an estimate, never spending).",
		},
		{
			name:   "lower bounds stay labelled",
			recent: totals(value("4263000000", partial), value("1180420000", partial), "9499", "501"),
			all:    totals(value("21870000000", partial), value("6204180000", partial), "1", "1"),
			tiles:  [3]string{"≥4.26B", "≥$1,180.42", "94.9%"}, note: "All-time  ≥21.9B tokens · ≥$6,204.18 est",
			description: "At least 4.26B tokens with coding agents in the last 30 days. At least $1,180.42 at API list prices (an estimate, never spending).",
		},
		{
			name:   "usage with no known price",
			recent: totals(value("987", complete), value("", unknown), "0", "987"),
			all:    totals(value("987", complete), value("", unknown), "0", "987"),
			tiles:  [3]string{"987", "—", "0%"}, note: "All-time  987 tokens · no estimate",
			description: "987 tokens with coding agents in the last 30 days. No estimate: this usage has no known price.",
		},
		{
			name:   "no usage in the period",
			recent: totals(value("", empty), value("", empty), "0", "0"),
			all:    totals(value("1350000", complete), value("20000", complete), "1350000", "0"),
			tiles:  [3]string{"—", "—", "—"}, note: "No usage in the last 30 days · All-time  1.35M tokens · $0.02 est",
			description: "No coding-agent usage in the last 30 days.",
		},
		{
			name:   "totals the server could not state",
			recent: totals(value("", unknown), value("", unknown), "", ""),
			all:    totals(value("not-a-number", complete), value("-5", complete), "x", "y"),
			tiles:  [3]string{"—", "—", "—"}, note: "All-time  — tokens · no estimate",
			description: "Token total unknown in the last 30 days.",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			page := profileView(serveraccount.PublicProfile{Login: "dana", Last30Days: test.recent, AllTime: test.all})
			card := page.Card
			if len(card.Tiles) != 3 || [3]string{card.Tiles[0].Value, card.Tiles[1].Value, card.Tiles[2].Value} != test.tiles {
				t.Errorf("tiles = %+v, want %v", card.Tiles, test.tiles)
			}
			if card.Note != test.note || page.Description != test.description {
				t.Errorf("note = %q\ndescription = %q", card.Note, page.Description)
			}
			if card.Tiles[1].Label != "$ Est" || !strings.Contains(card.Footnote, "never spending") || card.Title != "@dana" || page.Title != "@dana on tokenholic" {
				t.Errorf("labels = %q, %q, %q", card.Tiles[1].Label, card.Footnote, card.Title)
			}
			if test.name != "known totals" && test.name != "lower bounds stay labelled" && test.name != "no usage in the period" {
				for _, text := range append(card.Text(), page.Description) {
					if strings.Contains(text, "$0") || strings.Contains(text, " 0 tokens") {
						t.Errorf("an unknown value reads as zero in %q", text)
					}
				}
			}
			if strings.Contains(strings.ToLower(strings.Join(card.Text(), " ")+page.Description), "spent") {
				t.Error("an estimate is described as spent")
			}
		})
	}
}

func TestBoardViewKeepsTheServersRanksAndReasons(t *testing.T) {
	t.Parallel()
	complete := serveraccount.AvailabilityComplete
	board := serverleaderboard.Board{
		GroupName: "night-shift", Period: serverleaderboard.PeriodAllTime, Metric: serverleaderboard.MetricTotalTokens,
		MinRankedMembers: 3, TotalMembers: 5,
		Rows: []serverleaderboard.Row{
			// Deliberately not in value order: the view must not re-sort.
			{Rank: rank(1), DisplayName: "kai", Value: value("2100000000", complete)},
			{Rank: rank(1), DisplayName: "dana", Value: value("4263000000", serveraccount.AvailabilityPartial)},
			{DisplayName: "sam", Value: value("", serveraccount.AvailabilityUnavailable)},
			{Rank: rank(4), DisplayName: "rio", Value: value("1", complete)},
		},
	}
	page := boardView(board)
	want := []sharecard.Row{
		{Rank: "1", Tone: sharecard.ToneGold, Name: "@kai", Value: "2.1B"},
		{Rank: "1", Tone: sharecard.ToneGold, Name: "@dana", Value: "≥4.26B"},
		{Rank: "—", Tone: sharecard.ToneMuted, Name: "@sam", Value: "—"},
	}
	if len(page.Card.Rows) != len(want) {
		t.Fatalf("rows = %+v", page.Card.Rows)
	}
	for index, row := range want {
		if page.Card.Rows[index] != row {
			t.Errorf("row %d = %+v, want %+v", index, page.Card.Rows[index], row)
		}
	}
	if page.Card.Section != "All-time · tokens" || page.Card.Note != "5 public members" || page.Card.Footnote != tokensFootnote {
		t.Errorf("card = %q, %q, %q", page.Card.Section, page.Card.Note, page.Card.Footnote)
	}
	if page.Description != "All-time by total tokens: 1. @kai 2.1B, 1. @dana at least 4.26B, @sam —." {
		t.Errorf("description = %q", page.Description)
	}

	board.Metric, board.RanksHidden = serverleaderboard.MetricAPIEstimate, true
	for index := range board.Rows {
		board.Rows[index].Rank = nil
	}
	hidden := boardView(board)
	if hidden.Card.Note != "Ranks appear once 3 members can be ranked" || hidden.Card.Rows[0].Rank != "—" || hidden.Card.Rows[0].Tone != sharecard.ToneMuted {
		t.Errorf("hidden ranks = %+v, note %q", hidden.Card.Rows, hidden.Card.Note)
	}
	if !strings.Contains(hidden.Card.Section, "$ est") || !strings.Contains(hidden.Card.Footnote, "never spending") || !strings.Contains(hidden.Description, "never spending") {
		t.Errorf("an estimate board does not label its estimates: %q / %q", hidden.Card.Section, hidden.Description)
	}

	nobody := boardView(serverleaderboard.Board{Period: serverleaderboard.PeriodWeek, Metric: serverleaderboard.MetricAPIEstimate})
	if nobody.Card.Title != "Public board" || nobody.Card.Note != "No member has a public profile yet" || len(nobody.Card.Rows) != 0 {
		t.Errorf("empty board = %+v", nobody.Card)
	}
	single := boardView(serverleaderboard.Board{GroupName: "solo", Period: serverleaderboard.PeriodToday, Metric: serverleaderboard.MetricTotalTokens, TotalMembers: 1,
		Rows: []serverleaderboard.Row{{DisplayName: "dana", Value: value("5", complete)}}})
	if single.Card.Note != "1 public member" || single.Card.Section != "Today · tokens" {
		t.Errorf("single-member board = %q, %q", single.Card.Note, single.Card.Section)
	}
}
