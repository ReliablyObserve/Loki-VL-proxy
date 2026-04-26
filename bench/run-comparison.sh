#!/usr/bin/env bash
# Run the full Loki vs VL+proxy read-path comparison benchmark.
#
# Prerequisites:
#   - Loki running at $LOKI_URL (default: http://localhost:3101)
#   - loki-vl-proxy running at $PROXY_URL (default: http://localhost:3100)
#   - Both have data ingested (use the e2e compose stack: cd test/e2e-compat && docker compose up -d)
#
# Usage:
#   ./bench/run-comparison.sh                          # full suite (all workloads, 10/50/100/500 clients)
#   ./bench/run-comparison.sh --workloads=small        # quick smoke test
#   ./bench/run-comparison.sh --clients=10,50          # fewer concurrency levels
#   ./bench/run-comparison.sh --duration=60s           # longer per-level runs
#   ./bench/run-comparison.sh --skip-loki              # proxy only (no Loki comparison)
#   ./bench/run-comparison.sh --version=v1.17.1        # tag results for tracking
#
# All extra flags are forwarded to loki-bench.
set -euo pipefail

LOKI_URL="${LOKI_URL:-http://localhost:3101}"
PROXY_URL="${PROXY_URL:-http://localhost:3100}"
LOKI_METRICS="${LOKI_METRICS:-}"
PROXY_METRICS="${PROXY_METRICS:-http://localhost:3100/metrics}"

# Auto-detect Loki metrics if available.
if [ -z "$LOKI_METRICS" ]; then
  if curl -sf "$LOKI_URL/metrics" -o /dev/null 2>/dev/null; then
    LOKI_METRICS="$LOKI_URL/metrics"
    echo "✓ Loki metrics detected at $LOKI_METRICS"
  fi
fi

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
OUTPUT_DIR="${OUTPUT_DIR:-$SCRIPT_DIR/results}"

echo "════════════════════════════════════════════════════════════"
echo " loki-vl-proxy Read Performance Benchmark"
echo "════════════════════════════════════════════════════════════"
echo " Loki target:   $LOKI_URL"
echo " Proxy target:  $PROXY_URL"
echo " Loki metrics:  ${LOKI_METRICS:-not configured}"
echo " Proxy metrics: ${PROXY_METRICS:-not configured}"
echo " Output:        $OUTPUT_DIR"
echo "════════════════════════════════════════════════════════════"
echo

# Build the tool.
echo "Building loki-bench..."
cd "$REPO_ROOT"
go build -o /tmp/loki-bench ./bench/cmd/loki-bench/
echo "✓ Built /tmp/loki-bench"
echo

# Wait for both endpoints to be ready.
wait_ready() {
  local url="$1"
  local name="$2"
  local max_wait=30
  local waited=0
  printf "Waiting for %s at %s" "$name" "$url"
  while ! curl -sf "$url/ready" -o /dev/null 2>/dev/null && \
        ! curl -sf "$url/loki/api/v1/labels" -o /dev/null 2>/dev/null; do
    if [ $waited -ge $max_wait ]; then
      echo " TIMEOUT — is $name running?"
      exit 1
    fi
    printf "."
    sleep 2
    waited=$((waited + 2))
  done
  echo " ✓"
}

wait_ready "$LOKI_URL" "Loki"
wait_ready "$PROXY_URL" "proxy"

mkdir -p "$OUTPUT_DIR"

/tmp/loki-bench \
  --loki="$LOKI_URL" \
  --proxy="$PROXY_URL" \
  --loki-metrics="$LOKI_METRICS" \
  --proxy-metrics="$PROXY_METRICS" \
  --output="$OUTPUT_DIR" \
  "$@"

echo
echo "════════════════════════════════════════════════════════════"
echo " Results saved to $OUTPUT_DIR"
ls -lh "$OUTPUT_DIR"/*.json "$OUTPUT_DIR"/*.md 2>/dev/null || true
echo "════════════════════════════════════════════════════════════"
