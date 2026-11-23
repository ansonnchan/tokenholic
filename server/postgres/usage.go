package postgres

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"

	protocol "github.com/ansonnchan/tokenholic/internal/sync"
	serverauth "github.com/ansonnchan/tokenholic/server/auth"
	serversync "github.com/ansonnchan/tokenholic/server/sync"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// UsageRepository applies absolute device-bucket revisions.
type UsageRepository struct {
	pool *pgxpool.Pool
}

// NewUsageRepository constructs the PostgreSQL synchronization repository.
func NewUsageRepository(pool *pgxpool.Pool) (*UsageRepository, error) {
	if pool == nil {
		return nil, errors.New("initialize usage repository: pool is required")
	}
	return &UsageRepository{pool: pool}, nil
}

// storedRevision is what the server already holds for one bucket key.
type storedRevision struct {
	revision uint64
	hash     []byte
}

// ApplyUsageBuckets applies a batch atomically. Equal revisions are no-ops,
// older revisions are stale, and only newer absolute values replace storage.
// Conflict detection compares client evidence alone, so a catalog update on
// the server can never turn a retry into a conflict.
//
// The database is a network hop away, so the batch waits on it a fixed
// number of times however many buckets it holds: one read that locks every
// stored row the batch names, then one pipelined write of the rows that
// change. A wait per bucket made a full batch take seconds.
func (r *UsageRepository) ApplyUsageBuckets(ctx context.Context, principal serverauth.Principal, buckets []serversync.Bucket) (acknowledgements []protocol.Acknowledgement, err error) {
	ordered := append([]serversync.Bucket(nil), buckets...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Envelope.BucketKey < ordered[j].Envelope.BucketKey })
	keys := make([]string, len(ordered))
	hashes := make([][32]byte, len(ordered))
	for index, bucket := range ordered {
		keys[index] = bucket.Envelope.BucketKey
		if index > 0 && keys[index] == keys[index-1] {
			return nil, fmt.Errorf("%w: bucket %q is duplicated", serversync.ErrInvalidBatch, keys[index])
		}
		if _, hashes[index], err = protocol.CanonicalEnvelope(bucket.Envelope); err != nil {
			return nil, err
		}
	}

	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, fmt.Errorf("begin usage synchronization: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback(context.WithoutCancel(ctx))
		}
	}()
	stored, err := lockStoredRevisions(ctx, tx, principal, keys)
	if err != nil {
		return nil, err
	}

	acknowledgements = make([]protocol.Acknowledgement, 0, len(ordered))
	writes := &pgx.Batch{}
	for index, bucket := range ordered {
		envelope := bucket.Envelope
		status := protocol.AckApplied
		current, exists := stored[envelope.BucketKey]
		switch {
		case !exists || envelope.Bucket.Revision > current.revision:
			queueUsageBucket(writes, principal.DeviceID, bucket, hashes[index])
		case envelope.Bucket.Revision < current.revision:
			status = protocol.AckStale
		case !bytes.Equal(current.hash, hashes[index][:]):
			return nil, serversync.ErrRevisionConflict
		default:
			status = protocol.AckUnchanged
		}
		acknowledgements = append(acknowledgements, protocol.Acknowledgement{
			BucketKey: envelope.BucketKey, Revision: envelope.Bucket.Revision, Status: status,
		})
	}
	if writes.Len() > 0 {
		// Queued in key order, so two batches never take row locks crosswise.
		if err = tx.SendBatch(ctx, writes).Close(); err != nil {
			return nil, fmt.Errorf("write server usage buckets: %w", err)
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit usage synchronization: %w", err)
	}
	return acknowledgements, nil
}

// lockStoredRevisions confirms the device may still upload and returns what
// is stored for each of keys, holding a row lock on every one it finds. Both
// questions go to the database together and come back in one wait.
func lockStoredRevisions(ctx context.Context, tx pgx.Tx, principal serverauth.Principal, keys []string) (map[string]storedRevision, error) {
	questions := &pgx.Batch{}
	questions.Queue(`
		SELECT 1
		FROM devices d JOIN users u ON u.id = d.user_id
		WHERE d.id = $1::uuid AND d.user_id = $2::uuid
		  AND d.revoked_at IS NULL AND u.deleted_at IS NULL
		FOR SHARE OF d, u
	`, principal.DeviceID, principal.UserID)
	// Locked in key order, like the writes that follow.
	questions.Queue(`
		SELECT bucket_key, revision::text, payload_hash
		FROM usage_buckets
		WHERE device_id = $1::uuid AND bucket_key = ANY($2::text[])
		ORDER BY bucket_key COLLATE "C"
		FOR UPDATE
	`, principal.DeviceID, keys)
	answers := tx.SendBatch(ctx, questions)
	var active int
	if err := answers.QueryRow().Scan(&active); errors.Is(err, pgx.ErrNoRows) {
		_ = answers.Close()
		return nil, serverauth.ErrUnauthorized
	} else if err != nil {
		_ = answers.Close()
		return nil, fmt.Errorf("verify active usage device: %w", err)
	}
	rows, err := answers.Query()
	if err != nil {
		_ = answers.Close()
		return nil, fmt.Errorf("read server usage revisions: %w", err)
	}
	stored := make(map[string]storedRevision, len(keys))
	for rows.Next() {
		var key, revisionText string
		var current storedRevision
		if err := rows.Scan(&key, &revisionText, &current.hash); err != nil {
			rows.Close()
			_ = answers.Close()
			return nil, fmt.Errorf("read server usage revision: %w", err)
		}
		if current.revision, err = strconv.ParseUint(revisionText, 10, 64); err != nil {
			rows.Close()
			_ = answers.Close()
			return nil, fmt.Errorf("decode server usage revision: %w", err)
		}
		stored[key] = current
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		_ = answers.Close()
		return nil, fmt.Errorf("read server usage revisions: %w", err)
	}
	if err := answers.Close(); err != nil {
		return nil, fmt.Errorf("read server usage revisions: %w", err)
	}
	return stored, nil
}

