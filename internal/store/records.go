package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ansonnchan/tokenholic/internal/normalize"
)

// SourceIdentity is a privacy-safe identity for one discovered evidence root.
// Its local key is hashed on construction and cannot be recovered from storage.
type SourceIdentity struct {
	Source  normalize.Source
	Surface normalize.Surface
	value   string
}

// NewSourceIdentity derives a stable stored identity without retaining the
// private local key, which may describe a source location.
func NewSourceIdentity(source normalize.Source, surface normalize.Surface, localKey string) SourceIdentity {
	return SourceIdentity{
		Source:  source,
		Surface: surface,
		value:   fingerprint("source", string(source), string(surface), localKey),
	}
}

// Checkpoint is the latest durable progress for one source. LastSourceRecordID
// is accepted transiently and is fingerprinted before any SQL is executed.
type Checkpoint struct {
	Source             SourceIdentity
	Cursor             uint64
	LastSourceRecordID string
	CollectedAt        time.Time
}

// AdapterHealth is a durable, path-redacted source health snapshot.
type AdapterHealth struct {
	Source         SourceIdentity
	Health         normalize.SourceHealth
	AdapterVersion string
	CheckedAt      time.Time
	LastSuccessAt  *time.Time
}

// ObservationBatch atomically advances one source with its normalized records
// and current adapter health.
type ObservationBatch struct {
	Observations []normalize.Observation
	Checkpoint   Checkpoint
	Health       AdapterHealth
}

// CommitResult reports how many observations were newly inserted, had their
// usage replaced by a newer adapter revision, or were day totals that grew.
type CommitResult struct {
	InsertedObservations int64
}

// StoredObservation is normalized usage with only privacy-safe local identity.
type StoredObservation struct {
	Identity           string
	Source             normalize.Source
	Surface            normalize.Surface
	SourceVersion      string
	SessionFingerprint *string
	OccurredAt         time.Time
	ObservedModelID    string
	CanonicalModelID   *string
	ModelProvider      *string
	PricingKey         *string
	ModelResolution    normalize.ModelResolution
	Tokens             normalize.TokenCounts
	// ServiceTier and PromptTokens are pricing evidence; empty and nil mean
	// the source did not prove them.
	ServiceTier          normalize.ServiceTier
	PromptTokens         *uint64
	CountSemantics       normalize.CountSemantics
	Measurement          normalize.Measurement
	AdapterVersion       string
	NormalizationVersion string
	Warnings             []normalize.Warning
}

// StoredCheckpoint is a checkpoint with an irreversible record fingerprint.
type StoredCheckpoint struct {
	Source                SourceIdentity
	Cursor                uint64
	LastRecordFingerprint *string
	CollectedAt           time.Time
}

// StoredAdapterHealth is a path-redacted adapter health record.
type StoredAdapterHealth struct {
	Source         SourceIdentity
	Health         normalize.SourceHealth
	AdapterVersion string
	CheckedAt      time.Time
	LastSuccessAt  *time.Time
}

// CommitObservations writes observations, the matching checkpoint, and health
// in one transaction. A checkpoint never advances ahead of normalized state.
func (db *DB) CommitObservations(ctx context.Context, batch ObservationBatch) (CommitResult, error) {
	return db.CommitScan(ctx, []ObservationBatch{batch})
}

