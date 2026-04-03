# Loki-VL-proxy

HTTP proxy that exposes a **Loki-compatible API** on the frontend and translates requests to **VictoriaLogs** on the backend. Allows using Grafana's native Loki datasource (Explore, Drilldown, dashboards) with VictoriaLogs — no custom datasource plugin needed.

## Architecture

```
              Grafana (Loki datasource)
              Explore / Drilldown / Dashboards
              MCP servers / LLM agents
                        │
                        ▼
         ┌──────────────────────────────┐
         │        Loki-VL-proxy         │
         │          :3100               │
         │                              │
         │  ┌────────────────────────┐  │
         │  │   Rate Limiter         │  │  Per-client token bucket
         │  │   (per-client + global)│  │  + global concurrent semaphore
         │  ├────────────────────────┤  │
         │  │   Request Coalescer    │  │  singleflight: N identical queries
         │  │   (singleflight)       │  │  → 1 backend request
         │  ├────────────────────────┤  │
         │  │   Query Normalizer     │  │  Sort matchers, collapse whitespace
         │  │                        │  │  → better cache hit rate
         │  ├────────────────────────┤  │
         │  │   LogQL → LogsQL       │  │  Stream selectors, line filters,
         │  │   Translator           │  │  parsers, metric queries
         │  ├────────────────────────┤  │
         │  │   TTL Cache (L1)       │  │  Per-endpoint TTLs, max bytes,
         │  │   In-memory            │  │  eviction tracking
         │  ├────────────────────────┤  │
         │  │   Response Converter   │  │  VL NDJSON → Loki streams format
         │  │                        │  │  VL stats → Prometheus matrix
         │  ├────────────────────────┤  │
         │  │   Circuit Breaker      │  │  Closed→Open→HalfOpen→Closed
         │  │                        │  │  Protects VL from cascading failure
         │  ├────────────────────────┤  │
         │  │   /metrics  (Prom)     │  │  Request counters, latency histograms,
         │  │   JSON logs (slog)     │  │  cache stats, circuit breaker state
         │  └────────────────────────┘  │
         └──────────────┬───────────────┘
                        │
                        ▼
                  VictoriaLogs
                    :9428
```

## Features

- **LogQL → LogsQL translation**: stream selectors, line filters, label filters, parsers, metric queries
- **Response format conversion**: VL NDJSON → Loki streams, VL stats → Prometheus matrix/vector
- **Request coalescing**: N identical concurrent queries → 1 backend request (singleflight)
- **Rate limiting**: per-client token bucket + global concurrent query semaphore
- **Circuit breaker**: opens after consecutive failures, auto-recovers via half-open probing
- **Query normalization**: sort label matchers, collapse whitespace for better cache hits
- **Tiered cache**: per-endpoint TTLs (labels=60s, queries=10s), max bytes (256MB), eviction stats
- **Observability**: Prometheus `/metrics`, structured JSON logs via `slog`
- **Single static binary**, ~10MB Docker image, zero external dependencies at runtime

## API Coverage

| Loki Endpoint | Status | VL Backend | Cached | Tests |
|---|---|---|---|---|
| `/loki/api/v1/query_range` (logs) | Implemented | `/select/logsql/query` | 10s | 6 |
| `/loki/api/v1/query_range` (metrics) | Implemented | `/select/logsql/stats_query_range` | 10s | 1 |
| `/loki/api/v1/query` | Implemented | `/select/logsql/query` or `stats_query` | 10s | 1 |
| `/loki/api/v1/labels` | Implemented | `/select/logsql/field_names` | 60s | 3 |
| `/loki/api/v1/label/{name}/values` | Implemented | `/select/logsql/field_values` | 60s | 3 |
| `/loki/api/v1/series` | Implemented | `/select/logsql/streams` | 30s | 2 |
| `/loki/api/v1/index/stats` | Stub | — | — | 1 |
| `/loki/api/v1/index/volume` | Stub | — | — | 1 |
| `/loki/api/v1/index/volume_range` | Stub | — | — | 1 |
| `/loki/api/v1/detected_fields` | Implemented | `/select/logsql/field_names` | 30s | 1 |
| `/loki/api/v1/patterns` | Stub | — | — | 1 |
| `/loki/api/v1/tail` | Not yet | `/select/logsql/tail` | — | 1 |
| `/ready` | Implemented | `/health` | — | 2 |
| `/loki/api/v1/status/buildinfo` | Implemented | — | — | 1 |
| `/metrics` | Implemented | — | — | 1 |

**72 tests total** (contract tests + translation + cache + middleware)

## Protection Layers

| Layer | Purpose | Default Config |
|---|---|---|
| Per-client rate limiter | Prevent individual client abuse | 50 req/s, burst 100 |
| Global concurrent limit | Cap total backend load | 100 concurrent queries |
| Request coalescing | Deduplicate identical queries | Automatic (singleflight) |
| Query normalization | Improve cache hit rate | Sort matchers, collapse whitespace |
| In-memory TTL cache | Reduce backend calls | Per-endpoint TTLs, 256MB max |
| Circuit breaker | Protect VL from cascading failure | Opens after 5 failures, 10s backoff |

