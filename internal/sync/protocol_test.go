package sync

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ansonnchan/tokenholic/internal/normalize"
	"github.com/ansonnchan/tokenholic/internal/store"
)

func TestBuildBatchUsesExplicitPrivacyAllowlist(t *testing.T) {
	envelope := sampleEnvelope(1, 7)
	payload, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	batch, body, err := BuildBatch([]store.OutboxRecord{{
		BucketKey: envelope.BucketKey, Revision: envelope.Bucket.Revision, Payload: payload,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if batch.SchemaVersion != SchemaVersion || len(batch.Buckets) != 1 {
		t.Fatalf("batch = %#v", batch)
	}
	for _, prohibited := range []string{"prompt", "response", "source_code", "file_path", "project_name", "repository", "branch_name", "command_text", "provider_credential", "session_id"} {
		if strings.Contains(strings.ToLower(string(body)), prohibited) {
			t.Fatalf("serialized batch contains prohibited field %q: %s", prohibited, body)
		}
	}
	var decoded Batch
	if err := json.Unmarshal(body, &decoded); err != nil || decoded.Buckets[0].Bucket.Revision != 7 {
		t.Fatalf("encoded batch = %#v, %v", decoded, err)
	}
}

func TestBuildBatchRejectsIdentityAndAvailabilityContradictions(t *testing.T) {
	envelope := sampleEnvelope(1, 1)
	count := func(v uint64) Value { return Value{Value: &v, State: "complete"} }
	rekey := func(value *Envelope) { value.BucketKey = bucketKey(value.Bucket) }
	tests := []struct {
		name   string
		mutate func(*Envelope)
	}{
		{name: "key", mutate: func(value *Envelope) { value.BucketKey = strings.Repeat("a", 64) }},
		{name: "evidence changed without rekeying", mutate: func(value *Envelope) { value.Bucket.ModelEvidence = "inferred" }},
		{name: "catalog result offered as evidence", mutate: func(value *Envelope) { value.Bucket.ModelEvidence = "resolved"; rekey(value) }},
		{name: "attributed model without an identifier", mutate: func(value *Envelope) { value.Bucket.ObservedModelID = ""; rekey(value) }},
		{name: "unsupported source", mutate: func(value *Envelope) { value.Bucket.Source = "chat"; rekey(value) }},
		{name: "missing complete value", mutate: func(value *Envelope) { value.Bucket.Tokens.Output.Value = nil }},
		{name: "reasoning exceeds output", mutate: func(value *Envelope) { value.Bucket.Tokens.ReasoningOutput = count(8) }},
		{name: "lifetimes without a total", mutate: func(value *Envelope) {
			value.Bucket.Tokens.InputCacheWrite = Value{State: "unavailable"}
			value.Bucket.Tokens.InputCacheWrite5m = count(1)
		}},
		{name: "lifetimes exceed the total", mutate: func(value *Envelope) {
			value.Bucket.Tokens.InputCacheWrite = count(5)
			value.Bucket.Tokens.InputCacheWrite5m, value.Bucket.Tokens.InputCacheWrite1h = count(3), count(3)
		}},
		{name: "lifetimes overflow", mutate: func(value *Envelope) {
			value.Bucket.Tokens.InputCacheWrite = count(5)
			value.Bucket.Tokens.InputCacheWrite5m, value.Bucket.Tokens.InputCacheWrite1h = count(^uint64(0)), count(1)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			changed := envelope
			test.mutate(&changed)
			payload, err := json.Marshal(changed)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := BuildBatch([]store.OutboxRecord{{BucketKey: changed.BucketKey, Revision: changed.Bucket.Revision, Payload: payload}}); err == nil {
				t.Fatal("BuildBatch() accepted contradictory payload")
			}
		})
	}
}

func TestEnvelopeAcceptsEveryEvidenceShape(t *testing.T) {
	count := func(v uint64) Value { return Value{Value: &v, State: "complete"} }
	tests := []struct {
		name   string
		mutate func(*Bucket)
	}{
		{name: "reported model"},
		{name: "model no catalog has heard of", mutate: func(bucket *Bucket) { bucket.ObservedModelID = "model-released-tomorrow" }},
		{name: "inferred model", mutate: func(bucket *Bucket) {
			bucket.Source, bucket.Surface, bucket.ModelEvidence = "codex", "shared_local", "inferred"
		}},
		{name: "no model at all", mutate: func(bucket *Bucket) { bucket.ObservedModelID, bucket.ModelEvidence = "", "unknown" }},
		{name: "combined cache write only", mutate: func(bucket *Bucket) {
			bucket.Tokens.InputCacheWrite = count(40)
			bucket.Tokens.InputCacheWrite5m, bucket.Tokens.InputCacheWrite1h = Value{State: "unavailable"}, Value{State: "unavailable"}
		}},
		{name: "partly split cache write", mutate: func(bucket *Bucket) {
			bucket.Tokens.InputCacheWrite = count(50)
			bucket.Tokens.InputCacheWrite5m, bucket.Tokens.InputCacheWrite1h = Value{State: "unavailable"}, Value{Value: count(30).Value, State: "partial"}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			bucket := sampleEnvelope(1, 1).Bucket
			if test.mutate != nil {
				test.mutate(&bucket)
			}
			if err := (Envelope{BucketKey: bucketKey(bucket), Bucket: bucket}).Validate(); err != nil {
				t.Fatalf("Validate() error = %v", err)
			}
		})
	}
}

// The request body is the whole protocol surface. This pins its exact field
// set so a client conclusion can never be added to an upload unnoticed.
func TestBatchWireFormatIsObservedEvidenceOnly(t *testing.T) {
	envelope := sampleEnvelope(1, 7)
	payload, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	_, body, err := BuildBatch([]store.OutboxRecord{{BucketKey: envelope.BucketKey, Revision: 7, Payload: payload}})
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"schema_version":2,"buckets":[{"bucket_key":"` + "%s" + `","bucket":{` +
		`"start":"2026-11-18T12:01:00Z","source":"claude","surface":"cli","observed_model_id":"synthetic-model",` +
		`"model_evidence":"reported","measurement":"reported","normalization_version":"1","tokens":{` +
		`"input_uncached":{"value":3,"state":"complete"},"input_cache_read":{"value":0,"state":"complete"},` +
		`"input_cache_write":{"value":9,"state":"complete"},"input_cache_write_5m":{"value":4,"state":"complete"},` +
		`"input_cache_write_1h":{"value":5,"state":"complete"},"output":{"value":7,"state":"complete"},` +
		`"reasoning_output":{"value":null,"state":"unavailable"}},"revision":7}}]}`
	if got := string(body); got != strings.Replace(want, "%s", envelope.BucketKey, 1) {
		t.Fatalf("request body =\n%s\nwant\n%s", got, strings.Replace(want, "%s", envelope.BucketKey, 1))
	}
}

func TestCanonicalEnvelopeIdentifiesClientEvidence(t *testing.T) {
	envelope := sampleEnvelope(1, 1)
	_, originalHash, err := CanonicalEnvelope(envelope)
	if err != nil {
		t.Fatal(err)
	}
	_, repeatedHash, err := CanonicalEnvelope(sampleEnvelope(1, 1))
	if err != nil {
		t.Fatal(err)
	}
	if originalHash != repeatedHash {
		t.Fatal("identical evidence produced different identities")
	}

	changedUsage := envelope
	otherOutput := uint64(8)
	changedUsage.Bucket.Tokens.Output.Value = &otherOutput
	_, usageHash, err := CanonicalEnvelope(changedUsage)
	if err != nil {
		t.Fatal(err)
	}
	if originalHash == usageHash {
		t.Fatal("usage change did not change client-evidence identity")
	}
}

func sampleEnvelope(minute int, revision uint64) Envelope {
	zero, input, output := uint64(0), uint64(3), uint64(7)
	write, fiveMinute, oneHour := uint64(9), uint64(4), uint64(5)
	bucket := Bucket{
		Start:  time.Date(2026, 11, 18, 12, minute, 0, 0, time.UTC),
		Source: "claude", Surface: "cli", ObservedModelID: "synthetic-model",
		ModelEvidence: "reported", Measurement: "reported", NormalizationVersion: "1",
		Tokens: Tokens{
			InputUncached:     Value{Value: &input, State: "complete"},
			InputCacheRead:    Value{Value: &zero, State: "complete"},
			InputCacheWrite:   Value{Value: &write, State: "complete"},
			InputCacheWrite5m: Value{Value: &fiveMinute, State: "complete"},
			InputCacheWrite1h: Value{Value: &oneHour, State: "complete"},
			Output:            Value{Value: &output, State: "complete"},
			ReasoningOutput:   Value{State: "unavailable"},
		},
		Revision: revision,
	}
	return Envelope{BucketKey: bucketKey(bucket), Bucket: bucket}
}

// A bucket without pricing evidence must keep the identity and bytes it had
// before the evidence fields existed, or the server would see every stored
// bucket as changed. The expected values were computed by the previous
// release of this package.
func TestBucketWithoutPricingEvidenceKeepsItsIdentity(t *testing.T) {
	envelope := sampleEnvelope(1, 7)
	encoded, hash, err := CanonicalEnvelope(envelope)
	if err != nil {
		t.Fatal(err)
	}
	const (
		wantKey  = "c2759133b96430065ed666ad7f77531790deb3464a38d8f472ecb7ba63878ab7"
		wantHash = "2468ab3c993dd71b3316a2feedb61315dc435f7d00651fb8e5f016a2b615ba4b"
	)
	if envelope.BucketKey != wantKey || hex.EncodeToString(hash[:]) != wantHash {
		t.Fatalf("key/payload hash = %s/%x, want %s/%s", envelope.BucketKey, hash, wantKey, wantHash)
	}
	if strings.Contains(string(encoded), "service_tier") || strings.Contains(string(encoded), "prompt_band") {
		t.Fatalf("absent evidence was serialized: %s", encoded)
	}
}

func TestPricingEvidenceIsValidatedIdentityAndWireContent(t *testing.T) {
	rekey := func(value *Envelope) { value.BucketKey = bucketKey(value.Bucket) }
	plain := sampleEnvelope(1, 7)
	tests := []struct {
		name    string
		mutate  func(*Envelope)
		wantErr bool
		// wantJSON must appear in the encoded bucket when the envelope is valid.
		wantJSON string
	}{
		{name: "service tier", mutate: func(v *Envelope) { v.Bucket.ServiceTier = "priority"; rekey(v) }, wantJSON: `"normalization_version":"1","service_tier":"priority","tokens":`},
		{name: "prompt band", mutate: func(v *Envelope) { v.Bucket.PromptBand = "gt_272k"; rekey(v) }, wantJSON: `"normalization_version":"1","prompt_band":"gt_272k","tokens":`},
		{name: "both", mutate: func(v *Envelope) { v.Bucket.ServiceTier, v.Bucket.PromptBand = "flex", "le_100k"; rekey(v) }, wantJSON: `"service_tier":"flex","prompt_band":"le_100k","tokens":`},
		{name: "evidence added without rekeying", mutate: func(v *Envelope) { v.Bucket.ServiceTier = "priority" }, wantErr: true},
		{name: "tier this protocol does not name", mutate: func(v *Envelope) { v.Bucket.ServiceTier = "turbo"; rekey(v) }, wantErr: true},
		{name: "band off the ladder", mutate: func(v *Envelope) { v.Bucket.PromptBand = "le_1m"; rekey(v) }, wantErr: true},
		{name: "a prompt size instead of a band", mutate: func(v *Envelope) { v.Bucket.PromptBand = "280000"; rekey(v) }, wantErr: true},
	}
	seen := map[string]string{plain.BucketKey: "no evidence"}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			envelope := sampleEnvelope(1, 7)
			test.mutate(&envelope)
			encoded, _, err := CanonicalEnvelope(envelope)
			if (err != nil) != test.wantErr {
				t.Fatalf("CanonicalEnvelope() error = %v, wantErr %t", err, test.wantErr)
			}
			if test.wantErr {
				return
			}
			if !strings.Contains(string(encoded), test.wantJSON) {
				t.Fatalf("encoded bucket = %s, want it to contain %s", encoded, test.wantJSON)
			}
			if other, taken := seen[envelope.BucketKey]; taken {
				t.Fatalf("evidence %q shares a bucket key with %q", test.name, other)
			}
			seen[envelope.BucketKey] = test.name
		})
	}
}

// The local store computes bucket keys on its own. BuildBatch recomputes each
// key from the payload, so buckets written by the store prove both agree for
// every combination of pricing evidence.
func TestStoreAndProtocolAgreeOnBucketKeys(t *testing.T) {
	database, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "tokenholic.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close(context.Background()) })
	output := uint64(7)
	zero := store.AggregateValue{Value: new(uint64), State: store.AvailabilityComplete}
	var buckets []store.UsageBucket
	for _, tier := range []normalize.ServiceTier{"", normalize.ServiceTierStandard, normalize.ServiceTierPriority, normalize.ServiceTierFlex, normalize.ServiceTierBatch, normalize.ServiceTierFast} {
		for _, band := range []normalize.PromptBand{"", normalize.PromptBandUpTo100K, normalize.PromptBandUpTo200K, normalize.PromptBandUpTo272K, normalize.PromptBandOver272K} {
			buckets = append(buckets, store.UsageBucket{
				Start: time.Date(2026, 11, 18, 12, 1, 0, 0, time.UTC), Source: normalize.SourceCodex, Surface: normalize.SurfaceCLI,
				ObservedModelID: "synthetic-model", ModelEvidence: normalize.ModelInferred, Measurement: normalize.MeasurementReported,
				NormalizationVersion: "1", ServiceTier: tier, PromptBand: band,
				Tokens: store.TokenBreakdown{
					InputUncached: zero, InputCacheRead: zero, InputCacheWrite: store.AggregateValue{State: store.AvailabilityUnavailable},
					InputCacheWrite5m: store.AggregateValue{State: store.AvailabilityUnavailable},
					InputCacheWrite1h: store.AggregateValue{State: store.AvailabilityUnavailable},
					Output:            store.AggregateValue{Value: &output, State: store.AvailabilityComplete},
					ReasoningOutput:   store.AggregateValue{State: store.AvailabilityUnavailable},
				},
				Preview:  store.Preview{Resolution: normalize.ModelUnknown, APIEstimate: store.AggregateValue{State: store.AvailabilityUnavailable}},
				Revision: 1,
			})
		}
	}
	if result, err := database.ApplyBucketRevisions(t.Context(), buckets); err != nil || result.Applied != len(buckets) {
		t.Fatalf("ApplyBucketRevisions() = %#v, %v; want %d distinct buckets", result, err, len(buckets))
	}
	pending, err := database.PendingOutbox(t.Context())
	if err != nil || len(pending) != len(buckets) {
		t.Fatalf("PendingOutbox() = %d, %v; want %d", len(pending), err, len(buckets))
	}
	batch, _, err := BuildBatch(pending)
	if err != nil {
		t.Fatalf("BuildBatch() rejected store buckets: %v", err)
	}
	if err := batch.Validate(); err != nil {
		t.Fatal(err)
	}
}
