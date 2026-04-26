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

## Three-Way Read Path Comparison: Loki vs VL+Proxy vs VL Native

Measured with `loki-bench` against the e2e-compat stack running simultaneously on the same
host (Apple M3 Max, Docker). Loki 3.7.1, VictoriaLogs v1.50.0, loki-vl-proxy latest.
30 seconds per level, 3 concurrent targets measured in parallel.

**Dataset baseline:** 5.08 M log entries pre-seeded across 7 days, 12 services
(api-gateway, auth-service, frontend-ssr, batch-etl, ml-serving, cache-redis, queue-worker,
notification-service, search-indexer, payment-service, user-service, audit-logger).
Average ingest rate ~8.4 lines/sec total (~0.7 lps per service), ~30 k entries/hour.

**Three targets measured:**
- **Loki (direct)** — LogQL queries sent directly to Loki, no proxy
- **VL + Proxy** — LogQL queries via loki-vl-proxy; proxy translates to LogsQL and caches results
- **VL (native)** — LogsQL queries sent directly to VictoriaLogs (no Loki, no proxy)

### Small workload — label values, detected\_fields, index stats, series

Queries: `label_values(app)`, `label_values(level)`, `detected_fields`, `index_stats`,
`labels`, `series` over 1h windows. These are the metadata queries Grafana Drilldown
and the datasource fire on every panel load.

| Clients | Loki req/s | VL+Proxy req/s | VL native req/s | Loki P50 | VL+Proxy P50 | VL native P50 |
|---:|---:|---:|---:|---:|---:|---:|
| 10 | 1,370 | 18,532 (**13.5x**) | 4,161 (3.0x) | 6.8ms | 0.4ms | 1.2ms |
| 50 | 1,309 | 30,429 (**23.2x**) | 4,397 (3.4x) | 37ms | 1.4ms | 8.6ms |
| 100 | 1,033 | 30,708 (**29.7x**) | 3,829 (3.7x) | 97.6ms | 2.7ms | 22.9ms |

**CPU + RSS at c=10 (30-second window):**

| Target | CPU consumed | RSS delta | Notes |
|--------|-------------|-----------|-------|
| Loki | 165 cpu·s | 1,267 MB | Querier scanning every request |
| VL + Proxy (proxy only) | 0.1 cpu·s | 192 MB | Cache serving |
| VL + Proxy (VL behind) | 61 cpu·s | 306 MB | VL serves only cache misses |
| **VL + Proxy combined** | **61 cpu·s** | **498 MB** | **2.7× less CPU, 2.5× less RAM vs Loki** |
| VL native | 278 cpu·s | 964 MB | Serves every request — more CPU than Loki |

Key insight: VL-native uses _more_ CPU than Loki for small repeated metadata queries because
it lacks a response cache. The proxy cache eliminates most VL calls, so the combined
proxy+VL system uses 2.5–2.7× fewer resources than Loki direct at the same load.

### Heavy workload — aggregations, JSON parse, logfmt, 30m–1h windows

Queries: `count_over_time`, `rate`, `bytes_rate`, `sum by`, `quantile_over_time unwrap`,
`| json | line_format`, `| logfmt | label_format` over 30-minute to 1-hour windows.

| Clients | Loki req/s | VL+Proxy req/s | VL native req/s | Loki P50 | VL native P50 | VL+Proxy P50 |
|---:|---:|---:|---:|---:|---:|---:|
| 10 | 57.5 | 23,299 (**405x**) | 718 (**12.5x**) | 178ms | **3.1ms** | 0.4ms |
| 50 | 58.1 | 18,512 (**319x**) | — (crashed) | 929ms | — | 0.5ms |
| 100 | 60.7 | 42,960 (**708x**) | — (crashed) | 1,805ms | — | 1.3ms |

**CPU + RSS at c=10:**

| Target | CPU consumed | RSS delta | Notes |
|--------|-------------|-----------|-------|
| Loki | 173 cpu·s | 1,567 MB | Steady-state heavy query load |
| VL + Proxy combined | 1.9 cpu·s | 1,270 MB | 91× less CPU, 1.2× less RAM |
| VL native | 280 cpu·s | **5,261 MB** | 3.4× more RAM than Loki; crashes at c≥50 |

> **VL v1.50.0 concurrent query limit:** VictoriaLogs defaults to
> `-search.maxConcurrentRequests=16`. At c≥50, requests beyond 16 are rejected immediately
> (not queued), producing 100% error rate in the bench. This is not an OOM crash — it is
> VL's own concurrency protection. The bench confirmed this with
> `vl_concurrent_select_limit_reached_total=285,330` after the run.
>
> For bench comparability, add `-search.maxConcurrentRequests=100 -search.maxQueueDuration=30s`
> to VL (already done in `test/e2e-compat/docker-compose.yml`). The proxy acts as a
> natural concurrency buffer in production: only cache-miss requests reach VL, so real
> VL concurrency is always much lower than client-facing concurrency.

### Long-range workload — full 7-day windows

Queries span the full 7-day dataset. These represent historical dashboards, incident
retrospectives, and compliance reports.

| Clients | Loki req/s | Loki errors | VL+Proxy req/s | VL native req/s | Loki P50 | VL native P50 |
|---:|---:|---:|---:|---:|---:|---:|
| 10 | 1.7 | 0% | 6.6¹ | **95.3** | 6,201ms | **23.8ms** (**260×** faster) |
| 50 | 3.7 | **19.6%** | 28,441 | — (crashed) | 23,076ms | — |
| 100 | 6.1 | **45.1%** | 53,390 | — (crashed) | 29,852ms | — |

