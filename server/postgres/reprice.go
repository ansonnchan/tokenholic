package postgres

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	protocol "github.com/ansonnchan/tokenholic/internal/sync"
	serverpricing "github.com/ansonnchan/tokenholic/server/pricing"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	// repriceLockID serializes repricing across API instances starting together.
	repriceLockID int64 = 0x544b485f52455052
	// repriceBatchSize bounds memory, not atomicity: every batch runs in the
	// same transaction.
	repriceBatchSize = 500
)

// UsagePricer derives the server-owned columns of stored usage with one
// catalog revision.
type UsagePricer interface {
	Revision() string
	Derive(serverpricing.Usage) (serverpricing.Derived, error)
}

// RepriceResult reports what one repricing pass changed.
type RepriceResult struct {
	Revision string
	Repriced int64
}

// RepriceUsage re-derives every stored bucket whose pricing revision differs
// from the pricer's, from the raw token counts kept beside it.
//
// The whole pass is one advisory-locked transaction. Readers keep seeing the
// previous, internally consistent values until it commits, and a failure
// leaves every row untouched, so history never mixes two revisions. Rows
// already at the active revision are skipped: derivation is a pure function of
// stored evidence and revision, so re-deriving them could not change anything.
// Running it again is therefore a no-op.
//
// The pricer's revision must already be registered with EnsurePricingRevision.
// Client evidence, bucket revisions, payload hashes, and receipt times are
// never modified.
func RepriceUsage(ctx context.Context, pool *pgxpool.Pool, pricer UsagePricer) (RepriceResult, error) {
	return repriceUsage(ctx, pool, pricer, repriceBatchSize)
}

func repriceUsage(ctx context.Context, pool *pgxpool.Pool, pricer UsagePricer, batchSize int) (result RepriceResult, err error) {
	if pool == nil || pricer == nil || pricer.Revision() == "" || batchSize < 1 {
		return RepriceResult{}, errors.New("reprice usage: a pool and a pricer with a revision are required")
	}
	result.Revision = pricer.Revision()
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return RepriceResult{}, fmt.Errorf("begin usage repricing: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback(context.WithoutCancel(ctx))
		}
	}()
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, repriceLockID); err != nil {
		return RepriceResult{}, fmt.Errorf("lock usage repricing: %w", err)
	}
	for {
		// Each updated row leaves this predicate, so the loop needs no cursor.
		batch, readErr := readStaleBuckets(ctx, tx, result.Revision, batchSize)
		if readErr != nil {
			return RepriceResult{}, readErr
		}
		if len(batch) == 0 {
			break
		}
		update := newRepriceUpdate(len(batch))
		for _, stale := range batch {
			derived, deriveErr := pricer.Derive(stale.usage)
			if deriveErr != nil {
				return RepriceResult{}, fmt.Errorf("reprice usage bucket: %w", deriveErr)
			}
			if derived.PricingRevision != result.Revision {
				return RepriceResult{}, fmt.Errorf("reprice usage bucket: pricer derived revision %q, want %q", derived.PricingRevision, result.Revision)
			}
			update.add(stale, derived)
		}
		changed, writeErr := update.apply(ctx, tx, result.Revision)
		if writeErr != nil {
			return RepriceResult{}, writeErr
		}
		if changed != int64(len(batch)) {
			return RepriceResult{}, fmt.Errorf("reprice usage: updated %d of %d buckets", changed, len(batch))
		}
		result.Repriced += changed
	}
	if err = tx.Commit(ctx); err != nil {
		return RepriceResult{}, fmt.Errorf("commit usage repricing: %w", err)
	}
	return result, nil
}

type staleBucket struct {
	deviceID  string
	bucketKey string
	usage     serverpricing.Usage
}

