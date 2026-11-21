package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ansonnchan/tokenholic/internal/modelcatalog"
	"github.com/ansonnchan/tokenholic/internal/normalize"
)

const bucketInterval = time.Minute

// Availability distinguishes complete, partial, unavailable, and empty values.
type Availability string

const (
	// AvailabilityEmpty means the query period has no contributing buckets.
	AvailabilityEmpty Availability = "empty"
	// AvailabilityComplete means every contributing record reported the value.
	AvailabilityComplete Availability = "complete"
	// AvailabilityPartial means the value sums known records but some were unavailable.
	AvailabilityPartial Availability = "partial"
	// AvailabilityUnavailable means records exist but none report the value.
	AvailabilityUnavailable Availability = "unavailable"
)

// AggregateValue is a value plus its coverage state; nil is never reported as zero.
type AggregateValue struct {
	Value *uint64      `json:"value"`
	State Availability `json:"state"`
}

// TokenBreakdown holds the six token categories plus reasoning subset
// metadata. InputCacheWrite is the total across lifetimes; the 5m and 1h
// values split it and must not be added to it. ReasoningOutput is already
// inside Output.
type TokenBreakdown struct {
	InputUncached     AggregateValue `json:"input_uncached"`
	InputCacheRead    AggregateValue `json:"input_cache_read"`
	InputCacheWrite   AggregateValue `json:"input_cache_write"`
	InputCacheWrite5m AggregateValue `json:"input_cache_write_5m"`
	InputCacheWrite1h AggregateValue `json:"input_cache_write_1h"`
	Output            AggregateValue `json:"output"`
	ReasoningOutput   AggregateValue `json:"reasoning_output"`
}

// UsageBucket is an absolute one-minute UTC usage snapshot. Its JSON form is
// exactly the upload payload: observed evidence only, with no canonical
// model, price, or estimate.
type UsageBucket struct {
	Start           time.Time         `json:"start"`
	Source          normalize.Source  `json:"source"`
	Surface         normalize.Surface `json:"surface"`
	ObservedModelID string            `json:"observed_model_id"`
	// ModelEvidence is how the source attributed the model: reported,
	// inferred, or unknown. It is evidence quality, not a catalog result.
	ModelEvidence        normalize.ModelResolution `json:"model_evidence"`
	Measurement          normalize.Measurement     `json:"measurement"`
	NormalizationVersion string                    `json:"normalization_version"`
	// ServiceTier and PromptBand are pricing evidence shared by every request
	// in the bucket. Both are omitted when the source proved neither, so such
	// a bucket keeps the key, JSON, and content hash it had before this
	// evidence existed and is never uploaded again.
	ServiceTier normalize.ServiceTier `json:"service_tier,omitempty"`
	PromptBand  normalize.PromptBand  `json:"prompt_band,omitempty"`
	Tokens      TokenBreakdown        `json:"tokens"`
	Revision    uint64                `json:"revision"`
	// Preview never leaves the machine; the server derives its own values.
	Preview Preview `json:"-"`
}

// Preview is this build's offline model resolution and estimate for one
// bucket. A newer server catalog may resolve or price the same bucket
// differently, and that difference never blocks an upload.
type Preview struct {
	CanonicalModelID *string                   `json:"canonical_model_id"`
	ModelProvider    *string                   `json:"model_provider"`
	Resolution       normalize.ModelResolution `json:"model_resolution"`
	UnpricedReason   string                    `json:"unpriced_reason,omitempty"`
	APIEstimate      AggregateValue            `json:"api_equivalent_estimate_microusd"`
	PricedTokens     uint64                    `json:"priced_tokens"`
	UnpricedTokens   uint64                    `json:"unpriced_tokens"`
	CatalogRevision  string                    `json:"catalog_revision,omitempty"`
}

// Previewer derives the local preview with the bundled catalog.
type Previewer interface {
	Derive(modelcatalog.Usage) (modelcatalog.Derivation, error)
}

// BucketApplyResult reports replaced buckets and ignored ones. A bucket whose
// uploaded content is unchanged is ignored and keeps its revision, so a
// refresh does not re-upload history.
type BucketApplyResult struct {
	Applied int
	Ignored int
}

// OutboxRecord is one pending allowlisted absolute bucket revision.
type OutboxRecord struct {
	BucketKey string
	Revision  uint64
	Payload   json.RawMessage
}

