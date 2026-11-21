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

// LeaderboardRepository aggregates active group members and their active
// devices in one repeatable-read snapshot.
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

// Leaderboard reads one membership-authorized group ranking snapshot. Every
// row carries its value and cost coverage for both the current window and the
// comparison window, read in the same snapshot so a trend never compares two
// different states of the data.
func (r *LeaderboardRepository) Leaderboard(ctx context.Context, principal serverauth.Principal, query leaderboard.Query) (board leaderboard.Board, err error) {
	board = leaderboard.Board{
		GroupID: query.GroupID, Period: query.Period, Metric: query.Metric,
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
	location, err := time.LoadLocation(board.Timezone)
	if err != nil {
		return board, fmt.Errorf("load group timezone: %w", err)
	}
	board.Window, err = leaderboard.WindowFor(query.Period, board.GeneratedAt, location)
	if err != nil {
		return board, err
	}
	board.Comparison, err = leaderboard.ComparisonFor(query.Period, board.Window, location)
	if err != nil {
		return board, err
	}
	if query.Metric != leaderboard.MetricTotalTokens && query.Metric != leaderboard.MetricAPIEstimate {
		return board, servergroup.ErrInvalidInput
	}
	start, previousStart := windowStart(board.Window), windowStart(board.Comparison.Window)
	rows, err := tx.Query(ctx, memberAggregateSQL, query.GroupID, start, board.Window.End, previousStart, board.Comparison.Window.End)
	if err != nil {
		return board, fmt.Errorf("query leaderboard members: %w", err)
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
			return board, fmt.Errorf("decode leaderboard member: %w", err)
		}
		row.Value, row.Coverage = current.value(query.Metric), leaderboard.NewCoverage(current.priced, current.unpriced)
		row.Trend.PreviousValue, row.PreviousCoverage = previous.value(query.Metric), leaderboard.NewCoverage(previous.priced, previous.unpriced)
		row.SourceMix = make([]leaderboard.SourceMix, 0)
		board.Rows = append(board.Rows, row)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return board, fmt.Errorf("iterate leaderboard members: %w", err)
	}
	rows.Close()
	rowByUser := make(map[string]*leaderboard.Row, len(board.Rows))
	for index := range board.Rows {
		rowByUser[board.Rows[index].UserID] = &board.Rows[index]
	}
	if err = readSourceMix(ctx, tx, query.GroupID, start, board.Window.End, rowByUser); err != nil {
		return board, err
	}
	if err = readMostUsedModels(ctx, tx, query.GroupID, start, board.Window.End, rowByUser); err != nil {
		return board, err
	}
	if err = readImplausibleDays(ctx, tx, query.GroupID, start, board.Window.End, board.Timezone, rowByUser); err != nil {
		return board, err
	}
	if err = tx.Commit(ctx); err != nil {
		return board, fmt.Errorf("commit leaderboard snapshot: %w", err)
	}
	return board, nil
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
func readImplausibleDays(ctx context.Context, tx pgx.Tx, groupID string, start any, end time.Time, timezone string, rowsByUser map[string]*leaderboard.Row) error {
	rows, err := tx.Query(ctx, implausibleDaySQL, groupID, start, end, timezone, strconv.FormatUint(leaderboard.PlausibleDailyTokens, 10))
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

func readSourceMix(ctx context.Context, tx pgx.Tx, groupID string, start any, end time.Time, rowsByUser map[string]*leaderboard.Row) error {
	rows, err := tx.Query(ctx, sourceMixSQL, groupID, start, end)
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

func readMostUsedModels(ctx context.Context, tx pgx.Tx, groupID string, start any, end time.Time, rowsByUser map[string]*leaderboard.Row) error {
	rows, err := tx.Query(ctx, mostUsedModelSQL, groupID, start, end)
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

// knownTotalSQL is a bucket's total tokens: uncached input, cache read, cache
// write, and output. It is NULL only when no category is known, so an
// unavailable category is never counted as zero usage.
const knownTotalSQL = `CASE WHEN b.input_uncached_tokens IS NOT NULL OR b.input_cache_read_tokens IS NOT NULL OR b.input_cache_write_tokens IS NOT NULL OR b.output_tokens IS NOT NULL
	THEN COALESCE(b.input_uncached_tokens,0)+COALESCE(b.input_cache_read_tokens,0)+COALESCE(b.input_cache_write_tokens,0)+COALESCE(b.output_tokens,0) END`

// memberAggregateSQL aggregates each active member's active devices over the
// current window ($2, $3) and the comparison window ($4, $5) in one pass.
// Window starts are inclusive and may be NULL for no lower bound; ends are
// exclusive. A member without usage still gets a row.
const memberAggregateSQL = `
	WITH members AS (
		SELECT m.user_id, u.display_name
		FROM group_memberships m
		JOIN users u ON u.id = m.user_id AND u.deleted_at IS NULL
		WHERE m.group_id = $1::uuid AND m.revoked_at IS NULL
	), usage AS (
		SELECT d.user_id, b.received_at,
			` + knownTotalSQL + ` AS total_tokens,
			(b.input_uncached_state='complete' AND b.input_cache_read_state='complete' AND b.input_cache_write_state='complete' AND b.output_state='complete') AS total_complete,
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

// implausibleDaySQL returns members with more than $5 tokens on one day of
// the window, where a day is a calendar date in timezone $4.
const implausibleDaySQL = `
	SELECT DISTINCT d.user_id::text
	FROM usage_buckets b
	JOIN devices d ON d.id = b.device_id AND d.revoked_at IS NULL
	JOIN group_memberships m ON m.user_id = d.user_id AND m.group_id = $1::uuid AND m.revoked_at IS NULL
	WHERE ($2::timestamptz IS NULL OR b.bucket_start >= $2::timestamptz) AND b.bucket_start < $3::timestamptz
	GROUP BY d.user_id, date_trunc('day', b.bucket_start AT TIME ZONE $4)
	HAVING SUM(` + knownTotalSQL + `) > $5::numeric
`

const sourceMixSQL = `
	WITH filtered AS (
		SELECT d.user_id, b.*
		FROM usage_buckets b
		JOIN devices d ON d.id = b.device_id
		WHERE d.revoked_at IS NULL
		  AND ($2::timestamptz IS NULL OR b.bucket_start >= $2)
		  AND b.bucket_start < $3
	)
	SELECT
		f.user_id::text,
		f.source,
		COUNT(*),
		SUM(CASE WHEN f.input_uncached_tokens IS NOT NULL OR f.input_cache_read_tokens IS NOT NULL OR f.input_cache_write_tokens IS NOT NULL OR f.output_tokens IS NOT NULL
			THEN COALESCE(f.input_uncached_tokens,0)+COALESCE(f.input_cache_read_tokens,0)+COALESCE(f.input_cache_write_tokens,0)+COALESCE(f.output_tokens,0) END)::text,
		COUNT(*) FILTER (WHERE f.input_uncached_tokens IS NOT NULL OR f.input_cache_read_tokens IS NOT NULL OR f.input_cache_write_tokens IS NOT NULL OR f.output_tokens IS NOT NULL),
		COALESCE(BOOL_AND(f.input_uncached_state='complete' AND f.input_cache_read_state='complete' AND f.input_cache_write_state='complete' AND f.output_state='complete'), false)
	FROM filtered f
	JOIN group_memberships m ON m.user_id = f.user_id AND m.group_id = $1::uuid AND m.revoked_at IS NULL
	GROUP BY f.user_id, f.source
	ORDER BY f.user_id, f.source
`

const mostUsedModelSQL = `
	WITH filtered AS (
		SELECT d.user_id, b.*
		FROM usage_buckets b
		JOIN devices d ON d.id = b.device_id
		WHERE d.revoked_at IS NULL
		  AND ($2::timestamptz IS NULL OR b.bucket_start >= $2)
		  AND b.bucket_start < $3
	)
	SELECT
		f.user_id::text,
		COALESCE(f.canonical_model_id, f.observed_model_id) AS model_id,
		f.model_resolution,
		COUNT(*),
		SUM(CASE WHEN f.input_uncached_tokens IS NOT NULL OR f.input_cache_read_tokens IS NOT NULL OR f.input_cache_write_tokens IS NOT NULL OR f.output_tokens IS NOT NULL
			THEN COALESCE(f.input_uncached_tokens,0)+COALESCE(f.input_cache_read_tokens,0)+COALESCE(f.input_cache_write_tokens,0)+COALESCE(f.output_tokens,0) END)::text AS total_tokens,
		COUNT(*) FILTER (WHERE f.input_uncached_tokens IS NOT NULL OR f.input_cache_read_tokens IS NOT NULL OR f.input_cache_write_tokens IS NOT NULL OR f.output_tokens IS NOT NULL),
		COALESCE(BOOL_AND(f.input_uncached_state='complete' AND f.input_cache_read_state='complete' AND f.input_cache_write_state='complete' AND f.output_state='complete'), false)
	FROM filtered f
	JOIN group_memberships m ON m.user_id = f.user_id AND m.group_id = $1::uuid AND m.revoked_at IS NULL
	GROUP BY f.user_id, model_id, f.model_resolution
	HAVING SUM(CASE WHEN f.input_uncached_tokens IS NOT NULL OR f.input_cache_read_tokens IS NOT NULL OR f.input_cache_write_tokens IS NOT NULL OR f.output_tokens IS NOT NULL
		THEN COALESCE(f.input_uncached_tokens,0)+COALESCE(f.input_cache_read_tokens,0)+COALESCE(f.input_cache_write_tokens,0)+COALESCE(f.output_tokens,0) END) IS NOT NULL
	ORDER BY f.user_id, SUM(CASE WHEN f.input_uncached_tokens IS NOT NULL OR f.input_cache_read_tokens IS NOT NULL OR f.input_cache_write_tokens IS NOT NULL OR f.output_tokens IS NOT NULL
		THEN COALESCE(f.input_uncached_tokens,0)+COALESCE(f.input_cache_read_tokens,0)+COALESCE(f.input_cache_write_tokens,0)+COALESCE(f.output_tokens,0) END) DESC,
		model_id, f.model_resolution
`
