package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	serveraccount "github.com/ansonnchan/tokenholic/server/account"
	serverauth "github.com/ansonnchan/tokenholic/server/auth"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// AccountRepository aggregates privacy-limited usage across active devices.
type AccountRepository struct {
	pool *pgxpool.Pool
	now  func() time.Time
}

// NewAccountRepository constructs the PostgreSQL account repository.
func NewAccountRepository(pool *pgxpool.Pool) (*AccountRepository, error) {
	if pool == nil {
		return nil, errors.New("initialize account repository: pool is required")
	}
	return &AccountRepository{pool: pool, now: time.Now}, nil
}

// AccountUsage returns one account row. Revoked devices remain visible for
// management and audit, but only active devices contribute to totals.
func (r *AccountRepository) AccountUsage(ctx context.Context, principal serverauth.Principal) (serveraccount.Usage, error) {
	usage := serveraccount.Usage{UserID: principal.UserID, GeneratedAt: r.now().UTC()}
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return serveraccount.Usage{}, fmt.Errorf("begin account usage snapshot: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if err := tx.QueryRow(ctx, `
		SELECT display_name, global_opt_in FROM users WHERE id = $1::uuid AND deleted_at IS NULL
	`, principal.UserID).Scan(&usage.DisplayName, &usage.GlobalOptIn); errors.Is(err, pgx.ErrNoRows) {
		return serveraccount.Usage{}, serverauth.ErrUnauthorized
	} else if err != nil {
		return serveraccount.Usage{}, fmt.Errorf("read account identity: %w", err)
	}
	rows, err := tx.Query(ctx, `
		SELECT d.id::text, d.revoked_at, d.last_seen_at, MAX(b.received_at)
		FROM devices d
		LEFT JOIN usage_buckets b ON b.device_id = d.id
		WHERE d.user_id = $1::uuid
		GROUP BY d.id, d.created_at, d.revoked_at, d.last_seen_at
		ORDER BY d.created_at, d.id
	`, principal.UserID)
	if err != nil {
		return serveraccount.Usage{}, fmt.Errorf("query account devices: %w", err)
	}
	for rows.Next() {
		var device serveraccount.Device
		var revokedAt *time.Time
		if err := rows.Scan(&device.DeviceID, &revokedAt, &device.LastSeenAt, &device.LastSyncedAt); err != nil {
			rows.Close()
			return serveraccount.Usage{}, fmt.Errorf("decode account device: %w", err)
		}
		device.Revoked = revokedAt != nil
		if !device.Revoked {
			usage.ActiveDevices++
			if device.LastSyncedAt != nil && (usage.LastSyncedAt == nil || device.LastSyncedAt.After(*usage.LastSyncedAt)) {
				value := *device.LastSyncedAt
				usage.LastSyncedAt = &value
			}
		}
		usage.Devices = append(usage.Devices, device)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return serveraccount.Usage{}, fmt.Errorf("iterate account devices: %w", err)
	}
	rows.Close()
	var totalBuckets int64
	var total aggregateScan
	var input, cacheRead, cacheWrite, output, reasoning, estimate aggregateScan
	err = tx.QueryRow(ctx, `
		SELECT
			COUNT(*),
			SUM(CASE WHEN input_uncached_tokens IS NOT NULL OR input_cache_read_tokens IS NOT NULL OR input_cache_write_tokens IS NOT NULL OR output_tokens IS NOT NULL
				THEN COALESCE(input_uncached_tokens,0)+COALESCE(input_cache_read_tokens,0)+COALESCE(input_cache_write_tokens,0)+COALESCE(output_tokens,0) END)::text,
			COUNT(*) FILTER (WHERE input_uncached_tokens IS NOT NULL OR input_cache_read_tokens IS NOT NULL OR input_cache_write_tokens IS NOT NULL OR output_tokens IS NOT NULL),
			COALESCE(BOOL_AND(input_uncached_state='complete' AND input_cache_read_state='complete' AND input_cache_write_state='complete' AND output_state='complete'), false),
			SUM(input_uncached_tokens)::text, COUNT(input_uncached_tokens), COALESCE(BOOL_AND(input_uncached_state='complete'), false),
			SUM(input_cache_read_tokens)::text, COUNT(input_cache_read_tokens), COALESCE(BOOL_AND(input_cache_read_state='complete'), false),
			SUM(input_cache_write_tokens)::text, COUNT(input_cache_write_tokens), COALESCE(BOOL_AND(input_cache_write_state='complete'), false),
			SUM(output_tokens)::text, COUNT(output_tokens), COALESCE(BOOL_AND(output_state='complete'), false),
			SUM(reasoning_output_tokens)::text, COUNT(reasoning_output_tokens), COALESCE(BOOL_AND(reasoning_output_state='complete'), false),
			SUM(api_estimate_microusd)::text, COUNT(api_estimate_microusd), COALESCE(BOOL_AND(api_estimate_state='complete'), false)
		FROM usage_buckets b
		JOIN devices d ON d.id = b.device_id
		WHERE d.user_id = $1::uuid AND d.revoked_at IS NULL
	`, principal.UserID).Scan(
		&totalBuckets,
		&total.value, &total.known, &total.complete,
		&input.value, &input.known, &input.complete,
		&cacheRead.value, &cacheRead.known, &cacheRead.complete,
		&cacheWrite.value, &cacheWrite.known, &cacheWrite.complete,
		&output.value, &output.known, &output.complete,
		&reasoning.value, &reasoning.known, &reasoning.complete,
		&estimate.value, &estimate.known, &estimate.complete,
	)
	if err != nil {
		return serveraccount.Usage{}, fmt.Errorf("aggregate account usage: %w", err)
	}
	usage.TotalTokens = total.coverage(totalBuckets)
	usage.Tokens = serveraccount.Tokens{
		InputUncached: input.coverage(totalBuckets), InputCacheRead: cacheRead.coverage(totalBuckets),
		InputCacheWrite: cacheWrite.coverage(totalBuckets), Output: output.coverage(totalBuckets),
		ReasoningOutput: reasoning.coverage(totalBuckets),
	}
	usage.APIEstimate = estimate.coverage(totalBuckets)
	if err := tx.Commit(ctx); err != nil {
		return serveraccount.Usage{}, fmt.Errorf("commit account usage snapshot: %w", err)
	}
	return usage, nil
}

// DeleteAccount hard-deletes one authenticated user. Ownership foreign keys
// cascade through identities, devices, credentials, uploaded usage, owned
// groups, invitations, and memberships in the same transaction.
func (r *AccountRepository) DeleteAccount(ctx context.Context, principal serverauth.Principal) (err error) {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin account deletion: %w", err)
	}
	defer rollbackOnError(ctx, tx, &err)
	tag, err := tx.Exec(ctx, `DELETE FROM users WHERE id = $1::uuid AND deleted_at IS NULL`, principal.UserID)
	if err != nil {
		return fmt.Errorf("delete account-owned server data: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return serverauth.ErrUnauthorized
	}
	if err = tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit account deletion: %w", err)
	}
	return nil
}

// SetGlobalOptIn records whether the account is listed on the global board.
// It requires the presenting device to still be active, so a revoked device
// cannot change what its former account shows.
func (r *AccountRepository) SetGlobalOptIn(ctx context.Context, principal serverauth.Principal, participating bool) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE users u SET global_opt_in = $3, updated_at = CURRENT_TIMESTAMP
		WHERE u.id = $1::uuid AND u.deleted_at IS NULL
		  AND EXISTS (SELECT 1 FROM devices d WHERE d.id = $2::uuid AND d.user_id = u.id AND d.revoked_at IS NULL)
	`, principal.UserID, principal.DeviceID, participating)
	if err != nil {
		return fmt.Errorf("update global board participation: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return serverauth.ErrUnauthorized
	}
	return nil
}

type aggregateScan struct {
	value    *string
	known    int64
	complete bool
}

func (a aggregateScan) coverage(total int64) serveraccount.Value {
	switch {
	case total == 0:
		return serveraccount.Value{State: serveraccount.AvailabilityEmpty}
	case a.known == 0:
		return serveraccount.Value{State: serveraccount.AvailabilityUnavailable}
	case a.known == total && a.complete:
		return serveraccount.Value{Value: a.value, State: serveraccount.AvailabilityComplete}
	default:
		return serveraccount.Value{Value: a.value, State: serveraccount.AvailabilityPartial}
	}
}
