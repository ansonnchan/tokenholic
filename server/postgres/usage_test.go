package postgres

import (
	"context"
	"crypto/sha256"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/ansonnchan/tokenholic/internal/modelcatalog"
	"github.com/ansonnchan/tokenholic/internal/normalize"
	"github.com/ansonnchan/tokenholic/internal/store"
	protocol "github.com/ansonnchan/tokenholic/internal/sync"
	serverauth "github.com/ansonnchan/tokenholic/server/auth"
	serverpricing "github.com/ansonnchan/tokenholic/server/pricing"
	serversync "github.com/ansonnchan/tokenholic/server/sync"
)

func TestPostgresUsageRevisionLifecycle(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	pricing, err := modelcatalog.DefaultPricingRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if err := EnsurePricingRevision(ctx, pool, pricing); err != nil {
		t.Fatal(err)
	}
	credentialHash := sha256.Sum256([]byte("synthetic-sync-secret"))
	authRepository, err := NewAuthRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := authRepository.RegisterDevice(ctx, serverauth.GitHubUser{ID: 601, Login: "sync-user"}, serverauth.DeviceRegistration{
		UserID: "20000000-0000-4000-8000-000000000001", DeviceID: "20000000-0000-4000-8000-000000000011", ClientVersion: "test",
		Credential: serverauth.NewCredential{CredentialID: "20000000-0000-4000-8000-000000000021", SecretHash: credentialHash, ExpiresAt: time.Now().UTC().Add(time.Hour)},
	})
	if err != nil {
		t.Fatal(err)
	}
	engine := bundledEngine(t)
	envelope := localProtocolEnvelope(t, "synthetic-model")
	repository, err := NewUsageRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	principal := serverauth.Principal{UserID: identity.UserID, DeviceID: identity.DeviceID}
	acknowledgements, err := repository.ApplyUsageBuckets(ctx, principal, []serversync.Bucket{derived(t, engine, envelope)})
	if err != nil || len(acknowledgements) != 1 || acknowledgements[0].Status != protocol.AckApplied {
		t.Fatalf("first apply = %#v, %v", acknowledgements, err)
	}
	acknowledgements, err = repository.ApplyUsageBuckets(ctx, principal, []serversync.Bucket{derived(t, engine, envelope)})
	if err != nil || acknowledgements[0].Status != protocol.AckUnchanged {
		t.Fatalf("repeat apply = %#v, %v", acknowledgements, err)
	}
	newer := envelope
	newer.Bucket.Revision = 2
	newOutput := uint64(15)
	newer.Bucket.Tokens.Output.Value = &newOutput
	acknowledgements, err = repository.ApplyUsageBuckets(ctx, principal, []serversync.Bucket{derived(t, engine, newer)})
	if err != nil || acknowledgements[0].Status != protocol.AckApplied {
		t.Fatalf("newer apply = %#v, %v", acknowledgements, err)
	}
	acknowledgements, err = repository.ApplyUsageBuckets(ctx, principal, []serversync.Bucket{derived(t, engine, envelope)})
	if err != nil || acknowledgements[0].Status != protocol.AckStale {
		t.Fatalf("stale apply = %#v, %v", acknowledgements, err)
	}
	conflict := newer
	conflictingOutput := uint64(16)
	conflict.Bucket.Tokens.Output.Value = &conflictingOutput
	if _, err := repository.ApplyUsageBuckets(ctx, principal, []serversync.Bucket{derived(t, engine, conflict)}); !errors.Is(err, serversync.ErrRevisionConflict) {
		t.Fatalf("equal revision conflict error = %v", err)
	}
	var storedOutput, storedRevision string
	if err := pool.QueryRow(ctx, `SELECT output_tokens::text, revision::text FROM usage_buckets WHERE device_id = $1::uuid AND bucket_key = $2`, principal.DeviceID, envelope.BucketKey).Scan(&storedOutput, &storedRevision); err != nil {
		t.Fatal(err)
	}
	if storedOutput != "15" || storedRevision != "2" {
		t.Fatalf("stored output/revision = %q/%q", storedOutput, storedRevision)
	}
	if err := authRepository.RevokeDevice(ctx, serverauth.CredentialProof{
		CredentialID: "20000000-0000-4000-8000-000000000021", SecretHash: credentialHash,
	}); err != nil {
		t.Fatal(err)
	}
	newer.Bucket.Revision = 3
	if _, err := repository.ApplyUsageBuckets(ctx, principal, []serversync.Bucket{derived(t, engine, newer)}); !errors.Is(err, serverauth.ErrUnauthorized) {
		t.Fatalf("revoked device upload error = %v", err)
	}
}

// localProtocolEnvelope builds an upload exactly as a client does: a local
// bucket passes through the store and the outbox serializer.
func localProtocolEnvelope(t *testing.T, model string) protocol.Envelope {
	t.Helper()
	database, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "sync.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close(context.Background()) })
	zero, input, output := uint64(0), uint64(3), uint64(7)
	complete := func(value *uint64) store.AggregateValue {
		return store.AggregateValue{Value: value, State: store.AvailabilityComplete}
	}
	bucket := store.UsageBucket{
		Start:  time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC),
		Source: normalize.SourceClaude, Surface: normalize.SurfaceCLI,
		ObservedModelID: model, ModelEvidence: normalize.ModelReported,
		Measurement: normalize.MeasurementReported, NormalizationVersion: "1",
		Tokens: store.TokenBreakdown{
			InputUncached: complete(&input), InputCacheRead: complete(&zero), InputCacheWrite: complete(&zero),
			InputCacheWrite5m: complete(&zero), InputCacheWrite1h: complete(&zero), Output: complete(&output),
			ReasoningOutput: store.AggregateValue{State: store.AvailabilityUnavailable},
		},
		Preview:  store.Preview{Resolution: normalize.ModelUnknown, APIEstimate: store.AggregateValue{State: store.AvailabilityUnavailable}},
		Revision: 1,
	}
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
	return batch.Buckets[0]
}

// bundledEngine is the server's authoritative pricer for the embedded catalog.
func bundledEngine(t testing.TB) *serverpricing.Engine {
	t.Helper()
	deriver, err := modelcatalog.BundledDeriver()
	if err != nil {
		t.Fatal(err)
	}
	engine, err := serverpricing.NewEngine(deriver)
	if err != nil {
		t.Fatal(err)
	}
	return engine
}

// derived pairs client evidence with what the given engine derives from it.
func derived(t testing.TB, engine *serverpricing.Engine, envelope protocol.Envelope) serversync.Bucket {
	t.Helper()
	values, err := engine.Derive(serverpricing.Usage{
		ObservedModelID: envelope.Bucket.ObservedModelID, ModelEvidence: envelope.Bucket.ModelEvidence, Tokens: envelope.Bucket.Tokens,
	})
	if err != nil {
		t.Fatal(err)
	}
	return serversync.Bucket{Envelope: envelope, Derived: values}
}
