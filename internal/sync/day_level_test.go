package sync

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ansonnchan/tokenholic/internal/normalize"
	"github.com/ansonnchan/tokenholic/internal/store"
)

func dayEnvelope(source string, day time.Time, revision uint64) Envelope {
	input, output := uint64(300), uint64(40)
	bucket := Bucket{
		Start: day, Source: source, Surface: "cli", ObservedModelID: "synthetic-model",
		ModelEvidence: "reported", Measurement: "reported", NormalizationVersion: "1", Granularity: "day",
		Tokens: Tokens{
			InputUncached:     Value{Value: &input, State: "complete"},
			InputCacheRead:    Value{State: "unavailable"},
			InputCacheWrite:   Value{State: "unavailable"},
			InputCacheWrite5m: Value{State: "unavailable"},
			InputCacheWrite1h: Value{State: "unavailable"},
			Output:            Value{Value: &output, State: "complete"},
			ReasoningOutput:   Value{State: "unavailable"},
		},
		Revision: revision,
	}
	return Envelope{BucketKey: bucketKey(bucket), Bucket: bucket}
}

func TestProtocolAdmitsDayLevelSourcesOnlyAsDays(t *testing.T) {
	day := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	rekey := func(value *Envelope) { value.BucketKey = bucketKey(value.Bucket) }
	tests := []struct {
		name    string
		build   func() Envelope
		wantErr bool
	}{
		{name: "a day of Copilot", build: func() Envelope { return dayEnvelope("copilot", day, 1) }},
		{name: "a day of Gemini", build: func() Envelope { return dayEnvelope("gemini", day, 1) }},
		{name: "a day of Grok", build: func() Envelope { return dayEnvelope("grok", day, 1) }},
		{name: "a day of OpenCode", build: func() Envelope { return dayEnvelope("opencode", day, 1) }},
		{name: "a day of Qwen", build: func() Envelope { return dayEnvelope("qwen", day, 1) }},
		{name: "a day with no model named", build: func() Envelope {
			value := dayEnvelope("gemini", day, 1)
			value.Bucket.ObservedModelID, value.Bucket.ModelEvidence = "", "unknown"
			rekey(&value)
			return value
		}},
		{name: "a day of Claude", build: func() Envelope { return dayEnvelope("claude", day, 1) }, wantErr: true},
		{name: "a day of Codex", build: func() Envelope { return dayEnvelope("codex", day, 1) }, wantErr: true},
		{name: "a source nobody admitted", build: func() Envelope { return dayEnvelope("cursor", day, 1) }, wantErr: true},
		{name: "a minute of Gemini", build: func() Envelope {
			value := dayEnvelope("gemini", day, 1)
			value.Bucket.Granularity = ""
			rekey(&value)
			return value
		}, wantErr: true},
		{name: "a Gemini desktop surface", build: func() Envelope {
			value := dayEnvelope("gemini", day, 1)
			value.Bucket.Surface = "desktop"
			rekey(&value)
			return value
		}, wantErr: true},
		{name: "a day filed inside the day", build: func() Envelope { return dayEnvelope("gemini", day.Add(9*time.Hour), 1) }, wantErr: true},
		{name: "a day with a service tier", build: func() Envelope {
			value := dayEnvelope("gemini", day, 1)
			value.Bucket.ServiceTier = "standard"
			rekey(&value)
			return value
		}, wantErr: true},
		{name: "a day with a prompt band", build: func() Envelope {
			value := dayEnvelope("gemini", day, 1)
			value.Bucket.PromptBand = "le_100k"
			rekey(&value)
			return value
		}, wantErr: true},
		{name: "a week", build: func() Envelope {
			value := dayEnvelope("gemini", day, 1)
			value.Bucket.Granularity = "week"
			rekey(&value)
			return value
		}, wantErr: true},
		{name: "granularity added without rekeying", build: func() Envelope {
			value := dayEnvelope("gemini", day, 1)
			minute := value.Bucket
			minute.Granularity = ""
			value.BucketKey = bucketKey(minute)
			return value
		}, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			envelope := test.build()
			encoded, _, err := CanonicalEnvelope(envelope)
			if (err != nil) != test.wantErr {
				t.Fatalf("CanonicalEnvelope() error = %v, wantErr %t", err, test.wantErr)
			}
			if err == nil && !strings.Contains(string(encoded), `"normalization_version":"1","granularity":"day","tokens":`) {
				t.Fatalf("encoded bucket = %s, want the granularity on the wire", encoded)
			}
			batch := Batch{SchemaVersion: SchemaVersion, Buckets: []Envelope{envelope}}
			if err := batch.Validate(); (err != nil) != test.wantErr {
				t.Fatalf("Batch.Validate() error = %v, wantErr %t", err, test.wantErr)
			}
		})
	}
	// The same day, model, and counts from two agents are two buckets.
	if dayEnvelope("gemini", day, 1).BucketKey == dayEnvelope("qwen", day, 1).BucketKey {
		t.Fatal("two agents share a bucket key")
	}
	// A minute bucket is untouched by the new field.
	if encoded, _, err := CanonicalEnvelope(sampleEnvelope(1, 7)); err != nil || strings.Contains(string(encoded), "granularity") {
		t.Fatalf("a minute bucket serialized a granularity: %s, %v", encoded, err)
	}
}

