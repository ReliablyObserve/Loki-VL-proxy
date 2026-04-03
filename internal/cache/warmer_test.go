package cache

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestWarmer_WarmsTopQueries(t *testing.T) {
	c := New(1*time.Hour, 100)
	defer c.Close()

	var warmCalls atomic.Int32

	topN := func(n int) []string {
		return []string{"query-a", "query-b"}
	}
	warmFn := func(ctx context.Context, query string) ([]byte, error) {
		warmCalls.Add(1)
		return []byte("result-" + query), nil
	}

	w := NewWarmer(c, topN, warmFn, WarmerConfig{
		Interval: 50 * time.Millisecond,
		Count:    5,
	})
	w.Start()
	defer w.Stop()

	time.Sleep(120 * time.Millisecond)

	if warmCalls.Load() < 2 {
		t.Errorf("expected at least 2 warm calls, got %d", warmCalls.Load())
	}
	if w.WarmedTotal.Load() < 2 {
		t.Errorf("expected at least 2 warmed, got %d", w.WarmedTotal.Load())
	}

	// Verify cached
	v, ok := c.Get("query-a")
	if !ok || string(v) != "result-query-a" {
		t.Errorf("expected cached result, got %q %v", v, ok)
	}
}

func TestWarmer_SkipsCachedQueries(t *testing.T) {
	c := New(1*time.Hour, 100)
	defer c.Close()

	// Pre-populate cache
	c.Set("query-a", []byte("existing"))

	var warmCalls atomic.Int32

	topN := func(n int) []string {
		return []string{"query-a"}
	}
	warmFn := func(ctx context.Context, query string) ([]byte, error) {
		warmCalls.Add(1)
		return []byte("new"), nil
	}

	w := NewWarmer(c, topN, warmFn, WarmerConfig{
		Interval: 50 * time.Millisecond,
		Count:    5,
	})
	w.Start()
	defer w.Stop()

	time.Sleep(120 * time.Millisecond)

	if warmCalls.Load() != 0 {
		t.Errorf("expected 0 warm calls (already cached), got %d", warmCalls.Load())
	}
}

func TestWarmer_Stop(t *testing.T) {
	c := New(1*time.Hour, 100)
	defer c.Close()

	w := NewWarmer(c, func(int) []string { return nil }, nil, WarmerConfig{Interval: time.Hour})
	w.Start()
	w.Stop()
	w.Stop() // double stop should not panic
}
