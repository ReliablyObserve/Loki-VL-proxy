#!/usr/bin/env bash
set -euo pipefail

if [ $# -ne 3 ]; then
  echo "usage: $0 <base-json> <head-json> <output-md>" >&2
  exit 1
fi

BASE_JSON="$1"
HEAD_JSON="$2"
OUTPUT_MD="$3"

json_field() {
  local file="$1"
  local path="$2"
  jq -r "$path" "$file"
}

format_delta() {
  local current="$1"
  local base="$2"
  local better="$3"
  local pct_threshold="$4"
  local abs_threshold="$5"
  local min_base="$6"
  python3 - "$current" "$base" "$better" "$pct_threshold" "$abs_threshold" "$min_base" <<'PY'
import sys
current=float(sys.argv[1])
base=float(sys.argv[2])
better=sys.argv[3]
pct_threshold=float(sys.argv[4])
abs_threshold=float(sys.argv[5])
min_base=float(sys.argv[6])
if base == 0:
    print("n/a")
    raise SystemExit
delta=((current-base)/base)*100.0
absolute_delta=abs(current-base)
state="stable"
if base < min_base:
    state="stable"
elif better == "higher":
    if delta >= pct_threshold and absolute_delta >= abs_threshold:
        state="improved"
    elif delta <= -pct_threshold and absolute_delta >= abs_threshold:
        state="regressed"
elif better == "lower":
    if delta <= -pct_threshold and absolute_delta >= abs_threshold:
        state="improved"
    elif delta >= pct_threshold and absolute_delta >= abs_threshold:
        state="regressed"
sign="+" if delta > 0 else ""
print(f"{sign}{delta:.1f}% ({state})")
PY
}

render_component_rows() {
  python3 - "$BASE_JSON" "$HEAD_JSON" <<'PY'
import json
import sys

with open(sys.argv[1], "r", encoding="utf-8") as fh:
    base = json.load(fh)
with open(sys.argv[2], "r", encoding="utf-8") as fh:
    head = json.load(fh)

tracks = [
    ("loki", "Loki API"),
    ("drilldown", "Logs Drilldown"),
    ("vl", "VictoriaLogs"),
]


def format_component(entry):
    if not entry:
        return "n/a"
    passed = entry.get("passed", 0)
    total = entry.get("total", 0)
    pct = entry.get("pct", 0)
    return f"{passed}/{total} ({pct}%)"


def format_component_delta(base_entry, head_entry):
    if not base_entry or not head_entry:
        return "n/a"
    base_pct = float(base_entry.get("pct", 0))
    head_pct = float(head_entry.get("pct", 0))
    if base_pct == 0:
        return "n/a"
    delta = ((head_pct - base_pct) / base_pct) * 100.0
    absolute_delta = abs(head_pct - base_pct)
    state = "stable"
    if base_pct >= 1.0:
        if delta >= 0.1 and absolute_delta >= 0.1:
            state = "improved"
        elif delta <= -0.1 and absolute_delta >= 0.1:
            state = "regressed"
    sign = "+" if delta > 0 else ""
    return f"{sign}{delta:.1f}% ({state})"


rows = []
for key, label in tracks:
    base_components = base.get("compatibility", {}).get(key, {}).get("components", {}) or {}
    head_components = head.get("compatibility", {}).get(key, {}).get("components", {}) or {}
    component_names = sorted(set(base_components.keys()) | set(head_components.keys()))
    for component in component_names:
        base_entry = base_components.get(component)
        head_entry = head_components.get(component)
        rows.append(
            "| {track} | `{component}` | {base_val} | {head_val} | {delta} |".format(
                track=label,
                component=component,
                base_val=format_component(base_entry),
                head_val=format_component(head_entry),
                delta=format_component_delta(base_entry, head_entry),
            )
        )

if not rows:
    rows.append("| n/a | n/a | n/a | n/a | n/a |")

print("\n".join(rows))
PY
}

HEAD_TESTS="$(json_field "$HEAD_JSON" '.tests.count')"
BASE_TESTS="$(json_field "$BASE_JSON" '.tests.count')"
HEAD_COVERAGE="$(json_field "$HEAD_JSON" '.tests.coverage_pct')"
BASE_COVERAGE="$(json_field "$BASE_JSON" '.tests.coverage_pct')"

HEAD_LOKI_PASS="$(json_field "$HEAD_JSON" '.compatibility.loki.passed')"
HEAD_LOKI_TOTAL="$(json_field "$HEAD_JSON" '.compatibility.loki.total')"
HEAD_LOKI_PCT="$(json_field "$HEAD_JSON" '.compatibility.loki.pct')"
BASE_LOKI_PCT="$(json_field "$BASE_JSON" '.compatibility.loki.pct')"

HEAD_DRILL_PASS="$(json_field "$HEAD_JSON" '.compatibility.drilldown.passed')"
HEAD_DRILL_TOTAL="$(json_field "$HEAD_JSON" '.compatibility.drilldown.total')"
HEAD_DRILL_PCT="$(json_field "$HEAD_JSON" '.compatibility.drilldown.pct')"
BASE_DRILL_PCT="$(json_field "$BASE_JSON" '.compatibility.drilldown.pct')"

HEAD_VL_PASS="$(json_field "$HEAD_JSON" '.compatibility.vl.passed')"
HEAD_VL_TOTAL="$(json_field "$HEAD_JSON" '.compatibility.vl.total')"
HEAD_VL_PCT="$(json_field "$HEAD_JSON" '.compatibility.vl.pct')"
BASE_VL_PCT="$(json_field "$BASE_JSON" '.compatibility.vl.pct')"

HEAD_QUERY_NS="$(json_field "$HEAD_JSON" '.performance.benchmarks.query_range_cache_hit_ns_per_op')"
BASE_QUERY_NS="$(json_field "$BASE_JSON" '.performance.benchmarks.query_range_cache_hit_ns_per_op')"
HEAD_QUERY_BYTES="$(json_field "$HEAD_JSON" '.performance.benchmarks.query_range_cache_hit_bytes_per_op')"
BASE_QUERY_BYTES="$(json_field "$BASE_JSON" '.performance.benchmarks.query_range_cache_hit_bytes_per_op')"
HEAD_QUERY_ALLOCS="$(json_field "$HEAD_JSON" '.performance.benchmarks.query_range_cache_hit_allocs_per_op')"
BASE_QUERY_ALLOCS="$(json_field "$BASE_JSON" '.performance.benchmarks.query_range_cache_hit_allocs_per_op')"
HEAD_QUERY_BYPASS_NS="$(json_field "$HEAD_JSON" '.performance.benchmarks.query_range_cache_bypass_ns_per_op')"
BASE_QUERY_BYPASS_NS="$(json_field "$BASE_JSON" '.performance.benchmarks.query_range_cache_bypass_ns_per_op')"
HEAD_QUERY_BYPASS_BYTES="$(json_field "$HEAD_JSON" '.performance.benchmarks.query_range_cache_bypass_bytes_per_op')"
BASE_QUERY_BYPASS_BYTES="$(json_field "$BASE_JSON" '.performance.benchmarks.query_range_cache_bypass_bytes_per_op')"
HEAD_QUERY_BYPASS_ALLOCS="$(json_field "$HEAD_JSON" '.performance.benchmarks.query_range_cache_bypass_allocs_per_op')"
BASE_QUERY_BYPASS_ALLOCS="$(json_field "$BASE_JSON" '.performance.benchmarks.query_range_cache_bypass_allocs_per_op')"
HEAD_LABELS_NS="$(json_field "$HEAD_JSON" '.performance.benchmarks.labels_cache_hit_ns_per_op')"
BASE_LABELS_NS="$(json_field "$BASE_JSON" '.performance.benchmarks.labels_cache_hit_ns_per_op')"
HEAD_LABELS_BYTES="$(json_field "$HEAD_JSON" '.performance.benchmarks.labels_cache_hit_bytes_per_op')"
BASE_LABELS_BYTES="$(json_field "$BASE_JSON" '.performance.benchmarks.labels_cache_hit_bytes_per_op')"
HEAD_LABELS_ALLOCS="$(json_field "$HEAD_JSON" '.performance.benchmarks.labels_cache_hit_allocs_per_op')"
BASE_LABELS_ALLOCS="$(json_field "$BASE_JSON" '.performance.benchmarks.labels_cache_hit_allocs_per_op')"
HEAD_LABELS_BYPASS_NS="$(json_field "$HEAD_JSON" '.performance.benchmarks.labels_cache_bypass_ns_per_op')"
BASE_LABELS_BYPASS_NS="$(json_field "$BASE_JSON" '.performance.benchmarks.labels_cache_bypass_ns_per_op')"
HEAD_LABELS_BYPASS_BYTES="$(json_field "$HEAD_JSON" '.performance.benchmarks.labels_cache_bypass_bytes_per_op')"
BASE_LABELS_BYPASS_BYTES="$(json_field "$BASE_JSON" '.performance.benchmarks.labels_cache_bypass_bytes_per_op')"
HEAD_LABELS_BYPASS_ALLOCS="$(json_field "$HEAD_JSON" '.performance.benchmarks.labels_cache_bypass_allocs_per_op')"
BASE_LABELS_BYPASS_ALLOCS="$(json_field "$BASE_JSON" '.performance.benchmarks.labels_cache_bypass_allocs_per_op')"
HEAD_THROUGHPUT="$(json_field "$HEAD_JSON" '.performance.load.high_concurrency_req_per_s')"
BASE_THROUGHPUT="$(json_field "$BASE_JSON" '.performance.load.high_concurrency_req_per_s')"
HEAD_MEM_GROWTH="$(json_field "$HEAD_JSON" '.performance.load.high_concurrency_memory_growth_mb')"
BASE_MEM_GROWTH="$(json_field "$BASE_JSON" '.performance.load.high_concurrency_memory_growth_mb')"
HEAD_PERF_MODE="$(json_field "$HEAD_JSON" '.performance.mode // "full"')"

COVERAGE_DELTA="$(format_delta "$HEAD_COVERAGE" "$BASE_COVERAGE" higher 0.1 0.1 1)"
LOKI_DELTA="$(format_delta "$HEAD_LOKI_PCT" "$BASE_LOKI_PCT" higher 0.1 0.1 1)"
DRILL_DELTA="$(format_delta "$HEAD_DRILL_PCT" "$BASE_DRILL_PCT" higher 0.1 0.1 1)"
VL_DELTA="$(format_delta "$HEAD_VL_PCT" "$BASE_VL_PCT" higher 0.1 0.1 1)"
QUERY_DELTA="$(format_delta "$HEAD_QUERY_NS" "$BASE_QUERY_NS" lower 25 750 500)"
QUERY_BYTES_DELTA="$(format_delta "$HEAD_QUERY_BYTES" "$BASE_QUERY_BYTES" lower 15 32 64)"
QUERY_ALLOCS_DELTA="$(format_delta "$HEAD_QUERY_ALLOCS" "$BASE_QUERY_ALLOCS" lower 15 1 1)"
QUERY_BYPASS_DELTA="$(format_delta "$HEAD_QUERY_BYPASS_NS" "$BASE_QUERY_BYPASS_NS" lower 25 750 500)"
QUERY_BYPASS_BYTES_DELTA="$(format_delta "$HEAD_QUERY_BYPASS_BYTES" "$BASE_QUERY_BYPASS_BYTES" lower 15 32 64)"
QUERY_BYPASS_ALLOCS_DELTA="$(format_delta "$HEAD_QUERY_BYPASS_ALLOCS" "$BASE_QUERY_BYPASS_ALLOCS" lower 15 1 1)"
LABELS_DELTA="$(format_delta "$HEAD_LABELS_NS" "$BASE_LABELS_NS" lower 25 750 500)"
LABELS_BYTES_DELTA="$(format_delta "$HEAD_LABELS_BYTES" "$BASE_LABELS_BYTES" lower 15 32 64)"
LABELS_ALLOCS_DELTA="$(format_delta "$HEAD_LABELS_ALLOCS" "$BASE_LABELS_ALLOCS" lower 15 1 1)"
LABELS_BYPASS_DELTA="$(format_delta "$HEAD_LABELS_BYPASS_NS" "$BASE_LABELS_BYPASS_NS" lower 25 750 500)"
LABELS_BYPASS_BYTES_DELTA="$(format_delta "$HEAD_LABELS_BYPASS_BYTES" "$BASE_LABELS_BYPASS_BYTES" lower 15 32 64)"
LABELS_BYPASS_ALLOCS_DELTA="$(format_delta "$HEAD_LABELS_BYPASS_ALLOCS" "$BASE_LABELS_BYPASS_ALLOCS" lower 15 1 1)"
THROUGHPUT_DELTA="$(format_delta "$HEAD_THROUGHPUT" "$BASE_THROUGHPUT" higher 25 2000 5000)"
MEMORY_DELTA="$(format_delta "$HEAD_MEM_GROWTH" "$BASE_MEM_GROWTH" lower 300 5 5)"
COMPAT_COMPONENT_ROWS="$(render_component_rows)"

if [ "$HEAD_PERF_MODE" = "skipped" ]; then
  PERFORMANCE_SECTION="$(cat <<'EOF'
### Performance smoke

Performance smoke was skipped for this PR because no perf-sensitive paths changed.
EOF
)"
  PERFORMANCE_STATE_NOTE="- Performance smoke was intentionally skipped because no perf-sensitive paths changed in this PR."