// Day-level buckets written by the store must satisfy the protocol, and what
// show-data prints for them must be exactly what an upload sends, across a
// day that grows between two syncs.
func TestDayLevelBucketsUploadExactlyWhatShowDataPrints(t *testing.T) {
	database, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "tokenholic.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close(context.Background()) })
	day := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	now := time.Date(2026, 1, 5, 12, 0, 0, 0, time.UTC)
	commit := func(input, output uint64) {
		t.Helper()
		var batches []store.ObservationBatch
		for _, source := range normalize.DayLevelSources() {
			identity := store.NewSourceIdentity(source, normalize.SurfaceCLI, "synthetic-"+string(source))
			batches = append(batches, store.ObservationBatch{
				Observations: []normalize.Observation{{
					Source: source, Surface: normalize.SurfaceCLI, SourceVersion: "synthetic-v1",
					SourceRecordID: "day|" + string(source) + "|2026-01-02|model-synthetic", OccurredAt: day,
					ObservedModelID: "model-synthetic", ModelResolution: normalize.ModelReported,
					Tokens:         normalize.TokenCounts{InputUncached: &input, Output: &output},
					CountSemantics: normalize.CountDayTotal, Measurement: normalize.MeasurementReported,
					AdapterVersion: "adapter-v1", NormalizationVersion: normalize.SchemaVersion,
				}},
				Checkpoint: store.Checkpoint{Source: identity, Cursor: 1, CollectedAt: now},
				Health: store.AdapterHealth{Source: identity, AdapterVersion: "adapter-v1", CheckedAt: now, Health: normalize.SourceHealth{
					Source: source, Surface: normalize.SurfaceCLI, Status: normalize.HealthDetected,
					Capabilities: normalize.Capabilities{
						Model: normalize.CapabilityReported, InputUncached: normalize.CapabilityReported,
						InputCacheRead: normalize.CapabilityUnsupported, InputCacheWrite: normalize.CapabilityUnsupported,
						Output: normalize.CapabilityReported, ReasoningOutput: normalize.CapabilityUnsupported,
					},
				}},
			})
		}
		if _, err := database.CommitScan(t.Context(), batches); err != nil {
			t.Fatal(err)
		}
		revision, err := database.NextUsageBucketRevision(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := database.RebuildUsageBuckets(t.Context(), revision, nil); err != nil {
			t.Fatal(err)
		}
	}
	var uploaded []string
	server := map[string]Bucket{}
	transport := syncRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatal(err)
		}
		uploaded = append(uploaded, string(body))
		var batch Batch
		if err := json.Unmarshal(body, &batch); err != nil {
			t.Fatal(err)
		}
		if err := batch.Validate(); err != nil {
			t.Fatalf("the upload does not satisfy the protocol: %v", err)
		}
		response := BatchResponse{}
		for _, envelope := range batch.Buckets {
			// The server keeps one absolute value per key: the newest.
			server[envelope.BucketKey] = envelope.Bucket
			response.Acknowledgements = append(response.Acknowledgements, Acknowledgement{
				BucketKey: envelope.BucketKey, Revision: envelope.Bucket.Revision, Status: AckApplied,
			})
		}
		encoded, _ := json.Marshal(response)
		return syncResponse(request, http.StatusOK, string(encoded), nil), nil
	})
	client, err := NewClient("https://api.example.test", &http.Client{Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	serverTotal := func() uint64 {
		var total uint64
		for _, bucket := range server {
			total += *bucket.Tokens.InputUncached.Value + *bucket.Tokens.Output.Value
		}
		return total
	}
	for round, reading := range [][2]uint64{{100, 10}, {100, 10}, {450, 60}} {
		commit(reading[0], reading[1])
		var preview strings.Builder
		if _, err := DryRun(t.Context(), database, &preview); err != nil {
			t.Fatal(err)
		}
		before := len(uploaded)
		if _, err := client.Run(t.Context(), database, "device-credential"); err != nil {
			t.Fatal(err)
		}
		sent := strings.Join(uploaded[before:], "\n")
		if sent != "" {
			sent += "\n"
		}
		if preview.String() != sent {
			t.Fatalf("round %d: show-data differs from the upload:\npreview:  %s\nuploaded: %s", round, preview.String(), sent)
		}
		// The second reading is unchanged, so nothing is sent for it.
		if (round == 1) != (sent == "") {
			t.Fatalf("round %d sent %q", round, sent)
		}
		if round != 1 && strings.Count(sent, `"granularity":"day"`) != len(normalize.DayLevelSources()) {
			t.Fatalf("round %d upload does not mark every bucket as a day: %s", round, sent)
		}
		// Repeated sync never adds: the server holds each day once.
		if want := uint64(len(normalize.DayLevelSources())) * (reading[0] + reading[1]); serverTotal() != want || len(server) != len(normalize.DayLevelSources()) {
			t.Fatalf("round %d: server holds %d tokens in %d buckets, want %d in %d", round, serverTotal(), len(server), want, len(normalize.DayLevelSources()))
		}
	}
}