// CommitScan atomically writes every source batch in one completed local scan.
func (db *DB) CommitScan(ctx context.Context, batches []ObservationBatch) (CommitResult, error) {
	for _, batch := range batches {
		if err := validateBatch(batch); err != nil {
			return CommitResult{}, err
		}
	}
	tx, err := db.sql.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return CommitResult{}, fmt.Errorf("begin scan commit: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	var result CommitResult
	for _, batch := range batches {
		observations := append([]normalize.Observation(nil), batch.Observations...)
		sort.Slice(observations, func(i, j int) bool {
			return observationIdentity(observations[i]) < observationIdentity(observations[j])
		})
		for _, observation := range observations {
			inserted, err := insertObservation(ctx, tx, observation)
			if err != nil {
				return CommitResult{}, err
			}
			result.InsertedObservations += inserted
		}
		if err := upsertCheckpoint(ctx, tx, batch.Checkpoint); err != nil {
			return CommitResult{}, err
		}
		if err := upsertAdapterHealth(ctx, tx, batch.Health); err != nil {
			return CommitResult{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return CommitResult{}, fmt.Errorf("commit local scan: %w", err)
	}
	committed = true
	return result, nil
}

// Observations returns all stored normalized observations in deterministic order.
func (db *DB) Observations(ctx context.Context) ([]StoredObservation, error) {
	rows, err := db.sql.QueryContext(ctx, `
		SELECT identity, source, surface, source_version, session_fingerprint,
		       occurred_at, observed_model_id, canonical_model_id, model_provider,
		       pricing_key, model_resolution, input_uncached_tokens,
		       input_cache_read_tokens, input_cache_write_tokens,
		       input_cache_write_5m_tokens, input_cache_write_1h_tokens, output_tokens,
		       reasoning_output_tokens, count_semantics, measurement,
		       adapter_version, normalization_version, warnings_json,
		       service_tier, prompt_tokens
		FROM observations ORDER BY occurred_at, identity
	`)
	if err != nil {
		return nil, fmt.Errorf("query observations: %w", err)
	}
	defer rows.Close()
	var observations []StoredObservation
	for rows.Next() {
		observation, err := scanObservation(rows)
		if err != nil {
			return nil, err
		}
		observations = append(observations, observation)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate observations: %w", err)
	}
	return observations, nil
}

// SourceCheckpoint returns the latest checkpoint for a source identity.
func (db *DB) SourceCheckpoint(ctx context.Context, source SourceIdentity) (StoredCheckpoint, bool, error) {
	row := db.sql.QueryRowContext(ctx, `
		SELECT cursor_value, last_record_fingerprint, collected_at
		FROM source_checkpoints WHERE source_identity = ?
	`, source.value)
	var cursor string
	var last sql.NullString
	var collected string
	if err := row.Scan(&cursor, &last, &collected); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return StoredCheckpoint{}, false, nil
		}
		return StoredCheckpoint{}, false, fmt.Errorf("query source checkpoint: %w", err)
	}
	parsedCursor, err := strconv.ParseUint(cursor, 10, 64)
	if err != nil {
		return StoredCheckpoint{}, false, fmt.Errorf("decode source checkpoint cursor: %w", err)
	}
	collectedAt, err := parseTime(collected)
	if err != nil {
		return StoredCheckpoint{}, false, fmt.Errorf("decode source checkpoint time: %w", err)
	}
	return StoredCheckpoint{
		Source: source, Cursor: parsedCursor, LastRecordFingerprint: nullableString(last), CollectedAt: collectedAt,
	}, true, nil
}

// AdapterHealthRecords returns durable source health in deterministic order.
func (db *DB) AdapterHealthRecords(ctx context.Context) ([]StoredAdapterHealth, error) {
	rows, err := db.sql.QueryContext(ctx, `
		SELECT source_identity, source, surface, status, record_count, format,
		       capabilities_json, warnings_json, adapter_version, checked_at,
		       last_success_at
		FROM adapter_health ORDER BY source, surface, source_identity
	`)
	if err != nil {
		return nil, fmt.Errorf("query adapter health: %w", err)
	}
	defer rows.Close()
	var records []StoredAdapterHealth
	for rows.Next() {
		record, err := scanAdapterHealth(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate adapter health: %w", err)
	}
	return records, nil
}

func validateBatch(batch ObservationBatch) error {
	checkpoint := batch.Checkpoint
	health := batch.Health
	if !validSourceIdentity(checkpoint.Source) || checkpoint.CollectedAt.IsZero() {
		return errors.New("commit observations: valid checkpoint source and collection time are required")
	}
	if health.Source != checkpoint.Source || health.Health.Source != checkpoint.Source.Source || health.Health.Surface != checkpoint.Source.Surface {
		return errors.New("commit observations: checkpoint and adapter health sources must match")
	}
	if health.AdapterVersion == "" || health.CheckedAt.IsZero() {
		return errors.New("commit observations: adapter version and health check time are required")
	}
	if err := health.Health.Capabilities.Validate(); err != nil {
		return fmt.Errorf("commit observations: invalid capabilities: %w", err)
	}
	if err := validateWarnings(health.Health.Warnings); err != nil {
		return fmt.Errorf("commit observations: invalid health warnings: %w", err)
	}
	if !validHealthStatus(health.Health.Status) {
		return errors.New("commit observations: invalid adapter health status")
	}
	for _, observation := range batch.Observations {
		if observation.Source != checkpoint.Source.Source || observation.Surface != checkpoint.Source.Surface {
			return errors.New("commit observations: observation source does not match checkpoint")
		}
		if err := observation.Validate(); err != nil {
			return fmt.Errorf("commit observations: invalid observation: %w", err)
		}
	}
	return nil
}

// insertObservation stores one observation. Rescanning the same evidence is a
// no-op; only a different adapter revision replaces the usage values and
// pricing evidence, so a corrected parser repairs records whose source files
// still exist.
//
// A day total is the exception. It is the absolute value of one agent, day,
// and model, and it grows until the day ends, so a later reading that
// differs replaces the stored one. It is never a second record: the identity
// is the same and the values are overwritten, not added. Model attribution, measurement, and normalization version stay
// as first stored. Pricing evidence is part of the usage-bucket identity too,
// so replacing it moves the record to another bucket; the rebuild that follows
// retracts the bucket it left.
func insertObservation(ctx context.Context, tx *sql.Tx, observation normalize.Observation) (int64, error) {
	warnings, err := json.Marshal(observation.Warnings)
	if err != nil {
		return 0, fmt.Errorf("encode observation warnings: %w", err)
	}
	result, err := tx.ExecContext(ctx, `
		INSERT INTO observations(
			identity, source, surface, source_version, session_fingerprint,
			occurred_at, observed_model_id, canonical_model_id, model_provider,
			pricing_key, model_resolution, input_uncached_tokens,
			input_cache_read_tokens, input_cache_write_tokens,
			input_cache_write_5m_tokens, input_cache_write_1h_tokens, output_tokens,
			reasoning_output_tokens, count_semantics, measurement,
			adapter_version, normalization_version, warnings_json,
			service_tier, prompt_tokens
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(identity) DO UPDATE SET
			input_uncached_tokens = excluded.input_uncached_tokens,
			input_cache_read_tokens = excluded.input_cache_read_tokens,
			input_cache_write_tokens = excluded.input_cache_write_tokens,
			input_cache_write_5m_tokens = excluded.input_cache_write_5m_tokens,
			input_cache_write_1h_tokens = excluded.input_cache_write_1h_tokens,
			output_tokens = excluded.output_tokens,
			reasoning_output_tokens = excluded.reasoning_output_tokens,
			adapter_version = excluded.adapter_version,
			warnings_json = excluded.warnings_json,
			service_tier = excluded.service_tier,
			prompt_tokens = excluded.prompt_tokens
		WHERE excluded.adapter_version <> observations.adapter_version
		   OR (observations.count_semantics = 'day_total' AND (
			excluded.input_uncached_tokens IS NOT observations.input_uncached_tokens OR
			excluded.input_cache_read_tokens IS NOT observations.input_cache_read_tokens OR
			excluded.input_cache_write_tokens IS NOT observations.input_cache_write_tokens OR
			excluded.output_tokens IS NOT observations.output_tokens OR
			excluded.reasoning_output_tokens IS NOT observations.reasoning_output_tokens OR
			excluded.warnings_json IS NOT observations.warnings_json))
	`,
		observationIdentity(observation), observation.Source, observation.Surface,
		observation.SourceVersion, sessionFingerprint(observation), formatTime(observation.OccurredAt),
		observation.ObservedModelID, observation.CanonicalModelID, observation.ModelProvider,
		observation.PricingKey, observation.ModelResolution, tokenString(observation.Tokens.InputUncached),
		tokenString(observation.Tokens.InputCacheRead), tokenString(observation.Tokens.InputCacheWrite),
		tokenString(observation.Tokens.InputCacheWrite5m), tokenString(observation.Tokens.InputCacheWrite1h),
		tokenString(observation.Tokens.Output), tokenString(observation.Tokens.ReasoningOutput),
		observation.CountSemantics, observation.Measurement, observation.AdapterVersion,
		observation.NormalizationVersion, string(warnings),
		serviceTierValue(observation.ServiceTier), tokenString(observation.PromptTokens),
	)
	if err != nil {
		return 0, fmt.Errorf("insert normalized observation: %w", err)
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("count inserted observations: %w", err)
	}
	return inserted, nil
}

func upsertCheckpoint(ctx context.Context, tx *sql.Tx, checkpoint Checkpoint) error {
	var last any
	if checkpoint.LastSourceRecordID != "" {
		last = fingerprint("record", string(checkpoint.Source.Source), checkpoint.LastSourceRecordID)
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO source_checkpoints(
			source_identity, source, surface, cursor_value,
			last_record_fingerprint, collected_at
		) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(source_identity) DO UPDATE SET
			cursor_value = excluded.cursor_value,
			last_record_fingerprint = excluded.last_record_fingerprint,
			collected_at = excluded.collected_at
	`, checkpoint.Source.value, checkpoint.Source.Source, checkpoint.Source.Surface,
		strconv.FormatUint(checkpoint.Cursor, 10), last, formatTime(checkpoint.CollectedAt))
	if err != nil {
		return fmt.Errorf("write source checkpoint: %w", err)
	}
	return nil
}

func upsertAdapterHealth(ctx context.Context, tx *sql.Tx, health AdapterHealth) error {
	capabilities, err := json.Marshal(health.Health.Capabilities)
	if err != nil {
		return fmt.Errorf("encode adapter capabilities: %w", err)
	}
	warnings, err := json.Marshal(health.Health.Warnings)
	if err != nil {
		return fmt.Errorf("encode adapter warnings: %w", err)
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO adapter_health(
			source_identity, source, surface, status, record_count, format,
			capabilities_json, warnings_json, adapter_version, checked_at,
			last_success_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(source_identity) DO UPDATE SET
			status = excluded.status,
			record_count = excluded.record_count,
			format = excluded.format,
			capabilities_json = excluded.capabilities_json,
			warnings_json = excluded.warnings_json,
			adapter_version = excluded.adapter_version,
			checked_at = excluded.checked_at,
			last_success_at = COALESCE(excluded.last_success_at, adapter_health.last_success_at)
	`, health.Source.value, health.Source.Source, health.Source.Surface,
		health.Health.Status, tokenString(health.Health.RecordCount), health.Health.Format,
		string(capabilities), string(warnings), health.AdapterVersion,
		formatTime(health.CheckedAt), nullableTimeValue(health.LastSuccessAt))
	if err != nil {
		return fmt.Errorf("write adapter health: %w", err)
	}
	return nil
}

type rowScanner interface {
	Scan(...any) error
}

func scanObservation(row rowScanner) (StoredObservation, error) {
	var observation StoredObservation
	var session, canonical, provider, pricing sql.NullString
	var occurred, warnings string
	var tokens [7]sql.NullString
	var tier, prompt sql.NullString
	if err := row.Scan(
		&observation.Identity, &observation.Source, &observation.Surface,
		&observation.SourceVersion, &session, &occurred, &observation.ObservedModelID,
		&canonical, &provider, &pricing, &observation.ModelResolution, &tokens[0],
		&tokens[1], &tokens[2], &tokens[3], &tokens[4], &tokens[5], &tokens[6], &observation.CountSemantics,
		&observation.Measurement, &observation.AdapterVersion,
		&observation.NormalizationVersion, &warnings, &tier, &prompt,
	); err != nil {
		return StoredObservation{}, fmt.Errorf("decode observation: %w", err)
	}
	var err error
	observation.ServiceTier = normalize.ServiceTier(tier.String)
	if !observation.ServiceTier.Valid() {
		return StoredObservation{}, fmt.Errorf("decode observation: service tier %q is invalid", tier.String)
	}
	if observation.PromptTokens, err = parseToken(prompt); err != nil {
		return StoredObservation{}, fmt.Errorf("decode observation prompt size: %w", err)
	}
	observation.OccurredAt, err = parseTime(occurred)
	if err != nil {
		return StoredObservation{}, fmt.Errorf("decode observation time: %w", err)
	}
	values := []*(*uint64){
		&observation.Tokens.InputUncached, &observation.Tokens.InputCacheRead,
		&observation.Tokens.InputCacheWrite, &observation.Tokens.InputCacheWrite5m,
		&observation.Tokens.InputCacheWrite1h, &observation.Tokens.Output,
		&observation.Tokens.ReasoningOutput,
	}
	for index := range tokens {
		*values[index], err = parseToken(tokens[index])
		if err != nil {
			return StoredObservation{}, fmt.Errorf("decode observation tokens: %w", err)
		}
	}
	if err := json.Unmarshal([]byte(warnings), &observation.Warnings); err != nil {
		return StoredObservation{}, fmt.Errorf("decode observation warnings: %w", err)
	}
	observation.SessionFingerprint = nullableString(session)
	observation.CanonicalModelID = nullableString(canonical)
	observation.ModelProvider = nullableString(provider)
	observation.PricingKey = nullableString(pricing)
	return observation, nil
}

func scanAdapterHealth(row rowScanner) (StoredAdapterHealth, error) {
	var record StoredAdapterHealth
	var identity, source, surface, status, format, capabilities, warnings, checked string
	var recordCount, lastSuccess sql.NullString
	if err := row.Scan(
		&identity, &source, &surface, &status, &recordCount, &format,
		&capabilities, &warnings, &record.AdapterVersion, &checked, &lastSuccess,
	); err != nil {
		return StoredAdapterHealth{}, fmt.Errorf("decode adapter health: %w", err)
	}
	record.Source = SourceIdentity{Source: normalize.Source(source), Surface: normalize.Surface(surface), value: identity}
	record.Health.Source = record.Source.Source
	record.Health.Surface = record.Source.Surface
	record.Health.Status = normalize.HealthStatus(status)
	record.Health.Format = format
	var err error
	record.Health.RecordCount, err = parseToken(recordCount)
	if err != nil {
		return StoredAdapterHealth{}, fmt.Errorf("decode adapter record count: %w", err)
	}
	if err := json.Unmarshal([]byte(capabilities), &record.Health.Capabilities); err != nil {
		return StoredAdapterHealth{}, fmt.Errorf("decode adapter capabilities: %w", err)
	}
	if err := json.Unmarshal([]byte(warnings), &record.Health.Warnings); err != nil {
		return StoredAdapterHealth{}, fmt.Errorf("decode adapter warnings: %w", err)
	}
	record.CheckedAt, err = parseTime(checked)
	if err != nil {
		return StoredAdapterHealth{}, fmt.Errorf("decode adapter health time: %w", err)
	}
	if lastSuccess.Valid {
		parsed, err := parseTime(lastSuccess.String)
		if err != nil {
			return StoredAdapterHealth{}, fmt.Errorf("decode adapter success time: %w", err)
		}
		record.LastSuccessAt = &parsed
	}
	return record, nil
}

func observationIdentity(observation normalize.Observation) string {
	return fingerprint("observation", string(observation.Source), observation.SourceRecordID)
}

func sessionFingerprint(observation normalize.Observation) any {
	if observation.SessionIDLocal == nil {
		return nil
	}
	return fingerprint("session", string(observation.Source), *observation.SessionIDLocal)
}

func fingerprint(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:])
}

