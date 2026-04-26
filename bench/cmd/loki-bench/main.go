// loki-bench: read-path performance comparison between Loki (direct),
// VictoriaLogs via loki-vl-proxy, and VictoriaLogs native LogsQL API.
// Measures throughput, latency percentiles, CPU/memory overhead, and
// network efficiency across configurable concurrency levels and workloads.
//
// Usage:
//
//	loki-bench \
//	  --loki=http://localhost:3101 \
//	  --proxy=http://localhost:3100 \
//	  --vl-direct=http://localhost:9428 \
//	  --loki-metrics=http://localhost:3101/metrics \
//	  --proxy-metrics=http://localhost:3100/metrics \
//	  --workloads=small,heavy,long_range \
//	  --clients=10,50,100,500 \
//	  --duration=30s \
//	  --output=results/
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ReliablyObserve/Loki-VL-proxy/bench/internal/metricscrape"
	"github.com/ReliablyObserve/Loki-VL-proxy/bench/internal/report"
	"github.com/ReliablyObserve/Loki-VL-proxy/bench/internal/runner"
	"github.com/ReliablyObserve/Loki-VL-proxy/bench/internal/workload"
)

func main() {
	var (
		lokiURL      = flag.String("loki", "http://localhost:3101", "Loki direct API base URL")
		proxyURL     = flag.String("proxy", "http://localhost:3100", "loki-vl-proxy base URL")
		vlURL        = flag.String("vl", "", "VictoriaLogs API base URL (optional; for resource tracking)")
		vlDirectURL  = flag.String("vl-direct", "", "VictoriaLogs native LogsQL API URL (optional; enables 3-way comparison)")
		lokiMetrics  = flag.String("loki-metrics", "", "Loki /metrics URL for resource tracking (optional)")
		proxyMetrics = flag.String("proxy-metrics", "", "Proxy /metrics URL for resource tracking (optional)")
		vlMetrics    = flag.String("vl-metrics", "", "VictoriaLogs /metrics URL for resource tracking (optional)")
		workloadList = flag.String("workloads", "small,heavy,long_range", "Comma-separated workloads: small,heavy,long_range")
		clientList   = flag.String("clients", "10,50,100,500", "Comma-separated concurrency levels")
		duration     = flag.Duration("duration", 30*time.Second, "Test duration per concurrency level per workload")
		outputDir    = flag.String("output", "results", "Output directory for JSON and markdown reports")
		warmup       = flag.Duration("warmup", 5*time.Second, "Warmup duration before each run (warms proxy cache)")
		skipLoki     = flag.Bool("skip-loki", false, "Skip Loki target (benchmark proxy only)")
		skipProxy    = flag.Bool("skip-proxy", false, "Skip proxy target (benchmark Loki only)")
		skipVLDirect = flag.Bool("skip-vl-direct", false, "Skip VL-direct target (LogsQL native benchmark)")
		verbose      = flag.Bool("verbose", false, "Print per-request errors")
		version      = flag.String("version", "", "Version tag attached to results (e.g. v1.17.1)")
	)
	flag.Parse()

	concurrencies, err := parseInts(*clientList)
	if err != nil {
		fatalf("--clients: %v", err)
	}
	workloadNames := splitTrim(*workloadList)
	now := time.Now()
	workloads := workload.ByName(workloadNames, now)
	if len(workloads) == 0 {
		fatalf("no matching workloads (available: small,heavy,long_range)")
	}

	ctx := context.Background()

	// VL-native workloads use LogsQL syntax and VL-specific endpoints.
	vlWorkloads := workload.VLByName(workloadNames, now)

	var records []report.RunRecord

	for i, wl := range workloads {
		for _, conc := range concurrencies {
			// vlMetricsURL is scraped alongside proxy runs to show VL backend resource impact.
			vlMetricsURL := *vlMetrics
			if vlMetricsURL == "" && *vlURL != "" {
				vlMetricsURL = *vlURL + "/metrics"
			}
			// For vl_direct runs, we also scrape VL metrics (it IS the target).
			vlDirectMetrics := vlMetricsURL
			if vlDirectMetrics == "" && *vlDirectURL != "" {
				vlDirectMetrics = *vlDirectURL + "/metrics"
			}

			// VL-direct workload queries (LogsQL) for this workload slot.
			var vlDirectQueries []workload.Query
			if i < len(vlWorkloads) {
				vlDirectQueries = vlWorkloads[i].Queries
			}

			type target struct {
				name       string
				url        string
				queries    []workload.Query
				metricsURL string
				vlMetrics  string
				skip       bool
			}
			targets := []target{
				{"loki", *lokiURL, wl.Queries, *lokiMetrics, "", *skipLoki},
				{"proxy", *proxyURL, wl.Queries, *proxyMetrics, vlMetricsURL, *skipProxy},
				{"vl_direct", *vlDirectURL, vlDirectQueries, vlDirectMetrics, "", *skipVLDirect || *vlDirectURL == ""},
			}

			for _, tgt := range targets {
				if tgt.skip || tgt.url == "" || len(tgt.queries) == 0 {
					continue
				}

				fmt.Printf("\n▶ workload=%-12s  concurrency=%4d  target=%s\n",
					wl.Name, conc, tgt.name)

				// Warmup phase (warms caches, esp. proxy window cache).
				if *warmup > 0 {
					fmt.Printf("  warming up for %s...\n", *warmup)
					wCfg := runner.Config{
						TargetURL:   tgt.url,
						Concurrency: min(conc, 10),
						Duration:    *warmup,
						Queries:     tgt.queries,
					}
					runner.Run(ctx, wCfg) // discard warmup result
				}

				// Snapshot before (target + VL backend if configured).
				var resBefore, vlBefore metricscrape.ResourceSnapshot
				if tgt.metricsURL != "" {
					resBefore, err = metricscrape.Scrape(tgt.metricsURL)
					if err != nil {
						fmt.Fprintf(os.Stderr, "  warn: resource scrape before: %v\n", err)
					}
				}
				if tgt.vlMetrics != "" {
					vlBefore, err = metricscrape.Scrape(tgt.vlMetrics)
					if err != nil {
						fmt.Fprintf(os.Stderr, "  warn: vl resource scrape before: %v\n", err)
					}
				}

				// Benchmark run.
				fmt.Printf("  running %s (concurrency=%d duration=%s)...\n", tgt.name, conc, *duration)
				cfg := runner.Config{
					TargetURL:   tgt.url,
					Concurrency: conc,
					Duration:    *duration,
					Queries:     tgt.queries,
					Verbose:     *verbose,
				}
				result := runner.Run(ctx, cfg)
				result.Workload = wl.Name

				// Snapshot after.
				var resAfter, vlAfter metricscrape.ResourceSnapshot
				if tgt.metricsURL != "" {
					resAfter, err = metricscrape.Scrape(tgt.metricsURL)
					if err != nil {
						fmt.Fprintf(os.Stderr, "  warn: resource scrape after: %v\n", err)
					}
				}
				if tgt.vlMetrics != "" {
					vlAfter, err = metricscrape.Scrape(tgt.vlMetrics)
					if err != nil {
						fmt.Fprintf(os.Stderr, "  warn: vl resource scrape after: %v\n", err)
					}
				}
				delta := resBefore.Delta(resAfter)
				vlDelta := vlBefore.Delta(vlAfter)

				// Print quick summary.
				s := result.Overall
				fmt.Printf("  ✓ throughput=%.0f req/s  p50=%s  p90=%s  p99=%s  errors=%.2f%%  bytes=%.1f KB/req\n",
					s.Throughput,
					fmtDur(s.P50), fmtDur(s.P90), fmtDur(s.P99),
					s.ErrorRate*100,
					float64(s.TotalBytes)/float64(max(s.Count, 1))/1e3,
				)
				if tgt.metricsURL != "" {
					fmt.Printf("  ✓ cpu=%.3f s  rss=%.0f MB  heap=%.0f MB  gc_cycles=%.0f\n",
						delta.CPUSeconds, delta.MemRSSBytes/1e6, delta.HeapInUseBytes/1e6, delta.GCCycles)
				}
				if tgt.vlMetrics != "" {
					fmt.Printf("  ✓ vl backend: cpu=%.3f s  rss=%.0f MB  heap=%.0f MB\n",
						vlDelta.CPUSeconds, vlDelta.MemRSSBytes/1e6, vlDelta.HeapInUseBytes/1e6)
				}

				records = append(records, report.RunRecord{
					Timestamp:      now,
					Version:        *version,
					Target:         tgt.name,
					TargetURL:      tgt.url,
					WorkloadName:   wl.Name,
					Concurrency:    conc,
					Duration:       *duration,
					Result:         result,
					ResourceBefore: resBefore,
					ResourceAfter:  resAfter,
					ResourceDelta:  delta,
					VLBefore:       vlBefore,
					VLAfter:        vlAfter,
					VLDelta:        vlDelta,
				})
			}
		}
	}

	// Write outputs.
	fmt.Printf("\n%s\n", strings.Repeat("═", 90))
	report.WriteText(os.Stdout, records)

	ts := now.Format("2006-01-02T15-04-05")
	jsonPath := filepath.Join(*outputDir, fmt.Sprintf("bench-%s.json", ts))
	mdPath := filepath.Join(*outputDir, fmt.Sprintf("bench-%s.md", ts))

	if err := report.WriteJSON(jsonPath, records); err != nil {
		fmt.Fprintf(os.Stderr, "warn: write JSON: %v\n", err)
	} else {
		fmt.Printf("JSON results: %s\n", jsonPath)
	}
	if err := report.WriteMarkdown(mdPath, records); err != nil {
		fmt.Fprintf(os.Stderr, "warn: write markdown: %v\n", err)
	} else {
		fmt.Printf("Markdown results: %s\n", mdPath)
	}
}

func parseInts(s string) ([]int, error) {
	parts := splitTrim(s)
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return nil, fmt.Errorf("not an integer: %q", p)
		}
		out = append(out, n)
	}
	return out, nil
}

func splitTrim(s string) []string {
	parts := strings.Split(s, ",")
	out := parts[:0]
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}

func fmtDur(d time.Duration) string {
	if d == 0 {
		return "—"
	}
	if d < time.Millisecond {
		return fmt.Sprintf("%.0fµs", float64(d.Microseconds()))
	}
	return fmt.Sprintf("%.1fms", float64(d.Milliseconds()))
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "loki-bench: "+format+"\n", args...)
	os.Exit(1)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
