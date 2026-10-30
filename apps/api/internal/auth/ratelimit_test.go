package auth

import (
	"strconv"
	"testing"
	"time"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time          { return c.t }
func (c *clock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newTestLimiter(burst int, interval time.Duration) (*Limiter, *clock) {
	c := &clock{t: time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC)}
	l := NewLimiter(burst, interval)
	l.now = c.now
	return l, c
}

func TestLimiterAllowsABurstThenRefills(t *testing.T) {
	l, c := newTestLimiter(3, 10*time.Second)

	for i := range 3 {
		if ok, _ := l.Allow("1.2.3.4"); !ok {
			t.Fatalf("attempt %d of the burst was refused", i+1)
		}
	}
	ok, wait := l.Allow("1.2.3.4")
	if ok {
		t.Fatal("attempt beyond the burst was allowed")
	}
	if wait <= 0 || wait > 10*time.Second {
		t.Errorf("retry after %v, want within one interval", wait)
	}

	c.advance(10 * time.Second)
	if ok, _ := l.Allow("1.2.3.4"); !ok {
		t.Error("no attempt regained after one interval")
	}
	if ok, _ := l.Allow("1.2.3.4"); ok {
		t.Error("more than one attempt regained after one interval")
	}
}

func TestLimiterKeysAreIndependent(t *testing.T) {
	l, _ := newTestLimiter(1, time.Minute)
	if ok, _ := l.Allow("a"); !ok {
		t.Fatal("first attempt for a refused")
	}
	if ok, _ := l.Allow("b"); !ok {
		t.Error("b was limited by a's attempts")
	}
}

func TestLimiterForgetsIdleKeys(t *testing.T) {
	l, c := newTestLimiter(1, time.Second)
	for i := range maxIdleKeys {
		l.Allow(strconv.Itoa(i))
	}
	c.advance(time.Minute) // every bucket has refilled
	l.Allow("one more")
	if n := len(l.buckets); n > 1 {
		t.Errorf("%d buckets kept after pruning, want only the new one", n)
	}
}
