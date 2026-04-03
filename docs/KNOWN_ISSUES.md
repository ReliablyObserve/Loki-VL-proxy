# Known Issues & VL Compatibility Gaps

Based on research of VictoriaLogs GitHub issues, community reports, and VL team discussions.

## Critical: Silent Correctness Issues

### 1. Substring vs Word Matching (MUST FIX)

Loki's `|= "err"` matches any substring: "error", "stderr", "erroneous".
VL's word filter `"err"` matches only the exact word "err".

**Impact**: Queries return fewer results through the proxy than through Loki.
**Fix**: Convert `|= "text"` to VL's `~"text"` (substring regexp) instead of `"text"` (word filter).

Reference: [VL docs](https://docs.victoriametrics.com/victorialogs/logql-to-logsql/#line-filter)

### 2. Stream Filter vs Field Filter Performance Gap

VL stream selectors `{label="value"}` only match `_stream_fields`. Regular field filters
`label:="value"` are slower but match all fields.

**Impact**: Our proxy converts ALL Loki stream matchers to field filters. This is correct but
suboptimal. For known stream fields, using stream selectors would be 10-1000x faster.

**Mitigation**: Configurable list of stream fields to keep in `{...}` selectors.

Reference: [VL Issue #1077](https://github.com/VictoriaMetrics/VictoriaLogs/issues/1077)

## Grafana Integration Gaps

### 3. Volume API Missing (VL Issue #454)

`/loki/api/v1/index/volume` and `/index/volume_range` — Grafana Drilldown calls these
for log volume histograms. VL has no direct equivalent. Currently stubbed.

### 4. Tail WebSocket Not Implemented

`/loki/api/v1/tail` — Grafana Explore "Live" mode. VL has `/select/logsql/tail` (SSE),
but WebSocket bridge not implemented.

### 5. Detected Fields Response Format

Loki 3.x returns field type, cardinality, parsers. Our proxy returns simplified format
from VL's `/select/logsql/field_names`.

## Data Model Differences

### 6. Multitenancy Header Mismatch

Loki: `X-Scope-OrgID: my-tenant-name` (string)
VL: `AccountID: 123` + `ProjectID: 456` (numeric)

No string-based tenant names in VL. Proxy needs tenant mapping config.

Reference: [VL Issue #1251](https://github.com/VictoriaMetrics/VictoriaLogs/issues/1251)

### 7. Structured Metadata (Loki 3.x)

Loki 3.x supports structured metadata (key-value pairs per log entry, separate from labels).
VL ingests all fields as regular fields. The mapping is natural but not identical.

### 8. Large Body Fields (VL Issue #91)

VL may silently drop log records with very large body fields (50KB+).

Reference: [VL Datasource Issue #91](https://github.com/VictoriaMetrics/victorialogs-datasource/issues/91)

## LogQL Features with No VL Equivalent

| Feature | Status |
|---|---|
| `\| decolorize` | Not in LogsQL |
| `absent_over_time()` | No VL equivalent |
| Subqueries `rate(sum_over_time(...)[5m:1m])` | Not supported |
| Vector matching (`A / B` between queries) | Not in LogsQL |
| Complex Go templates in `line_format` | Partial (no conditionals/loops) |
| `bytes_over_time()` | Use `sum(len(_msg))` approximation |
| Cross-tenant queries (`org1\|org2`) | Not supported in VL |