### How Coalescing Works

When 50 Grafana dashboards (or MCP/LLM agents) send `{app="nginx"} |= "error"` simultaneously:

```
Client 1 ──┐
Client 2 ──┤
Client 3 ──┤──→ 1 request to VL ──→ response shared to all 50
  ...      │
Client 50 ─┘
```

Only **1** request reaches VictoriaLogs. All clients get the same response.

## LogQL Translation Reference

| LogQL | LogsQL |
|---|---|
| `{app="nginx"}` | `{app="nginx"}` |
| `\|= "error"` | `"error"` |
| `!= "debug"` | `-"debug"` |
| `\|~ "err.*"` | `~"err.*"` |
| `!~ "debug.*"` | `NOT ~"debug.*"` |
| `\| json` | `\| unpack_json` |
| `\| logfmt` | `\| unpack_logfmt` |
| `\| pattern "<ip> ..."` | `\| extract "<ip> ..."` |
| `\| regexp "..."` | `\| extract_regexp "..."` |
| `\| line_format "{{.x}}"` | `\| format "<x>"` |
| `\| label_format x="{{.y}}"` | `\| format "<y>" as x` |
| `\| drop a, b` | `\| delete a, b` |
| `\| keep a, b` | `\| fields a, b` |
| `\| label == "val"` | `label:=val` |
| `\| label != "val"` | `-label:=val` |
| `rate({...}[5m])` | `{...} \| stats rate()` |
| `count_over_time({...}[5m])` | `{...} \| stats count()` |
| `sum(rate({...}[5m])) by (x)` | `{...} \| stats by (x) rate()` |

Full reference: https://docs.victoriametrics.com/victorialogs/logql-to-logsql/

## Quick Start

```bash
# Build and run locally
go build -o loki-vl-proxy ./cmd/proxy
./loki-vl-proxy -backend=http://your-victorialogs:9428

# Docker
docker build -t loki-vl-proxy .
docker run -p 3100:3100 loki-vl-proxy -backend=http://victorialogs:9428

# Docker Compose (with VictoriaLogs + Grafana)
docker-compose up -d
# Open Grafana at http://localhost:3000, Loki datasource pre-configured
```

## Configuration

| Flag | Env | Default | Description |
|---|---|---|---|
| `-listen` | `LISTEN_ADDR` | `:3100` | Listen address |
| `-backend` | `VL_BACKEND_URL` | `http://localhost:9428` | VictoriaLogs backend URL |
| `-cache-ttl` | — | `60s` | Default cache TTL |
| `-cache-max` | — | `10000` | Maximum cache entries |
| `-log-level` | — | `info` | Log level: debug, info, warn, error |

## Observability

### Metrics (Prometheus scrape)

`GET /metrics` exposes:

```
# Request tracking
loki_vl_proxy_requests_total{endpoint, status}
loki_vl_proxy_request_duration_seconds{endpoint}  (histogram)

# Cache efficiency
loki_vl_proxy_cache_hits_total
loki_vl_proxy_cache_misses_total

# Translation tracking
loki_vl_proxy_translations_total
loki_vl_proxy_translation_errors_total

# System
loki_vl_proxy_uptime_seconds
```

### Logs

Structured JSON to stdout via Go's `slog`:

```json
{"time":"2024-01-15T10:30:00Z","level":"INFO","msg":"query_range request","logql":"{app=\"nginx\"} |= \"error\""}
{"time":"2024-01-15T10:30:00Z","level":"DEBUG","msg":"translated query","logsql":"{app=\"nginx\"} \"error\""}
```

## Testing

```bash
# Unit + contract tests (72 tests)
go test ./...

# Verbose with individual test names
go test ./... -v

# E2E compatibility tests (requires docker-compose)
cd test/e2e-compat
docker-compose up -d
go test -v -tags=e2e -timeout=120s ./test/e2e-compat/

# Build binary
go build -o loki-vl-proxy ./cmd/proxy
```

### Test Coverage by Category

| Category | Tests | What they verify |
|---|---|---|
| Loki API contracts | 25 | Exact response JSON structure per Loki spec |
| LogQL translation | 26 | Query conversion correctness |
| Cache behavior | 4 | Hit/miss/TTL/protection |
| Middleware | 12 | Coalescing, rate limiting, circuit breaker |
| Normalization | 7 | Query canonicalization |
| E2E (gated) | 10 | Full stack with real Loki + VL comparison |

## Roadmap

- [ ] `/loki/api/v1/tail` — WebSocket→SSE bridge for live tailing
- [ ] L2 on-disk cache (bbolt/badger) for query result persistence
- [ ] OTLP push for proxy's own telemetry
- [ ] `/loki/api/v1/index/stats` — real implementation via VL `/select/logsql/hits`
- [ ] `/loki/api/v1/index/volume` — volume data via VL hits with field grouping
- [ ] `/loki/api/v1/detected_field/{name}/values` endpoint
- [ ] Query fingerprinting + analytics dashboard
- [ ] Auto-warming cache for top-N queries
