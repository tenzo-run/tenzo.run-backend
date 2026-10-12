package ratelimit

import (
	"context"
	"sync"
	"time"
)

const maxKeys = 100_000

type Limiter struct {
	mu      sync.Mutex
	rate    float64
	burst   float64
	buckets map[string]*bucket
	now     func() time.Time
}

type bucket struct {
	tokens float64
	last   time.Time
}

func New(events int, per time.Duration) *Limiter {
	return &Limiter{
		rate:    float64(events) / per.Seconds(),
		burst:   float64(events),
		buckets: make(map[string]*bucket),
		now:     time.Now,
	}
}

func (l *Limiter) Allow(key string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()

	b, ok := l.buckets[key]
	if !ok {
		if len(l.buckets) >= maxKeys {
			l.evictFull(now)
			if len(l.buckets) >= maxKeys {
				return false, l.fullRefill()
			}
		}
		b = &bucket{tokens: l.burst, last: now}
		l.buckets[key] = b
	}

	b.tokens = min(l.burst, b.tokens+now.Sub(b.last).Seconds()*l.rate)
	b.last = now

	if b.tokens < 1 {
		wait := time.Duration((1 - b.tokens) / l.rate * float64(time.Second))
		return false, wait
	}

	b.tokens--
	return true, 0
}

func (l *Limiter) Run(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			l.mu.Lock()
			l.evictFull(l.now())
			l.mu.Unlock()
		}
	}
}

func (l *Limiter) evictFull(now time.Time) {
	refill := l.fullRefill()
	for key, b := range l.buckets {
		if now.Sub(b.last) >= refill {
			delete(l.buckets, key)
		}
	}
}

func (l *Limiter) fullRefill() time.Duration {
	return time.Duration(l.burst / l.rate * float64(time.Second))
}
