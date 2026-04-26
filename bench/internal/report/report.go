// Package report formats benchmark results as text tables, markdown, and JSON.
package report

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ReliablyObserve/Loki-VL-proxy/bench/internal/histogram"
	"github.com/ReliablyObserve/Loki-VL-proxy/bench/internal/metricscrape"
	"github.com/ReliablyObserve/Loki-VL-proxy/bench/internal/runner"
)

// RunRecord holds a full benchmark run for one target × workload × concurrency.
type RunRecord struct {
	Timestamp    time.Time
	Version      string // optional version tag
	Target       string // "loki" | "proxy"
	TargetURL    string
	WorkloadName string
	Concurrency  int
	Duration     time.Duration
	Result       runner.Result
	ResourceBefore metricscrape.ResourceSnapshot
	ResourceAfter  metricscrape.ResourceSnapshot
	ResourceDelta  metricscrape.Delta
	// VL backend resource deltas captured alongside proxy runs.
	VLBefore metricscrape.ResourceSnapshot
	VLAfter  metricscrape.ResourceSnapshot
	VLDelta  metricscrape.Delta
}

// ComparisonRow holds Loki vs Proxy stats for one metric at one concurrency level.
type ComparisonRow struct {
	Workload    string
	Concurrency int
	Metric      string
	Loki        string
	Proxy       string
	Delta       string // proxy - loki or proxy/loki ratio
}

