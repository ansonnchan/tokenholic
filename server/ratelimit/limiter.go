package ratelimit

import (
	"errors"
	"sync"
	"time"
)

// Limit is a token bucket. A key may spend Burst requests at once and earns
// one back every Every, so its sustained rate is one request per Every.
type Limit struct {
	Burst int
	Every time.Duration
}

// defaultMaxKeys bounds memory when a caller does not. At roughly a hundred
// bytes per key this is a few megabytes.
const defaultMaxKeys = 50_000

// Limiter grants or refuses requests for independent keys.
//
// Memory is bounded. A key whose bucket has refilled is indistinguishable from
// one never seen, so idle keys are dropped. If more than maxKeys keys are
// active at once, the rest share one overflow bucket: a flood of distinct keys
// is then limited together rather than each getting a fresh allowance.
type Limiter struct {
	limit   Limit
	maxKeys int
	now     func() time.Time

	mu        sync.Mutex
	buckets   map[string]bucket
	overflow  bucket
	lastSweep time.Time
}

// bucket stores the time at which the key will next be full. Spending a token
// moves that time one Every later, which needs no stored count and no refill
// arithmetic on read.
type bucket struct {
	fullAt time.Time
}

// New constructs a limiter. maxKeys below one selects a default; now may be
// nil for the wall clock.
func New(limit Limit, maxKeys int, now func() time.Time) (*Limiter, error) {
	if limit.Burst < 1 || limit.Every <= 0 {
		return nil, errors.New("initialize rate limiter: burst and refill interval must be positive")
	}
	if maxKeys < 1 {
		maxKeys = defaultMaxKeys
	}
	if now == nil {
		now = time.Now
	}
	return &Limiter{limit: limit, maxKeys: maxKeys, now: now, buckets: make(map[string]bucket)}, nil
}

// Allow spends one token for key. When none is left it reports how long until
// the next one, always more than zero.
func (l *Limiter) Allow(key string) (allowed bool, retryAfter time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	current, tracked := l.buckets[key]
	if !tracked && len(l.buckets) >= l.maxKeys {
		l.sweep(now)
		if len(l.buckets) >= l.maxKeys {
			l.overflow, allowed, retryAfter = l.spend(l.overflow, now)
			return allowed, retryAfter
		}
	}
	current, allowed, retryAfter = l.spend(current, now)
	if allowed {
		l.buckets[key] = current
	}
	if now.Sub(l.lastSweep) >= l.window() {
		l.sweep(now)
	}
	return allowed, retryAfter
}

// window is how long an untouched bucket takes to refill completely.
func (l *Limiter) window() time.Duration {
	return time.Duration(l.limit.Burst) * l.limit.Every
}

// spend takes one token from a bucket. The bucket is empty when it would not
// be full again for a whole window.
func (l *Limiter) spend(current bucket, now time.Time) (bucket, bool, time.Duration) {
	fullAt := current.fullAt
	if fullAt.Before(now) {
		fullAt = now
	}
	fullAt = fullAt.Add(l.limit.Every)
	if wait := fullAt.Sub(now) - l.window(); wait > 0 {
		return current, false, wait
	}
	return bucket{fullAt: fullAt}, true, 0
}

// sweep drops every bucket that has refilled.
func (l *Limiter) sweep(now time.Time) {
	for key, current := range l.buckets {
		if !current.fullAt.After(now) {
			delete(l.buckets, key)
		}
	}
	l.lastSweep = now
}

// Keys returns how many keys are currently tracked. It exists for tests and
// diagnostics.
func (l *Limiter) Keys() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.buckets)
}