// queueUsageBucket adds one absolute bucket to the batch's write. The caller
// holds the row lock on any stored revision and has already decided that
// this one replaces it.
func queueUsageBucket(writes *pgx.Batch, deviceID string, bucket serversync.Bucket, payloadHash [32]byte) {
	envelope, derived := bucket.Envelope, bucket.Derived
	tokens := envelope.Bucket.Tokens
	writes.Queue(`
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
			canonical_model_id, model_provider, pricing_key, model_resolution, model_match,
			unpriced_reason, priced_tokens, unpriced_tokens,
			api_estimate_microusd, api_estimate_state, pricing_revision,
			service_tier, prompt_band
		) VALUES (
			$1::uuid,$2,$3,$39,$4,$5,$6,$7,$8,$9,
			$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,
			$24,$25,$26,$27,$28,$29,$30,$31,$32,$33,$34,$35,$36,
			$37,$38
		)
		ON CONFLICT (device_id, bucket_key) DO UPDATE SET
			bucket_start=excluded.bucket_start, interval_seconds=excluded.interval_seconds,
			source=excluded.source, surface=excluded.surface,
			observed_model_id=excluded.observed_model_id, model_evidence=excluded.model_evidence,
			measurement=excluded.measurement, normalization_version=excluded.normalization_version,
			input_uncached_tokens=excluded.input_uncached_tokens, input_uncached_state=excluded.input_uncached_state,
			input_cache_read_tokens=excluded.input_cache_read_tokens, input_cache_read_state=excluded.input_cache_read_state,
			input_cache_write_tokens=excluded.input_cache_write_tokens, input_cache_write_state=excluded.input_cache_write_state,
			input_cache_write_5m_tokens=excluded.input_cache_write_5m_tokens, input_cache_write_5m_state=excluded.input_cache_write_5m_state,
			input_cache_write_1h_tokens=excluded.input_cache_write_1h_tokens, input_cache_write_1h_state=excluded.input_cache_write_1h_state,
			output_tokens=excluded.output_tokens, output_state=excluded.output_state,
			reasoning_output_tokens=excluded.reasoning_output_tokens, reasoning_output_state=excluded.reasoning_output_state,
			revision=excluded.revision, payload_hash=excluded.payload_hash, received_at=CURRENT_TIMESTAMP,
			canonical_model_id=excluded.canonical_model_id, model_provider=excluded.model_provider,
			pricing_key=excluded.pricing_key, model_resolution=excluded.model_resolution,
			model_match=excluded.model_match, unpriced_reason=excluded.unpriced_reason,
			priced_tokens=excluded.priced_tokens, unpriced_tokens=excluded.unpriced_tokens,
			api_estimate_microusd=excluded.api_estimate_microusd, api_estimate_state=excluded.api_estimate_state,
			pricing_revision=excluded.pricing_revision,
			service_tier=excluded.service_tier, prompt_band=excluded.prompt_band
	`,
		deviceID, envelope.BucketKey, envelope.Bucket.Start, envelope.Bucket.Source, envelope.Bucket.Surface,
		envelope.Bucket.ObservedModelID, envelope.Bucket.ModelEvidence, envelope.Bucket.Measurement, envelope.Bucket.NormalizationVersion,
		numericValue(tokens.InputUncached), tokens.InputUncached.State,
		numericValue(tokens.InputCacheRead), tokens.InputCacheRead.State,
		numericValue(tokens.InputCacheWrite), tokens.InputCacheWrite.State,
		numericValue(tokens.InputCacheWrite5m), tokens.InputCacheWrite5m.State,
		numericValue(tokens.InputCacheWrite1h), tokens.InputCacheWrite1h.State,
		numericValue(tokens.Output), tokens.Output.State,
		numericValue(tokens.ReasoningOutput), tokens.ReasoningOutput.State,
		strconv.FormatUint(envelope.Bucket.Revision, 10), payloadHash[:],
		derived.CanonicalModelID, derived.ModelProvider, derived.PricingKey, derived.ModelResolution, derived.ModelMatch,
		derived.UnpricedReason, strconv.FormatUint(derived.PricedTokens, 10), strconv.FormatUint(derived.UnpricedTokens, 10),
		numericPointer(derived.EstimateMicroUSD), derived.EstimateState, derived.PricingRevision,
		evidenceValue(envelope.Bucket.ServiceTier), evidenceValue(envelope.Bucket.PromptBand),
		intervalSeconds(envelope.Bucket.Granularity),
	)
}

// Bucket intervals, in seconds, as stored in usage_buckets.interval_seconds.
const (
	minuteInterval = 60
	dayInterval    = 86400
)

// intervalSeconds is how much time a bucket covers. A day-level bucket is the
// only kind that is not a minute; the protocol has already validated which
// sources may send one.
func intervalSeconds(granularity string) int {
	if granularity == "day" {
		return dayInterval
	}
	return minuteInterval
}

// evidenceValue stores unproven pricing evidence as NULL, never as a name.
func evidenceValue(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func numericValue(value protocol.Value) any {
	return numericPointer(value.Value)
}

func numericPointer(value *uint64) any {
	if value == nil {
		return nil
	}
	return strconv.FormatUint(*value, 10)
}
