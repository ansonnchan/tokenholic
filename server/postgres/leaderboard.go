package postgres

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	serveraccount "github.com/ansonnchan/tokenholic/server/account"
	serverauth "github.com/ansonnchan/tokenholic/server/auth"
	servergroup "github.com/ansonnchan/tokenholic/server/group"
	"github.com/ansonnchan/tokenholic/server/leaderboard"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// LeaderboardRepository aggregates a board's members and their active devices
// in one repeatable-read snapshot.
type LeaderboardRepository struct {
	pool *pgxpool.Pool
}

// NewLeaderboardRepository constructs the PostgreSQL leaderboard repository.
func NewLeaderboardRepository(pool *pgxpool.Pool) (*LeaderboardRepository, error) {
	if pool == nil {
		return nil, errors.New("initialize leaderboard repository: pool is required")
	}
	return &LeaderboardRepository{pool: pool}, nil
}

// boardScope selects whose usage a board aggregates. members is the body of a
// CTE yielding (user_id, display_name) and takes exactly one parameter, $1.
// Every board query is built from it, so no query can read a user the scope
// does not include.
type boardScope struct {
	members  string
	argument any
}

// groupScope is the active members of one group.
func groupScope(groupID string) boardScope {
	return boardScope{argument: groupID, members: `
		SELECT m.user_id, u.display_name
		FROM group_memberships m
		JOIN users u ON u.id = m.user_id AND u.deleted_at IS NULL
		WHERE m.group_id = $1::uuid AND m.revoked_at IS NULL`}
}

// globalScope is every account that opted in to the global board. The column
// is false by default, so an account appears only after choosing to.
func globalScope() boardScope {
	return boardScope{argument: true, members: `
		SELECT u.id AS user_id, u.display_name
		FROM users u
		WHERE u.global_opt_in = $1::boolean AND u.deleted_at IS NULL`}
}

// Leaderboard reads one membership-authorized group ranking snapshot. Every
// row carries its value and cost coverage for both the current window and the
// comparison window, read in the same snapshot so a trend never compares two
// different states of the data.
func (r *LeaderboardRepository) Leaderboard(ctx context.Context, principal serverauth.Principal, query leaderboard.Query) (board leaderboard.Board, err error) {
	board = leaderboard.Board{
		Scope: leaderboard.ScopeGroup, GroupID: query.GroupID, Period: query.Period, Metric: query.Metric,
		GeneratedAt: query.GeneratedAt.UTC(), Rows: make([]leaderboard.Row, 0),
	}
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return board, fmt.Errorf("begin leaderboard snapshot: %w", err)
	}
	defer rollbackOnError(ctx, tx, &err)
	err = tx.QueryRow(ctx, `
		SELECT g.name, g.timezone
		FROM groups g
		JOIN group_memberships viewer ON viewer.group_id = g.id
		WHERE g.id = $1::uuid AND viewer.user_id = $2::uuid AND viewer.revoked_at IS NULL
	`, query.GroupID, principal.UserID).Scan(&board.GroupName, &board.Timezone)
	if errors.Is(err, pgx.ErrNoRows) {
		return board, servergroup.ErrNotFound
	}
	if err != nil {
		return board, fmt.Errorf("authorize leaderboard read: %w", err)
	}
	if err = readBoard(ctx, tx, groupScope(query.GroupID), &board); err != nil {
		return board, err
	}
	if err = tx.Commit(ctx); err != nil {
		return board, fmt.Errorf("commit leaderboard snapshot: %w", err)
	}
	return board, nil
}

// GlobalLeaderboard reads the opt-in global board. It needs no authorization
// of its own: the scope contains only accounts that chose to be listed, and
// it uses UTC boundaries because its members share no timezone.
func (r *LeaderboardRepository) GlobalLeaderboard(ctx context.Context, query leaderboard.Query) (board leaderboard.Board, err error) {
	board = leaderboard.Board{
		Scope: leaderboard.ScopeGlobal, Timezone: "UTC", Period: query.Period, Metric: query.Metric,
		GeneratedAt: query.GeneratedAt.UTC(), Rows: make([]leaderboard.Row, 0),
	}
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return board, fmt.Errorf("begin global leaderboard snapshot: %w", err)
	}
	defer rollbackOnError(ctx, tx, &err)
	if err = readBoard(ctx, tx, globalScope(), &board); err != nil {
		return board, err
	}
	if err = tx.Commit(ctx); err != nil {
		return board, fmt.Errorf("commit global leaderboard snapshot: %w", err)
	}
	return board, nil
}

