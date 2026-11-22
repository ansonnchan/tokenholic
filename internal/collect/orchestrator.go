package collect

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/ansonnchan/tokenholic/internal/adapters"
	"github.com/ansonnchan/tokenholic/internal/adapters/ccusage"
	"github.com/ansonnchan/tokenholic/internal/normalize"
	"github.com/ansonnchan/tokenholic/internal/store"
)

const canceledCommitTimeout = 5 * time.Second

// Discovery supplies privacy-scoped local source findings.
type Discovery interface {
	Discover(context.Context) []adapters.Finding
}

// ScanStore atomically persists one completed application scan.
type ScanStore interface {
	CommitScan(context.Context, []store.ObservationBatch) (store.CommitResult, error)
}

// Orchestrator owns the application boundary from discovery through durable
// normalized state. Provider-specific parsing remains in adapters. It stores
// observed evidence only: catalog resolution is derived later, per bucket, so
// a catalog update never leaves stale attribution behind.
type Orchestrator struct {
	Discovery      Discovery
	Collector      Collector
	Store          ScanStore
	AdapterVersion string
	Now            func() time.Time
	// DayLevel reads the agents that have no native adapter. Nil leaves
	// them out of the scan entirely.
	DayLevel DayLevelReader
}

// DayLevelReader supplies one reading per day-level agent.
type DayLevelReader interface {
	Read(context.Context) []DayReading
}

// ScanResult reports durable progress and safe collection warnings.
type ScanResult struct {
	DiscoveredSources    int
	NormalizedRecords    int
	InsertedObservations int64
	Warnings             []normalize.Warning
	Canceled             bool
	Persisted            bool
}

// Scan discovers, normalizes, deduplicates, reduces, and atomically persists
// local evidence. Completed valid work is committed with a short bounded
// context even when cancellation arrives during collection.
func (o Orchestrator) Scan(ctx context.Context) (ScanResult, error) {
	if o.Discovery == nil || o.Store == nil {
		return ScanResult{}, errors.New("collect scan requires discovery and storage")
	}
	findings := o.Discovery.Discover(ctx)
	candidates := make([]adapters.Candidate, 0)
	for _, finding := range findings {
		candidates = append(candidates, finding.Candidates...)
	}
	collected := o.Collector.Collect(ctx, candidates)
	now := time.Now().UTC()
	if o.Now != nil {
		now = o.Now().UTC()
	}
	groups := buildSourceGroups(findings, collected, now, o.adapterVersion())
	batches := make([]store.ObservationBatch, 0, len(groups))
	for _, group := range groups {
		batches = append(batches, group.batch)
	}

	result := ScanResult{
		DiscoveredSources: len(findings), NormalizedRecords: len(collected.Observations),
		Warnings: append([]normalize.Warning(nil), collected.Warnings...),
	}
	// Day-level agents are read after the native sources and committed in
	// the same transaction. They share nothing with the native path: their
	// batches are built here from their own readings, so nothing they report
	// can reach a Claude or Codex record.
	if o.DayLevel != nil {
		for _, reading := range o.DayLevel.Read(ctx) {
			batches = append(batches, dayLevelBatch(reading, now))
			result.DiscoveredSources++
			result.NormalizedRecords += len(reading.Observations)
		}
	}
	commitCtx := ctx
	var cancel context.CancelFunc
	if ctx.Err() != nil {
		result.Canceled = true
		// Parsed observations are already detached from raw source content. Finish
		// their local transaction so cancellation cannot create an inflated retry.
		commitCtx, cancel = context.WithTimeout(context.WithoutCancel(ctx), canceledCommitTimeout)
		defer cancel()
	}
	committed, err := o.Store.CommitScan(commitCtx, batches)
	if err != nil {
		return result, fmt.Errorf("persist local collection: %w", err)
	}
	result.InsertedObservations = committed.InsertedObservations
	result.Persisted = true
	if result.Canceled {
		return result, ctx.Err()
	}
	return result, nil
}