// OutboxAcknowledgement marks one exact bucket revision as durable remotely.
type OutboxAcknowledgement struct {
	BucketKey string
	Revision  uint64
}

// Period names one local calendar query window.
type Period string

const (
	// PeriodToday starts at midnight in the requested location.
	PeriodToday Period = "today"
	// PeriodWeek starts at Monday midnight in the requested location.
	PeriodWeek Period = "week"
	// PeriodLast30Days includes today and the preceding 29 local dates.
	PeriodLast30Days Period = "last_30_days"
	// PeriodAllTime includes every bucket through the current UTC minute.
	PeriodAllTime Period = "all_time"
)

// ModelBreakdown preserves observed and previewed canonical attribution.
// UnpricedReason explains the largest unpriced share of the model's tokens;
// the estimate may still cover the rest.
type ModelBreakdown struct {
	ObservedModelID  string
	CanonicalModelID *string
	Resolution       normalize.ModelResolution
	UnpricedReason   string
	Tokens           TokenBreakdown
	APIEstimate      AggregateValue
}

// NamedBreakdown is one source or surface aggregate.
type NamedBreakdown struct {
	Name        string
	Tokens      TokenBreakdown
	APIEstimate AggregateValue
}

// UsageSummary is a timezone-aware local period query.
type UsageSummary struct {
	Period      Period
	Start       *time.Time
	End         time.Time
	Tokens      TokenBreakdown
	APIEstimate AggregateValue
	Models      []ModelBreakdown
	Sources     []NamedBreakdown
	Surfaces    []NamedBreakdown
}

// BuildUsageBuckets deterministically reduces observations to absolute UTC buckets.
func BuildUsageBuckets(observations []StoredObservation, revision uint64, previewer Previewer) ([]UsageBucket, error) {
	if revision == 0 {
		return nil, errors.New("usage bucket revision must be positive")
	}
	groups := make(map[string]*bucketAccumulator)
	for _, observation := range observations {
		start := observation.OccurredAt.UTC().Truncate(bucketInterval)
		key := bucketGroupKey(start, observation)
		accumulator := groups[key]
		if accumulator == nil {
			accumulator = newBucketAccumulator(start, observation, revision)
			groups[key] = accumulator
		}
		if err := accumulator.tokens.add(observation.Tokens); err != nil {
			return nil, err
		}
	}
	keys := make([]string, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	buckets := make([]UsageBucket, 0, len(keys))
	for _, key := range keys {
		bucket, err := groups[key].bucket(previewer)
		if err != nil {
			return nil, err
		}
		buckets = append(buckets, bucket)
	}
	return buckets, nil
}

// ApplyBucketRevisions replaces changed absolute values and enqueues only the
// newest revision. Unchanged buckets refresh their local preview in place.
func (db *DB) ApplyBucketRevisions(ctx context.Context, buckets []UsageBucket) (BucketApplyResult, error) {
	tx, err := db.sql.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return BucketApplyResult{}, fmt.Errorf("begin usage bucket update: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	ordered := append([]UsageBucket(nil), buckets...)
	sort.Slice(ordered, func(i, j int) bool {
		left, right := usageBucketKey(ordered[i]), usageBucketKey(ordered[j])
		if left != right {
			return left < right
		}
		return ordered[i].Revision < ordered[j].Revision
	})
	var result BucketApplyResult
	for _, bucket := range ordered {
		applied, err := applyBucket(ctx, tx, bucket)
		if err != nil {
			return BucketApplyResult{}, err
		}
		if applied {
			result.Applied++
		} else {
			result.Ignored++
		}
	}
	if err := tx.Commit(); err != nil {
		return BucketApplyResult{}, fmt.Errorf("commit usage bucket update: %w", err)
	}
	committed = true
	return result, nil
}

// RebuildUsageBuckets reads committed observations and applies one absolute revision.
func (db *DB) RebuildUsageBuckets(ctx context.Context, revision uint64, previewer Previewer) (BucketApplyResult, error) {
	observations, err := db.Observations(ctx)
	if err != nil {
		return BucketApplyResult{}, err
	}
	buckets, err := BuildUsageBuckets(observations, revision, previewer)
	if err != nil {
		return BucketApplyResult{}, err
	}
	retracted, err := db.retractedBuckets(ctx, buckets, revision, previewer)
	if err != nil {
		return BucketApplyResult{}, err
	}
	return db.ApplyBucketRevisions(ctx, append(buckets, retracted...))
}

// retractedBuckets returns a zeroed replacement for every stored bucket the
// current observations no longer produce. Buckets are absolute values, so
// usage that disappears, for example when a corrected adapter stops counting
// a record, must be replaced by zero both here and on the server. Leaving the
// old bucket in place would keep counting it forever.
func (db *DB) retractedBuckets(ctx context.Context, produced []UsageBucket, revision uint64, previewer Previewer) ([]UsageBucket, error) {
	current := make(map[string]struct{}, len(produced))
	for _, bucket := range produced {
		current[usageBucketKey(bucket)] = struct{}{}
	}
	rows, err := db.sql.QueryContext(ctx, `SELECT bucket_key FROM usage_buckets ORDER BY bucket_key`)
	if err != nil {
		return nil, fmt.Errorf("query stored usage bucket keys: %w", err)
	}
	var vanished []string
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			rows.Close()
			return nil, fmt.Errorf("decode stored usage bucket key: %w", err)
		}
		if _, found := current[key]; !found {
			vanished = append(vanished, key)
		}
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close stored usage bucket keys: %w", err)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate stored usage bucket keys: %w", err)
	}
	zero := func() AggregateValue {
		return AggregateValue{Value: new(uint64), State: AvailabilityComplete}
	}
	retracted := make([]UsageBucket, 0, len(vanished))
	for _, key := range vanished {
		var payload []byte
		if err := db.sql.QueryRowContext(ctx, `SELECT payload_json FROM usage_buckets WHERE bucket_key = ?`, key).Scan(&payload); err != nil {
			return nil, fmt.Errorf("read stored usage bucket: %w", err)
		}
		var stored bucketPayload
		if err := json.Unmarshal(payload, &stored); err != nil {
			return nil, fmt.Errorf("decode stored usage bucket: %w", err)
		}
		bucket := stored.Bucket
		bucket.Revision = revision
		// Known zero, not unknown: the usage this bucket once held is gone.
		bucket.Tokens = TokenBreakdown{
			InputUncached: zero(), InputCacheRead: zero(), InputCacheWrite: zero(),
			InputCacheWrite5m: zero(), InputCacheWrite1h: zero(), Output: zero(), ReasoningOutput: zero(),
		}
		if bucket.Preview, err = previewBucket(bucket, previewer); err != nil {
			return nil, err
		}
		retracted = append(retracted, bucket)
	}
	return retracted, nil
}