else
  PERFORMANCE_SECTION="$(cat <<EOF
### Performance smoke

Lower CPU cost (\`ns/op\`) is better. Lower benchmark memory cost (\`B/op\`, \`allocs/op\`) is better. Higher throughput is better. Lower load-test memory growth is better. Benchmark rows are medians from repeated samples.

| Signal | Base | PR | Delta |
|---|---:|---:|---:|
| QueryRange cache-hit CPU cost | ${BASE_QUERY_NS} ns/op | ${HEAD_QUERY_NS} ns/op | ${QUERY_DELTA} |
| QueryRange cache-hit memory | ${BASE_QUERY_BYTES} B/op | ${HEAD_QUERY_BYTES} B/op | ${QUERY_BYTES_DELTA} |
| QueryRange cache-hit allocations | ${BASE_QUERY_ALLOCS} allocs/op | ${HEAD_QUERY_ALLOCS} allocs/op | ${QUERY_ALLOCS_DELTA} |
| QueryRange cache-bypass CPU cost | ${BASE_QUERY_BYPASS_NS} ns/op | ${HEAD_QUERY_BYPASS_NS} ns/op | ${QUERY_BYPASS_DELTA} |
| QueryRange cache-bypass memory | ${BASE_QUERY_BYPASS_BYTES} B/op | ${HEAD_QUERY_BYPASS_BYTES} B/op | ${QUERY_BYPASS_BYTES_DELTA} |
| QueryRange cache-bypass allocations | ${BASE_QUERY_BYPASS_ALLOCS} allocs/op | ${HEAD_QUERY_BYPASS_ALLOCS} allocs/op | ${QUERY_BYPASS_ALLOCS_DELTA} |
| Labels cache-hit CPU cost | ${BASE_LABELS_NS} ns/op | ${HEAD_LABELS_NS} ns/op | ${LABELS_DELTA} |
| Labels cache-hit memory | ${BASE_LABELS_BYTES} B/op | ${HEAD_LABELS_BYTES} B/op | ${LABELS_BYTES_DELTA} |
| Labels cache-hit allocations | ${BASE_LABELS_ALLOCS} allocs/op | ${HEAD_LABELS_ALLOCS} allocs/op | ${LABELS_ALLOCS_DELTA} |
| Labels cache-bypass CPU cost | ${BASE_LABELS_BYPASS_NS} ns/op | ${HEAD_LABELS_BYPASS_NS} ns/op | ${LABELS_BYPASS_DELTA} |
| Labels cache-bypass memory | ${BASE_LABELS_BYPASS_BYTES} B/op | ${HEAD_LABELS_BYPASS_BYTES} B/op | ${LABELS_BYPASS_BYTES_DELTA} |
| Labels cache-bypass allocations | ${BASE_LABELS_BYPASS_ALLOCS} allocs/op | ${HEAD_LABELS_BYPASS_ALLOCS} allocs/op | ${LABELS_BYPASS_ALLOCS_DELTA} |
| High-concurrency throughput | ${BASE_THROUGHPUT} req/s | ${HEAD_THROUGHPUT} req/s | ${THROUGHPUT_DELTA} |
| High-concurrency memory growth | ${BASE_MEM_GROWTH} MB | ${HEAD_MEM_GROWTH} MB | ${MEMORY_DELTA} |
EOF
)"
  PERFORMANCE_STATE_NOTE="- Performance is a smoke comparison, not a full benchmark lab run."
fi

cat >"$OUTPUT_MD" <<EOF
<!-- pr-quality-report -->
## PR Quality Report

Compared against base branch \`main\`.

### Coverage and tests

| Signal | Base | PR | Delta |
|---|---:|---:|---:|
| Test count | ${BASE_TESTS} | ${HEAD_TESTS} | $((HEAD_TESTS-BASE_TESTS)) |
| Coverage | ${BASE_COVERAGE}% | ${HEAD_COVERAGE}% | ${COVERAGE_DELTA} |

### Compatibility

| Track | Base | PR | Delta |
|---|---:|---:|---:|
| Loki API | ${BASE_LOKI_PCT}% | ${HEAD_LOKI_PASS}/${HEAD_LOKI_TOTAL} (${HEAD_LOKI_PCT}%) | ${LOKI_DELTA} |
| Logs Drilldown | ${BASE_DRILL_PCT}% | ${HEAD_DRILL_PASS}/${HEAD_DRILL_TOTAL} (${HEAD_DRILL_PCT}%) | ${DRILL_DELTA} |
| VictoriaLogs | ${BASE_VL_PCT}% | ${HEAD_VL_PASS}/${HEAD_VL_TOTAL} (${HEAD_VL_PCT}%) | ${VL_DELTA} |

### Compatibility components

| Track | Component | Base | PR | Delta |
|---|---|---:|---:|---:|
${COMPAT_COMPONENT_ROWS}

${PERFORMANCE_SECTION}

### State

- Coverage, compatibility, and sampled performance are reported here from the same PR workflow.
- This is a delta report, not a release gate by itself. Required checks still decide merge safety.
${PERFORMANCE_STATE_NOTE}
- Delta states use the same noise guards as the quality gate (percent + absolute + low-baseline checks), so report labels match merge-gate behavior.
EOF