// dayLevelBatch stores one agent's reading. An agent that was not read
// carries no observations, so its stored day totals stay as they were while
// its health records why.
func dayLevelBatch(reading DayReading, now time.Time) store.ObservationBatch {
	source := reading.Health.Source
	identity := store.NewSourceIdentity(source, normalize.SurfaceCLI,
		strings.Join([]string{"day-level-v1", string(source)}, "\x00"))
	batch := store.ObservationBatch{
		Observations: reading.Observations,
		Checkpoint:   store.Checkpoint{Source: identity, Cursor: uint64(len(reading.Observations)), CollectedAt: now},
		Health: store.AdapterHealth{
			Source: identity, Health: reading.Health, AdapterVersion: ccusage.AdapterVersion, CheckedAt: now,
		},
	}
	if reading.Read {
		batch.Health.LastSuccessAt = &now
	}
	return batch
}

func (o Orchestrator) adapterVersion() string {
	if o.AdapterVersion != "" {
		return o.AdapterVersion
	}
	return "native-v1"
}

type sourceGroup struct {
	batch store.ObservationBatch
}

type sourceGroupKey struct {
	source  normalize.Source
	surface normalize.Surface
}

func buildSourceGroups(findings []adapters.Finding, collected Result, now time.Time, adapterVersion string) []sourceGroup {
	groups := make(map[sourceGroupKey]*sourceGroup)
	for _, finding := range findings {
		key := sourceGroupKey{source: finding.Source, surface: finding.Surface}
		health := healthFromFinding(finding)
		group, found := groups[key]
		if !found {
			groups[key] = newSourceGroup(key, health, now, adapterVersion)
			continue
		}
		group.batch.Health.Health = mergeHealth(group.batch.Health.Health, health)
	}
	for _, observation := range collected.Observations {
		key := sourceGroupKey{source: observation.Source, surface: observation.Surface}
		group, found := groups[key]
		if !found {
			health := normalize.SourceHealth{
				Source: observation.Source, Surface: observation.Surface,
				Status: normalize.HealthDetected, Capabilities: capabilitiesFromObservation(observation),
			}
			group = newSourceGroup(key, health, now, adapterVersion)
			groups[key] = group
		}
		group.batch.Observations = append(group.batch.Observations, observation)
	}
	for _, issue := range collected.SourceIssues {
		key := sourceGroupKey{source: issue.Source, surface: issue.Surface}
		group, found := groups[key]
		if !found {
			continue
		}
		group.batch.Health.Health.Warnings = append(group.batch.Health.Health.Warnings, issue.Warning)
		if group.batch.Health.Health.Status == normalize.HealthDetected {
			group.batch.Health.Health.Status = normalize.HealthDegraded
		}
	}
	if hasWarningCode(collected.Warnings, "collection_canceled") {
		for _, group := range groups {
			group.batch.Health.Health.Status = normalize.HealthError
			group.batch.Health.Health.Warnings = append(
				group.batch.Health.Health.Warnings,
				normalize.Warning{Code: "collection_canceled"},
			)
		}
	}

	keys := make([]sourceGroupKey, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].source != keys[j].source {
			return keys[i].source < keys[j].source
		}
		return keys[i].surface < keys[j].surface
	})
	result := make([]sourceGroup, 0, len(keys))
	for _, key := range keys {
		group := groups[key]
		sort.Slice(group.batch.Observations, func(i, j int) bool {
			return group.batch.Observations[i].SourceRecordID < group.batch.Observations[j].SourceRecordID
		})
		group.batch.Checkpoint.Cursor = uint64(len(group.batch.Observations))
		if len(group.batch.Observations) > 0 {
			last := group.batch.Observations[len(group.batch.Observations)-1]
			group.batch.Checkpoint.LastSourceRecordID = last.SourceRecordID
			group.batch.Health.LastSuccessAt = &now
		}
		group.batch.Health.Health.Warnings = uniqueWarnings(group.batch.Health.Health.Warnings)
		result = append(result, *group)
	}
	return result
}

func newSourceGroup(key sourceGroupKey, health normalize.SourceHealth, now time.Time, adapterVersion string) *sourceGroup {
	identity := store.NewSourceIdentity(key.source, key.surface,
		strings.Join([]string{"collection-v1", string(key.source), string(key.surface)}, "\x00"))
	return &sourceGroup{batch: store.ObservationBatch{
		Checkpoint: store.Checkpoint{Source: identity, CollectedAt: now},
		Health: store.AdapterHealth{
			Source: identity, Health: health, AdapterVersion: adapterVersion, CheckedAt: now,
		},
	}}
}

