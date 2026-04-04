package proxy

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/szibis/Loki-VL-proxy/internal/cache"
)

// =============================================================================
// Performance: high-concurrency request handling
// =============================================================================

func BenchmarkProxy_QueryRange_CacheHit(b *testing.B) {
	vlBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"_time":"2024-01-15T10:30:00Z","_msg":"test","app":"nginx"}` + "\n"))
	}))
	defer vlBackend.Close()

	c := cache.New(60*time.Second, 10000)
	p, _ := New(Config{BackendURL: vlBackend.URL, Cache: c, LogLevel: "error"})

	// Warm cache
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", `/loki/api/v1/query_range?query={app="nginx"}&start=1&end=2&step=1`, nil)
	p.handleQueryRange(w, r)

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			w := httptest.NewRecorder()
			r := httptest.NewRequest("GET", `/loki/api/v1/query_range?query={app="nginx"}&start=1&end=2&step=1`, nil)
			p.handleQueryRange(w, r)
		}
	})
}

func BenchmarkProxy_Labels_CacheHit(b *testing.B) {
	vlBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"values": []map[string]interface{}{
				{"value": "app", "hits": 100},
				{"value": "namespace", "hits": 50},
			},
		})
	}))
	defer vlBackend.Close()

	c := cache.New(60*time.Second, 10000)
	p, _ := New(Config{BackendURL: vlBackend.URL, Cache: c, LogLevel: "error"})

	// Warm cache
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/loki/api/v1/labels", nil)
	p.handleLabels(w, r)

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			w := httptest.NewRecorder()
			r := httptest.NewRequest("GET", "/loki/api/v1/labels", nil)
			p.handleLabels(w, r)
		}
	})
}

// =============================================================================
// Load test: sustained high-concurrency with resource monitoring
// =============================================================================

func TestLoad_HighConcurrency_MemoryStability(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping load test in short mode")
	}

	var requestCount atomic.Int64
	vlBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"values": []map[string]interface{}{
				{"value": "app", "hits": 100},
			},
		})
	}))
	defer vlBackend.Close()

	c := cache.New(60*time.Second, 10000)
	p, err := New(Config{BackendURL: vlBackend.URL, Cache: c, LogLevel: "error"})
	if err != nil {
		t.Fatal(err)
	}

	// Baseline memory
	runtime.GC()
	var memBefore runtime.MemStats
	runtime.ReadMemStats(&memBefore)

	// Run 100 concurrent goroutines, 1000 requests each
	concurrency := 100
	requestsPerGoroutine := 1000
	var wg sync.WaitGroup

	start := time.Now()
	for g := 0; g < concurrency; g++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < requestsPerGoroutine; i++ {
				w := httptest.NewRecorder()
				r := httptest.NewRequest("GET",
					fmt.Sprintf("/loki/api/v1/labels?start=%d&end=%d", i, i+1), nil)
				p.handleLabels(w, r)
			}
		}(g)
	}
	wg.Wait()
	elapsed := time.Since(start)

	// Post-run memory
	runtime.GC()
	var memAfter runtime.MemStats
	runtime.ReadMemStats(&memAfter)

	totalRequests := concurrency * requestsPerGoroutine
	rps := float64(totalRequests) / elapsed.Seconds()
	memGrowthMB := float64(memAfter.Alloc-memBefore.Alloc) / 1024 / 1024

	t.Logf("Load test results:")
	t.Logf("  Total requests: %d", totalRequests)
	t.Logf("  Concurrency: %d", concurrency)
	t.Logf("  Duration: %s", elapsed)
	t.Logf("  Throughput: %.0f req/s", rps)
	t.Logf("  Backend calls: %d (cache effectiveness: %.1f%%)",
		requestCount.Load(), 100*(1-float64(requestCount.Load())/float64(totalRequests)))
	t.Logf("  Memory growth: %.1f MB (before: %.1f MB, after: %.1f MB)",
		memGrowthMB, float64(memBefore.Alloc)/1024/1024, float64(memAfter.Alloc)/1024/1024)
	t.Logf("  GC cycles: %d", memAfter.NumGC-memBefore.NumGC)

	// Assertions
	if rps < 10000 {
		t.Errorf("throughput too low: %.0f req/s (expected >10,000)", rps)
	}
	// Memory growth should be bounded — not linearly growing with request count
	if memGrowthMB > 100 {
		t.Errorf("memory growth too high: %.1f MB (expected <100 MB for %d requests)", memGrowthMB, totalRequests)
	}
}

func TestLoad_CacheMiss_BackendPressure(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping load test in short mode")
	}

	var backendCalls atomic.Int64
	vlBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backendCalls.Add(1)
		// Simulate slow backend
		time.Sleep(1 * time.Millisecond)
		w.Write([]byte(`{"_time":"2024-01-15T10:30:00Z","_msg":"test","app":"nginx"}` + "\n"))
	}))
	defer vlBackend.Close()

	c := cache.New(1*time.Millisecond, 10) // Very short TTL, small cache → many misses
	p, err := New(Config{BackendURL: vlBackend.URL, Cache: c, LogLevel: "error"})
	if err != nil {
		t.Fatal(err)
	}

	concurrency := 50
	requestsPerGoroutine := 100
	var wg sync.WaitGroup

	start := time.Now()
	for g := 0; g < concurrency; g++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < requestsPerGoroutine; i++ {
				w := httptest.NewRecorder()
				// Unique query per request → all cache misses
				r := httptest.NewRequest("GET",
					fmt.Sprintf(`/loki/api/v1/query_range?query={app="nginx-%d"}&start=1&end=2&step=1`, i), nil)
				p.handleQueryRange(w, r)
			}
		}(g)
	}
	wg.Wait()
	elapsed := time.Since(start)

	totalRequests := concurrency * requestsPerGoroutine
	t.Logf("Cache-miss load test:")
	t.Logf("  Total requests: %d", totalRequests)
	t.Logf("  Duration: %s", elapsed)
	t.Logf("  Throughput: %.0f req/s", float64(totalRequests)/elapsed.Seconds())
	t.Logf("  Backend calls: %d", backendCalls.Load())

	// All requests should complete without panic or deadlock
	if backendCalls.Load() == 0 {
		t.Error("expected backend calls with cache misses")
	}
}

// =============================================================================
// Benchmark: response conversion
// =============================================================================

func BenchmarkVLLogsToLokiStreams(b *testing.B) {
	// Simulate 100 VL NDJSON log lines
	var body []byte
	for i := 0; i < 100; i++ {
		line := fmt.Sprintf(`{"_time":"2024-01-15T10:30:%02d.000Z","_msg":"request %d processed","_stream":"{app=\"nginx\",namespace=\"prod\"}","app":"nginx","namespace":"prod","level":"info"}`, i%60, i)
		body = append(body, []byte(line+"\n")...)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		vlLogsToLokiStreams(body)
	}
}

func BenchmarkWrapAsLokiResponse(b *testing.B) {
	body := []byte(`{"results":[{"metric":{"app":"nginx"},"values":[[1705312200,"42"],[1705312260,"43"]]}]}`)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		wrapAsLokiResponse(body, "matrix")
	}
}