// readBoard fills a board's windows and rows for one scope. The board's
// timezone, period, metric, and generation time must already be set.
func readBoard(ctx context.Context, tx pgx.Tx, scope boardScope, board *leaderboard.Board) error {
	if board.Metric != leaderboard.MetricTotalTokens && board.Metric != leaderboard.MetricAPIEstimate {
		return servergroup.ErrInvalidInput
	}
	location, err := time.LoadLocation(board.Timezone)
	if err != nil {
		return fmt.Errorf("load leaderboard timezone: %w", err)
	}
	if board.Window, err = leaderboard.WindowFor(board.Period, board.GeneratedAt, location); err != nil {
		return err
	}
	if board.Comparison, err = leaderboard.ComparisonFor(board.Period, board.Window, location); err != nil {
		return err
	}
	start, previousStart := windowStart(board.Window), windowStart(board.Comparison.Window)
	rows, err := tx.Query(ctx, scope.sql(memberAggregateSQL), scope.argument, start, board.Window.End, previousStart, board.Comparison.Window.End)
	if err != nil {
		return fmt.Errorf("query leaderboard members: %w", err)
	}
	for rows.Next() {
		var row leaderboard.Row
		var current, previous memberWindowScan
		if err := rows.Scan(
			&row.UserID, &row.DisplayName, &row.LastSyncedAt,
			&current.buckets, &current.total.value, &current.total.known, &current.total.complete,
			&current.estimate.value, &current.estimate.known, &current.estimate.complete,
			&current.priced, &current.unpriced,
			&previous.buckets, &previous.total.value, &previous.total.known, &previous.total.complete,
			&previous.estimate.value, &previous.estimate.known, &previous.estimate.complete,
			&previous.priced, &previous.unpriced,
			&row.Limitations.UnknownModelBuckets,
			&row.Limitations.InferredModelBuckets,
			&row.Limitations.APIEstimateUnavailableBuckets,
		); err != nil {
			rows.Close()
			return fmt.Errorf("decode leaderboard member: %w", err)
		}
		row.Value, row.Coverage = current.value(board.Metric), leaderboard.NewCoverage(current.priced, current.unpriced)
		row.Trend.PreviousValue, row.PreviousCoverage = previous.value(board.Metric), leaderboard.NewCoverage(previous.priced, previous.unpriced)
		row.SourceMix = make([]leaderboard.SourceMix, 0)
		board.Rows = append(board.Rows, row)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("iterate leaderboard members: %w", err)
	}
	rows.Close()
	rowByUser := make(map[string]*leaderboard.Row, len(board.Rows))
	for index := range board.Rows {
		rowByUser[board.Rows[index].UserID] = &board.Rows[index]
	}
	if err := readSourceMix(ctx, tx, scope, start, board.Window.End, rowByUser); err != nil {
		return err
	}
	if err := readMostUsedModels(ctx, tx, scope, start, board.Window.End, rowByUser); err != nil {
		return err
	}
	return readImplausibleDays(ctx, tx, scope, start, board.Window.End, board.Timezone, rowByUser)
}

// sql prefixes a query with the scope's members CTE.
func (s boardScope) sql(query string) string {
	return "WITH members AS (" + s.members + ")" + query
}

// memberWindowScan is one member's aggregates over one window.
type memberWindowScan struct {
	buckets          int64
	total, estimate  aggregateScan
	priced, unpriced string
}

func (w memberWindowScan) value(metric leaderboard.Metric) serveraccount.Value {
	if metric == leaderboard.MetricAPIEstimate {
		return w.estimate.coverage(w.buckets)
	}
	return w.total.coverage(w.buckets)
}

func windowStart(window leaderboard.Window) any {
	if window.Start == nil {
		return nil
	}
	return *window.Start
}

