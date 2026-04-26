// Package runner executes concurrent query workloads against a target endpoint.
package runner

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/ReliablyObserve/Loki-VL-proxy/bench/internal/histogram"
	"github.com/ReliablyObserve/Loki-VL-proxy/bench/internal/workload"
)

// Config controls a single benchmark run.
type Config struct {
	TargetURL   string
	Concurrency int
	Duration    time.Duration
	Queries     []workload.Query
	Verbose     bool
}

// Result holds the outcome of one benchmark run.
type Result struct {
	Target      string
	Workload    string
	Concurrency int
	Duration    time.Duration
	// Per-query stats keyed by query name.
	ByQuery map[string]*histogram.Stats
	// Aggregate across all queries.
	Overall histogram.Stats
}

var httpClient = &http.Client{
	Timeout: 120 * time.Second,
	Transport: &http.Transport{
		MaxIdleConns:        512,
		MaxIdleConnsPerHost: 512,
		MaxConnsPerHost:     0,
		IdleConnTimeout:     90 * time.Second,
		DisableCompression:  false,
	},
}

// Run executes the benchmark and returns aggregated results.
func Run(ctx context.Context, cfg Config) Result {
	if len(cfg.Queries) == 0 {
		return Result{}
	}

	type sample struct {
		name      string
		latency   time.Duration
		bytes     int64
		isErr     bool
	}

	samples := make(chan sample, cfg.Concurrency*4)
	var wg sync.WaitGroup

	deadline := time.Now().Add(cfg.Duration)

	// Spawn workers.
	for i := range cfg.Concurrency {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			qi := workerID % len(cfg.Queries) // round-robin query selection
			for {
				if ctx.Err() != nil || time.Now().After(deadline) {
					return
				}
				q := cfg.Queries[qi%len(cfg.Queries)]
				qi++

				url := q.URL(cfg.TargetURL)
				start := time.Now()
				n, err := doRequest(url)
				elapsed := time.Since(start)

				samples <- sample{
					name:    q.Name,
					latency: elapsed,
					bytes:   n,
					isErr:   err != nil,
				}
			}
		}(i)
	}

	// Close samples channel when all workers finish.
	go func() {
		wg.Wait()
		close(samples)
	}()

	// Collect into per-query histograms.
	hists := make(map[string]*histogram.Histogram)
	overall := histogram.New()

	for s := range samples {
		h, ok := hists[s.name]
		if !ok {
			h = histogram.New()
			hists[s.name] = h
		}
		h.Record(s.latency, s.bytes, s.isErr)
		overall.Record(s.latency, s.bytes, s.isErr)
	}

	byQuery := make(map[string]*histogram.Stats, len(hists))
	queryDuration := cfg.Duration // approximate — each query ran for cfg.Duration total
	for name, h := range hists {
		snap := h.Snapshot(queryDuration)
		byQuery[name] = &snap
	}
	overallSnap := overall.Snapshot(cfg.Duration)

	return Result{
		Target:      cfg.TargetURL,
		Workload:    "",
		Concurrency: cfg.Concurrency,
		Duration:    cfg.Duration,
		ByQuery:     byQuery,
		Overall:     overallSnap,
	}
}

func doRequest(url string) (int64, error) {
	resp, err := httpClient.Get(url)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	n, err := io.Copy(io.Discard, resp.Body)
	if err != nil {
		return n, err
	}
	if resp.StatusCode >= 500 {
		return n, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return n, nil
}
