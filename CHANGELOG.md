# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.11.0] - 2026-04-04

### Features

- **OTel label translation**: Bidirectional dot↔underscore conversion for 50+ OTel semantic convention fields
  - `-label-style=underscores` converts VL dotted names (service.name) to Loki underscores (service_name)
  - `-label-style=passthrough` (default) passes VL field names as-is
  - Query direction: `{service_name="x"}` → VL `"service.name":"x"` with automatic field quoting
  - Response direction: all 7 response paths translated (labels, label_values, detected_fields, series, query results, streaming, tail)
- **Custom field remapping**: `-field-mapping` JSON config for arbitrary VL↔Loki field name mappings
- **Per-tenant metrics**: `loki_vl_proxy_tenant_requests_total{tenant,endpoint,status}` and latency histograms
- **Client error breakdown**: `loki_vl_proxy_client_errors_total{endpoint,reason}` — bad_request, rate_limited, not_found, body_too_large
- **Request logging middleware**: Structured JSON log per request with tenant, query, status, duration, client IP
- **Tenant wildcard**: `X-Scope-OrgID: "*"` or `"0"` skips tenant headers for single-tenant VL setups
- **Grafana dashboard**: Pre-built importable dashboard (`examples/grafana-dashboard.json`) with tenant breakdown
- **Alerting rules**: 16 Prometheus/VM alert rules (`examples/alerting-rules.yaml`) including per-tenant abuse detection
- **Operations guide**: SRE documentation (`docs/operations.md`) — capacity planning, perf tuning, troubleshooting

### Tests

- 44 new unit tests for label translation (SanitizeLabelName, LabelTranslator, TranslateLogQLWithLabels)
- 50+ OTel e2e compatibility tests across 12 scenarios (dots→underscores, passthrough, mixed, query direction, Drilldown)
- Docker-compose: added `loki-vl-proxy-underscore` service at :3102 for e2e label translation tests
- **263 unit tests, 50+ e2e tests** — all passing

## [0.6.0] - 2026-04-03

### Features

- **Loki-VL-proxy**: HTTP proxy translating Loki API to VictoriaLogs
- **LogQL to LogsQL translator**: stream selectors, line filters (substring semantics),
  parsers (json, logfmt, pattern, regexp), label filters, metric queries (rate,
  count_over_time, bytes_over_time, sum by, topk), unwrap handling
- **Response converter**: VL NDJSON → Loki streams format, VL stats → Prometheus matrix/vector
- **Request coalescing**: singleflight deduplication — N identical concurrent queries → 1 backend request
- **Rate limiting**: per-client token bucket + global concurrent query semaphore
- **Circuit breaker**: opens after consecutive failures, auto-recovers via half-open probing
- **Query normalization**: sort label matchers, collapse whitespace for better cache hits
- **TTL cache**: per-endpoint TTLs (labels=60s, queries=10s), max 256MB, eviction tracking
- **Prometheus metrics**: `/metrics` endpoint with request counters, latency histograms, cache stats
- **JSON structured logs**: via Go's slog to stdout
- **Helm chart**: VictoriaMetrics-style with extraArgs, ServiceMonitor, security context
- **GHA CI/CD**: build, test, lint, Docker multi-arch, GitHub Release with binaries + checksums
- **Docker**: single static binary, ~10MB Alpine-based image

### Critical Fixes

- **Substring vs word matching**: Loki `|= "text"` is substring match; translated to VL `~"text"`
  (not `"text"` which is word-only). Without this fix, queries silently return fewer results.
- **Stream filter vs field filter**: Loki stream selectors `{level="error"}` converted to VL
  field filters `level:=error` (not stream filters which only match `_stream_fields`)
- **Parser + filter chains**: `| json | status >= 400` correctly becomes
  `| unpack_json | filter status:>=400` in VL
- **Regex quoting**: `namespace=~"prod|staging"` properly quoted as `namespace:~"prod|staging"`
- **Keep fields**: `| keep app, level` always includes `_time, _msg, _stream` for response building

### API Coverage

| Endpoint | Status |
|---|---|
| `/loki/api/v1/query_range` | Implemented (streams + matrix) |
| `/loki/api/v1/query` | Implemented |
| `/loki/api/v1/labels` | Implemented + cached |
| `/loki/api/v1/label/{name}/values` | Implemented + cached |
| `/loki/api/v1/series` | Implemented |
| `/loki/api/v1/detected_fields` | Implemented |
| `/loki/api/v1/index/stats` | Stub |
| `/loki/api/v1/index/volume` | Stub |
| `/loki/api/v1/index/volume_range` | Stub |
| `/loki/api/v1/patterns` | Stub |
| `/loki/api/v1/tail` | Not implemented |
| `/ready` | Implemented |
| `/loki/api/v1/status/buildinfo` | Implemented |
| `/metrics` | Implemented |

### Tests

- 126 unit tests (translator, proxy contracts, cache, middleware, normalization)
- 54 e2e tests (Loki vs proxy side-by-side comparison with compatibility scoring)
- 10 performance e2e tests (Loki direct vs proxy latency comparison)
- All at 100% compatibility

### Performance

Proxy is 40-77% faster than direct Loki for all query endpoints (VictoriaLogs is faster).
Cache provides 3.2x speedup on warm hits.

### Known Limitations

See [docs/KNOWN_ISSUES.md](docs/KNOWN_ISSUES.md) for full list:
- `/loki/api/v1/tail` WebSocket not implemented
- Volume API endpoints return stubs
- Multitenancy header mapping not implemented (Loki string org IDs vs VL numeric AccountID)
- Some LogQL features have no VL equivalent (decolorize, absent_over_time, subqueries)
