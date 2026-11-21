package postgres

import (
	"bytes"
	"errors"
	"fmt"
	"testing"
	"time"

	serverauth "github.com/ansonnchan/tokenholic/server/auth"
	"github.com/ansonnchan/tokenholic/server/leaderboard"
)

// "Now" is 10:00 UTC on Wednesday November 18. The global board uses UTC
// days whatever timezone a member's groups keep.
func TestPostgresGlobalBoardListsOnlyAccountsThatOptedIn(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	user := func(n int) string { return fmt.Sprintf("62000000-0000-4000-8000-%012d", n) }
	device := func(n int) string { return user(n + 100) }
	names := []string{"Alpha", "Beta", "Charlie", "Delta", "Echo", "Foxtrot"}
	for index, name := range names {
		if _, err := pool.Exec(ctx, `INSERT INTO users(id, display_name) VALUES ($1::uuid, $2)`, user(index+1), name); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO devices(id, user_id) VALUES ($1::uuid, $2::uuid)`, device(index+1), user(index+1)); err != nil {
			t.Fatal(err)
		}
	}
	principal := func(n int) serverauth.Principal {
		return serverauth.Principal{UserID: user(n), DeviceID: device(n)}
	}
	alpha, beta, charlie, delta, echo, foxtrot := principal(1), principal(2), principal(3), principal(4), principal(5), principal(6)
	if _, err := pool.Exec(ctx, `INSERT INTO pricing_revisions(revision, catalog_checksum, effective_at) VALUES ('leaderboard-r1', $1, $2)`,
		bytes.Repeat([]byte{0x61}, 32), time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 18, 10, 0, 0, 0, time.UTC)
	sequence := 0
	add := func(member serverauth.Principal, start time.Time, tokens int64) {
		t.Helper()
		sequence++
		estimate := tokens
		insertLeaderboardBucket(t, pool, leaderboardBucket{
			deviceID: member.DeviceID, key: fmt.Sprintf("%064x", sequence), start: start,
			source: "claude", surface: "cli", observedModel: "model-a", canonicalModel: "model-a", modelProvider: "synthetic", pricingKey: "model-a",
			resolution: "resolved", input: tokens, estimate: &estimate, estimateState: "complete",
		})
	}
	today := time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC)
	add(alpha, today.Add(time.Hour), 300)
	add(alpha, today.Add(-time.Minute), 7000) // 23:59 UTC yesterday: not today
	add(beta, today, 200)                     // the first minute of the UTC day
	add(charlie, today.Add(2*time.Hour), 100)
	add(delta, today.Add(3*time.Hour), 9000) // the largest usage, never opted in
	add(foxtrot, today.Add(4*time.Hour), 8000)
	// Echo opts in without any usage.

	accounts, err := NewAccountRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	repository, err := NewLeaderboardRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	board := func(viewer serverauth.Principal, period leaderboard.Period) leaderboard.Board {
		t.Helper()
		service, err := leaderboard.NewService(staticLeaderboardAuthenticator{principal: viewer}, fixedLeaderboardRepository{repository: repository, now: now})
		if err != nil {
			t.Fatal(err)
		}
		result, err := service.Global(ctx, "credential", period, leaderboard.MetricTotalTokens)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	names = names[:0]
	listed := func(board leaderboard.Board) string {
		text := ""
		for _, row := range board.Rows {
			rank := "-"
			if row.Rank != nil {
				rank = fmt.Sprint(*row.Rank)
			}
			value := string(row.Value.State)
			if row.Value.Value != nil {
				value = *row.Value.Value
			}
			text += fmt.Sprintf("%s:%s:%s ", row.DisplayName, rank, value)
		}
		return text
	}

	// Nobody has opted in: the default is private, whatever the usage.
	if empty := board(alpha, leaderboard.PeriodToday); len(empty.Rows) != 0 || empty.TotalMembers != 0 || !empty.RanksHidden {
		t.Fatalf("board before anyone opted in = %q", listed(empty))
	}
	if usage, err := accounts.AccountUsage(ctx, alpha); err != nil || usage.GlobalOptIn {
		t.Fatalf("account before opting in = %t, %v; want private by default", usage.GlobalOptIn, err)
	}

	for _, member := range []serverauth.Principal{alpha, beta, charlie, echo, foxtrot} {
		if err := accounts.SetGlobalOptIn(ctx, member, true); err != nil {
			t.Fatal(err)
		}
	}
	// Joining twice changes nothing.
	if err := accounts.SetGlobalOptIn(ctx, alpha, true); err != nil {
		t.Fatal(err)
	}
	if usage, err := accounts.AccountUsage(ctx, alpha); err != nil || !usage.GlobalOptIn {
		t.Fatalf("account after opting in = %t, %v", usage.GlobalOptIn, err)
	}
	// Foxtrot deletes their account after opting in.
	if _, err := pool.Exec(ctx, `UPDATE users SET deleted_at = CURRENT_TIMESTAMP WHERE id = $1::uuid`, foxtrot.UserID); err != nil {
		t.Fatal(err)
	}

	// Delta views the board without being on it.
	viewed := board(delta, leaderboard.PeriodToday)
	if got, want := listed(viewed), "Alpha:1:300 Beta:2:200 Charlie:3:100 Echo:-:empty "; got != want {
		t.Fatalf("global board =\n  %s\nwant\n  %s", got, want)
	}
	if viewed.Scope != leaderboard.ScopeGlobal || viewed.Timezone != "UTC" || viewed.GroupID != "" || viewed.TotalMembers != 4 || viewed.RanksHidden ||
		!viewed.Window.Start.Equal(today) || !viewed.Comparison.Window.Start.Equal(today.AddDate(0, 0, -1)) {
		t.Fatalf("global board header = %#v", viewed)
	}
	for _, row := range viewed.Rows {
		if row.UserID != "" || row.CurrentUser || row.LastSyncedAt != nil {
			t.Fatalf("row %s exposes account identity or sync times to another account: %#v", row.DisplayName, row)
		}
	}
	// A member sees their own row marked, and nobody else's account ID.
	own := board(beta, leaderboard.PeriodToday)
	for _, row := range own.Rows {
		if row.CurrentUser != (row.DisplayName == "Beta") || (row.UserID != "") != row.CurrentUser {
			t.Fatalf("row %s for its own viewer = current %t, user ID %q", row.DisplayName, row.CurrentUser, row.UserID)
		}
	}
	// All-time includes yesterday's usage and still never the account that stayed private.
	if got, want := listed(board(delta, leaderboard.PeriodAllTime)), "Alpha:1:7300 Beta:2:200 Charlie:3:100 Echo:-:empty "; got != want {
		t.Fatalf("all-time global board =\n  %s\nwant\n  %s", got, want)
	}

	// Charlie leaves: two rankable members remain, so ranks are hidden.
	if err := accounts.SetGlobalOptIn(ctx, charlie, false); err != nil {
		t.Fatal(err)
	}
	small := board(alpha, leaderboard.PeriodToday)
	if got, want := listed(small), "Alpha:-:300 Beta:-:200 Echo:-:empty "; got != want || !small.RanksHidden || small.MinRankedMembers != 3 {
		t.Fatalf("board below the minimum population = %q hidden %t", got, small.RanksHidden)
	}
	if small.Rows[0].UnrankedReason != leaderboard.UnrankedScopeTooSmall || small.Rows[2].UnrankedReason != leaderboard.UnrankedNoUsage {
		t.Fatalf("unranked reasons = %q / %q", small.Rows[0].UnrankedReason, small.Rows[2].UnrankedReason)
	}

	// A revoked device cannot change what its former account shows.
	if _, err := pool.Exec(ctx, `UPDATE devices SET revoked_at = CURRENT_TIMESTAMP WHERE id = $1::uuid`, delta.DeviceID); err != nil {
		t.Fatal(err)
	}
	if err := accounts.SetGlobalOptIn(ctx, delta, true); !errors.Is(err, serverauth.ErrUnauthorized) {
		t.Fatalf("opt-in from a revoked device error = %v", err)
	}
	// Neither can a device acting for another account.
	if err := accounts.SetGlobalOptIn(ctx, serverauth.Principal{UserID: delta.UserID, DeviceID: alpha.DeviceID}, true); !errors.Is(err, serverauth.ErrUnauthorized) {
		t.Fatalf("opt-in through another account's device error = %v", err)
	}
	var optedIn bool
	if err := pool.QueryRow(ctx, `SELECT global_opt_in FROM users WHERE id = $1::uuid`, delta.UserID).Scan(&optedIn); err != nil || optedIn {
		t.Fatalf("Delta opted in = %t, %v; want still private", optedIn, err)
	}
}