// NextUsageBucketRevision returns a monotonic revision after every stored
// bucket, including values outside SQLite's signed integer range.
func (db *DB) NextUsageBucketRevision(ctx context.Context) (uint64, error) {
	rows, err := db.sql.QueryContext(ctx, `SELECT revision FROM usage_buckets`)
	if err != nil {
		return 0, fmt.Errorf("query usage bucket revisions: %w", err)
	}
	defer rows.Close()
	var maximum uint64
	for rows.Next() {
		var encoded string
		if err := rows.Scan(&encoded); err != nil {
			return 0, fmt.Errorf("decode usage bucket revision row: %w", err)
		}
		revision, err := strconv.ParseUint(encoded, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("decode usage bucket revision: %w", err)
		}
		if revision > maximum {
			maximum = revision
		}
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("iterate usage bucket revisions: %w", err)
	}
	if maximum == math.MaxUint64 {
		return 0, errors.New("usage bucket revision is exhausted")
	}
	return maximum + 1, nil
}

// PendingOutbox returns current pending absolute revisions without network behavior.
func (db *DB) PendingOutbox(ctx context.Context) ([]OutboxRecord, error) {
	rows, err := db.sql.QueryContext(ctx, `SELECT bucket_key, revision, payload_json FROM outbox_records WHERE state = 'pending' ORDER BY bucket_key`)
	if err != nil {
		return nil, fmt.Errorf("query local outbox: %w", err)
	}
	defer rows.Close()
	var records []OutboxRecord
	for rows.Next() {
		var record OutboxRecord
		var revision, payload string
		if err := rows.Scan(&record.BucketKey, &revision, &payload); err != nil {
			return nil, fmt.Errorf("decode local outbox: %w", err)
		}
		parsed, err := strconv.ParseUint(revision, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("decode outbox revision: %w", err)
		}
		record.Revision = parsed
		record.Payload = json.RawMessage(payload)
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate local outbox: %w", err)
	}
	return records, nil
}

