package sync

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/ansonnchan/tokenholic/internal/modelcatalog"
	"github.com/ansonnchan/tokenholic/internal/normalize"
	"github.com/ansonnchan/tokenholic/internal/store"
	protocol "github.com/ansonnchan/tokenholic/internal/sync"
	serverauth "github.com/ansonnchan/tokenholic/server/auth"
	"github.com/ansonnchan/tokenholic/server/pricing"
)

// The synthetic server prices "model-a" at $2 input, $0.10 cache read, $2.50
// base cache write, $4 one-hour cache write, and $10 output per million.
func TestApplyDerivesModelAndPriceOnTheServer(t *testing.T) {
	service, repository := testService(t)
	batch := batchFor(t, "model-a", "reported")

	response, err := service.Apply(t.Context(), "credential", batch)
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Acknowledgements) != 1 || len(repository.buckets) != 1 {
		t.Fatalf("response/repository = %#v/%#v", response, repository.buckets)
	}
	stored := repository.buckets[0]
	// One million tokens each: 2 + 0.10 + 2.50 (5m) + 4 (1h) + 10 = $18.60.
	derived := stored.Derived
	if derived.EstimateMicroUSD == nil || *derived.EstimateMicroUSD != 18_600_000 || derived.EstimateState != "complete" {
		t.Fatalf("server estimate = %v/%q", derived.EstimateMicroUSD, derived.EstimateState)
	}
	if derived.CanonicalModelID == nil || *derived.CanonicalModelID != "model-a" || derived.ModelMatch != "exact" ||
		derived.PricedTokens != 5_000_000 || derived.UnpricedTokens != 0 || derived.PricingRevision != "server-r2" {
		t.Fatalf("derived = %#v", derived)
	}
	// What is stored as evidence is exactly what the client sent.
	sent, _ := json.Marshal(batch.Buckets[0])
	kept, _ := json.Marshal(stored.Envelope)
	if string(sent) != string(kept) {
		t.Fatalf("stored evidence differs from the upload:\nsent %s\nkept %s", sent, kept)
	}
}

// A client never tells the server what a model is, so a client built against
// an older or newer catalog uploads the same bytes and is always accepted.
func TestApplyAcceptsModelsRegardlessOfTheClientCatalog(t *testing.T) {
	tests := []struct {
		name, observed, evidence string
		wantResolution           string
		wantReason               string
		wantPriced               bool
	}{
		{name: "model the server prices", observed: "model-a", evidence: "reported", wantResolution: "reported", wantPriced: true},
		{name: "alias only the server resolves", observed: "model-a[1m]", evidence: "reported", wantResolution: "resolved", wantPriced: true},
		{name: "model newer than the server catalog", observed: "model-released-tomorrow", evidence: "reported", wantResolution: "unknown", wantReason: modelcatalog.ReasonUnknownModel},
		{name: "inferred model the server does not know", observed: "model-other", evidence: "inferred", wantResolution: "unknown", wantReason: modelcatalog.ReasonUnknownModel},
		{name: "routing alias", observed: "auto", evidence: "reported", wantResolution: "unknown", wantReason: modelcatalog.ReasonAmbiguousAlias},
		{name: "no model", observed: "", evidence: "unknown", wantResolution: "unknown", wantReason: modelcatalog.ReasonModelUnavailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service, repository := testService(t)
			if _, err := service.Apply(t.Context(), "credential", batchFor(t, test.observed, test.evidence)); err != nil {
				t.Fatalf("Apply() error = %v; a catalog difference must never reject an upload", err)
			}
			derived := repository.buckets[0].Derived
			reason := ""
			if derived.UnpricedReason != nil {
				reason = *derived.UnpricedReason
			}
			if derived.ModelResolution != test.wantResolution || reason != test.wantReason || (derived.EstimateMicroUSD != nil) != test.wantPriced {
				t.Fatalf("derived = %#v (reason %q)", derived, reason)
			}
			// Unpriced tokens are kept and counted, never dropped.
			if derived.PricedTokens+derived.UnpricedTokens != 5_000_000 {
				t.Fatalf("priced+unpriced = %d+%d, want every uploaded token", derived.PricedTokens, derived.UnpricedTokens)
			}
		})
	}
}

func TestApplyRejectsProtocolViolationsBeforeStorage(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*protocol.Batch)
	}{
		{name: "retired schema version", mutate: func(batch *protocol.Batch) { batch.SchemaVersion = 1 }},
		{name: "empty batch", mutate: func(batch *protocol.Batch) { batch.Buckets = nil }},
		{name: "duplicate bucket", mutate: func(batch *protocol.Batch) { batch.Buckets = append(batch.Buckets, batch.Buckets[0]) }},
		{name: "key does not match evidence", mutate: func(batch *protocol.Batch) { batch.Buckets[0].Bucket.ObservedModelID = "model-b" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service, repository := testService(t)
			batch := batchFor(t, "model-a", "reported")
			test.mutate(&batch)
			if _, err := service.Apply(t.Context(), "credential", batch); !errors.Is(err, ErrInvalidBatch) {
				t.Fatalf("Apply() error = %v, want ErrInvalidBatch", err)
			}
			if repository.called {
				t.Fatal("repository received an invalid batch")
			}
		})
	}
}