// WriteText writes a human-readable table to w.
func WriteText(w io.Writer, records []RunRecord) {
	// Group by workload × concurrency, then compare loki vs proxy.
	type key struct{ workload string; concurrency int }
	type pair struct{ loki, proxy *RunRecord }
	grouped := make(map[key]*pair)
	for i := range records {
		r := &records[i]
		k := key{r.WorkloadName, r.Concurrency}
		if _, ok := grouped[k]; !ok {
			grouped[k] = &pair{}
		}
		switch r.Target {
		case "loki":
			grouped[k].loki = r
		case "proxy":
			grouped[k].proxy = r
		}
	}

	// Sort keys.
	keys := make([]key, 0, len(grouped))
	for k := range grouped {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].workload != keys[j].workload {
			return keys[i].workload < keys[j].workload
		}
		return keys[i].concurrency < keys[j].concurrency
	})

	for _, k := range keys {
		p := grouped[k]
		fmt.Fprintf(w, "\n%s\n", strings.Repeat("═", 90))
		fmt.Fprintf(w, "  Workload: %-20s  Concurrency: %d clients\n", k.workload, k.concurrency)
		fmt.Fprintf(w, "%s\n", strings.Repeat("═", 90))
		fmt.Fprintf(w, "%-32s  %-20s  %-20s  %s\n", "Metric", "Loki (direct)", "VL + Proxy", "Δ Proxy vs Loki")
		fmt.Fprintf(w, "%s\n", strings.Repeat("─", 90))

		printRow := func(metric string, lv, pv string, delta string) {
			fmt.Fprintf(w, "%-32s  %-20s  %-20s  %s\n", metric, lv, pv, delta)
		}

		fmtDur := func(d time.Duration) string {
			if d < time.Millisecond {
				return fmt.Sprintf("%.1f µs", float64(d.Microseconds()))
			}
			return fmt.Sprintf("%.1f ms", float64(d.Milliseconds()))
		}
		fmtRate := func(r float64) string { return fmt.Sprintf("%.0f req/s", r) }
		fmtBytes := func(b float64) string {
			if b > 1e9 {
				return fmt.Sprintf("%.2f GB/s", b/1e9)
			}
			if b > 1e6 {
				return fmt.Sprintf("%.2f MB/s", b/1e6)
			}
			return fmt.Sprintf("%.2f KB/s", b/1e3)
		}
		fmtPct := func(v float64) string { return fmt.Sprintf("%.2f%%", v*100) }
		fmtMB := func(b float64) string { return fmt.Sprintf("%.1f MB", b/1e6) }
		na := "—"

		// Helper to compute delta string between loki and proxy durations.
		durDelta := func(l, p time.Duration) string {
			if l == 0 {
				return na
			}
			ratio := float64(p) / float64(l)
			sign := "+"
			if p < l {
				sign = ""
			}
			d := p - l
			return fmt.Sprintf("%s%s (%.2fx)", sign, fmtDur(d), ratio)
		}
		rateDelta := func(l, p float64) string {
			if l == 0 {
				return na
			}
			ratio := p / l
			sign := "+"
			if p < l {
				sign = ""
			}
			return fmt.Sprintf("%s%.0f req/s (%.2fx)", sign, p-l, ratio)
		}

		lStats := func() histogram.Stats {
			if p.loki != nil {
				return p.loki.Result.Overall
			}
			return histogram.Stats{}
		}()
		pStats := func() histogram.Stats {
			if p.proxy != nil {
				return p.proxy.Result.Overall
			}
			return histogram.Stats{}
		}()

		ls := func(d time.Duration) string {
			if p.loki == nil {
				return na
			}
			return fmtDur(d)
		}
		ps := func(d time.Duration) string {
			if p.proxy == nil {
				return na
			}
			return fmtDur(d)
		}
		dd := func(l, pp time.Duration) string {
			if p.loki == nil || p.proxy == nil {
				return na
			}
			return durDelta(l, pp)
		}

		// Throughput
		lokiRate, proxyRate := na, na
		if p.loki != nil {
			lokiRate = fmtRate(lStats.Throughput)
		}
		if p.proxy != nil {
			proxyRate = fmtRate(pStats.Throughput)
		}
		rDelta := na
		if p.loki != nil && p.proxy != nil {
			rDelta = rateDelta(lStats.Throughput, pStats.Throughput)
		}
		printRow("Throughput", lokiRate, proxyRate, rDelta)

		// Latencies
		printRow("P50 Latency", ls(lStats.P50), ps(pStats.P50), dd(lStats.P50, pStats.P50))
		printRow("P90 Latency", ls(lStats.P90), ps(pStats.P90), dd(lStats.P90, pStats.P90))
		printRow("P99 Latency", ls(lStats.P99), ps(pStats.P99), dd(lStats.P99, pStats.P99))
		printRow("P99.9 Latency", ls(lStats.P999), ps(pStats.P999), dd(lStats.P999, pStats.P999))
		printRow("Max Latency", ls(lStats.Max), ps(pStats.Max), dd(lStats.Max, pStats.Max))

		// Error rate
		lErr, pErr := na, na
		if p.loki != nil {
			lErr = fmtPct(lStats.ErrorRate)
		}
		if p.proxy != nil {
			pErr = fmtPct(pStats.ErrorRate)
		}
		printRow("Error Rate", lErr, pErr, na)

		// Network
		lBW, pBW := na, na
		if p.loki != nil {
			lBW = fmtBytes(lStats.BytesPerSec)
		}
		if p.proxy != nil {
			pBW = fmtBytes(pStats.BytesPerSec)
		}
		printRow("Response Bytes/s", lBW, pBW, na)

		// Resource deltas
		fmt.Fprintf(w, "%s\n", strings.Repeat("─", 90))
		fmt.Fprintf(w, "  Resource Usage During Run\n")
		fmt.Fprintf(w, "%s\n", strings.Repeat("─", 90))

		lCPU, pCPU := na, na
		if p.loki != nil {
			lCPU = fmt.Sprintf("%.3f cpu·s", p.loki.ResourceDelta.CPUSeconds)
		}
		if p.proxy != nil {
			pCPU = fmt.Sprintf("%.3f cpu·s", p.proxy.ResourceDelta.CPUSeconds)
		}
		printRow("CPU consumed", lCPU, pCPU, na)

		lMem, pMem := na, na
		if p.loki != nil {
			lMem = fmtMB(p.loki.ResourceDelta.MemRSSBytes)
		}
		if p.proxy != nil {
			pMem = fmtMB(p.proxy.ResourceDelta.MemRSSBytes)
		}
		printRow("RSS Memory (end)", lMem, pMem, na)

		lHeap, pHeap := na, na
		if p.loki != nil {
			lHeap = fmtMB(p.loki.ResourceDelta.HeapInUseBytes)
		}
		if p.proxy != nil {
			pHeap = fmtMB(p.proxy.ResourceDelta.HeapInUseBytes)
		}
		printRow("Heap In-Use (end)", lHeap, pHeap, na)

		lNet, pNet := na, na
		if p.loki != nil {
			lNet = fmt.Sprintf("rx:%.1fMB tx:%.1fMB",
				p.loki.ResourceDelta.NetRxBytes/1e6, p.loki.ResourceDelta.NetTxBytes/1e6)
		}
		if p.proxy != nil {
			pNet = fmt.Sprintf("rx:%.1fMB tx:%.1fMB",
				p.proxy.ResourceDelta.NetRxBytes/1e6, p.proxy.ResourceDelta.NetTxBytes/1e6)
		}
		printRow("Network I/O", lNet, pNet, na)

		// VL backend resource breakdown (captured alongside proxy runs).
		if p.proxy != nil && p.proxy.VLDelta.CPUSeconds > 0 {
			fmt.Fprintf(w, "%s\n", strings.Repeat("─", 90))
			fmt.Fprintf(w, "  VictoriaLogs Backend (during proxy run)\n")
			fmt.Fprintf(w, "%s\n", strings.Repeat("─", 90))
			printRow("VL CPU consumed", na, fmt.Sprintf("%.3f cpu·s", p.proxy.VLDelta.CPUSeconds), na)
			printRow("VL RSS Memory", na, fmtMB(p.proxy.VLDelta.MemRSSBytes), na)
			printRow("VL Heap In-Use", na, fmtMB(p.proxy.VLDelta.HeapInUseBytes), na)
			// Combined proxy+VL vs Loki.
			if p.loki != nil {
				combinedCPU := p.proxy.ResourceDelta.CPUSeconds + p.proxy.VLDelta.CPUSeconds
				combinedRSS := p.proxy.ResourceDelta.MemRSSBytes + p.proxy.VLDelta.MemRSSBytes
				lCPU := p.loki.ResourceDelta.CPUSeconds
				lRSS := p.loki.ResourceDelta.MemRSSBytes
				fmt.Fprintf(w, "%s\n", strings.Repeat("─", 90))
				fmt.Fprintf(w, "  Summary: Loki vs VL+Proxy Combined\n")
				fmt.Fprintf(w, "%s\n", strings.Repeat("─", 90))
				printRow("Total CPU (proxy+VL)",
					fmt.Sprintf("%.3f cpu·s", lCPU),
					fmt.Sprintf("%.3f cpu·s", combinedCPU),
					func() string {
						if lCPU == 0 { return na }
						return fmt.Sprintf("%.2fx less", lCPU/combinedCPU)
					}())
				printRow("Total RSS (proxy+VL)",
					fmtMB(lRSS),
					fmtMB(combinedRSS),
					func() string {
						if lRSS == 0 { return na }
						return fmt.Sprintf("%.2fx less", lRSS/combinedRSS)
					}())
			}
		}

		// Per-query breakdown
		if p.proxy != nil && len(p.proxy.Result.ByQuery) > 0 {
			fmt.Fprintf(w, "%s\n", strings.Repeat("─", 90))
			fmt.Fprintf(w, "  Per-Query Breakdown (proxy)\n")
			fmt.Fprintf(w, "%s\n", strings.Repeat("─", 90))
			fmt.Fprintf(w, "  %-36s  %10s  %10s  %10s  %8s\n", "Query", "P50", "P90", "P99", "req/s")
			names := make([]string, 0, len(p.proxy.Result.ByQuery))
			for n := range p.proxy.Result.ByQuery {
				names = append(names, n)
			}
			sort.Strings(names)
			for _, n := range names {
				s := p.proxy.Result.ByQuery[n]
				fmt.Fprintf(w, "  %-36s  %10s  %10s  %10s  %8.0f\n",
					n, fmtDur(s.P50), fmtDur(s.P90), fmtDur(s.P99), s.Throughput)
			}
		}
	}
	fmt.Fprintln(w)
}

