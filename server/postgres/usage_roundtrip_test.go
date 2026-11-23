package postgres

import (
	"context"
	"errors"
	"net"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ansonnchan/tokenholic/internal/modelcatalog"
	"github.com/ansonnchan/tokenholic/internal/normalize"
	"github.com/ansonnchan/tokenholic/internal/store"
	protocol "github.com/ansonnchan/tokenholic/internal/sync"
	serverauth "github.com/ansonnchan/tokenholic/server/auth"
	serversync "github.com/ansonnchan/tokenholic/server/sync"
	"github.com/jackc/pgx/v5/pgxpool"
)

// The hosted API and its database are a network hop apart, so what a batch
// costs is how many times it waits for the database, not how much it sends.
// One wait per bucket made a full batch take seconds. The number of waits
// must not grow with the batch.
func TestPostgresUsageBatchRoundTripsDoNotGrowWithTheBatch(t *testing.T) {
	admin := integrationPool(t)
	ctx := t.Context()
	if err := Migrate(ctx, admin); err != nil {
		t.Fatal(err)
	}
	seedPricing(t, admin)
	principal := registerSyncDevice(t, admin, 905, "90500000-0000-4000-8000-0000000000")
	engine := bundledEngine(t)
	envelopes := localProtocolEnvelopes(t, 2*protocol.MaxBatchBuckets)
	derive := func(envelopes []protocol.Envelope) []serversync.Bucket {
		buckets := make([]serversync.Bucket, len(envelopes))
		for index, envelope := range envelopes {
			buckets[index] = derived(t, engine, envelope)
		}
		return buckets
	}
	pool, waits := meteredPool(t, admin, 0)
	repository, err := NewUsageRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	apply := func(name string, buckets []serversync.Bucket, want protocol.AckStatus) int64 {
		t.Helper()
		before := waits.Load()
		acknowledgements, err := repository.ApplyUsageBuckets(ctx, principal, buckets)
		if err != nil || len(acknowledgements) != len(buckets) {
			t.Fatalf("%s = %d acknowledgements, %v", name, len(acknowledgements), err)
		}
		for _, acknowledgement := range acknowledgements {
			if acknowledgement.Status != want {
				t.Fatalf("%s acknowledged %s, want %s", name, acknowledgement.Status, want)
			}
		}
		return waits.Load() - before
	}

	// The first call also prepares its statements, so it is not compared.
	apply("warm-up", derive(envelopes[:1]), protocol.AckApplied)
	few := apply("few new buckets", derive(envelopes[1:11]), protocol.AckApplied)
	full := apply("a full batch of new buckets", derive(envelopes[protocol.MaxBatchBuckets:]), protocol.AckApplied)
	repeat := apply("the same full batch again", derive(envelopes[protocol.MaxBatchBuckets:]), protocol.AckUnchanged)
	t.Logf("database round trips: %d for 10 new buckets, %d for %d new, %d for %d unchanged", few, full, protocol.MaxBatchBuckets, repeat, protocol.MaxBatchBuckets)
	if full != few {
		t.Errorf("a full batch waited on the database %d times and a small one %d: the count grows with the batch", full, few)
	}
	if repeat > full {
		t.Errorf("repeating a batch waited %d times, more than writing it (%d)", repeat, full)
	}
	var stored int
	if err := admin.QueryRow(ctx, `SELECT count(*) FROM usage_buckets WHERE device_id = $1::uuid`, principal.DeviceID).Scan(&stored); err != nil || stored != 1+10+protocol.MaxBatchBuckets {
		t.Fatalf("stored buckets = %d, %v", stored, err)
	}
}

