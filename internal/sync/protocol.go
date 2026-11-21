package sync

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
	"time"

	"github.com/ansonnchan/tokenholic/internal/normalize"
	"github.com/ansonnchan/tokenholic/internal/store"
)

const (
	// SchemaVersion is the current upload contract version. Version 2 carries
	// observed evidence only; the server resolves models and derives prices.
	SchemaVersion = 2
	// MaxBatchBuckets bounds one upload request.
	MaxBatchBuckets = 100
	// BucketsPath is the versioned upload route.
	BucketsPath = "/v2/sync/buckets"
)

// Value preserves both a nullable unsigned value and its coverage state.
type Value struct {
	Value *uint64 `json:"value"`
	State string  `json:"state"`
}

// Tokens is the allowlisted token breakdown: six categories plus reasoning.
// InputCacheWrite is the total across cache lifetimes and the 5m and 1h values
// split it; ReasoningOutput is a subset of Output. Neither subset is added to
// its total.
type Tokens struct {
	InputUncached     Value `json:"input_uncached"`
	InputCacheRead    Value `json:"input_cache_read"`
	InputCacheWrite   Value `json:"input_cache_write"`
	InputCacheWrite5m Value `json:"input_cache_write_5m"`
	InputCacheWrite1h Value `json:"input_cache_write_1h"`
	Output            Value `json:"output"`
	ReasoningOutput   Value `json:"reasoning_output"`
}

// Bucket is one absolute versioned minute of privacy-limited usage. It holds
// what the client observed and nothing the client concluded: no canonical
// model, provider, pricing key, or estimate. A client and server with
// different catalogs therefore always agree on what a bucket says.
type Bucket struct {
	Start           time.Time `json:"start"`
	Source          string    `json:"source"`
	Surface         string    `json:"surface"`
	ObservedModelID string    `json:"observed_model_id"`
	// ModelEvidence is reported, inferred, or unknown: how the source
	// attributed the model, not whether any catalog recognizes it.
	ModelEvidence        string `json:"model_evidence"`
	Measurement          string `json:"measurement"`
	NormalizationVersion string `json:"normalization_version"`
	// ServiceTier and PromptBand are pricing evidence: the provider tier the
	// bucket's requests ran at and where their prompt sizes fall on the fixed
	// ladder in internal/normalize. Each is omitted when the source did not
	// prove it, and absence means unknown, never standard or short. A bucket
	// with neither is byte-identical to one sent before the fields existed.
	ServiceTier string `json:"service_tier,omitempty"`
	PromptBand  string `json:"prompt_band,omitempty"`
	Tokens      Tokens `json:"tokens"`
	Revision    uint64 `json:"revision"`
}

// Envelope gives a deterministic identity to one bucket.
type Envelope struct {
	BucketKey string `json:"bucket_key"`
	Bucket    Bucket `json:"bucket"`
}

// Batch is one bounded upload request.
type Batch struct {
	SchemaVersion int        `json:"schema_version"`
	Buckets       []Envelope `json:"buckets"`
}

// AckStatus describes how the server treated one absolute revision.
type AckStatus string

const (
	AckApplied   AckStatus = "applied"
	AckUnchanged AckStatus = "unchanged"
	AckStale     AckStatus = "stale"
)

// Acknowledgement identifies a durable server decision for one revision.
type Acknowledgement struct {
	BucketKey string    `json:"bucket_key"`
	Revision  uint64    `json:"revision"`
	Status    AckStatus `json:"status"`
}

// BatchResponse acknowledges every accepted, repeated, or stale revision.
type BatchResponse struct {
	Acknowledgements []Acknowledgement `json:"acknowledgements"`
}

// BuildBatch decodes the exact local outbox serialization into the explicit
// upload allowlist and returns deterministic request JSON.
func BuildBatch(records []store.OutboxRecord) (Batch, []byte, error) {
	if len(records) == 0 || len(records) > MaxBatchBuckets {
		return Batch{}, nil, fmt.Errorf("sync batch must contain 1 through %d buckets", MaxBatchBuckets)
	}
	batch := Batch{SchemaVersion: SchemaVersion, Buckets: make([]Envelope, 0, len(records))}
	for _, record := range records {
		decoder := json.NewDecoder(bytes.NewReader(record.Payload))
		decoder.DisallowUnknownFields()
		var envelope Envelope
		if err := decoder.Decode(&envelope); err != nil {
			return Batch{}, nil, fmt.Errorf("decode outbox bucket %q: %w", record.BucketKey, err)
		}
		var trailing any
		if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
			return Batch{}, nil, fmt.Errorf("decode outbox bucket %q: trailing JSON", record.BucketKey)
		}
		if envelope.BucketKey != record.BucketKey || envelope.Bucket.Revision != record.Revision {
			return Batch{}, nil, fmt.Errorf("outbox bucket %q identity differs from its payload", record.BucketKey)
		}
		if err := envelope.Validate(); err != nil {
			return Batch{}, nil, fmt.Errorf("validate outbox bucket %q: %w", record.BucketKey, err)
		}
		batch.Buckets = append(batch.Buckets, envelope)
	}
	body, err := json.Marshal(batch)
	if err != nil {
		return Batch{}, nil, fmt.Errorf("encode sync batch: %w", err)
	}
	return batch, body, nil
}

