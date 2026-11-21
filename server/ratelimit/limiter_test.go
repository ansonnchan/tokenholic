package ratelimit

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) Advance(duration time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(duration)
}

func newClock() *clock {
	return &clock{now: time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)}
}

func TestLimiterSpendsABurstThenRefillsOneTokenAtATime(t *testing.T) {
	t.Parallel()
	clock := newClock()
	limiter, err := New(Limit{Burst: 3, Every: 10 * time.Second}, 0, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	steps := []struct {
		name      string
		advance   time.Duration
		wantAllow bool
		wantRetry time.Duration
	}{
		{name: "first of the burst", wantAllow: true},
		{name: "second of the burst", wantAllow: true},
		{name: "third of the burst", wantAllow: true},
		{name: "burst spent", wantRetry: 10 * time.Second},
		{name: "a refused request costs nothing", advance: 4 * time.Second, wantRetry: 6 * time.Second},
		{name: "one token back after the interval", advance: 6 * time.Second, wantAllow: true},
		{name: "and only one", wantRetry: 10 * time.Second},
		{name: "idle long enough to refill completely", advance: time.Hour, wantAllow: true},
		{name: "second after the refill", wantAllow: true},
		{name: "third after the refill", wantAllow: true},
		{name: "the refill never exceeds the burst", wantRetry: 10 * time.Second},
	}
	for _, step := range steps {
		clock.Advance(step.advance)
		allowed, retry := limiter.Allow("key")
		if allowed != step.wantAllow || retry != step.wantRetry {
			t.Fatalf("%s: Allow() = %t, %s; want %t, %s", step.name, allowed, retry, step.wantAllow, step.wantRetry)
		}
	}
}

func TestLimiterKeepsKeysIndependentAndForgetsIdleOnes(t *testing.T) {
	t.Parallel()
	clock := newClock()
	limiter, err := New(Limit{Burst: 1, Every: time.Minute}, 0, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	if allowed, _ := limiter.Allow("first"); !allowed {
		t.Fatal("first key was refused its own burst")
	}
	if allowed, _ := limiter.Allow("first"); allowed {
		t.Fatal("first key exceeded its burst")
	}
	if allowed, _ := limiter.Allow("second"); !allowed {
		t.Fatal("one key's spending limited another")
	}
	if limiter.Keys() != 2 {
		t.Fatalf("tracked keys = %d, want 2", limiter.Keys())
	}
	// Both buckets have refilled, so the next request drops them.
	clock.Advance(2 * time.Minute)
	if allowed, _ := limiter.Allow("third"); !allowed {
		t.Fatal("third key was refused")
	}
	if limiter.Keys() != 1 {
		t.Fatalf("tracked keys after idling = %d, want only the active one", limiter.Keys())
	}
}

// More active keys than the limiter may track share one bucket, so memory
// stays bounded and a flood of new keys is still limited.
func TestLimiterBoundsMemoryWithASharedOverflowBucket(t *testing.T) {
	t.Parallel()
	clock := newClock()
	limiter, err := New(Limit{Burst: 2, Every: time.Minute}, 3, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 3; index++ {
		if allowed, _ := limiter.Allow(fmt.Sprint("tracked-", index)); !allowed {
			t.Fatalf("tracked key %d was refused", index)
		}
	}
	// Three keys are active. Every further key spends from the shared bucket.
	results := make([]bool, 0, 4)
	for index := 0; index < 4; index++ {
		allowed, _ := limiter.Allow(fmt.Sprint("flood-", index))
		results = append(results, allowed)
	}
	if fmt.Sprint(results) != "[true true false false]" || limiter.Keys() != 3 {
		t.Fatalf("overflow results = %v with %d keys tracked; want two shared tokens and no growth", results, limiter.Keys())
	}
	// A tracked key still has its own allowance.
	if allowed, _ := limiter.Allow("tracked-0"); !allowed {
		t.Fatal("a tracked key lost its allowance to the flood")
	}
	// Once the tracked keys go idle there is room again.
	clock.Advance(time.Hour)
	if allowed, _ := limiter.Allow("flood-9"); !allowed || limiter.Keys() != 1 {
		t.Fatalf("after idling: allowed %t with %d keys", allowed, limiter.Keys())
	}
}

func TestLimiterIsSafeForConcurrentUse(t *testing.T) {
	t.Parallel()
	limiter, err := New(Limit{Burst: 100, Every: time.Hour}, 0, newClock().Now)
	if err != nil {
		t.Fatal(err)
	}
	var group sync.WaitGroup
	var mu sync.Mutex
	granted := 0
	for worker := 0; worker < 20; worker++ {
		group.Add(1)
		go func() {
			defer group.Done()
			for attempt := 0; attempt < 20; attempt++ {
				if allowed, _ := limiter.Allow("shared"); allowed {
					mu.Lock()
					granted++
					mu.Unlock()
				}
			}
		}()
	}
	group.Wait()
	if granted != 100 {
		t.Fatalf("granted %d of 400 concurrent requests, want exactly the burst of 100", granted)
	}
}

func TestNewRejectsALimitThatCouldNeverGrant(t *testing.T) {
	t.Parallel()
	for _, limit := range []Limit{{Burst: 0, Every: time.Second}, {Burst: 1, Every: 0}, {Burst: -1, Every: -time.Second}} {
		if _, err := New(limit, 0, nil); err == nil {
			t.Fatalf("New(%+v) error = nil", limit)
		}
	}
}