// BenchmarkPostgresUsageBatch times one full batch of new buckets with the
// database next door and with it a network hop away.
func BenchmarkPostgresUsageBatch(b *testing.B) {
	for _, hop := range []time.Duration{0, 60 * time.Millisecond} {
		b.Run("round-trip="+hop.String(), func(b *testing.B) {
			admin := integrationPool(b)
			ctx := b.Context()
			if err := Migrate(ctx, admin); err != nil {
				b.Fatal(err)
			}
			seedPricing(b, admin)
			principal := registerSyncDevice(b, admin, 906, "90600000-0000-4000-8000-0000000000")
			engine := bundledEngine(b)
			envelopes := localProtocolEnvelopes(b, (b.N+1)*protocol.MaxBatchBuckets)
			pool, _ := meteredPool(b, admin, hop)
			repository, err := NewUsageRepository(pool)
			if err != nil {
				b.Fatal(err)
			}
			batch := func(index int) []serversync.Bucket {
				buckets := make([]serversync.Bucket, protocol.MaxBatchBuckets)
				for offset := range buckets {
					buckets[offset] = derived(b, engine, envelopes[index*protocol.MaxBatchBuckets+offset])
				}
				return buckets
			}
			// Prepare the statements outside the timed loop.
			if _, err := repository.ApplyUsageBuckets(ctx, principal, batch(b.N)); err != nil {
				b.Fatal(err)
			}
			b.ResetTimer()
			for index := 0; index < b.N; index++ {
				if _, err := repository.ApplyUsageBuckets(ctx, principal, batch(index)); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// seedPricing records the bundled pricing revision that derived values name.
func seedPricing(t testing.TB, pool *pgxpool.Pool) {
	t.Helper()
	pricing, err := modelcatalog.DefaultPricingRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if err := EnsurePricingRevision(t.Context(), pool, pricing); err != nil {
		t.Fatal(err)
	}
}

// meteredPool opens a second pool on the same schema whose connections count
// each wait for the database, and can make every wait take as long as a
// network hop would. A wait is the first read after something was written.
func meteredPool(t testing.TB, existing *pgxpool.Pool, hop time.Duration) (*pgxpool.Pool, *atomic.Int64) {
	t.Helper()
	waits := &atomic.Int64{}
	config := existing.Config()
	// One connection, so the statements one call prepares are there for the next.
	config.MaxConns = 1
	dialer := &net.Dialer{}
	config.ConnConfig.DialFunc = func(ctx context.Context, network, address string) (net.Conn, error) {
		connection, err := dialer.DialContext(ctx, network, address)
		if err != nil {
			return nil, err
		}
		return &meteredConn{Conn: connection, waits: waits, hop: hop}, nil
	}
	pool, err := pgxpool.NewWithConfig(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := pool.Ping(t.Context()); err != nil {
		t.Fatal(err)
	}
	return pool, waits
}

type meteredConn struct {
	net.Conn
	waits *atomic.Int64
	hop   time.Duration
	// wrote is set by a write and cleared by the read that waits on it.
	wrote atomic.Bool
}

func (c *meteredConn) Write(payload []byte) (int, error) {
	c.wrote.Store(true)
	return c.Conn.Write(payload)
}

func (c *meteredConn) Read(buffer []byte) (int, error) {
	if c.wrote.Swap(false) {
		c.waits.Add(1)
		time.Sleep(c.hop)
	}
	return c.Conn.Read(buffer)
}

// localProtocolEnvelopes builds count distinct minute buckets the way a
// client does: through the local store and its outbox.
func localProtocolEnvelopes(t testing.TB, count int) []protocol.Envelope {
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
	buckets := make([]store.UsageBucket, count)
	for index := range buckets {
		buckets[index] = store.UsageBucket{
			Start:  time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC).Add(time.Duration(index) * time.Minute),
			Source: normalize.SourceClaude, Surface: normalize.SurfaceCLI,
			ObservedModelID: "synthetic-model", ModelEvidence: normalize.ModelReported,
			Measurement: normalize.MeasurementReported, NormalizationVersion: "1",
			Tokens: store.TokenBreakdown{
				InputUncached: complete(&input), InputCacheRead: complete(&zero), InputCacheWrite: complete(&zero),
				InputCacheWrite5m: complete(&zero), InputCacheWrite1h: complete(&zero), Output: complete(&output),
				ReasoningOutput: store.AggregateValue{State: store.AvailabilityUnavailable},
			},
			Preview:  store.Preview{Resolution: normalize.ModelUnknown, APIEstimate: store.AggregateValue{State: store.AvailabilityUnavailable}},
			Revision: 1,
		}
	}
	if _, err := database.ApplyBucketRevisions(t.Context(), buckets); err != nil {
		t.Fatal(err)
	}
	records, err := database.PendingOutbox(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	envelopes := make([]protocol.Envelope, 0, len(records))
	for offset := 0; offset < len(records); offset += protocol.MaxBatchBuckets {
		batch, _, err := protocol.BuildBatch(records[offset:min(offset+protocol.MaxBatchBuckets, len(records))])
		if err != nil {
			t.Fatal(err)
		}
		envelopes = append(envelopes, batch.Buckets...)
	}
	if len(envelopes) != count {
		t.Fatalf("built %d envelopes, want %d", len(envelopes), count)
	}
	return envelopes
}

// One batch can hold buckets in every state. Each is decided on its own, the
// acknowledgements come back in key order, and a conflict writes nothing.
func TestPostgresUsageMixedBatchIsDecidedPerBucketAndAtomic(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	seedPricing(t, pool)
	principal := registerSyncDevice(t, pool, 907, "90700000-0000-4000-8000-0000000000")
	engine := bundledEngine(t)
	repository, err := NewUsageRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	envelopes := localProtocolEnvelopes(t, 5)
	revised := func(envelope protocol.Envelope, revision, output uint64) serversync.Bucket {
		envelope.Bucket.Revision = revision
		envelope.Bucket.Tokens.Output.Value = &output
		return derived(t, engine, envelope)
	}
	unchanged, stale, newer, conflicting, fresh := envelopes[0], envelopes[1], envelopes[2], envelopes[3], envelopes[4]
	if _, err := repository.ApplyUsageBuckets(ctx, principal, []serversync.Bucket{
		revised(unchanged, 2, 7), revised(stale, 2, 7), revised(newer, 2, 7), revised(conflicting, 2, 7),
	}); err != nil {
		t.Fatal(err)
	}
	outputs := func() map[string]string {
		t.Helper()
		rows, err := pool.Query(ctx, `SELECT bucket_key, revision::text || ':' || output_tokens::text FROM usage_buckets WHERE device_id = $1::uuid`, principal.DeviceID)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		stored := map[string]string{}
		for rows.Next() {
			var key, value string
			if err := rows.Scan(&key, &value); err != nil {
				t.Fatal(err)
			}
			stored[key] = value
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return stored
	}

	// The same revision with different contents refuses the whole batch,
	// including the buckets that would otherwise have been written.
	refused := []serversync.Bucket{revised(fresh, 1, 9), revised(newer, 3, 11), revised(conflicting, 2, 8)}
	if _, err := repository.ApplyUsageBuckets(ctx, principal, refused); !errors.Is(err, serversync.ErrRevisionConflict) {
		t.Fatalf("conflicting batch error = %v", err)
	}
	if stored := outputs(); len(stored) != 4 || stored[newer.BucketKey] != "2:7" {
		t.Fatalf("a refused batch changed storage: %v", stored)
	}

	mixed := []serversync.Bucket{revised(fresh, 1, 9), revised(newer, 3, 11), revised(stale, 1, 5), revised(unchanged, 2, 7)}
	acknowledgements, err := repository.ApplyUsageBuckets(ctx, principal, mixed)
	if err != nil || len(acknowledgements) != len(mixed) {
		t.Fatalf("mixed batch = %#v, %v", acknowledgements, err)
	}
	want := map[string]protocol.AckStatus{
		fresh.BucketKey: protocol.AckApplied, newer.BucketKey: protocol.AckApplied,
		stale.BucketKey: protocol.AckStale, unchanged.BucketKey: protocol.AckUnchanged,
	}
	for index, acknowledgement := range acknowledgements {
		if acknowledgement.Status != want[acknowledgement.BucketKey] {
			t.Errorf("bucket %d acknowledged %s, want %s", index, acknowledgement.Status, want[acknowledgement.BucketKey])
		}
		if index > 0 && acknowledgements[index-1].BucketKey >= acknowledgement.BucketKey {
			t.Errorf("acknowledgements are not in key order at %d", index)
		}
	}
	stored := outputs()
	for key, value := range map[string]string{
		fresh.BucketKey: "1:9", newer.BucketKey: "3:11", stale.BucketKey: "2:7", unchanged.BucketKey: "2:7", conflicting.BucketKey: "2:7",
	} {
		if stored[key] != value {
			t.Errorf("stored %s, want %s", stored[key], value)
		}
	}

	// A key named twice is refused before the database is asked anything.
	if _, err := repository.ApplyUsageBuckets(ctx, principal, []serversync.Bucket{revised(fresh, 2, 9), revised(fresh, 3, 9)}); !errors.Is(err, serversync.ErrInvalidBatch) {
		t.Fatalf("duplicated key error = %v", err)
	}
	// A device that was revoked uploads nothing.
	if _, err := pool.Exec(ctx, `UPDATE devices SET revoked_at = CURRENT_TIMESTAMP WHERE id = $1::uuid`, principal.DeviceID); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.ApplyUsageBuckets(ctx, principal, []serversync.Bucket{revised(fresh, 4, 9)}); !errors.Is(err, serverauth.ErrUnauthorized) {
		t.Fatalf("revoked device error = %v", err)
	}
	if stored := outputs(); stored[fresh.BucketKey] != "1:9" {
		t.Fatalf("a revoked device wrote %s", stored[fresh.BucketKey])
	}
}

// Two uploads of the same batch at once, as a manual sync beside a scheduled
// one would be, both finish and store each bucket once.
func TestPostgresUsageConcurrentBatchesBothFinish(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	seedPricing(t, pool)
	principal := registerSyncDevice(t, pool, 908, "90800000-0000-4000-8000-0000000000")
	engine := bundledEngine(t)
	repository, err := NewUsageRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	envelopes := localProtocolEnvelopes(t, protocol.MaxBatchBuckets)
	buckets := make([]serversync.Bucket, len(envelopes))
	for index, envelope := range envelopes {
		buckets[index] = derived(t, engine, envelope)
	}
	const uploads = 6
	failures := make(chan error, uploads)
	for range uploads {
		go func() {
			acknowledgements, err := repository.ApplyUsageBuckets(ctx, principal, buckets)
			if err == nil && len(acknowledgements) != len(buckets) {
				err = errors.New("an upload was not acknowledged in full")
			}
			failures <- err
		}()
	}
	for range uploads {
		if err := <-failures; err != nil {
			t.Fatalf("concurrent upload: %v", err)
		}
	}
	var stored int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM usage_buckets WHERE device_id = $1::uuid`, principal.DeviceID).Scan(&stored); err != nil || stored != len(buckets) {
		t.Fatalf("stored buckets = %d, %v", stored, err)
	}
}
