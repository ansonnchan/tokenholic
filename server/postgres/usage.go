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

// ApplyUsageBuckets applies a batch atomically. Equal revisions are no-ops,
// older revisions are stale, and only newer absolute values replace storage.
// Conflict detection compares client evidence alone, so a catalog update on
// the server can never turn a retry into a conflict.
func (r *UsageRepository) ApplyUsageBuckets(ctx context.Context, principal serverauth.Principal, buckets []serversync.Bucket) (acknowledgements []protocol.Acknowledgement, err error) {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, fmt.Errorf("begin usage synchronization: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback(context.WithoutCancel(ctx))
		}
	}()
	var active int
	if err = tx.QueryRow(ctx, `
		SELECT 1
		FROM devices d JOIN users u ON u.id = d.user_id
		WHERE d.id = $1::uuid AND d.user_id = $2::uuid
		  AND d.revoked_at IS NULL AND u.deleted_at IS NULL
		FOR SHARE OF d, u
	`, principal.DeviceID, principal.UserID).Scan(&active); errors.Is(err, pgx.ErrNoRows) {
		return nil, serverauth.ErrUnauthorized
	} else if err != nil {
		return nil, fmt.Errorf("verify active usage device: %w", err)
	}
	ordered := append([]serversync.Bucket(nil), buckets...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Envelope.BucketKey < ordered[j].Envelope.BucketKey })
	acknowledgements = make([]protocol.Acknowledgement, 0, len(ordered))
	for _, bucket := range ordered {
		envelope := bucket.Envelope
		_, payloadHash, hashErr := protocol.CanonicalEnvelope(envelope)
		if hashErr != nil {
			return nil, hashErr
		}
		var currentRevisionText string
		var currentHash []byte
		readErr := tx.QueryRow(ctx, `
			SELECT revision::text, payload_hash
			FROM usage_buckets
			WHERE device_id = $1::uuid AND bucket_key = $2
			FOR UPDATE
		`, principal.DeviceID, envelope.BucketKey).Scan(&currentRevisionText, &currentHash)
		status := protocol.AckApplied
		switch {
		case errors.Is(readErr, pgx.ErrNoRows):
			if err = upsertUsageBucket(ctx, tx, principal.DeviceID, bucket, payloadHash); err != nil {
				return nil, err
			}
		case readErr != nil:
			return nil, fmt.Errorf("read server usage revision: %w", readErr)
		default:
			currentRevision, parseErr := strconv.ParseUint(currentRevisionText, 10, 64)
			if parseErr != nil {
				return nil, fmt.Errorf("decode server usage revision: %w", parseErr)
			}
			switch {
			case envelope.Bucket.Revision < currentRevision:
				status = protocol.AckStale
			case envelope.Bucket.Revision == currentRevision:
				if !bytes.Equal(currentHash, payloadHash[:]) {
					return nil, serversync.ErrRevisionConflict
				}
				status = protocol.AckUnchanged
			default:
				if err = upsertUsageBucket(ctx, tx, principal.DeviceID, bucket, payloadHash); err != nil {
					return nil, err
				}
			}
		}
		acknowledgements = append(acknowledgements, protocol.Acknowledgement{
			BucketKey: envelope.BucketKey, Revision: envelope.Bucket.Revision, Status: status,
		})
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit usage synchronization: %w", err)
	}
	return acknowledgements, nil
}

// upsertUsageBucket writes one absolute bucket. The caller holds the row lock
// and has already decided that this revision replaces any stored one.
func upsertUsageBucket(ctx context.Context, tx pgx.Tx, deviceID string, bucket serversync.Bucket, payloadHash [32]byte) error {
	envelope, derived := bucket.Envelope, bucket.Derived
	tokens := envelope.Bucket.Tokens
	_, err := tx.Exec(ctx, `
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
			$1::uuid,$2,$3,60,$4,$5,$6,$7,$8,$9,
			$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,
			$24,$25,$26,$27,$28,$29,$30,$31,$32,$33,$34,$35,$36,
			$37,$38
		)
		ON CONFLICT (device_id, bucket_key) DO UPDATE SET
			bucket_start=excluded.bucket_start, source=excluded.source, surface=excluded.surface,
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
	)
	if err != nil {
		return fmt.Errorf("write server usage bucket: %w", err)
	}
	return nil
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