// AcknowledgeOutbox marks only the exact revisions confirmed by the server.
// A newer local replacement remains pending if an older request completes late.
func (db *DB) AcknowledgeOutbox(ctx context.Context, acknowledgements []OutboxAcknowledgement) (int64, error) {
	tx, err := db.sql.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return 0, fmt.Errorf("begin outbox acknowledgement: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	var acknowledged int64
	for _, acknowledgement := range acknowledgements {
		if len(acknowledgement.BucketKey) != 64 || acknowledgement.Revision == 0 {
			return 0, errors.New("outbox acknowledgement has invalid identity")
		}
		result, err := tx.ExecContext(ctx, `
			UPDATE outbox_records SET state = 'acknowledged'
			WHERE bucket_key = ? AND revision = ? AND state = 'pending'
		`, acknowledgement.BucketKey, strconv.FormatUint(acknowledgement.Revision, 10))
		if err != nil {
			return 0, fmt.Errorf("acknowledge outbox revision: %w", err)
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return 0, fmt.Errorf("count acknowledged outbox revision: %w", err)
		}
		acknowledged += changed
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit outbox acknowledgement: %w", err)
	}
	committed = true
	return acknowledged, nil
}

// PendingOutboxCount returns the number of newest revisions awaiting future sync.
func (db *DB) PendingOutboxCount(ctx context.Context) (int, error) {
	var count int
	if err := db.sql.QueryRowContext(ctx, `SELECT COUNT(*) FROM outbox_records WHERE state = 'pending'`).Scan(&count); err != nil {
		return 0, fmt.Errorf("count local outbox: %w", err)
	}
	return count, nil
}

// QueryUsage aggregates buckets for a local calendar period in location.
func (db *DB) QueryUsage(ctx context.Context, period Period, now time.Time, location *time.Location) (UsageSummary, error) {
	start, end, err := periodBounds(period, now, location)
	if err != nil {
		return UsageSummary{}, err
	}
	query := `SELECT payload_json, preview_json FROM usage_buckets WHERE bucket_start < ?`
	args := []any{formatTime(end)}
	if start != nil {
		query += ` AND bucket_start >= ?`
		args = append(args, formatTime(*start))
	}
	query += ` ORDER BY bucket_start, bucket_key`
	rows, err := db.sql.QueryContext(ctx, query, args...)
	if err != nil {
		return UsageSummary{}, fmt.Errorf("query usage buckets: %w", err)
	}
	defer rows.Close()
	accumulator := newSummaryAccumulator(period, start, end)
	for rows.Next() {
		var payload, preview []byte
		if err := rows.Scan(&payload, &preview); err != nil {
			return UsageSummary{}, fmt.Errorf("decode usage bucket row: %w", err)
		}
		var stored bucketPayload
		if err := json.Unmarshal(payload, &stored); err != nil {
			return UsageSummary{}, fmt.Errorf("decode usage bucket payload: %w", err)
		}
		if err := json.Unmarshal(preview, &stored.Bucket.Preview); err != nil {
			return UsageSummary{}, fmt.Errorf("decode usage bucket preview: %w", err)
		}
		if err := accumulator.add(stored.Bucket); err != nil {
			return UsageSummary{}, err
		}
	}
	if err := rows.Err(); err != nil {
		return UsageSummary{}, fmt.Errorf("iterate usage buckets: %w", err)
	}
	return accumulator.summary(), nil
}

type valueAccumulator struct {
	seen, known, missing bool
	sum                  uint64
}

func (a *valueAccumulator) add(value *uint64) error {
	a.seen = true
	if value == nil {
		a.missing = true
		return nil
	}
	a.known = true
	if math.MaxUint64-a.sum < *value {
		return errors.New("usage aggregation overflows uint64")
	}
	a.sum += *value
	return nil
}

func (a *valueAccumulator) addAggregate(value AggregateValue) error {
	if value.State == AvailabilityEmpty {
		return nil
	}
	a.seen = true
	if value.Value != nil {
		a.known = true
		if math.MaxUint64-a.sum < *value.Value {
			return errors.New("usage aggregation overflows uint64")
		}
		a.sum += *value.Value
	}
	if value.State != AvailabilityComplete {
		a.missing = true
	}
	return nil
}

func (a valueAccumulator) value() AggregateValue {
	if !a.seen {
		return AggregateValue{State: AvailabilityEmpty}
	}
	if !a.known {
		return AggregateValue{State: AvailabilityUnavailable}
	}
	value := a.sum
	if a.missing {
		return AggregateValue{Value: &value, State: AvailabilityPartial}
	}
	return AggregateValue{Value: &value, State: AvailabilityComplete}
}

type tokenAccumulator struct {
	input, read, write, writeFiveMinute, writeOneHour, output, reasoning valueAccumulator
}

func (a *tokenAccumulator) add(tokens normalize.TokenCounts) error {
	for _, pair := range []struct {
		accumulator *valueAccumulator
		value       *uint64
	}{
		{&a.input, tokens.InputUncached}, {&a.read, tokens.InputCacheRead}, {&a.write, tokens.InputCacheWrite},
		{&a.writeFiveMinute, tokens.InputCacheWrite5m}, {&a.writeOneHour, tokens.InputCacheWrite1h},
		{&a.output, tokens.Output}, {&a.reasoning, tokens.ReasoningOutput},
	} {
		if err := pair.accumulator.add(pair.value); err != nil {
			return err
		}
	}
	return nil
}

func (a *tokenAccumulator) addBreakdown(tokens TokenBreakdown) error {
	for _, pair := range []struct {
		accumulator *valueAccumulator
		value       AggregateValue
	}{
		{&a.input, tokens.InputUncached}, {&a.read, tokens.InputCacheRead}, {&a.write, tokens.InputCacheWrite},
		{&a.writeFiveMinute, tokens.InputCacheWrite5m}, {&a.writeOneHour, tokens.InputCacheWrite1h},
		{&a.output, tokens.Output}, {&a.reasoning, tokens.ReasoningOutput},
	} {
		if err := pair.accumulator.addAggregate(pair.value); err != nil {
			return err
		}
	}
	return nil
}

func (a tokenAccumulator) value() TokenBreakdown {
	return TokenBreakdown{
		InputUncached: a.input.value(), InputCacheRead: a.read.value(), InputCacheWrite: a.write.value(),
		InputCacheWrite5m: a.writeFiveMinute.value(), InputCacheWrite1h: a.writeOneHour.value(),
		Output: a.output.value(), ReasoningOutput: a.reasoning.value(),
	}
}

type bucketAccumulator struct {
	template UsageBucket
	tokens   tokenAccumulator
}

func newBucketAccumulator(start time.Time, observation StoredObservation, revision uint64) *bucketAccumulator {
	return &bucketAccumulator{template: UsageBucket{
		Start: start, Source: observation.Source, Surface: observation.Surface,
		ObservedModelID: observation.ObservedModelID, ModelEvidence: modelEvidence(observation),
		Measurement: observation.Measurement, NormalizationVersion: observation.NormalizationVersion,
		ServiceTier: observation.ServiceTier, PromptBand: promptBand(observation), Revision: revision,
	}}
}

func (a bucketAccumulator) bucket(previewer Previewer) (UsageBucket, error) {
	bucket := a.template
	bucket.Tokens = a.tokens.value()
	preview, err := previewBucket(bucket, previewer)
	if err != nil {
		return UsageBucket{}, err
	}
	bucket.Preview = preview
	return bucket, nil
}

// modelEvidence reduces a stored resolution to source evidence. Catalog
// results are derived per bucket and never stored on an observation.
func modelEvidence(observation StoredObservation) normalize.ModelResolution {
	switch {
	case observation.ObservedModelID == "":
		return normalize.ModelUnknown
	case observation.ModelResolution == normalize.ModelInferred:
		return normalize.ModelInferred
	default:
		return normalize.ModelReported
	}
}

// promptBand places a single request's prompt size on the upload ladder. A
// record that does not prove its size has no band.
func promptBand(observation StoredObservation) normalize.PromptBand {
	if observation.PromptTokens == nil {
		return normalize.PromptBandUnknown
	}
	return normalize.PromptBandFor(*observation.PromptTokens)
}

// previewBucket prices the bucket totals exactly as the server will, so the
// preview matches the server whenever both bundle the same catalog revision.
func previewBucket(bucket UsageBucket, previewer Previewer) (Preview, error) {
	preview := Preview{Resolution: normalize.ModelUnknown, APIEstimate: AggregateValue{State: AvailabilityUnavailable}}
	if previewer == nil {
		return preview, nil
	}
	tokens := bucket.Tokens
	partial := false
	for _, value := range []AggregateValue{tokens.InputUncached, tokens.InputCacheRead, tokens.InputCacheWrite, tokens.Output} {
		partial = partial || value.State == AvailabilityPartial
	}
	derivation, err := previewer.Derive(modelcatalog.Usage{
		ObservedModelID: bucket.ObservedModelID, ModelEvidence: bucket.ModelEvidence,
		Tokens: normalize.TokenCounts{
			InputUncached: tokens.InputUncached.Value, InputCacheRead: tokens.InputCacheRead.Value,
			InputCacheWrite: tokens.InputCacheWrite.Value, InputCacheWrite5m: tokens.InputCacheWrite5m.Value,
			InputCacheWrite1h: tokens.InputCacheWrite1h.Value, Output: tokens.Output.Value,
		},
		ServiceTier: bucket.ServiceTier, PromptBand: bucket.PromptBand, Partial: partial,
	})
	if err != nil {
		return Preview{}, fmt.Errorf("preview usage bucket: %w", err)
	}
	preview.CanonicalModelID = cloneString(derivation.Resolution.CanonicalID)
	preview.ModelProvider = cloneString(derivation.Resolution.Provider)
	preview.Resolution = derivation.Resolution.Quality
	preview.UnpricedReason = derivation.Resolution.UnpricedReason
	preview.PricedTokens, preview.UnpricedTokens = derivation.PricedTokens, derivation.UnpricedTokens
	preview.CatalogRevision = derivation.Revision
	if derivation.EstimateMicroUSD != nil {
		value := *derivation.EstimateMicroUSD
		preview.APIEstimate = AggregateValue{Value: &value, State: Availability(derivation.EstimateCoverage)}
	}
	return preview, nil
}

type bucketPayload struct {
	BucketKey string      `json:"bucket_key"`
	Bucket    UsageBucket `json:"bucket"`
}

// ErrBucketRevisionConflict marks different payloads claiming the same revision.
var ErrBucketRevisionConflict = errors.New("usage bucket revision conflicts with stored payload")

func applyBucket(ctx context.Context, tx *sql.Tx, bucket UsageBucket) (bool, error) {
	if err := validateBucket(bucket); err != nil {
		return false, err
	}
	key := usageBucketKey(bucket)
	payload, err := json.Marshal(bucketPayload{BucketKey: key, Bucket: bucket})
	if err != nil {
		return false, fmt.Errorf("encode usage bucket: %w", err)
	}
	preview, err := json.Marshal(bucket.Preview)
	if err != nil {
		return false, fmt.Errorf("encode usage bucket preview: %w", err)
	}
	content, err := bucketContentHash(bucket)
	if err != nil {
		return false, err
	}
	var currentRevision, currentContent string
	err = tx.QueryRowContext(ctx, `SELECT revision, content_hash FROM usage_buckets WHERE bucket_key = ?`, key).Scan(&currentRevision, &currentContent)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, fmt.Errorf("read usage bucket revision: %w", err)
	}
	if err == nil {
		current, parseErr := strconv.ParseUint(currentRevision, 10, 64)
		if parseErr != nil {
			return false, fmt.Errorf("decode usage bucket revision: %w", parseErr)
		}
		if bucket.Revision < current {
			return false, nil
		}
		if currentContent == content {
			// Nothing uploadable changed. Only the local preview may differ,
			// for example after a build with a newer bundled catalog.
			if _, err := tx.ExecContext(ctx, `UPDATE usage_buckets SET preview_json = ? WHERE bucket_key = ? AND preview_json <> ?`, string(preview), key, string(preview)); err != nil {
				return false, fmt.Errorf("refresh usage bucket preview: %w", err)
			}
			return false, nil
		}
		if bucket.Revision == current {
			return false, ErrBucketRevisionConflict
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO usage_buckets(bucket_key,bucket_start,revision,content_hash,payload_json,preview_json)
		VALUES (?,?,?,?,?,?)
		ON CONFLICT(bucket_key) DO UPDATE SET
			revision=excluded.revision,content_hash=excluded.content_hash,payload_json=excluded.payload_json,
			preview_json=excluded.preview_json,updated_at=CURRENT_TIMESTAMP`,
		key, formatTime(bucket.Start), strconv.FormatUint(bucket.Revision, 10), content, string(payload), string(preview))
	if err != nil {
		return false, fmt.Errorf("write usage bucket: %w", err)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO outbox_records(bucket_key,revision,payload_json,state) VALUES (?,?,?,'pending') ON CONFLICT(bucket_key) DO UPDATE SET revision=excluded.revision,payload_json=excluded.payload_json,state='pending',created_at=CURRENT_TIMESTAMP`, key, strconv.FormatUint(bucket.Revision, 10), string(payload))
	if err != nil {
		return false, fmt.Errorf("enqueue usage bucket revision: %w", err)
	}
	return true, nil
}

// bucketContentHash identifies the uploadable content apart from its
// revision, so an unchanged bucket is neither bumped nor re-enqueued.
func bucketContentHash(bucket UsageBucket) (string, error) {
	bucket.Revision = 0
	encoded, err := json.Marshal(bucket)
	if err != nil {
		return "", fmt.Errorf("encode usage bucket content: %w", err)
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

func validateBucket(bucket UsageBucket) error {
	if bucket.Revision == 0 || bucket.Start.Location() != time.UTC || !bucket.Start.Equal(bucket.Start.Truncate(bucketInterval)) {
		return errors.New("usage bucket requires a positive revision and aligned UTC start")
	}
	if !bucket.ServiceTier.Valid() || !bucket.PromptBand.Valid() {
		return errors.New("usage bucket has invalid pricing evidence")
	}
	tokens := bucket.Tokens
	for _, value := range []AggregateValue{
		tokens.InputUncached, tokens.InputCacheRead, tokens.InputCacheWrite, tokens.InputCacheWrite5m,
		tokens.InputCacheWrite1h, tokens.Output, tokens.ReasoningOutput, bucket.Preview.APIEstimate,
	} {
		if value.State != AvailabilityComplete && value.State != AvailabilityPartial && value.State != AvailabilityUnavailable {
			return errors.New("usage bucket has invalid availability")
		}
		if value.State == AvailabilityComplete && value.Value == nil || value.State == AvailabilityUnavailable && value.Value != nil {
			return errors.New("usage bucket availability contradicts its value")
		}
	}
	return nil
}

func aggregateSQL(value AggregateValue) any {
	if value.Value == nil {
		return nil
	}
	return strconv.FormatUint(*value.Value, 10)
}

// usageBucketKey identifies a bucket by observed evidence only. Catalog
// results are excluded so a catalog update never moves a bucket.
func usageBucketKey(bucket UsageBucket) string {
	parts := []string{
		formatTime(bucket.Start), string(bucket.Source), string(bucket.Surface), bucket.ObservedModelID,
		string(bucket.ModelEvidence), string(bucket.Measurement), bucket.NormalizationVersion,
	}
	parts = append(parts, pricingEvidenceKey(bucket.ServiceTier, bucket.PromptBand)...)
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:])
}

func bucketGroupKey(start time.Time, observation StoredObservation) string {
	parts := []string{
		formatTime(start), string(observation.Source), string(observation.Surface), observation.ObservedModelID,
		string(modelEvidence(observation)), string(observation.Measurement), observation.NormalizationVersion,
	}
	parts = append(parts, pricingEvidenceKey(observation.ServiceTier, promptBand(observation))...)
	return strings.Join(parts, "\x00")
}

// pricingEvidenceKey returns the identity parts for pricing evidence. It adds
// nothing when there is none, which keeps every key computed before the
// evidence existed. The marker keeps a tier from ever reading as another
// dimension. internal/sync derives the same key and must stay in step.
func pricingEvidenceKey(tier normalize.ServiceTier, band normalize.PromptBand) []string {
	if tier == normalize.ServiceTierUnknown && band == normalize.PromptBandUnknown {
		return nil
	}
	return []string{"pricing-evidence", string(tier), string(band)}
}
func pointerText(value *string) string {
	if value == nil {
		return "<null>"
	}
	return *value
}
func cloneString(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func periodBounds(period Period, now time.Time, location *time.Location) (*time.Time, time.Time, error) {
	if location == nil {
		return nil, time.Time{}, errors.New("usage period location is required")
	}
	local := now.In(location)
	day := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, location)
	end := now.UTC().Truncate(bucketInterval).Add(bucketInterval)
	var start time.Time
	switch period {
	case PeriodToday:
		start = day
	case PeriodWeek:
		days := (int(day.Weekday()) + 6) % 7
		start = day.AddDate(0, 0, -days)
	case PeriodLast30Days:
		start = day.AddDate(0, 0, -29)
	case PeriodAllTime:
		return nil, end, nil
	default:
		return nil, time.Time{}, fmt.Errorf("unsupported usage period %q", period)
	}
	utc := start.UTC()
	return &utc, end, nil
}

type summaryAccumulator struct {
	period            Period
	start             *time.Time
	end               time.Time
	all               summaryValues
	models            map[string]*modelSummaryAccumulator
	sources, surfaces map[string]*summaryValues
}
type summaryValues struct {
	tokens   tokenAccumulator
	estimate valueAccumulator
}
type modelSummaryAccumulator struct {
	key    ModelBreakdown
	values summaryValues
	// unpriced sums unpriced tokens per reason. Pricing evidence can leave
	// part of one model's usage unpriced, so the reason is not a model fact.
	unpriced map[string]uint64
}

func newSummaryAccumulator(period Period, start *time.Time, end time.Time) *summaryAccumulator {
	return &summaryAccumulator{period: period, start: start, end: end, models: map[string]*modelSummaryAccumulator{}, sources: map[string]*summaryValues{}, surfaces: map[string]*summaryValues{}}
}
func (a *summaryAccumulator) add(bucket UsageBucket) error {
	if err := addSummaryValues(&a.all, bucket); err != nil {
		return err
	}
	preview := bucket.Preview
	mkey := strings.Join([]string{bucket.ObservedModelID, pointerText(preview.CanonicalModelID), string(preview.Resolution)}, "\x00")
	model := a.models[mkey]
	if model == nil {
		model = &modelSummaryAccumulator{key: ModelBreakdown{
			ObservedModelID: bucket.ObservedModelID, CanonicalModelID: cloneString(preview.CanonicalModelID),
			Resolution: preview.Resolution,
		}, unpriced: map[string]uint64{}}
		a.models[mkey] = model
	}
	if preview.UnpricedReason != "" {
		// Saturating: the sums only rank reasons against each other.
		sum := model.unpriced[preview.UnpricedReason] + preview.UnpricedTokens
		if sum < preview.UnpricedTokens {
			sum = math.MaxUint64
		}
		model.unpriced[preview.UnpricedReason] = sum
	}
	if err := addSummaryValues(&model.values, bucket); err != nil {
		return err
	}
	for name, collection := range map[string]map[string]*summaryValues{string(bucket.Source): a.sources, string(bucket.Surface): a.surfaces} {
		values := collection[name]
		if values == nil {
			values = &summaryValues{}
			collection[name] = values
		}
		if err := addSummaryValues(values, bucket); err != nil {
			return err
		}
	}
	return nil
}
func addSummaryValues(values *summaryValues, bucket UsageBucket) error {
	if err := values.tokens.addBreakdown(bucket.Tokens); err != nil {
		return err
	}
	return values.estimate.addAggregate(bucket.Preview.APIEstimate)
}
func (a *summaryAccumulator) summary() UsageSummary {
	result := UsageSummary{Period: a.period, Start: a.start, End: a.end, Tokens: a.all.tokens.value(), APIEstimate: a.all.estimate.value()}
	modelKeys := sortedMapKeys(a.models)
	for _, key := range modelKeys {
		entry := a.models[key]
		entry.key.Tokens = entry.values.tokens.value()
		entry.key.APIEstimate = entry.values.estimate.value()
		// Show the reason behind the most unpriced tokens; names break ties.
		for _, reason := range sortedMapKeys(entry.unpriced) {
			if entry.key.UnpricedReason == "" || entry.unpriced[reason] > entry.unpriced[entry.key.UnpricedReason] {
				entry.key.UnpricedReason = reason
			}
		}
		result.Models = append(result.Models, entry.key)
	}
	for _, pair := range []struct {
		source map[string]*summaryValues
		target *[]NamedBreakdown
	}{{a.sources, &result.Sources}, {a.surfaces, &result.Surfaces}} {
		for _, name := range sortedMapKeys(pair.source) {
			values := pair.source[name]
			*pair.target = append(*pair.target, NamedBreakdown{Name: name, Tokens: values.tokens.value(), APIEstimate: values.estimate.value()})
		}
	}
	return result
}
func sortedMapKeys[T any](values map[string]T) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