func readStaleBuckets(ctx context.Context, tx pgx.Tx, revision string, limit int) ([]staleBucket, error) {
	rows, err := tx.Query(ctx, `
		SELECT device_id::text, bucket_key, observed_model_id, model_evidence,
		       input_uncached_tokens::text, input_uncached_state,
		       input_cache_read_tokens::text, input_cache_read_state,
		       input_cache_write_tokens::text, input_cache_write_state,
		       input_cache_write_5m_tokens::text, input_cache_write_5m_state,
		       input_cache_write_1h_tokens::text, input_cache_write_1h_state,
		       output_tokens::text, output_state,
		       reasoning_output_tokens::text, reasoning_output_state,
		       COALESCE(service_tier, ''), COALESCE(prompt_band, '')
		FROM usage_buckets
		WHERE pricing_revision <> $1
		ORDER BY device_id, bucket_key
		LIMIT $2
		FOR UPDATE
	`, revision, limit)
	if err != nil {
		return nil, fmt.Errorf("read usage buckets to reprice: %w", err)
	}
	defer rows.Close()
	var batch []staleBucket
	for rows.Next() {
		var stale staleBucket
		var counts [7]*string
		var states [7]string
		if err := rows.Scan(
			&stale.deviceID, &stale.bucketKey, &stale.usage.ObservedModelID, &stale.usage.ModelEvidence,
			&counts[0], &states[0], &counts[1], &states[1], &counts[2], &states[2], &counts[3], &states[3],
			&counts[4], &states[4], &counts[5], &states[5], &counts[6], &states[6],
			&stale.usage.ServiceTier, &stale.usage.PromptBand,
		); err != nil {
			return nil, fmt.Errorf("decode usage bucket to reprice: %w", err)
		}
		targets := []*protocol.Value{
			&stale.usage.Tokens.InputUncached, &stale.usage.Tokens.InputCacheRead, &stale.usage.Tokens.InputCacheWrite,
			&stale.usage.Tokens.InputCacheWrite5m, &stale.usage.Tokens.InputCacheWrite1h,
			&stale.usage.Tokens.Output, &stale.usage.Tokens.ReasoningOutput,
		}
		for index, target := range targets {
			target.State = states[index]
			if counts[index] == nil {
				continue
			}
			value, err := strconv.ParseUint(*counts[index], 10, 64)
			if err != nil {
				return nil, fmt.Errorf("decode stored token count: %w", err)
			}
			target.Value = &value
		}
		batch = append(batch, stale)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate usage buckets to reprice: %w", err)
	}
	return batch, nil
}

// repriceUpdate collects one batch as parallel arrays for a single UPDATE.
type repriceUpdate struct {
	deviceIDs, bucketKeys                   []string
	canonical, provider, pricingKey, reason []*string
	resolution, match, state                []string
	priced, unpriced                        []string
	estimate                                []*string
}

func newRepriceUpdate(capacity int) *repriceUpdate {
	return &repriceUpdate{
		deviceIDs: make([]string, 0, capacity), bucketKeys: make([]string, 0, capacity),
		canonical: make([]*string, 0, capacity), provider: make([]*string, 0, capacity),
		pricingKey: make([]*string, 0, capacity), reason: make([]*string, 0, capacity),
		resolution: make([]string, 0, capacity), match: make([]string, 0, capacity), state: make([]string, 0, capacity),
		priced: make([]string, 0, capacity), unpriced: make([]string, 0, capacity), estimate: make([]*string, 0, capacity),
	}
}

func (u *repriceUpdate) add(stale staleBucket, derived serverpricing.Derived) {
	u.deviceIDs = append(u.deviceIDs, stale.deviceID)
	u.bucketKeys = append(u.bucketKeys, stale.bucketKey)
	u.canonical = append(u.canonical, derived.CanonicalModelID)
	u.provider = append(u.provider, derived.ModelProvider)
	u.pricingKey = append(u.pricingKey, derived.PricingKey)
	u.reason = append(u.reason, derived.UnpricedReason)
	u.resolution = append(u.resolution, derived.ModelResolution)
	u.match = append(u.match, derived.ModelMatch)
	u.state = append(u.state, derived.EstimateState)
	u.priced = append(u.priced, strconv.FormatUint(derived.PricedTokens, 10))
	u.unpriced = append(u.unpriced, strconv.FormatUint(derived.UnpricedTokens, 10))
	var estimate *string
	if derived.EstimateMicroUSD != nil {
		text := strconv.FormatUint(*derived.EstimateMicroUSD, 10)
		estimate = &text
	}
	u.estimate = append(u.estimate, estimate)
}

// apply rewrites only server-derived columns. The schema's checks validate
// every new row against the evidence it sits beside.
func (u *repriceUpdate) apply(ctx context.Context, tx pgx.Tx, revision string) (int64, error) {
	tag, err := tx.Exec(ctx, `
		UPDATE usage_buckets AS b SET
			canonical_model_id = v.canonical, model_provider = v.provider, pricing_key = v.pricing_key,
			model_resolution = v.resolution, model_match = v.match, unpriced_reason = v.reason,
			priced_tokens = v.priced::numeric, unpriced_tokens = v.unpriced::numeric,
			api_estimate_microusd = v.estimate::numeric, api_estimate_state = v.state,
			pricing_revision = $13
		FROM unnest(
			$1::uuid[], $2::text[], $3::text[], $4::text[], $5::text[], $6::text[],
			$7::text[], $8::text[], $9::text[], $10::text[], $11::text[], $12::text[]
		) AS v(device_id, bucket_key, canonical, provider, pricing_key, reason, resolution, match, state, priced, unpriced, estimate)
		WHERE b.device_id = v.device_id AND b.bucket_key = v.bucket_key
	`, u.deviceIDs, u.bucketKeys, u.canonical, u.provider, u.pricingKey, u.reason,
		u.resolution, u.match, u.state, u.priced, u.unpriced, u.estimate, revision)
	if err != nil {
		return 0, fmt.Errorf("write repriced usage buckets: %w", err)
	}
	return tag.RowsAffected(), nil
}
