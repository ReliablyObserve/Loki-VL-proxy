# Benchmarks

Measured on Apple M3 Max (14 cores), Go 1.25, `-benchmem`.

## Per-Request Latency

| Operation | Latency | Allocs | Bytes/op | Notes |
|---|---|---|---|---|
| Labels (cache hit) | 2.0 us | 25 | 6.6 KB | Serve from in-memory cache |
| QueryRange (cache hit) | 118 us | 600 | 142 KB | Query translation + cache lookup |
| wrapAsLokiResponse | 2.8 us | 58 | 2.6 KB | JSON re-envelope |
| VL NDJSON to Loki streams (100 lines) | 188 us | 3417 | 139 KB | Parse + group + convert |
| LogQL translation | ~5 us | ~20 | ~2 KB | String manipulation (no AST) |

## Throughput

| Scenario | Requests | Concurrency | Throughput | Cache Hit % | Memory Growth |
|---|---|---|---|---|---|
| Labels (cache hit) | 100,000 | 100 | 175,726 req/s | 98.2% | 0.5 MB |
| QueryRange (cache miss, 1ms backend) | 5,000 | 50 | 12,976 req/s | 0% | - |

## Resource Usage at Scale

Estimated from benchmarks (proxy overhead only, excludes network I/O):

| Load (req/s) | CPU (single core) | Memory (steady state) | Notes |
|---|---|---|---|
| 100 | <1% | ~10 MB | Idle, mostly cache hits |
| 1,000 | ~5% | ~20 MB | Mix of cache hits/misses |
| 10,000 | ~50% | ~50 MB | Significant cache miss rate, backend-bound |
| 100,000+ | ~100% | ~100 MB | CPU-bound, needs horizontal scaling |

The proxy is CPU-bound at high load. Memory usage is stable — the cache has a fixed maximum size (configurable via `-cache-max`). Scaling strategy:

- **< 1,000 req/s**: Single replica, 100m CPU, 128Mi memory
- **1,000-10,000 req/s**: 2-3 replicas with HPA on CPU
- **> 10,000 req/s**: HPA with 5+ replicas, tune `cache-max` for hit rate

## Known Hot Paths

1. **VL NDJSON to Loki streams** (3417 allocs/100 lines): Each JSON line is unmarshaled into a `map[string]interface{}`, creating many small allocations. A streaming JSON decoder with pre-allocated buffers would reduce this.

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