// Validate checks the bounded protocol and every bucket invariant.
func (b Batch) Validate() error {
	if b.SchemaVersion != SchemaVersion {
		return fmt.Errorf("unsupported sync schema version %d", b.SchemaVersion)
	}
	if len(b.Buckets) == 0 || len(b.Buckets) > MaxBatchBuckets {
		return fmt.Errorf("sync batch must contain 1 through %d buckets", MaxBatchBuckets)
	}
	seen := make(map[string]struct{}, len(b.Buckets))
	for _, envelope := range b.Buckets {
		if err := envelope.Validate(); err != nil {
			return err
		}
		if _, exists := seen[envelope.BucketKey]; exists {
			return fmt.Errorf("bucket %q is duplicated", envelope.BucketKey)
		}
		seen[envelope.BucketKey] = struct{}{}
	}
	return nil
}

// Validate checks identity, source and evidence semantics, and nullable coverage.
func (e Envelope) Validate() error {
	if len(e.BucketKey) != 64 {
		return errors.New("bucket key must be a 64-character SHA-256 digest")
	}
	if _, err := hex.DecodeString(e.BucketKey); err != nil || strings.ToLower(e.BucketKey) != e.BucketKey {
		return errors.New("bucket key must be lowercase hexadecimal")
	}
	bucket := e.Bucket
	if bucket.Revision == 0 || bucket.Start.Location() != time.UTC || !bucket.Start.Equal(bucket.Start.Truncate(time.Minute)) {
		return errors.New("bucket requires a positive revision and minute-aligned UTC start")
	}
	if !validSourceSurface(bucket.Source, bucket.Surface) {
		return errors.New("bucket has an invalid source and surface")
	}
	if len(bucket.ObservedModelID) > 256 || len(bucket.NormalizationVersion) < 1 || len(bucket.NormalizationVersion) > 64 {
		return errors.New("bucket model or normalization version is invalid")
	}
	if bucket.Measurement != "reported" && bucket.Measurement != "derived" && bucket.Measurement != "estimated" {
		return errors.New("bucket measurement is invalid")
	}
	switch bucket.ModelEvidence {
	case "unknown":
	case "reported", "inferred":
		if bucket.ObservedModelID == "" {
			return errors.New("attributed model is missing its observed identifier")
		}
	default:
		return errors.New("bucket model evidence is invalid")
	}
	if !normalize.ServiceTier(bucket.ServiceTier).Valid() || !normalize.PromptBand(bucket.PromptBand).Valid() {
		return errors.New("bucket pricing evidence is invalid")
	}
	tokens := bucket.Tokens
	for _, value := range []Value{
		tokens.InputUncached, tokens.InputCacheRead, tokens.InputCacheWrite, tokens.InputCacheWrite5m,
		tokens.InputCacheWrite1h, tokens.Output, tokens.ReasoningOutput,
	} {
		if err := validateValue(value); err != nil {
			return err
		}
	}
	if tokens.ReasoningOutput.Value != nil && tokens.Output.Value != nil && *tokens.ReasoningOutput.Value > *tokens.Output.Value {
		return errors.New("reasoning output exceeds total output")
	}
	if err := validateCacheWrite(tokens); err != nil {
		return err
	}
	if expected := bucketKey(bucket); expected != e.BucketKey {
		return errors.New("bucket key does not match its dimensions")
	}
	return nil
}

// validateCacheWrite keeps the lifetimes a split of the total, so a bucket
// can never count cache writes twice or price more than it reports.
func validateCacheWrite(tokens Tokens) error {
	fiveMinute, oneHour := tokens.InputCacheWrite5m.Value, tokens.InputCacheWrite1h.Value
	if fiveMinute == nil && oneHour == nil {
		return nil
	}
	if tokens.InputCacheWrite.Value == nil {
		return errors.New("cache-write lifetimes require the combined total")
	}
	var split uint64
	for _, lifetime := range []*uint64{fiveMinute, oneHour} {
		if lifetime == nil {
			continue
		}
		if math.MaxUint64-split < *lifetime {
			return errors.New("cache-write lifetimes overflow")
		}
		split += *lifetime
	}
	if split > *tokens.InputCacheWrite.Value {
		return errors.New("cache-write lifetimes exceed the combined total")
	}
	return nil
}

// CanonicalEnvelope returns deterministic bytes and their digest for server
// conflict detection. Every field is client evidence, so the digest changes
// only when the client reports something different.
func CanonicalEnvelope(envelope Envelope) ([]byte, [32]byte, error) {
	if err := envelope.Validate(); err != nil {
		return nil, [32]byte{}, err
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return nil, [32]byte{}, err
	}
	return encoded, sha256.Sum256(encoded), nil
}

func validSourceSurface(source, surface string) bool {
	return source == "claude" && (surface == "cli" || surface == "desktop_code" || surface == "desktop_agent" || surface == "desktop_cowork") ||
		source == "codex" && (surface == "cli" || surface == "desktop" || surface == "shared_local")
}

func validateValue(value Value) error {
	switch value.State {
	case "complete", "partial":
		if value.Value == nil {
			return errors.New("available value is null")
		}
	case "unavailable":
		if value.Value != nil {
			return errors.New("unavailable value is non-null")
		}
	default:
		return errors.New("bucket availability is invalid")
	}
	return nil
}

func bucketKey(bucket Bucket) string {
	parts := []string{
		bucket.Start.UTC().Format(time.RFC3339Nano), bucket.Source, bucket.Surface, bucket.ObservedModelID,
		bucket.ModelEvidence, bucket.Measurement, bucket.NormalizationVersion,
	}
	// Pricing evidence joins the identity only when present, so a bucket
	// without it keeps the key it always had. internal/store derives the same
	// key and must stay in step.
	if bucket.ServiceTier != "" || bucket.PromptBand != "" {
		parts = append(parts, "pricing-evidence", bucket.ServiceTier, bucket.PromptBand)
	}
	digest := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(digest[:])
}
