package collect

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/ansonnchan/tokenholic/internal/adapters"
	"github.com/ansonnchan/tokenholic/internal/normalize"
	"github.com/ansonnchan/tokenholic/internal/store"
)

func TestOrchestratorRepeatedAndReversedScansAreIdentical(t *testing.T) {
	t.Parallel()
	findings := syntheticFindings(t)
	fixedNow := time.Date(2026, time.November, 17, 20, 0, 0, 0, time.UTC)

	firstDB := openCollectionStore(t)
	first := Orchestrator{
		Discovery: staticDiscovery(findings), Collector: Collector{SourceVersion: "synthetic-v1"},
		Store: firstDB, AdapterVersion: "native-test-v1", Now: func() time.Time { return fixedNow },
	}
	firstResult, err := first.Scan(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if firstResult.InsertedObservations != 4 {
		t.Fatalf("first inserted observations = %d, want 4", firstResult.InsertedObservations)
	}
	before, err := firstDB.Observations(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	repeated, err := first.Scan(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if repeated.InsertedObservations != 0 {
		t.Fatalf("repeated inserted observations = %d, want 0", repeated.InsertedObservations)
	}
	after, err := firstDB.Observations(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("repeated scan changed observations:\nbefore=%#v\nafter=%#v", before, after)
	}

	reversedFindings := reverseFindings(findings)
	reversedDB := openCollectionStore(t)
	reversed := Orchestrator{
		Discovery: staticDiscovery(reversedFindings), Collector: Collector{SourceVersion: "synthetic-v1"},
		Store: reversedDB, AdapterVersion: "native-test-v1", Now: func() time.Time { return fixedNow },
	}
	if _, err := reversed.Scan(t.Context()); err != nil {
		t.Fatal(err)
	}
	reversedObservations, err := reversedDB.Observations(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, reversedObservations) {
		t.Fatalf("candidate order changed stored observations:\nfirst=%#v\nreversed=%#v", before, reversedObservations)
	}
}

func TestOrchestratorRescanKeepsClaudeCacheWriteTotals(t *testing.T) {
	t.Parallel()
	root := filepath.Join(t.TempDir(), "claude")
	copyFixture(t, filepath.Join("claude-code", "cache-write-lifetimes.jsonl"), filepath.Join(root, "lifetimes.jsonl"))
	copyFixture(t, filepath.Join("claude-code", "local-notices.jsonl"), filepath.Join(root, "notices.jsonl"))
	db := openCollectionStore(t)
	orchestrator := Orchestrator{
		Discovery: adapters.Discoverer{Roots: []adapters.RootSpec{
			testRoot(adapters.RootClaudeCLI, normalize.SourceClaude, normalize.SurfaceCLI, root),
		}},
		Collector: Collector{SourceVersion: "synthetic-v1"}, Store: db,
		Now: func() time.Time { return time.Date(2026, 11, 20, 20, 0, 0, 0, time.UTC) },
	}
	for scan, wantInserted := range []int64{8, 0} {
		result, err := orchestrator.Scan(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if result.InsertedObservations != wantInserted {
			t.Fatalf("scan %d changed %d observations, want %d", scan, result.InsertedObservations, wantInserted)
		}
		observations, err := db.Observations(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		// Hand-computed from both fixtures: six lifetime records plus the two
		// real usage lines that sit beside the excluded local notices.
		var total, write, fiveMinute, oneHour, unknownTTL uint64
		for _, observation := range observations {
			if observation.ObservedModelID != "claude-sonnet-synthetic" {
				t.Fatalf("scan %d stored model %q", scan, observation.ObservedModelID)
			}
			known, err := observation.Tokens.KnownTotal()
			if err != nil || known == nil {
				t.Fatalf("KnownTotal() = %v, %v", known, err)
			}
			total += *known
			write += *observation.Tokens.InputCacheWrite
			unknownTTL += *observation.Tokens.CacheWriteUnknownTTL()
			if observation.Tokens.InputCacheWrite5m != nil {
				fiveMinute += *observation.Tokens.InputCacheWrite5m
			}
			if observation.Tokens.InputCacheWrite1h != nil {
				oneHour += *observation.Tokens.InputCacheWrite1h
			}
		}
		if len(observations) != 8 || total != 482+33 || write != 407 || fiveMinute != 107 || oneHour != 230 || unknownTTL != 70 {
			t.Fatalf("scan %d totals = %d records, %d tokens, cache write %d/%d/%d/%d",
				scan, len(observations), total, write, fiveMinute, oneHour, unknownTTL)
		}
	}
}

func TestOrchestratorRescanKeepsCodexForkTotals(t *testing.T) {
	t.Parallel()
	root := filepath.Join(t.TempDir(), "codex")
	copyFixture(t, filepath.Join("codex-cli", "fork-parent.jsonl"), filepath.Join(root, "parent.jsonl"))
	copyFixture(t, filepath.Join("codex-cli", "fork-child.jsonl"), filepath.Join(root, "child.jsonl"))
	db := openCollectionStore(t)
	orchestrator := Orchestrator{
		Discovery: adapters.Discoverer{Roots: []adapters.RootSpec{
			testRoot(adapters.RootCodexCLI, normalize.SourceCodex, normalize.SurfaceCLI, root),
		}},
		Collector: Collector{SourceVersion: "synthetic-v1"}, Store: db,
		Now: func() time.Time { return time.Date(2026, 11, 21, 20, 0, 0, 0, time.UTC) },
	}
	for scan, wantInserted := range []int64{5, 0} {
		result, err := orchestrator.Scan(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if result.InsertedObservations != wantInserted || len(result.Warnings) != 0 {
			t.Fatalf("scan %d changed %d observations with warnings %#v, want %d and none", scan, result.InsertedObservations, result.Warnings, wantInserted)
		}
		observations, err := db.Observations(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		// Five distinct requests across both files: 180 uncached input, 620
		// cache read, 70 output.
		var total uint64
		for _, observation := range observations {
			known, err := observation.Tokens.KnownTotal()
			if err != nil || known == nil {
				t.Fatalf("KnownTotal() = %v, %v", known, err)
			}
			total += *known
		}
		if len(observations) != 5 || total != 180+620+70 {
			t.Fatalf("scan %d stored %d observations totalling %d tokens, want 5 and 870", scan, len(observations), total)
		}
	}
}

// Archiving moves a rollout, but a scan can catch it in both places. Whatever
// the overlap, the usage counts once: the same five requests as the test above.
func TestOrchestratorCountsArchivedCodexRolloutsOnce(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	sessions := filepath.Join(home, ".codex", "sessions", "2026", "01", "07")
	archive := filepath.Join(home, ".codex", "archived_sessions")
	copyFixture(t, filepath.Join("codex-cli", "fork-parent.jsonl"), filepath.Join(sessions, "rollout-parent.jsonl"))
	copyFixture(t, filepath.Join("codex-cli", "fork-parent.jsonl"), filepath.Join(archive, "rollout-parent.jsonl"))
	// The fork exists only in the archive.
	copyFixture(t, filepath.Join("codex-cli", "fork-child.jsonl"), filepath.Join(archive, "rollout-child.jsonl"))
	db := openCollectionStore(t)
	result, err := (Orchestrator{
		Discovery: adapters.Discoverer{Roots: adapters.MacRoots(home, "")},
		Collector: Collector{SourceVersion: "synthetic-v1"}, Store: db,
		Now: func() time.Time { return time.Date(2026, 11, 21, 20, 0, 0, 0, time.UTC) },
	}).Scan(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if hasWarning(result.Warnings, "duplicate_conflict") || hasWarning(result.Warnings, "counter_reset") {
		t.Fatalf("warnings = %#v", result.Warnings)
	}
	observations, err := db.Observations(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var total uint64
	for _, observation := range observations {
		if observation.Source != normalize.SourceCodex || observation.Surface != normalize.SurfaceSharedLocal {
			t.Fatalf("observation = %s/%s", observation.Source, observation.Surface)
		}
		known, err := observation.Tokens.KnownTotal()
		if err != nil || known == nil {
			t.Fatalf("KnownTotal() = %v, %v", known, err)
		}
		total += *known
	}
	if len(observations) != 5 || total != 870 {
		t.Fatalf("stored %d observations totalling %d tokens, want 5 and 870", len(observations), total)
	}
}

// Claude Code 1.0.28 to 1.0.30 wrote sessions under the XDG configuration
// directory on Linux. A transcript present there and in the usual place is
// the same usage and counts once.
func TestOrchestratorCountsEarlierLinuxClaudeDirectoryOnce(t *testing.T) {
	t.Parallel()
	scan := func(home string) []store.StoredObservation {
		db := openCollectionStore(t)
		_, err := (Orchestrator{
			Discovery: adapters.Discoverer{Roots: adapters.LinuxRoots(home, "", "")},
			Collector: Collector{SourceVersion: "synthetic-v1"}, Store: db,
			Now: func() time.Time { return time.Date(2026, 11, 21, 20, 0, 0, 0, time.UTC) },
		}).Scan(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		observations, err := db.Observations(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		return observations
	}
	usual := t.TempDir()
	copyFixture(t, filepath.Join("claude-code", "basic.jsonl"), filepath.Join(usual, ".claude", "projects", "synthetic", "session.jsonl"))
	want := scan(usual)
	if len(want) == 0 {
		t.Fatal("the fixture produced no observations")
	}

	both := t.TempDir()
	copyFixture(t, filepath.Join("claude-code", "basic.jsonl"), filepath.Join(both, ".claude", "projects", "synthetic", "session.jsonl"))
	copyFixture(t, filepath.Join("claude-code", "basic.jsonl"), filepath.Join(both, ".config", "claude", "projects", "synthetic", "session.jsonl"))
	if got := scan(both); len(got) != len(want) {
		t.Fatalf("observations with both directories = %d, want %d", len(got), len(want))
	}

	// A machine that only ever ran those versions still has its usage read.
	earlier := t.TempDir()
	copyFixture(t, filepath.Join("claude-code", "basic.jsonl"), filepath.Join(earlier, ".config", "claude", "projects", "synthetic", "session.jsonl"))
	got := scan(earlier)
	if len(got) != len(want) {
		t.Fatalf("observations from the earlier directory alone = %d, want %d", len(got), len(want))
	}
	for _, observation := range got {
		if observation.Source != normalize.SourceClaude || observation.Surface != normalize.SurfaceCLI {
			t.Fatalf("observation = %s/%s", observation.Source, observation.Surface)
		}
	}
}

func TestOrchestratorPersistsValidSourcesWhenAnotherIsPartial(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	claudeRoot := filepath.Join(root, "claude")
	codexRoot := filepath.Join(root, "codex")
	copyFixture(t, filepath.Join("claude-code", "malformed-middle.jsonl"), filepath.Join(claudeRoot, "malformed.jsonl"))
	copyFixture(t, filepath.Join("codex-desktop", "basic.jsonl"), filepath.Join(codexRoot, "basic.jsonl"))
	discovery := adapters.Discoverer{Roots: []adapters.RootSpec{
		testRoot(adapters.RootClaudeCLI, normalize.SourceClaude, normalize.SurfaceCLI, claudeRoot),
		testRoot(adapters.RootCodexShared, normalize.SourceCodex, normalize.SurfaceSharedLocal, codexRoot),
	}}
	db := openCollectionStore(t)
	result, err := (Orchestrator{
		Discovery: discovery, Collector: Collector{SourceVersion: "synthetic-v1"}, Store: db,
		Now: func() time.Time { return time.Date(2026, 11, 17, 20, 0, 0, 0, time.UTC) },
	}).Scan(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !hasWarning(result.Warnings, "malformed_record") {
		t.Fatalf("warnings = %#v, want malformed_record", result.Warnings)
	}
	observations, err := db.Observations(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	sources := map[normalize.Source]int{}
	for _, observation := range observations {
		sources[observation.Source]++
		// Collection stores source evidence only; catalog results are derived
		// per bucket and must not be frozen into an observation.
		if observation.CanonicalModelID != nil || observation.ModelProvider != nil || observation.PricingKey != nil {
			t.Fatalf("observation carries a catalog result: %#v", observation)
		}
		wantEvidence := normalize.ModelReported
		if observation.Source == normalize.SourceCodex {
			wantEvidence = normalize.ModelInferred
		}
		if observation.ModelResolution != wantEvidence {
			t.Fatalf("%s evidence = %q, want %q", observation.Source, observation.ModelResolution, wantEvidence)
		}
	}
	if sources[normalize.SourceClaude] != 2 || sources[normalize.SourceCodex] != 1 {
		t.Fatalf("stored source counts = %#v", sources)
	}
	health, err := db.AdapterHealthRecords(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range health {
		if record.Health.Source == normalize.SourceClaude && record.Health.Status != normalize.HealthDegraded {
			t.Fatalf("Claude health = %q, want degraded", record.Health.Status)
		}
	}
}

func TestOrchestratorMakesCancellationVisibleAndDurable(t *testing.T) {
	t.Parallel()
	findings := syntheticFindings(t)
	ctx, cancel := context.WithCancel(context.Background())
	discovery := cancelingDiscovery{findings: findings, cancel: cancel}
	db := openCollectionStore(t)
	result, err := (Orchestrator{Discovery: discovery, Store: db}).Scan(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Scan() error = %v, want context cancellation", err)
	}
	if !result.Canceled || !result.Persisted {
		t.Fatalf("Scan() result = %#v, want canceled durable state", result)
	}
	health, healthErr := db.AdapterHealthRecords(context.Background())
	if healthErr != nil {
		t.Fatal(healthErr)
	}
	if len(health) == 0 {
		t.Fatal("canceled scan did not persist source health")
	}
	for _, record := range health {
		if record.Health.Status != normalize.HealthError || !hasWarning(record.Health.Warnings, "collection_canceled") {
			t.Fatalf("canceled source health = %#v", record.Health)
		}
	}
}

type staticDiscovery []adapters.Finding

func (d staticDiscovery) Discover(context.Context) []adapters.Finding {
	return append([]adapters.Finding(nil), d...)
}

type cancelingDiscovery struct {
	findings []adapters.Finding
	cancel   context.CancelFunc
}

func (d cancelingDiscovery) Discover(context.Context) []adapters.Finding {
	d.cancel()
	return append([]adapters.Finding(nil), d.findings...)
}

func syntheticFindings(t *testing.T) []adapters.Finding {
	t.Helper()
	root := t.TempDir()
	roots := []adapters.RootSpec{
		testRoot(adapters.RootClaudeCLI, normalize.SourceClaude, normalize.SurfaceCLI, filepath.Join(root, "claude-cli")),
		testRoot(adapters.RootClaudeDesktopCode, normalize.SourceClaude, normalize.SurfaceDesktopCode, filepath.Join(root, "claude-desktop")),
		testRoot(adapters.RootCodexCLI, normalize.SourceCodex, normalize.SurfaceCLI, filepath.Join(root, "codex-cli")),
		testRoot(adapters.RootCodexDesktop, normalize.SourceCodex, normalize.SurfaceDesktop, filepath.Join(root, "codex-desktop")),
	}
	copyFixture(t, filepath.Join("claude-code", "duplicates.jsonl"), filepath.Join(roots[0].Path, "usage.jsonl"))
	copyFixture(t, filepath.Join("claude-desktop", "code", "overlap.jsonl"), filepath.Join(roots[1].Path, "usage.jsonl"))
	copyFixture(t, filepath.Join("codex-cli", "cumulative.jsonl"), filepath.Join(roots[2].Path, "rollout.jsonl"))
	copyFixture(t, filepath.Join("codex-desktop", "overlap.jsonl"), filepath.Join(roots[3].Path, "rollout.jsonl"))
	return (adapters.Discoverer{Roots: roots}).Discover(context.Background())
}

func testRoot(kind adapters.RootKind, source normalize.Source, surface normalize.Surface, path string) adapters.RootSpec {
	return adapters.RootSpec{
		Kind: kind, Source: source, Surface: surface, Path: path,
		Format: "synthetic-jsonl", Extensions: []string{".jsonl"},
		Capabilities: normalize.Capabilities{
			Model: normalize.CapabilityReported, InputUncached: normalize.CapabilityReported,
			InputCacheRead: normalize.CapabilityReported, InputCacheWrite: normalize.CapabilityReported,
			Output: normalize.CapabilityReported, ReasoningOutput: normalize.CapabilityUnsupported,
		},
	}
}

func copyFixture(t *testing.T, relative, destination string) {
	t.Helper()
	contents, err := os.ReadFile(filepath.Join("..", "..", "fixtures", relative))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(destination, contents, 0o600); err != nil {
		t.Fatal(err)
	}
}

func reverseFindings(findings []adapters.Finding) []adapters.Finding {
	result := append([]adapters.Finding(nil), findings...)
	for index := range result {
		result[index].Candidates = append([]adapters.Candidate(nil), result[index].Candidates...)
		for left, right := 0, len(result[index].Candidates)-1; left < right; left, right = left+1, right-1 {
			result[index].Candidates[left], result[index].Candidates[right] = result[index].Candidates[right], result[index].Candidates[left]
		}
	}
	for left, right := 0, len(result)-1; left < right; left, right = left+1, right-1 {
		result[left], result[right] = result[right], result[left]
	}
	return result
}

func openCollectionStore(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "tokenholic.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close(context.Background()) })
	return db
}
