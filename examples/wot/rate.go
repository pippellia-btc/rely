package main

import (
	"context"
	"sync"
	"time"
)

type Limiter struct {
	mu      sync.RWMutex
	buckets map[string]*Bucket

	TimeToLive      time.Duration
	CleanupInterval time.Duration
}

type Bucket struct {
	mu          sync.Mutex
	tokens      int
	lastRequest time.Time
}

func NewLimiter(ctx context.Context) *Limiter {
	limiter := &Limiter{
		buckets:         make(map[string]*Bucket, 100),
		TimeToLive:      time.Hour,
		CleanupInterval: 24 * time.Hour,
	}

	go limiter.cleaner(ctx)
	return limiter
}

func (l *Limiter) Bucket(ID string) *Bucket {
	l.mu.RLock()
	b, exists := l.buckets[ID]
	l.mu.RUnlock()

	if !exists {
		l.mu.Lock()
		defer l.mu.Unlock()

		b = &Bucket{}
		l.buckets[ID] = b
	}

	return b
}

type Refill func(*Bucket)

func (l *Limiter) Allow(ID string, refills ...Refill) bool {
	return l.consume(ID, 1, refills...)
}

// consume or pay the specified cost with the associated bucket after having applied all refill rules.
func (l *Limiter) consume(ID string, cost int, refills ...Refill) bool {
	b := l.Bucket(ID)
	b.mu.Lock()
	defer b.mu.Unlock()

	for _, refill := range refills {
		refill(b)
	}

	if b.tokens < cost {
		return false
	}

	b.tokens -= cost
	b.lastRequest = time.Now()
	return true
}

// Clean scans through the buckets and removes the ones that are too old.
func (l *Limiter) Clean() {
	l.mu.Lock()
	defer l.mu.Unlock()

	for ID, b := range l.buckets {
		if time.Since(b.lastRequest) > l.TimeToLive {
			delete(l.buckets, ID)
		}
	}
}

func (l *Limiter) cleaner(ctx context.Context) {
	timer := time.NewTicker(l.CleanupInterval)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return

		case <-timer.C:
			l.Clean()
		}
	}
}
