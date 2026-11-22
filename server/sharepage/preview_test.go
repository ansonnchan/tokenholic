package sharepage

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	serveraccount "github.com/ansonnchan/tokenholic/server/account"
	servergroup "github.com/ansonnchan/tokenholic/server/group"
	serverleaderboard "github.com/ansonnchan/tokenholic/server/leaderboard"
)

// TestWritePreviews writes each share card, and one share page, through the
// real handler when SHAREPAGE_PREVIEW names a directory, so the output can be
// looked at. It is skipped otherwise. Every name and number is synthetic.
func TestWritePreviews(t *testing.T) {
	directory := os.Getenv("SHAREPAGE_PREVIEW")
	if directory == "" {
		t.Skip("SHAREPAGE_PREVIEW is not set")
	}
	handler := newHandler(t, previewProfiles{}, previewBoards{})
	for name, path := range map[string]string{
		"card-profile.png":          "/s/u/dana/card.png",
		"card-profile-quiet.png":    "/s/u/newbie/card.png",
		"card-board.png":            "/s/b/g-nightshift/card.png",
		"card-board-tokens.png":     "/s/b/g-nightshift/card.png?period=all_time&metric=total_tokens",
		"card-board-small.png":      "/s/b/g-twoofus/card.png",
		"page-profile.html":         "/s/u/dana",
		"page-not-found.html":       "/s/u/nobody",
		"card-profile-longname.png": "/s/u/juno-with-a-very-long-github-login/card.png",
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "https://api.tokenholic.example"+path, nil))
		body := response.Body.Bytes()
		if strings.HasSuffix(name, ".html") {
			// Point the page at the card beside it and stop it leaving, so a
			// browser can show what a crawler reads.
			page := strings.ReplaceAll(string(body), "https://api.tokenholic.example/s/u/dana/card.png", "card-profile.png")
			lines := strings.Split(page, "\n")
			kept := lines[:0]
			for _, line := range lines {
				if !strings.Contains(line, `http-equiv="refresh"`) {
					kept = append(kept, line)
				}
			}
			body = []byte(strings.Join(kept, "\n"))
		}
		if err := os.WriteFile(filepath.Join(directory, name), body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

type previewProfiles struct{}

func (previewProfiles) PublicProfile(_ context.Context, login string) (serveraccount.PublicProfile, error) {
	partial, complete := serveraccount.AvailabilityPartial, serveraccount.AvailabilityComplete
	switch login {
	case "dana", "juno-with-a-very-long-github-login":
		return serveraccount.PublicProfile{
			Login:      login,
			Last30Days: totals(value("4263000000", partial), value("1180420000", partial), "3934749000", "328251000"),
			AllTime:    totals(value("21870000000", partial), value("6204180000", partial), "20623410000", "1246590000"),
		}, nil
	case "newbie":
		return serveraccount.PublicProfile{
			Login:      login,
			Last30Days: totals(value("", serveraccount.AvailabilityEmpty), value("", serveraccount.AvailabilityEmpty), "0", "0"),
			AllTime:    totals(value("987", complete), value("", serveraccount.AvailabilityUnavailable), "0", "987"),
		}, nil
	}
	return serveraccount.PublicProfile{}, serveraccount.ErrProfileNotFound
}

type previewBoards struct{}

func (previewBoards) PublicGroup(_ context.Context, slug string, period serverleaderboard.Period, metric serverleaderboard.Metric) (serverleaderboard.Board, error) {
	complete := serveraccount.AvailabilityComplete
	board := serverleaderboard.Board{GroupName: "night-shift", Period: period, Metric: metric, MinRankedMembers: 3, TotalMembers: 9}
	switch {
	case slug == "g-twoofus":
		board.GroupName, board.TotalMembers, board.RanksHidden = "two of us", 2, true
		board.Rows = []serverleaderboard.Row{
			{DisplayName: "dana", Value: value("1180000000", complete)},
			{DisplayName: "kai", Value: value("560750000", serveraccount.AvailabilityPartial)},
		}
	case slug != "g-nightshift":
		return serverleaderboard.Board{}, servergroup.ErrNotFound
	case metric == serverleaderboard.MetricTotalTokens:
		board.Rows = []serverleaderboard.Row{
			{Rank: rank(1), DisplayName: "kai", Value: value("4270000000", complete)},
			{Rank: rank(2), DisplayName: "dana", Value: value("4263000000", serveraccount.AvailabilityPartial)},
			{Rank: rank(3), DisplayName: "juno-with-a-very-long-github-login", Value: value("2100000000", complete)},
		}
	default:
		board.Rows = []serverleaderboard.Row{
			{Rank: rank(1), DisplayName: "dana", Value: value("1180000000", complete)},
			{Rank: rank(2), DisplayName: "kai", Value: value("1090000000", complete)},
			{Rank: rank(3), DisplayName: "rio", Value: value("840500000", complete)},
		}
	}
	return board, nil
}