// WriteJSON writes all records as a JSON array to path.
func WriteJSON(path string, records []RunRecord) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	return enc.Encode(records)
}

// WriteMarkdown writes a markdown summary table to path.
func WriteMarkdown(path string, records []RunRecord) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	fmt.Fprintf(f, "# loki-vl-proxy Read Performance Benchmark\n\n")
	fmt.Fprintf(f, "Generated: %s\n\n", time.Now().Format(time.RFC3339))

	// Group by target+workload, emit markdown tables.
	type key struct{ workload string; concurrency int }
	type pair struct{ loki, proxy *RunRecord }
	grouped := make(map[key]*pair)
	for i := range records {
		r := &records[i]
		k := key{r.WorkloadName, r.Concurrency}
		if _, ok := grouped[k]; !ok {
			grouped[k] = &pair{}
		}
		if r.Target == "loki" {
			grouped[k].loki = r
		} else {
			grouped[k].proxy = r
		}
	}

	keys := make([]key, 0, len(grouped))
	for k := range grouped {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].workload != keys[j].workload {
			return keys[i].workload < keys[j].workload
		}
		return keys[i].concurrency < keys[j].concurrency
	})

	fmtDur := func(d time.Duration) string {
		if d < time.Millisecond {
			return fmt.Sprintf("%.0fµs", float64(d.Microseconds()))
		}
		return fmt.Sprintf("%.0fms", float64(d.Milliseconds()))
	}

	for _, k := range keys {
		p := grouped[k]
		fmt.Fprintf(f, "## %s — %d clients\n\n", k.workload, k.concurrency)
		fmt.Fprintf(f, "| Metric | Loki (direct) | VL + Proxy | Δ |\n")
		fmt.Fprintf(f, "|--------|--------------|------------|---|\n")

		na := "—"
		ls, ps := histogram.Stats{}, histogram.Stats{}
		if p.loki != nil {
			ls = p.loki.Result.Overall
		}
		if p.proxy != nil {
			ps = p.proxy.Result.Overall
		}

		row := func(name, l, pp, d string) {
			fmt.Fprintf(f, "| %s | %s | %s | %s |\n", name, l, pp, d)
		}
		maybeRate := func(s histogram.Stats) string {
			if s.Count == 0 {
				return na
			}
			return fmt.Sprintf("%.0f req/s", s.Throughput)
		}
		maybeDur := func(d time.Duration) string {
			if d == 0 {
				return na
			}
			return fmtDur(d)
		}
		maybeRatio := func(l, p time.Duration) string {
			if l == 0 || p == 0 {
				return na
			}
			return fmt.Sprintf("%.2fx", float64(p)/float64(l))
		}

		row("Throughput", maybeRate(ls), maybeRate(ps), na)
		row("P50", maybeDur(ls.P50), maybeDur(ps.P50), maybeRatio(ls.P50, ps.P50))
		row("P90", maybeDur(ls.P90), maybeDur(ps.P90), maybeRatio(ls.P90, ps.P90))
		row("P99", maybeDur(ls.P99), maybeDur(ps.P99), maybeRatio(ls.P99, ps.P99))
		row("Error Rate", fmt.Sprintf("%.2f%%", ls.ErrorRate*100), fmt.Sprintf("%.2f%%", ps.ErrorRate*100), na)

		if p.loki != nil {
			row("CPU consumed", fmt.Sprintf("%.3f cpu·s", p.loki.ResourceDelta.CPUSeconds),
				func() string {
					if p.proxy != nil {
						return fmt.Sprintf("%.3f cpu·s", p.proxy.ResourceDelta.CPUSeconds)
					}
					return na
				}(), na)
			row("RSS Memory", fmt.Sprintf("%.0f MB", p.loki.ResourceDelta.MemRSSBytes/1e6),
				func() string {
					if p.proxy != nil {
						return fmt.Sprintf("%.0f MB", p.proxy.ResourceDelta.MemRSSBytes/1e6)
					}
					return na
				}(), na)
		}
		fmt.Fprintln(f)
	}

	return nil
}
