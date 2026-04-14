# LokiVLProxyRateLimiting

- Signal: sustained `reason="rate_limited"` client errors.
- Likely causes: burst traffic, low client limits, retry storms.

## Triage

1. `sum(rate(loki_vl_proxy_client_errors_total{reason="rate_limited"}[5m])) by (client, endpoint)`
2. Check per-client request volume and retry behavior.
3. Compare incident traffic with the current built-in limiter defaults (`50 req/s` per client, burst `100`).

## Mitigation

- Shape traffic at Grafana, ingress, or an outer proxy layer if the built-in limiter is too strict for the workload.
- Add client-side backoff/jitter and reduce retries.
- Isolate abusive client traffic if needed.

## Recovery Criteria

- Rate-limited errors return to expected baseline.
- Client-facing SLO returns to normal.

## Prevention

Apply [Deployment And Scaling Best Practices](deployment-best-practices.md) for client shaping, limit sizing, and retry-control practices.
