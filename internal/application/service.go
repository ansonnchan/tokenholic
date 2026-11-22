package application

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/ansonnchan/tokenholic/internal/collect"
	"github.com/ansonnchan/tokenholic/internal/store"
)

// Dashboard is a consistent snapshot of local usage and operational health.
type Dashboard struct {
	GeneratedAt time.Time
	Timezone    string
	Today       store.UsageSummary
	Week        store.UsageSummary
	Last30Days  store.UsageSummary
	AllTime     store.UsageSummary
	// Daily is total tokens for each of the last DailyDays local dates,
	// oldest first and ending today.
	Daily       []store.DailyUsage
	Diagnostics store.DiagnosticSnapshot
}

// Summary returns the totals for one period; an unrecognized period is
// all-time.
func (d Dashboard) Summary(period store.Period) store.UsageSummary {
	switch period {
	case store.PeriodToday:
		return d.Today
	case store.PeriodWeek:
		return d.Week
	case store.PeriodLast30Days:
		return d.Last30Days
	default:
		return d.AllTime
	}
}

// DailyDays is how many local dates Dashboard.Daily covers.
const DailyDays = 30

// Storage is the committed local-state boundary consumed by Service.
type Storage interface {
	QueryUsage(context.Context, store.Period, time.Time, *time.Location) (store.UsageSummary, error)
	QueryDailyUsage(context.Context, time.Time, *time.Location, int) ([]store.DailyUsage, error)
	Diagnostics(context.Context) (store.DiagnosticSnapshot, error)
	NextUsageBucketRevision(context.Context) (uint64, error)
	RebuildUsageBuckets(context.Context, uint64, store.Previewer) (store.BucketApplyResult, error)
}

// Collector owns discovery, parsing, normalization, and atomic persistence.
type Collector interface {
	Scan(context.Context) (collect.ScanResult, error)
}

// Service coordinates local application actions for presentation consumers.
type Service struct {
	mu        sync.RWMutex
	storage   Storage
	collector Collector
	previewer store.Previewer
	location  *time.Location
	now       func() time.Time
}

// NewService constructs a local-only application service. previewer supplies
// the offline model resolution and estimate shown before any server reply.
func NewService(storage Storage, collector Collector, previewer store.Previewer, location *time.Location, now func() time.Time) (*Service, error) {
	if storage == nil || collector == nil {
		return nil, errors.New("application service requires storage and collection")
	}
	if location == nil {
		return nil, errors.New("application service requires a timezone")
	}
	if now == nil {
		now = time.Now
	}
	return &Service{storage: storage, collector: collector, previewer: previewer, location: location, now: now}, nil
}

// Load reads usage periods and health from already committed local state.
func (s *Service) Load(ctx context.Context) (Dashboard, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	now := s.now()
	dashboard := Dashboard{GeneratedAt: now, Timezone: s.location.String()}
	queries := []struct {
		period store.Period
		target *store.UsageSummary
	}{
		{store.PeriodToday, &dashboard.Today},
		{store.PeriodWeek, &dashboard.Week},
		{store.PeriodLast30Days, &dashboard.Last30Days},
		{store.PeriodAllTime, &dashboard.AllTime},
	}
	for _, query := range queries {
		summary, err := s.storage.QueryUsage(ctx, query.period, now, s.location)
		if err != nil {
			return Dashboard{}, fmt.Errorf("load %s local usage: %w", query.period, err)
		}
		*query.target = summary
	}
	daily, err := s.storage.QueryDailyUsage(ctx, now, s.location, DailyDays)
	if err != nil {
		return Dashboard{}, fmt.Errorf("load daily local usage: %w", err)
	}
	dashboard.Daily = daily
	diagnostics, err := s.storage.Diagnostics(ctx)
	if err != nil {
		return Dashboard{}, fmt.Errorf("load local health: %w", err)
	}
	dashboard.Diagnostics = diagnostics
	return dashboard, nil
}

// Refresh collects local evidence, commits it, and then rebuilds absolute
// usage buckets. Presentation never observes uncommitted collection output.
func (s *Service) Refresh(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	result, err := s.collector.Scan(ctx)
	if err != nil {
		return fmt.Errorf("refresh local collection: %w", err)
	}
	if !result.Persisted {
		return errors.New("refresh local collection did not commit state")
	}
	revision, err := s.storage.NextUsageBucketRevision(ctx)
	if err != nil {
		return err
	}
	if _, err := s.storage.RebuildUsageBuckets(ctx, revision, s.previewer); err != nil {
		return fmt.Errorf("rebuild local usage: %w", err)
	}
	return nil
}