func TestApplyPassesThroughAuthenticationFailure(t *testing.T) {
	repository := &fakeRepository{}
	service, err := NewService(&fakeAuthenticator{err: serverauth.ErrUnauthorized}, repository, serverPricer(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Apply(t.Context(), "credential", batchFor(t, "model-a", "reported")); !errors.Is(err, serverauth.ErrUnauthorized) {
		t.Fatalf("Apply() error = %v", err)
	}
	if repository.called {
		t.Fatal("repository was reached without authentication")
	}
}

func testService(t *testing.T) (*Service, *fakeRepository) {
	t.Helper()
	repository := &fakeRepository{}
	service, err := NewService(&fakeAuthenticator{}, repository, serverPricer(t))
	if err != nil {
		t.Fatal(err)
	}
	return service, repository
}

// serverPricer is deliberately a different revision from anything a client
// in these tests could hold.
func serverPricer(t *testing.T) *pricing.Engine {
	t.Helper()
	rate := func(v uint64) *uint64 { return &v }
	registry, err := modelcatalog.NewPricingRegistry("server-r2", []modelcatalog.Price{{
		Key: "provider/model-a", Provider: "provider",
		Rate: modelcatalog.Rate{
			InputUncached: rate(2_000_000), InputCacheRead: rate(100_000), InputCacheWrite: rate(2_500_000),
			InputCacheWrite1h: rate(4_000_000), Output: rate(10_000_000),
		},
		SourceURL: "https://provider.invalid/pricing", SourceRevision: "synthetic", EffectiveDate: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}})
	if err != nil {
		t.Fatal(err)
	}
	deriver, err := modelcatalog.NewDeriver(modelcatalog.Catalog{
		Revision:         "server-r2",
		AmbiguousAliases: []string{"auto"},
		AliasRules:       modelcatalog.AliasRules{ContextMarkers: []string{"[1m]"}},
		Models:           []modelcatalog.Model{{CanonicalID: "model-a", Provider: "provider", PricingKey: "provider/model-a"}},
	}, registry)
	if err != nil {
		t.Fatal(err)
	}
	engine, err := pricing.NewEngine(deriver)
	if err != nil {
		t.Fatal(err)
	}
	return engine
}

// batchFor builds the request exactly as a client does: a local bucket goes
// through the outbox serializer. The client in these tests has no catalog at
// all, which is the strongest form of "an older catalog".
func batchFor(t *testing.T, observed, evidence string) protocol.Batch {
	t.Helper()
	million := uint64(1_000_000)
	twoMillion := 2 * million
	complete := store.AggregateValue{Value: &million, State: store.AvailabilityComplete}
	bucket := store.UsageBucket{
		Start: time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC), Source: "codex", Surface: "cli",
		ObservedModelID: observed, ModelEvidence: normalize.ModelResolution(evidence), Measurement: "reported", NormalizationVersion: "1",
		Tokens: store.TokenBreakdown{
			InputUncached: complete, InputCacheRead: complete,
			InputCacheWrite:   store.AggregateValue{Value: &twoMillion, State: store.AvailabilityComplete},
			InputCacheWrite5m: complete, InputCacheWrite1h: complete, Output: complete,
			ReasoningOutput: store.AggregateValue{State: store.AvailabilityUnavailable},
		},
		Preview:  store.Preview{Resolution: "unknown", APIEstimate: store.AggregateValue{State: store.AvailabilityUnavailable}},
		Revision: 1,
	}
	database, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "client.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close(context.Background()) })
	if _, err := database.ApplyBucketRevisions(t.Context(), []store.UsageBucket{bucket}); err != nil {
		t.Fatal(err)
	}
	records, err := database.PendingOutbox(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	batch, _, err := protocol.BuildBatch(records)
	if err != nil {
		t.Fatal(err)
	}
	return batch
}

type fakeAuthenticator struct{ err error }

func (f *fakeAuthenticator) Authenticate(context.Context, string) (serverauth.Principal, error) {
	return serverauth.Principal{UserID: "user", DeviceID: "device"}, f.err
}

type fakeRepository struct {
	called  bool
	buckets []Bucket
}

func (f *fakeRepository) ApplyUsageBuckets(_ context.Context, _ serverauth.Principal, buckets []Bucket) ([]protocol.Acknowledgement, error) {
	f.called = true
	f.buckets = append([]Bucket(nil), buckets...)
	acknowledgements := make([]protocol.Acknowledgement, 0, len(buckets))
	for _, bucket := range buckets {
		acknowledgements = append(acknowledgements, protocol.Acknowledgement{
			BucketKey: bucket.Envelope.BucketKey, Revision: bucket.Envelope.Bucket.Revision, Status: protocol.AckApplied,
		})
	}
	return acknowledgements, nil
}

// dayLevelBatch builds one bucket through the local store, as a client does.
func dayLevelBatch(t *testing.T, source normalize.Source, granularity normalize.Granularity, start time.Time) (protocol.Batch, error) {
	t.Helper()
	tokens := uint64(1_000_000)
	unavailable := store.AggregateValue{State: store.AvailabilityUnavailable}
	bucket := store.UsageBucket{
		Start: start, Source: source, Surface: "cli", ObservedModelID: "model-a", ModelEvidence: "reported",
		Measurement: "reported", NormalizationVersion: "1", Granularity: granularity,
		Tokens: store.TokenBreakdown{
			InputUncached:  store.AggregateValue{Value: &tokens, State: store.AvailabilityComplete},
			InputCacheRead: unavailable, InputCacheWrite: unavailable, InputCacheWrite5m: unavailable,
			InputCacheWrite1h: unavailable, Output: unavailable, ReasoningOutput: unavailable,
		},
		Preview:  store.Preview{Resolution: "unknown", APIEstimate: unavailable},
		Revision: 1,
	}
	database, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "client.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close(context.Background()) })
	if _, err := database.ApplyBucketRevisions(t.Context(), []store.UsageBucket{bucket}); err != nil {
		return protocol.Batch{}, err
	}
	records, err := database.PendingOutbox(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	batch, _, err := protocol.BuildBatch(records)
	return batch, err
}

func TestApplyAdmitsDayLevelSourcesAsDaysOnly(t *testing.T) {
	day := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	for _, source := range normalize.DayLevelSources() {
		t.Run(string(source), func(t *testing.T) {
			service, repository := testService(t)
			batch, err := dayLevelBatch(t, source, normalize.GranularityDay, day)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := service.Apply(t.Context(), "credential", batch); err != nil {
				t.Fatalf("Apply() = %v", err)
			}
			if !repository.called || len(repository.buckets) != 1 || repository.buckets[0].Envelope.Bucket.Granularity != "day" {
				t.Fatalf("stored buckets = %+v", repository.buckets)
			}
			// The server prices a day from its own catalog like any bucket.
			if derived := repository.buckets[0].Derived; derived.PricingRevision == "" || derived.PricedTokens+derived.UnpricedTokens != 1_000_000 {
				t.Fatalf("derived = %+v", derived)
			}
		})
	}

	// A hand-built request cannot smuggle a day in for a native source, or a
	// finer value in for a day-level one: the client store refuses to build
	// either, and the server refuses to accept either.
	valid, err := dayLevelBatch(t, normalize.SourceGemini, normalize.GranularityDay, day)
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*protocol.Bucket){
		"a day of Claude":            func(bucket *protocol.Bucket) { bucket.Source = "claude" },
		"a day of Codex":             func(bucket *protocol.Bucket) { bucket.Source = "codex" },
		"a minute of Gemini":         func(bucket *protocol.Bucket) { bucket.Granularity = "" },
		"a day filed inside the day": func(bucket *protocol.Bucket) { bucket.Start = bucket.Start.Add(time.Hour) },
		"an unknown source":          func(bucket *protocol.Bucket) { bucket.Source = "cursor" },
	} {
		t.Run(name, func(t *testing.T) {
			service, repository := testService(t)
			batch := protocol.Batch{SchemaVersion: valid.SchemaVersion, Buckets: []protocol.Envelope{valid.Buckets[0]}}
			mutate(&batch.Buckets[0].Bucket)
			if _, err := service.Apply(t.Context(), "credential", batch); !errors.Is(err, ErrInvalidBatch) {
				t.Fatalf("Apply() error = %v, want ErrInvalidBatch", err)
			}
			if repository.called {
				t.Fatal("repository received an invalid batch")
			}
		})
	}
	for name, build := range map[string]func() (protocol.Batch, error){
		"a day of Claude": func() (protocol.Batch, error) {
			return dayLevelBatch(t, normalize.SourceClaude, normalize.GranularityDay, day)
		},
		"a minute of Gemini": func() (protocol.Batch, error) {
			return dayLevelBatch(t, normalize.SourceGemini, normalize.GranularityMinute, day)
		},
	} {
		if _, err := build(); err == nil {
			t.Fatalf("the client store built %s", name)
		}
	}
}