func healthFromFinding(finding adapters.Finding) normalize.SourceHealth {
	health := normalize.SourceHealth{
		Source: finding.Source, Surface: finding.Surface, Status: finding.Status,
		Format: finding.Format, Capabilities: finding.Capabilities,
		Warnings: append([]normalize.Warning(nil), finding.Warnings...),
	}
	if finding.Status == normalize.HealthDetected || finding.Status == normalize.HealthDegraded {
		count := finding.RecordCount
		health.RecordCount = &count
	}
	return health
}

func mergeHealth(left, right normalize.SourceHealth) normalize.SourceHealth {
	left.Warnings = append(left.Warnings, right.Warnings...)
	if left.Format == "" {
		left.Format = right.Format
	} else if right.Format != "" && left.Format != right.Format {
		left.Warnings = append(left.Warnings, normalize.Warning{Code: "multiple_source_formats"})
	}
	left.Status = mergeHealthStatus(left.Status, right.Status)
	if left.RecordCount == nil || right.RecordCount == nil {
		left.RecordCount = nil
	} else if math.MaxUint64-*left.RecordCount < *right.RecordCount {
		left.RecordCount = nil
		left.Warnings = append(left.Warnings, normalize.Warning{Code: "source_record_count_overflow"})
	} else {
		count := *left.RecordCount + *right.RecordCount
		left.RecordCount = &count
	}
	return left
}

func mergeHealthStatus(left, right normalize.HealthStatus) normalize.HealthStatus {
	if left == right {
		return left
	}
	if left == normalize.HealthError || right == normalize.HealthError {
		return normalize.HealthError
	}
	if left == normalize.HealthPermissionDenied || right == normalize.HealthPermissionDenied {
		return normalize.HealthPermissionDenied
	}
	if left == normalize.HealthDetected || right == normalize.HealthDetected {
		return normalize.HealthDegraded
	}
	if left == normalize.HealthDegraded || right == normalize.HealthDegraded {
		return normalize.HealthDegraded
	}
	if left == normalize.HealthUnsupported || right == normalize.HealthUnsupported {
		return normalize.HealthUnsupported
	}
	return normalize.HealthNotFound
}

func capabilitiesFromObservation(observation normalize.Observation) normalize.Capabilities {
	capability := func(value *uint64) normalize.CapabilitySupport {
		if value == nil {
			return normalize.CapabilityUnsupported
		}
		if observation.Measurement == normalize.MeasurementReported {
			return normalize.CapabilityReported
		}
		return normalize.CapabilityDerived
	}
	model := normalize.CapabilityReported
	if observation.ModelResolution == normalize.ModelInferred {
		model = normalize.CapabilityInferred
	} else if observation.ModelResolution == normalize.ModelUnknown {
		model = normalize.CapabilityUnsupported
	}
	return normalize.Capabilities{
		Model: model, InputUncached: capability(observation.Tokens.InputUncached),
		InputCacheRead:  capability(observation.Tokens.InputCacheRead),
		InputCacheWrite: capability(observation.Tokens.InputCacheWrite),
		Output:          capability(observation.Tokens.Output),
		ReasoningOutput: capability(observation.Tokens.ReasoningOutput),
	}
}

func uniqueWarnings(warnings []normalize.Warning) []normalize.Warning {
	sort.Slice(warnings, func(i, j int) bool {
		if warnings[i].Code != warnings[j].Code {
			return warnings[i].Code < warnings[j].Code
		}
		return warnings[i].Message < warnings[j].Message
	})
	result := warnings[:0]
	for _, warning := range warnings {
		if len(result) == 0 || result[len(result)-1] != warning {
			result = append(result, warning)
		}
	}
	return result
}

func hasWarningCode(warnings []normalize.Warning, code string) bool {
	for _, warning := range warnings {
		if warning.Code == code {
			return true
		}
	}
	return false
}
