# Benchmarks

Measured on Apple M3 Max (14 cores), Go 1.26.2, `-benchmem`.

## Per-Request Latency

| Operation | Latency | Allocs | Bytes/op | Notes |
|---|---|---|---|---|
| Labels (cache hit) | 2.0 us | 25 | 6.6 KB | Serve from in-memory cache |
| QueryRange (cache hit) | 118 us | 600 | 142 KB | Query translation + cache lookup |
| wrapAsLokiResponse | 2.8 us | 58 | 2.6 KB | JSON re-envelope |
| VL NDJSON to Loki streams (100 lines) | 170 us | 3118 | 70 KB | Parse + group + convert (pooled) |
| LogQL translation | ~5 us | ~20 | ~2 KB | String manipulation (no AST) |

## Throughput

| Scenario | Requests | Concurrency | Throughput | Cache Hit % | Memory Growth |
|---|---|---|---|---|---|
| Labels (cache hit) | 100,000 | 100 | 175,726 req/s | 98.2% | 0.5 MB |
| QueryRange (cache miss, 1ms backend) | 5,000 | 50 | 12,976 req/s | 0% | - |

## Scaling Profile (No Cache — Raw Proxy Overhead)

| Profile | Requests | Concurrency | Throughput | Avg Latency | Total Alloc | Live Heap | Errors |
|---|---|---|---|---|---|---|---|
| low (100 rps) | 1,000 | 10 | 8,062 req/s | 124 us | 136 MB | 0.9 MB | 0 |
| medium (1K rps) | 5,000 | 50 | 12,465 req/s | 80 us | 572 MB | 1.3 MB | 0 |
| high (10K rps) | 20,000 | 200 | 39,057 req/s | 26 us | 1,331 MB | 8.7 MB | 0 |

Key observations:
- **Live heap stays &lt;10 MB** even at 20K requests — GC keeps up
- **Total alloc is high** (~70 KB/request) due to JSON parse/serialize — this is GC pressure, not leak
- **No errors** at 200 concurrent connections (after connection pool tuning)

## Scaling Profile (With Cache)

| Profile | Requests | Concurrency | Throughput | Avg Latency | Live Heap |
|---|---|---|---|---|---|
| low (100 rps) | 1,000 | 10 | 8,207 req/s | 122 us | 1.1 MB |
| medium (1K rps) | 5,000 | 50 | 12,821 req/s | 78 us | 1.1 MB |

Cache provides marginal throughput improvement but dramatically reduces backend load (98%+ hit rate).

## Resource Usage at Scale

Measured from load tests (proxy overhead only, excludes network I/O):

| Load (req/s) | CPU (single core) | Memory (steady state) | Notes |
|---|---|---|---|
| 100 | &lt;1% | ~10 MB | Idle, mostly cache hits |
| 1,000 | ~8% | ~20 MB | Mix of cache hits/misses |
| 10,000 | ~30% | ~50 MB | Significant cache miss rate, backend-bound |
| 40,000+ | ~100% | ~100 MB | CPU-bound, needs horizontal scaling |

The proxy is CPU-bound at high load. Memory usage is stable — the cache has a fixed maximum size (configurable via `-cache-max`). Scaling strategy:

- **< 1,000 req/s**: Single replica, 100m CPU, 128Mi memory
- **1,000-10,000 req/s**: 2-3 replicas with HPA on CPU
- **> 10,000 req/s**: HPA with 5+ replicas, tune `cache-max` for hit rate

## Connection Pool Tuning

The proxy's HTTP transport is tuned for high-concurrency single-backend proxying:

```go
transport.MaxIdleConns = 256         // total idle connections
transport.MaxIdleConnsPerHost = 256  // all slots for VL (single backend)
transport.MaxConnsPerHost = 0        // unlimited concurrent connections
transport.IdleConnTimeout = 90s     // reuse connections
```

Go's defaults (`MaxIdleConnsPerHost=2`) cause ephemeral port exhaustion at >50 concurrent requests. Our tuning eliminates this — tested clean at 200 concurrency, 33K req/s.

## Known Hot Paths

1. **VL NDJSON to Loki streams** (3118 allocs/100 lines, down from 3417): Optimized with byte scanning (no `strings.Split`), `sync.Pool` for JSON entry maps, pre-allocated slice estimates. **49% memory reduction** from original. Remaining allocs are from `json.Unmarshal` internals — further gains need a custom tokenizer.

2. **QueryRange cache hit** (600 allocs/request): Even on cache hit, response bytes are re-parsed and re-serialized. Serving raw cached bytes would eliminate this overhead.

## Running Benchmarks

```bash
# All proxy benchmarks
go test ./internal/proxy/ -bench . -benchmem -run "^$" -count=3

# Translator benchmarks
go test ./internal/translator/ -bench . -benchmem -run "^$" -count=3

# Cache benchmarks
go test ./internal/cache/ -bench . -benchmem -run "^$" -count=3

# Load tests (requires no -short flag)
go test ./internal/proxy/ -run "TestLoad" -v -timeout=60s

# Profile CPU
go test ./internal/proxy/ -bench BenchmarkVLLogsToLokiStreams -cpuprofile=cpu.prof
go tool pprof cpu.prof

# Profile memory
go test ./internal/proxy/ -bench BenchmarkVLLogsToLokiStreams -memprofile=mem.prof
go tool pprof mem.prof
```

## Loki vs VL + Proxy — End-to-End Read Path Comparison

