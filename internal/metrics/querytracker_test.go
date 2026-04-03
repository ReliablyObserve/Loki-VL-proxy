package metrics

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestQueryTracker_Record(t *testing.T) {
	qt := NewQueryTracker(100)
	qt.Record("query_range", `{app="nginx"} |= "error"`, 50*time.Millisecond, false)
	qt.Record("query_range", `{app="nginx"} |= "error"`, 100*time.Millisecond, false)
	qt.Record("query_range", `{app="api"} |= "timeout"`, 200*time.Millisecond, true)

	if qt.TotalQueries.Load() != 3 {
		t.Errorf("expected 3 total, got %d", qt.TotalQueries.Load())
	}
	if qt.size() != 2 {
		t.Errorf("expected 2 unique, got %d", qt.size())
	}
}

func TestQueryTracker_TopByFrequency(t *testing.T) {
	qt := NewQueryTracker(100)
	for range 10 {
		qt.Record("query_range", "query-a", 1*time.Millisecond, false)
	}
	for range 3 {
		qt.Record("query_range", "query-b", 1*time.Millisecond, false)
	}

	top := qt.TopByFrequency(5)
	if len(top) != 2 {
		t.Fatalf("expected 2 results, got %d", len(top))
	}
	if top[0].Query != "query-a" {
		t.Errorf("expected query-a first, got %q", top[0].Query)
	}
	if top[0].Count != 10 {
		t.Errorf("expected count=10, got %d", top[0].Count)
	}
}

func TestQueryTracker_TopByLatency(t *testing.T) {
	qt := NewQueryTracker(100)
	qt.Record("query", "fast", 1*time.Millisecond, false)
	qt.Record("query", "slow", 500*time.Millisecond, false)

	top := qt.TopByLatency(5)
	if top[0].Query != "slow" {
		t.Errorf("expected slow first, got %q", top[0].Query)
	}
}

func TestQueryTracker_TopByErrors(t *testing.T) {
	qt := NewQueryTracker(100)
	qt.Record("query", "good", 1*time.Millisecond, false)
	qt.Record("query", "bad", 1*time.Millisecond, true)
	qt.Record("query", "bad", 1*time.Millisecond, true)

	top := qt.TopByErrors(5)
	if top[0].Query != "bad" || top[0].Errors != 2 {
		t.Errorf("expected bad with 2 errors, got %q with %d", top[0].Query, top[0].Errors)
	}
}

func TestQueryTracker_Eviction(t *testing.T) {
	qt := NewQueryTracker(3)
	qt.Record("q", "a", time.Millisecond, false)
	qt.Record("q", "b", time.Millisecond, false)
	qt.Record("q", "c", time.Millisecond, false)
	qt.Record("q", "d", time.Millisecond, false) // should evict oldest

	if qt.size() > 3 {
		t.Errorf("expected max 3, got %d", qt.size())
	}
}

func TestQueryTracker_TopQueries(t *testing.T) {
	qt := NewQueryTracker(100)
	for range 5 {
		qt.Record("q", "popular", time.Millisecond, false)
	}
	qt.Record("q", "rare", time.Millisecond, false)

	top := qt.TopQueries(1)
	if len(top) != 1 || top[0] != "popular" {
		t.Errorf("expected [popular], got %v", top)
	}
}

func TestQueryTracker_Handler(t *testing.T) {
	qt := NewQueryTracker(100)
	qt.Record("query_range", "test-query", 50*time.Millisecond, false)

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/debug/queries", nil)
	qt.Handler(w, r)

	if w.Code != 200 {
		t.Errorf("expected 200, got %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "total_queries") {
		t.Error("expected total_queries in response")
	}
	if !strings.Contains(body, "test-query") {
		t.Error("expected test-query in response")
	}
}