// readImplausibleDays marks members whose tokens on some local day of the
// window exceed the advisory limit. Days follow the board's timezone, the
// same boundaries its periods use.
func readImplausibleDays(ctx context.Context, tx pgx.Tx, scope boardScope, start any, end time.Time, timezone string, rowsByUser map[string]*leaderboard.Row) error {
	rows, err := tx.Query(ctx, scope.sql(implausibleDaySQL), scope.argument, start, end, timezone, strconv.FormatUint(leaderboard.PlausibleDailyTokens, 10))
	if err != nil {
		return fmt.Errorf("query leaderboard plausibility: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var userID string
		if err := rows.Scan(&userID); err != nil {
			return fmt.Errorf("decode leaderboard plausibility: %w", err)
		}
		if row := rowsByUser[userID]; row != nil {
			row.Plausibility.DailyTokensAboveLimit = true
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate leaderboard plausibility: %w", err)
	}
	return nil
}

func readSourceMix(ctx context.Context, tx pgx.Tx, scope boardScope, start any, end time.Time, rowsByUser map[string]*leaderboard.Row) error {
	rows, err := tx.Query(ctx, scope.sql(sourceMixSQL), scope.argument, start, end)
	if err != nil {
		return fmt.Errorf("query leaderboard source mix: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var userID string
		var mix leaderboard.SourceMix
		var totalBuckets int64
		var total aggregateScan
		if err := rows.Scan(&userID, &mix.Source, &totalBuckets, &total.value, &total.known, &total.complete); err != nil {
			return fmt.Errorf("decode leaderboard source mix: %w", err)
		}
		mix.TotalTokens = total.coverage(totalBuckets)
		if row := rowsByUser[userID]; row != nil {
			row.SourceMix = append(row.SourceMix, mix)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate leaderboard source mix: %w", err)
	}
	return nil
}

func readMostUsedModels(ctx context.Context, tx pgx.Tx, scope boardScope, start any, end time.Time, rowsByUser map[string]*leaderboard.Row) error {
	rows, err := tx.Query(ctx, scope.sql(mostUsedModelSQL), scope.argument, start, end)
	if err != nil {
		return fmt.Errorf("query leaderboard models: %w", err)
	}
	defer rows.Close()
	seen := make(map[string]bool, len(rowsByUser))
	for rows.Next() {
		var userID string
		var model leaderboard.ModelUsage
		var totalBuckets int64
		var total aggregateScan
		if err := rows.Scan(&userID, &model.ModelID, &model.Resolution, &totalBuckets, &total.value, &total.known, &total.complete); err != nil {
			return fmt.Errorf("decode leaderboard model: %w", err)
		}
		if seen[userID] {
			continue
		}
		model.TotalTokens = total.coverage(totalBuckets)
		if row := rowsByUser[userID]; row != nil {
			row.MostUsedModel = &model
			seen[userID] = true
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate leaderboard models: %w", err)
	}
	return nil
}

// Every query below follows the scope's "WITH members AS (...)" prefix, whose
// single parameter is $1. Window starts are inclusive and may be NULL for no
// lower bound; window ends are exclusive.

// knownTotalSQL is a bucket's total tokens: uncached input, cache read, cache
// write, and output. It is NULL only when no category is known, so an
// unavailable category is never counted as zero usage.
const knownTotalSQL = `CASE WHEN b.input_uncached_tokens IS NOT NULL OR b.input_cache_read_tokens IS NOT NULL OR b.input_cache_write_tokens IS NOT NULL OR b.output_tokens IS NOT NULL
	THEN COALESCE(b.input_uncached_tokens,0)+COALESCE(b.input_cache_read_tokens,0)+COALESCE(b.input_cache_write_tokens,0)+COALESCE(b.output_tokens,0) END`

// totalCompleteSQL is true when every category behind a bucket's total is
// complete.
const totalCompleteSQL = `(b.input_uncached_state='complete' AND b.input_cache_read_state='complete' AND b.input_cache_write_state='complete' AND b.output_state='complete')`

// memberAggregateSQL aggregates each member's active devices over the current
// window ($2, $3) and the comparison window ($4, $5) in one pass. A member
// without usage still gets a row.
const memberAggregateSQL = `, usage AS (
		SELECT d.user_id, b.received_at,
			` + knownTotalSQL + ` AS total_tokens,
			` + totalCompleteSQL + ` AS total_complete,
			b.api_estimate_microusd, b.api_estimate_state, b.priced_tokens, b.unpriced_tokens, b.model_resolution,
			(b.bucket_start < $3::timestamptz AND ($2::timestamptz IS NULL OR b.bucket_start >= $2::timestamptz)) AS in_current,
			(b.bucket_start < $5::timestamptz AND ($4::timestamptz IS NULL OR b.bucket_start >= $4::timestamptz)) AS in_previous
		FROM usage_buckets b
		JOIN devices d ON d.id = b.device_id AND d.revoked_at IS NULL
		JOIN members m ON m.user_id = d.user_id
	)
	SELECT
		m.user_id::text,
		m.display_name,
		MAX(u.received_at),
		COUNT(*) FILTER (WHERE u.in_current),
		SUM(u.total_tokens) FILTER (WHERE u.in_current)::text,
		COUNT(u.total_tokens) FILTER (WHERE u.in_current),
		COALESCE(BOOL_AND(u.total_complete) FILTER (WHERE u.in_current), false),
		SUM(u.api_estimate_microusd) FILTER (WHERE u.in_current)::text,
		COUNT(u.api_estimate_microusd) FILTER (WHERE u.in_current),
		COALESCE(BOOL_AND(u.api_estimate_state='complete') FILTER (WHERE u.in_current), false),
		COALESCE(SUM(u.priced_tokens) FILTER (WHERE u.in_current), 0)::text,
		COALESCE(SUM(u.unpriced_tokens) FILTER (WHERE u.in_current), 0)::text,
		COUNT(*) FILTER (WHERE u.in_previous),
		SUM(u.total_tokens) FILTER (WHERE u.in_previous)::text,
		COUNT(u.total_tokens) FILTER (WHERE u.in_previous),
		COALESCE(BOOL_AND(u.total_complete) FILTER (WHERE u.in_previous), false),
		SUM(u.api_estimate_microusd) FILTER (WHERE u.in_previous)::text,
		COUNT(u.api_estimate_microusd) FILTER (WHERE u.in_previous),
		COALESCE(BOOL_AND(u.api_estimate_state='complete') FILTER (WHERE u.in_previous), false),
		COALESCE(SUM(u.priced_tokens) FILTER (WHERE u.in_previous), 0)::text,
		COALESCE(SUM(u.unpriced_tokens) FILTER (WHERE u.in_previous), 0)::text,
		COUNT(*) FILTER (WHERE u.in_current AND u.model_resolution='unknown'),
		COUNT(*) FILTER (WHERE u.in_current AND u.model_resolution='inferred'),
		COUNT(*) FILTER (WHERE u.in_current AND u.api_estimate_microusd IS NULL)
	FROM members m
	LEFT JOIN usage u ON u.user_id = m.user_id
	GROUP BY m.user_id, m.display_name
`

// windowBucketsSQL selects the members' active-device buckets inside the
// window ($2, $3).
const windowBucketsSQL = `
	FROM usage_buckets b
	JOIN devices d ON d.id = b.device_id AND d.revoked_at IS NULL
	JOIN members m ON m.user_id = d.user_id
	WHERE ($2::timestamptz IS NULL OR b.bucket_start >= $2::timestamptz) AND b.bucket_start < $3::timestamptz
`

// implausibleDaySQL returns members with more than $5 tokens on one day of
// the window, where a day is a calendar date in timezone $4.
const implausibleDaySQL = `
	SELECT DISTINCT d.user_id::text` + windowBucketsSQL + `
	GROUP BY d.user_id, date_trunc('day', b.bucket_start AT TIME ZONE $4)
	HAVING SUM(` + knownTotalSQL + `) > $5::numeric
`

const sourceMixSQL = `
	SELECT
		d.user_id::text,
		b.source,
		COUNT(*),
		SUM(` + knownTotalSQL + `)::text,
		COUNT(` + knownTotalSQL + `),
		COALESCE(BOOL_AND(` + totalCompleteSQL + `), false)` + windowBucketsSQL + `
	GROUP BY d.user_id, b.source
	ORDER BY d.user_id, b.source
`

// mostUsedModelSQL lists each member's models by total tokens, largest first;
// the reader keeps the first row per member.
const mostUsedModelSQL = `
	SELECT
		d.user_id::text,
		COALESCE(b.canonical_model_id, b.observed_model_id) AS model_id,
		b.model_resolution,
		COUNT(*),
		SUM(` + knownTotalSQL + `)::text AS total_tokens,
		COUNT(` + knownTotalSQL + `),
		COALESCE(BOOL_AND(` + totalCompleteSQL + `), false)` + windowBucketsSQL + `
	GROUP BY d.user_id, model_id, b.model_resolution
	HAVING SUM(` + knownTotalSQL + `) IS NOT NULL
	ORDER BY d.user_id, SUM(` + knownTotalSQL + `) DESC, model_id, b.model_resolution
`