// serviceTierValue stores an unproven tier as NULL, never as a tier name.
func serviceTierValue(tier normalize.ServiceTier) any {
	if tier == normalize.ServiceTierUnknown {
		return nil
	}
	return string(tier)
}

func tokenString(value *uint64) any {
	if value == nil {
		return nil
	}
	return strconv.FormatUint(*value, 10)
}

func parseToken(value sql.NullString) (*uint64, error) {
	if !value.Valid {
		return nil, nil
	}
	parsed, err := strconv.ParseUint(value.String, 10, 64)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}

func nullableString(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	result := value.String
	return &result
}

func nullableTimeValue(value *time.Time) any {
	if value == nil {
		return nil
	}
	return formatTime(*value)
}

func formatTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339Nano)
}

func parseTime(value string) (time.Time, error) {
	return time.Parse(time.RFC3339Nano, value)
}

func validSourceIdentity(source SourceIdentity) bool {
	if source.value == "" {
		return false
	}
	switch source.Source {
	case normalize.SourceClaude:
		return source.Surface == normalize.SurfaceCLI || source.Surface == normalize.SurfaceDesktopCode ||
			source.Surface == normalize.SurfaceDesktopAgent || source.Surface == normalize.SurfaceDesktopCowork
	case normalize.SourceCodex:
		return source.Surface == normalize.SurfaceCLI || source.Surface == normalize.SurfaceDesktop ||
			source.Surface == normalize.SurfaceSharedLocal
	default:
		return source.Source.DayLevel() && source.Surface == normalize.SurfaceCLI
	}
}

func validHealthStatus(status normalize.HealthStatus) bool {
	switch status {
	case normalize.HealthDetected, normalize.HealthNotFound, normalize.HealthPermissionDenied,
		normalize.HealthUnsupported, normalize.HealthDegraded, normalize.HealthError:
		return true
	default:
		return false
	}
}

func validateWarnings(warnings []normalize.Warning) error {
	for _, warning := range warnings {
		if warning.Code == "" {
			return errors.New("warning code is required")
		}
		if strings.ContainsAny(warning.Message, "\r\n") {
			return errors.New("warning message must be one line")
		}
	}
	return nil
}
