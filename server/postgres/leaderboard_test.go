package postgres

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	serveraccount "github.com/ansonnchan/tokenholic/server/account"
	serverauth "github.com/ansonnchan/tokenholic/server/auth"
	servergroup "github.com/ansonnchan/tokenholic/server/group"
	"github.com/ansonnchan/tokenholic/server/leaderboard"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestPostgresLeaderboardPeriodsDevicesPricesAndTies(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	owner := serverauth.Principal{UserID: "60000000-0000-4000-8000-000000000001"}
	member := serverauth.Principal{UserID: "60000000-0000-4000-8000-000000000002"}
	outsider := serverauth.Principal{UserID: "60000000-0000-4000-8000-000000000003"}
	if _, err := pool.Exec(ctx, `
		INSERT INTO users(id, display_name) VALUES
		($1::uuid, 'Alpha'), ($2::uuid, 'Beta'), ($3::uuid, 'Outside')
	`, owner.UserID, member.UserID, outsider.UserID); err != nil {
		t.Fatal(err)
	}
	groupRepository, err := NewGroupRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	groupID := "60000000-0000-4000-8000-000000000010"
	if _, err := groupRepository.CreateGroup(ctx, owner, servergroup.NewGroup{
		ID: groupID, Name: "Ranking group", Timezone: "America/Vancouver",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO group_memberships(group_id, user_id, role) VALUES ($1::uuid, $2::uuid, 'member')`, groupID, member.UserID); err != nil {
		t.Fatal(err)
	}
	ownerDeviceOne := "60000000-0000-4000-8000-000000000011"
	ownerDeviceTwo := "60000000-0000-4000-8000-000000000012"
	ownerRevokedDevice := "60000000-0000-4000-8000-000000000013"
	memberDevice := "60000000-0000-4000-8000-000000000014"
	if _, err := pool.Exec(ctx, `
		INSERT INTO devices(id, user_id, revoked_at) VALUES
		($1::uuid, $2::uuid, NULL), ($3::uuid, $2::uuid, NULL),
		($4::uuid, $2::uuid, CURRENT_TIMESTAMP), ($5::uuid, $6::uuid, NULL)
	`, ownerDeviceOne, owner.UserID, ownerDeviceTwo, ownerRevokedDevice, memberDevice, member.UserID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO pricing_revisions(revision, catalog_checksum, effective_at)
		VALUES ('leaderboard-r1', $1, $2)
	`, bytes.Repeat([]byte{0x61}, 32), time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	location, err := time.LoadLocation("America/Vancouver")
	if err != nil {
		t.Fatal(err)
	}
	dayStart := time.Date(2026, 11, 18, 0, 0, 0, 0, location).UTC()
	insertLeaderboardBucket(t, pool, leaderboardBucket{
		deviceID: ownerDeviceOne, key: strings.Repeat("a", 64), start: dayStart.Add(time.Minute),
		source: "claude", surface: "cli", observedModel: "model-a", canonicalModel: "model-a", modelProvider: "synthetic", pricingKey: "model-a", resolution: "resolved",
		input: 5, output: 5, estimate: int64Pointer(100), estimateState: "complete", pricingRevision: "leaderboard-r1",
	})
	insertLeaderboardBucket(t, pool, leaderboardBucket{
		deviceID: ownerDeviceTwo, key: strings.Repeat("b", 64), start: dayStart.Add(time.Hour),
		source: "codex", surface: "cli", observedModel: "route-model-b", canonicalModel: "model-b", modelProvider: "synthetic", pricingKey: "model-b", resolution: "inferred",
		input: 5, output: 5, estimate: int64Pointer(100), estimateState: "complete", pricingRevision: "leaderboard-r1",
	})
	insertLeaderboardBucket(t, pool, leaderboardBucket{
		deviceID: memberDevice, key: strings.Repeat("c", 64), start: dayStart.Add(10 * time.Minute),
		source: "codex", surface: "cli", observedModel: "unknown-model", resolution: "unknown",
		input: 10, output: 10, estimateState: "unavailable",
	})
	insertLeaderboardBucket(t, pool, leaderboardBucket{
		deviceID: ownerRevokedDevice, key: strings.Repeat("d", 64), start: dayStart.Add(20 * time.Minute),
		source: "claude", surface: "cli", observedModel: "model-a", canonicalModel: "model-a", modelProvider: "synthetic", pricingKey: "model-a", resolution: "resolved",
		input: 50, output: 50, estimate: int64Pointer(1000), estimateState: "complete", pricingRevision: "leaderboard-r1",
	})
	insertLeaderboardBucket(t, pool, leaderboardBucket{
		deviceID: ownerDeviceOne, key: strings.Repeat("e", 64), start: dayStart.Add(-time.Minute),
		source: "claude", surface: "cli", observedModel: "model-a", canonicalModel: "model-a", modelProvider: "synthetic", pricingKey: "model-a", resolution: "resolved",
		input: 50, output: 50, estimate: int64Pointer(1000), estimateState: "complete", pricingRevision: "leaderboard-r1",
	})

	repository, err := NewLeaderboardRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 18, 16, 30, 0, 0, time.UTC)
	// Rank through the public service using a clock-aligned repository wrapper.
	service, err := leaderboard.NewService(staticLeaderboardAuthenticator{principal: owner}, fixedLeaderboardRepository{repository: repository, now: now})
	if err != nil {
		t.Fatal(err)
	}
	board, err := service.Get(ctx, "credential", groupID, leaderboard.PeriodToday, leaderboard.MetricTotalTokens)
	if err != nil {
		t.Fatal(err)
	}
	if board.Window.Start == nil || !board.Window.Start.Equal(dayStart) || len(board.Rows) != 2 {
		t.Fatalf("leaderboard window/rows = %#v", board)
	}
	alpha := leaderboardRowByUser(t, board.Rows, owner.UserID)
	beta := leaderboardRowByUser(t, board.Rows, member.UserID)
	assertLeaderboardValue(t, alpha.Value, "20", serveraccount.AvailabilityComplete)
	assertLeaderboardValue(t, beta.Value, "20", serveraccount.AvailabilityComplete)
	if alpha.Rank == nil || beta.Rank == nil || *alpha.Rank != 1 || *beta.Rank != 1 {
		t.Fatalf("tie ranks = %#v/%#v", alpha.Rank, beta.Rank)
	}
	if !alpha.CurrentUser || beta.CurrentUser || len(alpha.SourceMix) != 2 || alpha.MostUsedModel == nil || alpha.MostUsedModel.ModelID != "model-a" || alpha.LastSyncedAt == nil {
		t.Fatalf("alpha metadata = %#v", alpha)
	}
	if alpha.Limitations.InferredModelBuckets != 1 || beta.Limitations.UnknownModelBuckets != 1 || beta.Limitations.APIEstimateUnavailableBuckets != 1 {
		t.Fatalf("resolution limitations = %#v/%#v", alpha.Limitations, beta.Limitations)
	}
	costBoard, err := service.Get(ctx, "credential", groupID, leaderboard.PeriodToday, leaderboard.MetricAPIEstimate)
	if err != nil {
		t.Fatal(err)
	}
	alphaCost := leaderboardRowByUser(t, costBoard.Rows, owner.UserID)
	betaCost := leaderboardRowByUser(t, costBoard.Rows, member.UserID)
	assertLeaderboardValue(t, alphaCost.Value, "200", serveraccount.AvailabilityComplete)
	if alphaCost.Rank == nil || *alphaCost.Rank != 1 || alphaCost.Coverage.PricedBasisPoints == nil || *alphaCost.Coverage.PricedBasisPoints != 10_000 {
		t.Fatalf("fully priced row = %#v", alphaCost)
	}
	if betaCost.Value.Value != nil || betaCost.Value.State != serveraccount.AvailabilityUnavailable || betaCost.Rank != nil ||
		betaCost.UnrankedReason != leaderboard.UnrankedValueUnavailable || betaCost.Coverage.UnpricedTokens != "20" {
		t.Fatalf("unknown-price row = %#v", betaCost)
	}
	if _, err := service.Get(ctx, "credential", groupID, leaderboard.PeriodToday, "output_tokens"); !errors.Is(err, servergroup.ErrInvalidInput) {
		t.Fatalf("retired output metric error = %v", err)
	}
	allTimeBoard, err := service.Get(ctx, "credential", groupID, leaderboard.PeriodAllTime, leaderboard.MetricTotalTokens)
	if err != nil {
		t.Fatal(err)
	}
	if allTimeBoard.Window.Start != nil {
		t.Fatalf("all-time start = %v", allTimeBoard.Window.Start)
	}
	assertLeaderboardValue(t, leaderboardRowByUser(t, allTimeBoard.Rows, owner.UserID).Value, "120", serveraccount.AvailabilityComplete)
	if _, err := repository.Leaderboard(ctx, outsider, leaderboard.Query{
		GroupID: groupID, Period: leaderboard.PeriodToday, Metric: leaderboard.MetricTotalTokens, GeneratedAt: now,
	}); !errors.Is(err, servergroup.ErrNotFound) {
		t.Fatalf("outsider leaderboard error = %v", err)
	}
}

// The group keeps Tokyo time, nine hours ahead of UTC with no clock changes,
// so every boundary below falls on a different UTC date than its local one.
// "Now" is Wednesday November 18 at 10:00 in Tokyo.
func TestPostgresLeaderboardTrendsCoverageAndPlausibility(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	user := func(n int) string { return fmt.Sprintf("61000000-0000-4000-8000-%012d", n) }
	names := []string{"Alpha", "Beta", "Charlie", "Delta", "Echo"}
	devices := map[string]string{}
	for index, name := range names {
		devices[user(index+1)] = user(index + 11)
		if _, err := pool.Exec(ctx, `INSERT INTO users(id, display_name) VALUES ($1::uuid, $2)`, user(index+1), name); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO devices(id, user_id) VALUES ($1::uuid, $2::uuid)`, user(index+11), user(index+1)); err != nil {
			t.Fatal(err)
		}
	}
	alpha, beta, charlie, delta, echo := user(1), user(2), user(3), user(4), user(5)
	groupRepository, err := NewGroupRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	groupID := user(100)
	if _, err := groupRepository.CreateGroup(ctx, serverauth.Principal{UserID: alpha}, servergroup.NewGroup{ID: groupID, Name: "Tokyo group", Timezone: "Asia/Tokyo"}); err != nil {
		t.Fatal(err)
	}
	for _, member := range []string{beta, charlie, delta, echo} {
		if _, err := pool.Exec(ctx, `INSERT INTO group_memberships(group_id, user_id, role) VALUES ($1::uuid, $2::uuid, 'member')`, groupID, member); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `INSERT INTO pricing_revisions(revision, catalog_checksum, effective_at) VALUES ('leaderboard-r1', $1, $2)`,
		bytes.Repeat([]byte{0x61}, 32), time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	tokyo, err := time.LoadLocation("Asia/Tokyo")
	if err != nil {
		t.Fatal(err)
	}
	at := func(day, hour, minute int) time.Time { return time.Date(2026, 11, day, hour, minute, 0, 0, tokyo) }
	now := at(18, 10, 0)
	sequence := 0
	// add stores tokens for a member. A positive estimate makes the bucket
	// fully priced; zero leaves all of its tokens unpriced.
	add := func(member string, start time.Time, tokens, estimate int64) {
		t.Helper()
		sequence++
		bucket := leaderboardBucket{
			deviceID: devices[member], key: fmt.Sprintf("%064x", sequence), start: start,
			source: "claude", surface: "cli", observedModel: "unknown-model", resolution: "unknown",
			input: tokens, estimateState: "unavailable",
		}
		if estimate > 0 {
			bucket.observedModel, bucket.canonicalModel, bucket.modelProvider, bucket.pricingKey = "model-a", "model-a", "synthetic", "model-a"
			bucket.resolution, bucket.estimate, bucket.estimateState = "resolved", &estimate, "complete"
		}
		insertLeaderboardBucket(t, pool, bucket)
	}
	// Alpha: 300 today, 100 in yesterday's first ten hours.
	add(alpha, at(18, 9, 0), 300, 3000)
	add(alpha, at(17, 9, 0), 100, 1000)
	add(alpha, at(17, 11, 0), 5000, 50000) // yesterday, but after the same elapsed time
	add(alpha, at(18, 10, 0), 9999, 99990) // exactly at "now": the window end is exclusive
	// Beta: 200 today on the first minute of the local day, 200 yesterday.
	add(beta, at(18, 0, 0), 200, 2000)
	add(beta, at(17, 8, 0), 200, 2000)
	add(beta, at(17, 23, 59), 7, 70) // the last minute of yesterday is in neither window
	// Charlie: exactly 95% priced today; half priced yesterday.
	add(charlie, at(18, 1, 0), 190, 9000)
	add(charlie, at(18, 2, 0), 10, 0)
	add(charlie, at(17, 1, 0), 50, 500)
	add(charlie, at(17, 2, 0), 50, 0)
	// Delta: first usage today, 94% priced, with the largest estimate.
	add(delta, at(18, 3, 0), 94, 9999)
	add(delta, at(18, 4, 0), 6, 0)
	// Echo has no usage at all.
	// Two members each report six billion tokens last week. Beta's fall on
	// one Tokyo date that spans two UTC dates; Alpha's fall on two Tokyo
	// dates inside one UTC date.
	const threeBillion = 3_000_000_000
	add(beta, at(10, 0, 10), threeBillion, 1)
	add(beta, at(10, 23, 50), threeBillion, 1)
	add(alpha, at(10, 23, 30), threeBillion, 1)
	add(alpha, at(11, 0, 30), threeBillion, 1)

	repository, err := NewLeaderboardRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	service, err := leaderboard.NewService(staticLeaderboardAuthenticator{principal: serverauth.Principal{UserID: alpha}}, fixedLeaderboardRepository{repository: repository, now: now})
	if err != nil {
		t.Fatal(err)
	}
	type line struct {
		name          string
		rank          string
		value         string
		previousValue string
		previousRank  string
		change        string
		isNew         bool
		basisPoints   string
	}
	describe := func(board leaderboard.Board) []line {
		number := func(value *int) string {
			if value == nil {
				return "-"
			}
			return fmt.Sprint(*value)
		}
		text := func(value serveraccount.Value) string {
			if value.Value == nil {
				return string(value.State)
			}
			return *value.Value
		}
		lines := make([]line, 0, len(board.Rows))
		for _, row := range board.Rows {
			rank := number(row.Rank)
			if row.Rank == nil {
				rank = string(row.UnrankedReason)
			}
			lines = append(lines, line{
				name: row.DisplayName, rank: rank, value: text(row.Value), previousValue: text(row.Trend.PreviousValue),
				previousRank: number(row.Trend.PreviousRank), change: number(row.Trend.RankChange), isNew: row.Trend.New,
				basisPoints: number(row.Coverage.PricedBasisPoints),
			})
		}
		return lines
	}
	assertLines := func(name string, got, want []line) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("%s: %d rows, want %d: %+v", name, len(got), len(want), got)
		}
		for index := range want {
			if got[index] != want[index] {
				t.Errorf("%s row %d =\n  %+v\nwant\n  %+v", name, index, got[index], want[index])
			}
		}
	}

	tokens, err := service.Get(ctx, "credential", groupID, leaderboard.PeriodToday, leaderboard.MetricTotalTokens)
	if err != nil {
		t.Fatal(err)
	}
	if !tokens.Window.Start.Equal(at(18, 0, 0)) || !tokens.Window.End.Equal(now) || tokens.Comparison.Kind != leaderboard.ComparisonPreviousPeriod ||
		!tokens.Comparison.Window.Start.Equal(at(17, 0, 0)) || !tokens.Comparison.Window.End.Equal(at(17, 10, 0)) {
		t.Fatalf("windows = %#v / %#v", tokens.Window, tokens.Comparison)
	}
	// Yesterday by 10:00: Beta 200, Alpha and Charlie 100 each.
	assertLines("tokens today", describe(tokens), []line{
		{name: "Alpha", rank: "1", value: "300", previousValue: "100", previousRank: "2", change: "1", basisPoints: "10000"},
		{name: "Beta", rank: "2", value: "200", previousValue: "200", previousRank: "1", change: "-1", basisPoints: "10000"},
		{name: "Charlie", rank: "2", value: "200", previousValue: "100", previousRank: "2", change: "0", basisPoints: "9500"},
		{name: "Delta", rank: "4", value: "100", previousValue: "empty", previousRank: "-", change: "-", isNew: true, basisPoints: "9400"},
		{name: "Echo", rank: "no_usage", value: "empty", previousValue: "empty", previousRank: "-", change: "-", basisPoints: "-"},
	})
	for _, row := range tokens.Rows {
		if row.Plausibility.DailyTokensAboveLimit {
			t.Fatalf("%s is flagged today, when nobody is near the limit", row.DisplayName)
		}
	}

	cost, err := service.Get(ctx, "credential", groupID, leaderboard.PeriodToday, leaderboard.MetricAPIEstimate)
	if err != nil {
		t.Fatal(err)
	}
	// Charlie ranks at exactly 95%. Delta has the largest estimate but only
	// 94% of the tokens behind it, so it is shown without a rank. Charlie was
	// half priced yesterday and so has no rank to move from.
	assertLines("estimate today", describe(cost), []line{
		{name: "Charlie", rank: "1", value: "9000", previousValue: "500", previousRank: "-", change: "-", basisPoints: "9500"},
		{name: "Alpha", rank: "2", value: "3000", previousValue: "1000", previousRank: "2", change: "0", basisPoints: "10000"},
		{name: "Beta", rank: "3", value: "2000", previousValue: "2000", previousRank: "1", change: "-2", basisPoints: "10000"},
		{name: "Delta", rank: "cost_coverage_below_threshold", value: "9999", previousValue: "empty", previousRank: "-", change: "-", isNew: true, basisPoints: "9400"},
		{name: "Echo", rank: "no_usage", value: "empty", previousValue: "empty", previousRank: "-", change: "-", basisPoints: "-"},
	})
	if delta := leaderboardRowByUser(t, cost.Rows, delta); delta.Value.State != serveraccount.AvailabilityPartial || delta.Coverage.UnpricedTokens != "6" {
		t.Fatalf("below-threshold estimate = %#v, want a lower bound with its unpriced tokens", delta)
	}
	if echo := leaderboardRowByUser(t, cost.Rows, echo); echo.Rank != nil || echo.Trend.New {
		t.Fatalf("member without usage = %#v", echo)
	}

	// Thirty local dates reach back past last week's large days.
	month, err := service.Get(ctx, "credential", groupID, leaderboard.PeriodLast30Days, leaderboard.MetricTotalTokens)
	if err != nil {
		t.Fatal(err)
	}
	flagged := map[string]bool{}
	for _, row := range month.Rows {
		flagged[row.DisplayName] = row.Plausibility.DailyTokensAboveLimit
	}
	if !flagged["Beta"] || flagged["Alpha"] || flagged["Charlie"] {
		t.Fatalf("flagged members = %v; want only Beta, whose six billion fall on one Tokyo date", flagged)
	}
	// A flag is advisory: the member keeps every token and the rank it earns,
	// here second to Alpha's 6,000,005,400.
	if beta := leaderboardRowByUser(t, month.Rows, beta); beta.Rank == nil || *beta.Rank != 2 || beta.Value.Value == nil || *beta.Value.Value != "6000000407" {
		t.Fatalf("flagged member = %#v, want rank 2 with every token counted", beta)
	}

	// All-time has no previous period: it is compared with 24 hours earlier.
	allTime, err := service.Get(ctx, "credential", groupID, leaderboard.PeriodAllTime, leaderboard.MetricTotalTokens)
	if err != nil {
		t.Fatal(err)
	}
	if allTime.Comparison.Kind != leaderboard.ComparisonDayEarlier || allTime.Comparison.Window.Start != nil || !allTime.Comparison.Window.End.Equal(at(17, 10, 0)) {
		t.Fatalf("all-time comparison = %#v", allTime.Comparison)
	}
	// Delta's first usage is inside the last 24 hours.
	deltaRow := leaderboardRowByUser(t, allTime.Rows, delta)
	if !deltaRow.Trend.New || deltaRow.Trend.PreviousRank != nil || deltaRow.Value.Value == nil || *deltaRow.Value.Value != "100" {
		t.Fatalf("all-time entrant = %#v", deltaRow)
	}
	// Charlie: 300 tokens now, 100 as of 24 hours ago.
	charlieRow := leaderboardRowByUser(t, allTime.Rows, charlie)
	if *charlieRow.Value.Value != "300" || charlieRow.Trend.PreviousValue.Value == nil || *charlieRow.Trend.PreviousValue.Value != "100" {
		t.Fatalf("all-time values = %v now, %v before", charlieRow.Value.Value, charlieRow.Trend.PreviousValue.Value)
	}
}

type fixedLeaderboardRepository struct {
	repository *LeaderboardRepository
	now        time.Time
}

func (f fixedLeaderboardRepository) Leaderboard(ctx context.Context, principal serverauth.Principal, query leaderboard.Query) (leaderboard.Board, error) {
	query.GeneratedAt = f.now
	return f.repository.Leaderboard(ctx, principal, query)
}

func (f fixedLeaderboardRepository) PublicGroupLeaderboard(ctx context.Context, slug string, query leaderboard.Query) (leaderboard.Board, error) {
	query.GeneratedAt = f.now
	return f.repository.PublicGroupLeaderboard(ctx, slug, query)
}

func (f fixedLeaderboardRepository) GlobalLeaderboard(ctx context.Context, query leaderboard.Query) (leaderboard.Board, error) {
	query.GeneratedAt = f.now
	return f.repository.GlobalLeaderboard(ctx, query)
}

type staticLeaderboardAuthenticator struct {
	principal serverauth.Principal
}

func (s staticLeaderboardAuthenticator) Authenticate(context.Context, string) (serverauth.Principal, error) {
	return s.principal, nil
}

type leaderboardBucket struct {
	deviceID        string
	key             string
	start           time.Time
	source          string
	surface         string
	observedModel   string
	canonicalModel  string
	modelProvider   string
	pricingKey      string
	resolution      string
	input           int64
	output          int64
	estimate        *int64
	estimateState   string
	pricingRevision string
}

func insertLeaderboardBucket(t *testing.T, pool interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}, bucket leaderboardBucket) {
	t.Helper()
	var canonicalModel, modelProvider, pricingKey, pricingRevision any
	if bucket.canonicalModel != "" {
		canonicalModel = bucket.canonicalModel
		modelProvider = bucket.modelProvider
		pricingKey = bucket.pricingKey
	}
	// Every v2 row names the revision that derived it, priced or not.
	pricingRevision = "leaderboard-r1"
	if bucket.pricingRevision != "" {
		pricingRevision = bucket.pricingRevision
	}
	// Derived columns follow the schema's invariants: a row is fully priced
	// when the test gives it an estimate and fully unpriced otherwise.
	if _, err := pool.Exec(t.Context(), `
		INSERT INTO usage_buckets(
			device_id, bucket_key, bucket_start, interval_seconds, source, surface,
			observed_model_id, model_evidence, measurement, normalization_version,
			input_uncached_tokens, input_uncached_state,
			input_cache_read_tokens, input_cache_read_state,
			input_cache_write_tokens, input_cache_write_state,
			input_cache_write_5m_tokens, input_cache_write_5m_state,
			input_cache_write_1h_tokens, input_cache_write_1h_state,
			output_tokens, output_state,
			reasoning_output_tokens, reasoning_output_state,
			revision, payload_hash,
			canonical_model_id, model_provider, pricing_key, model_resolution, model_match, unpriced_reason,
			priced_tokens, unpriced_tokens,
			api_estimate_microusd, api_estimate_state, pricing_revision
		) VALUES (
			$1::uuid, $2, $3, 60, $4, $5,
			$6, CASE WHEN $10 = 'inferred' THEN 'inferred' ELSE 'reported' END, 'reported', '1',
			$11, 'complete', 0, 'complete', 0, 'complete', 0, 'complete', 0, 'complete',
			$12, 'complete', NULL, 'unavailable',
			1, $16,
			$7, $8, CASE WHEN $13::numeric IS NULL THEN NULL ELSE COALESCE($9, 'synthetic/price') END, $10,
			CASE WHEN $10 = 'unknown' THEN 'unknown' ELSE 'exact' END,
			CASE WHEN $13::numeric IS NULL THEN 'unknown_model' END,
			CASE WHEN $13::numeric IS NULL THEN 0 ELSE $11::numeric + $12::numeric END,
			CASE WHEN $13::numeric IS NULL THEN $11::numeric + $12::numeric ELSE 0 END,
			$13, $14, $15
		)
	`, bucket.deviceID, bucket.key, bucket.start, bucket.source, bucket.surface,
		bucket.observedModel, canonicalModel, modelProvider, pricingKey, bucket.resolution,
		bucket.input, bucket.output, bucket.estimate, bucket.estimateState, pricingRevision,
		bytes.Repeat([]byte{0x62}, 32)); err != nil {
		t.Fatal(err)
	}
}

func leaderboardRowByUser(t *testing.T, rows []leaderboard.Row, userID string) leaderboard.Row {
	t.Helper()
	for _, row := range rows {
		if row.UserID == userID {
			return row
		}
	}
	t.Fatalf("missing leaderboard user %s", userID)
	return leaderboard.Row{}
}

func assertLeaderboardValue(t *testing.T, value serveraccount.Value, want string, state serveraccount.Availability) {
	t.Helper()
	if value.Value == nil || *value.Value != want || value.State != state {
		t.Fatalf("value = %#v, want %s/%s", value, want, state)
	}
}

func int64Pointer(value int64) *int64 { return &value }
