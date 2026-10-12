package ratelimit

import (
	"testing"
	"time"
)

func TestLimiter(t *testing.T) {
	now := time.Unix(0, 0)
	l := New(3, time.Minute)
	l.now = func() time.Time { return now }

	for i := range 3 {
		if ok, _ := l.Allow("k"); !ok {
			t.Fatalf("attempt %d denied", i+1)
		}
	}

	ok, wait := l.Allow("k")
	if ok || wait <= 0 || wait > 20*time.Second {
		t.Fatalf("4th attempt: ok=%v wait=%v", ok, wait)
	}

	if ok, _ := l.Allow("other"); !ok {
		t.Fatal("keys must be independent")
	}

	now = now.Add(20 * time.Second)
	if ok, _ := l.Allow("k"); !ok {
		t.Fatal("token should be refilled")
	}

	now = now.Add(time.Hour)
	l.evictFull(now)
	if len(l.buckets) != 0 {
		t.Fatalf("idle buckets were not evicted: %d", len(l.buckets))
	}
}