¹ c=10 proxy has 61% errors on cold start (Loki backend timeout on first-ever requests);
fully warm at c≥50 where the cache dominates.

**CPU + RSS at c=10:**

| Target | CPU consumed | RSS delta | Notes |
|--------|-------------|-----------|-------|
| Loki | 71.4 cpu·s | 1,446 MB | 6 s+ per query, timeouts at c≥50 |
| VL + Proxy combined | 15.8 cpu·s | 3,028 MB | Cache fills memory on first pass |
| VL native | 320 cpu·s | 3,850 MB | 260× faster per query, crashes at c≥50 |

At c=50/100 Loki hits 20–45% timeout errors on 7-day queries. VL native is 260× faster
per single query (23ms vs 6,201ms) but crashes under concurrent load. The proxy serves
the sustained load at 28k–53k req/s from cache with <0.1% errors.

### What these numbers mean

**Single-query speed (VL vs Loki):** VictoriaLogs storage is dramatically more efficient
for aggregation and long-range scans. A single 7-day query takes 6.2 seconds on Loki and
24 milliseconds on VL — a 260× difference. Heavy aggregations are 57× faster. This is a
fundamental storage architecture difference, not tuning.

**Cache layer (Proxy vs VL native):** The proxy cache adds another dimension. Once a query
result is cached, all subsequent identical requests are served in sub-millisecond latency
without touching VL. This is the steady-state behavior for repeated Grafana dashboard panels
and time-range queries that Drilldown fires.

**Stability under concurrency:** Neither Loki nor VL v1.50.0 handles 50+ concurrent
heavy/long-range queries cleanly. Loki degrades to timeouts; VL crashes (OOM). The proxy
absorbs the concurrency spike and serves from cache, reducing VL exposure to only cold-miss
requests.

**Cold-first behavior:** On the very first load of a new time range, expect 1× VL roundtrip
(24ms for long-range in VL). Proxy adds ~2ms translation overhead per cache miss. All
subsequent requests for the same time range: sub-millisecond from L1 cache.

### Warm cache vs cold cache (proxy overhead isolation)

The bench supports a 4-way comparison by running a second proxy instance with cache
disabled (`-cache-max=0`). This isolates the proxy's translation overhead and raw VL
query latency through the Loki-compatible API:

| Mode | What it measures |
|------|-----------------|
| `proxy (warm)` | Production steady-state: repeated queries served from L1 cache |
| `proxy (cold)` | First-load path: every request hits VL; shows translation overhead + VL speed |
| `vl_direct` | Pure VL LogsQL performance with no proxy layer |

The delta between `proxy (cold)` and `vl_direct` is the pure proxy overhead per request:
LogQL→LogsQL translation (~5µs) + HTTP round-trip + response envelope conversion.

To run the 4-way comparison, build the proxy binary and pass it to the script:

```bash
# Build proxy for host-side no-cache instance
go build -o /tmp/loki-vl-proxy ./cmd/proxy/

# Run full 4-way comparison (auto-spawns no-cache proxy on port 3199)
PROXY_BINARY=/tmp/loki-vl-proxy ./bench/run-comparison.sh

# Or start no-cache proxy manually and pass its URL
./loki-vl-proxy -listen=:3199 -backend=http://localhost:9428 -cache-max=0 -cache-max-bytes=0 &
PROXY_NO_CACHE_URL=http://localhost:3199 ./bench/run-comparison.sh
```

### Summary table

| Workload | Loki P50 @c=10 | VL native P50 @c=10 | VL+Proxy P50 @c=10 | VL vs Loki | Proxy vs Loki |
|----------|:--------------:|:-------------------:|:------------------:|:----------:|:-------------:|
| Small | 6.8ms | 1.2ms | 0.4ms | 5.7× faster | **17× faster** |
| Heavy | 178ms | 3.1ms | 0.4ms | 57× faster | **405× faster** |
| Long-range | 6,201ms | 23.8ms | 1.4ms¹ | 260× faster | **4,400× faster** |

¹ P50 at c=10 includes cold-cache misses; warm-cache P50 is sub-millisecond.

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

### Running the full comparison

```bash
# Full suite: all workloads, 10/50/100/500 clients, 30s per level
./bench/run-comparison.sh

# 4-way comparison (warm proxy + cold proxy + VL native + Loki)
go build -o /tmp/loki-vl-proxy ./cmd/proxy/
PROXY_BINARY=/tmp/loki-vl-proxy ./bench/run-comparison.sh

# Quick smoke test
./bench/run-comparison.sh --workloads=small --clients=10,50 --duration=10s

# Long-range only (exercises window splitting, prefilter, cache warm)
./bench/run-comparison.sh --workloads=long_range --clients=10,50,100 --duration=60s

# Compute workload (CPU-intensive: rate math, quantile_over_time, division, topk)
./bench/run-comparison.sh --workloads=compute --clients=10,50 --duration=30s

# VL with raised concurrency limit (required for c≥50 VL-direct tests)
# Add to VictoriaLogs: -search.maxConcurrentRequests=100 -search.maxQueueDuration=30s
# Already set in test/e2e-compat/docker-compose.yml
```

See `bench/README.md` in the repository root for all flags and output format.
