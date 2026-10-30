package auth

import (
	"sync"
	"time"
)

// Limiter is a token-bucket rate limiter keyed by client, used to slow down
// password guessing. Each key may make burst attempts at once and regains one
// attempt every interval. Idle keys are forgotten, so memory stays bounded.
type Limiter struct {
	mu       sync.Mutex
	burst    float64
	interval time.Duration
	buckets  map[string]*bucket
	now      func() time.Time
}

type bucket struct {
	tokens float64
	last   time.Time
}

// maxIdleKeys bounds how many keys are kept before full buckets are pruned.
const maxIdleKeys = 10_000

// NewLimiter returns a Limiter that allows burst attempts per key and
// regains one attempt every interval.
func NewLimiter(burst int, interval time.Duration) *Limiter {
	return &Limiter{
		burst:    float64(burst),
		interval: interval,
		buckets:  make(map[string]*bucket),
		now:      time.Now,
	}
}

// Allow records an attempt for key. It reports whether the attempt may
// proceed and, if not, how long until the next one will be allowed.
func (l *Limiter) Allow(key string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	b, ok := l.buckets[key]
	if !ok {
		if len(l.buckets) >= maxIdleKeys {
			l.pruneLocked(now)
		}
		b = &bucket{tokens: l.burst, last: now}
		l.buckets[key] = b
	}
	b.tokens = min(l.burst, b.tokens+float64(now.Sub(b.last))/float64(l.interval))
	b.last = now
	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	return false, time.Duration((1 - b.tokens) * float64(l.interval))
}

// pruneLocked drops buckets that have refilled completely: forgetting them
// changes nothing, because a new key starts with a full bucket.
func (l *Limiter) pruneLocked(now time.Time) {
	for key, b := range l.buckets {
		if b.tokens+float64(now.Sub(b.last))/float64(l.interval) >= l.burst {
			delete(l.buckets, key)
		}
	}
}