Results from `loki-bench` run against the e2e-compat stack (Loki 3.7.1, VictoriaLogs v1.50.0,
loki-vl-proxy on the same host). 15s per level, 5s warmup. Proxy metrics from
`/metrics`. Loki metrics from `/metrics`. Platform: Apple M3 Max (Docker containers).

### Small workload — labels, label values, series, log select, instant query, detected\_fields

| Clients | Loki req/s | Proxy req/s | Speedup | Loki P50 | Proxy P50 | Proxy P99 |
|---:|---:|---:|---:|---:|---:|---:|
| 10 | 1,373 | 16,126 | **11.7x** | 7ms | 377µs | 1ms |
| 50 | 1,371 | 35,507 | **25.9x** | 37ms | 1ms | 3ms |
| 100 | 1,351 | 39,064 | **28.9x** | 77ms | 2ms | 6ms |

Proxy resource consumption at 100 clients: `0.23 cpu·s` consumed / `238 MB` RSS / `165 MB` heap.

### Heavy workload — JSON parse, logfmt, multi-stage pipeline, metric aggregations, 30m–1h windows

| Clients | Loki req/s | Proxy req/s | Speedup | Loki P50 | Proxy P50 | Proxy P99 |
|---:|---:|---:|---:|---:|---:|---:|
| 10 | 26 | 22,396 | **866x** | 274ms | 382µs | 1ms |
| 50 | 23 | 38,330 | **1,662x** | 1,525ms | 1ms | 4ms |
| 100 | 46 | 47,272 | **1,032x** | 1,144ms | 1ms | 5ms |

Loki P99 at 50 clients: 5,033ms. Heavy queries include `count_over_time`, `rate`, `bytes_rate`,
`| json | line_format`, and `| logfmt | label_format` over 30m–1h windows.

### Long-range workload — 6h/24h/48h windows, cold cache pass

Real Loki resource metrics captured via `/metrics` before/after the 20s run.

| Clients | Loki req/s | Proxy req/s | Loki CPU·s | Proxy CPU·s | Loki RSS | Proxy RSS |
|---:|---:|---:|---:|---:|---:|---:|
| 10 | 11 | 24,884 | **101.9** | 0.048 | **1,097 MB** | 69 MB |
| 50 | 12 | 42,182 | **107.1** | 0.079 | **1,135 MB** | 71 MB |

**CPU ratio: ~2,200x.** Loki burned over 5× real-time CPU in a 20-second window scanning
6h/24h/48h windows. The proxy served 24,000–42,000 req/s from L1 cache consuming less than
0.1 cpu·s total.

**Memory ratio: ~16x.** Loki's ingesters and queriers held 1.1 GiB RSS while the proxy sat
at 69–71 MB.

### What these numbers mean

Proxy throughput in the heavy and long-range workloads reflects **L1 cache serving speed**,
not VictoriaLogs query speed. After the warmup pass, the proxy serves all repeated identical
queries from memory without touching VL. This is the steady-state production behavior for
repeated Grafana dashboard panels and time-range queries.

The Loki numbers are real backend execution: Loki scanned 6h/24h/48h windows on every
request with no cache.

For **cold-first pass** behavior (first load of a new time range, uncached query): expect
the proxy to add `~15–30ms` to one VL roundtrip on a cache miss, then serve all subsequent
identical requests in `< 1ms`.

## Cache Size Sizing Guide

### What `256 MB` default L1 covers

The default `-cache-max-bytes=268435456` (256 MB) holds roughly:

| Cache size | Approximate capacity | Eviction behavior |
|---|---|---|
| 256 MB (default) | 500–1,000 medium query results | LRU; hot dashboards stay warm, cold queries evict |
| 1 GB | 4,000–8,000 results | Large working set; multi-team dashboards stay warm |
| 4 GB | 16,000–32,000 results | Full-day working set for large teams rarely evicts |
| L2 disk (bbolt) | Any size; persistent across restarts | `~5–20ms` miss cost vs sub-µs L1, zero VL call on hits |

Average result size depends heavily on log volume per query. For `query_range` over
small time windows returning 100 log lines, expect `~100–300 KB` per result. For
label/series metadata queries, `~5–20 KB` per result.

### Configuring L1 + L2 for production

```bash
# 1 GB L1 in-memory + 10 GB L2 disk, cache survives pod restarts
-cache-max-bytes=1073741824 \
-disk-cache-path=/mnt/cache/proxy.db \
-disk-cache-max-bytes=10737418240
```

### Measuring eviction pressure

```promql
# Eviction rate — non-zero means L1 is too small
rate(loki_vl_proxy_cache_evictions_total[5m])

# If eviction rate > 0 and cache hit rate < 90%, increase -cache-max-bytes
rate(loki_vl_proxy_cache_hits_total[5m])
/
(rate(loki_vl_proxy_cache_hits_total[5m]) + rate(loki_vl_proxy_cache_misses_total[5m]))
```

Rule of thumb: `L1 size = (unique active queries per hour) × (average response size)`.

### Running the full Loki vs VL+proxy comparison

```bash
# Full suite: all workloads, 10/50/100/500 clients, 30s per level
./bench/run-comparison.sh

# Quick smoke test
./bench/run-comparison.sh --workloads=small --clients=10,50 --duration=10s

# Long-range only (exercises window splitting, prefilter, cache warm)
./bench/run-comparison.sh --workloads=long_range --clients=10,50,100 --duration=60s

# Cold vs warm pass: run long_range twice and compare JSON output
./bench/run-comparison.sh --workloads=long_range --version=cold --output=bench/results/cold
./bench/run-comparison.sh --workloads=long_range --version=warm --output=bench/results/warm
```

See `bench/README.md` in the repository root for all flags and output format.
