package metrics

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestMetrics_Handler_Output(t *testing.T) {
	m := NewMetrics()
	m.RecordRequest("labels", 200, 5*time.Millisecond)
	m.RecordRequest("query_range", 500, 100*time.Millisecond)
	m.RecordCacheHit()
	m.RecordCacheMiss()
	m.RecordTranslation()
	m.RecordTranslationError()

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/metrics", nil)
	m.Handler(w, r)

	body := w.Body.String()

	// Content-Type
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "text/plain") {
		t.Errorf("expected text/plain content type, got %q", ct)
	}

	// Request counters
	if !strings.Contains(body, "loki_vl_proxy_requests_total") {
		t.Error("missing loki_vl_proxy_requests_total")
	}
	if !strings.Contains(body, `endpoint="labels"`) {
		t.Error("missing labels endpoint in metrics")
	}
	if !strings.Contains(body, `status="200"`) {
		t.Error("missing status=200 in metrics")
	}

	// Histogram
	if !strings.Contains(body, "loki_vl_proxy_request_duration_seconds_bucket") {
		t.Error("missing duration histogram buckets")
	}
	if !strings.Contains(body, "loki_vl_proxy_request_duration_seconds_sum") {
		t.Error("missing duration histogram sum")
	}
	if !strings.Contains(body, "loki_vl_proxy_request_duration_seconds_count") {
		t.Error("missing duration histogram count")
	}

	// Cache
	if !strings.Contains(body, "loki_vl_proxy_cache_hits_total 1") {
		t.Error("expected cache_hits_total 1")
	}
	if !strings.Contains(body, "loki_vl_proxy_cache_misses_total 1") {
		t.Error("expected cache_misses_total 1")
	}

	// Translations
	if !strings.Contains(body, "loki_vl_proxy_translations_total 1") {
		t.Error("expected translations_total 1")
	}
	if !strings.Contains(body, "loki_vl_proxy_translation_errors_total 1") {
		t.Error("expected translation_errors_total 1")
	}

	// Uptime
	if !strings.Contains(body, "loki_vl_proxy_uptime_seconds") {
		t.Error("missing uptime metric")
	}
}

func TestMetrics_Handler_EmptyState(t *testing.T) {
	m := NewMetrics()
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/metrics", nil)
	m.Handler(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "loki_vl_proxy_cache_hits_total 0") {
		t.Error("expected zero cache hits")
	}
}

func TestMetrics_RecordTranslationError(t *testing.T) {
	m := NewMetrics()
	m.RecordTranslationError()
	m.RecordTranslationError()
	if m.translationErrors.Load() != 2 {
		t.Errorf("expected 2, got %d", m.translationErrors.Load())
	}
}

func TestResolveClientID_IgnoresUntrustedProxyHeadersByDefault(t *testing.T) {
	req := httptest.NewRequest("GET", "/metrics", nil)
	req.Header.Set("X-Grafana-User", "grafana-user")
	req.Header.Set("X-Forwarded-For", "203.0.113.10")
	req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("backend-user:secret")))
	req.RemoteAddr = "198.51.100.20:1234"

	got := ResolveClientID(req, false)
	if got != "backend-user" {
		t.Fatalf("expected basic auth user when proxy headers are untrusted, got %q", got)
	}
}

func TestResolveClientID_UsesTrustedGrafanaUser(t *testing.T) {
	req := httptest.NewRequest("GET", "/metrics", nil)
	req.Header.Set("X-Grafana-User", "grafana-user")
	req.Header.Set("X-Scope-OrgID", "tenant-a")
	req.RemoteAddr = "198.51.100.20:1234"

	got := ResolveClientID(req, true)
	if got != "grafana-user" {
		t.Fatalf("expected trusted grafana user to win, got %q", got)
	}
}

func TestMetrics_Handler_ExportsClientCentricBreakdowns(t *testing.T) {
	m := NewMetrics()
	m.RecordClientIdentity("grafana-user", "query_range", 20*time.Millisecond, 512)
	m.RecordClientStatus("grafana-user", "query_range", http.StatusTooManyRequests)
	m.RecordClientInflight("grafana-user", 1)
	m.RecordClientQueryLength("grafana-user", "query_range", len(`{app="api"}`))

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/metrics", nil)
	m.Handler(w, r)

	body := w.Body.String()
	for _, metric := range []string{
		"loki_vl_proxy_client_status_total",
		"loki_vl_proxy_client_inflight_requests",
		"loki_vl_proxy_client_query_length_chars_bucket",
	} {
		if !strings.Contains(body, metric) {
			t.Fatalf("expected %s in metrics output", metric)
		}
	}
	if !strings.Contains(body, `client="grafana-user"`) {
		t.Fatal("expected client label in client-centric metrics")
	}
	if !strings.Contains(body, `status="429"`) {
		t.Fatal("expected per-client status metric for 429")
	}
}

func TestMetrics_Handler_BoundsTenantAndClientCardinality(t *testing.T) {
	m := NewMetricsWithLimits(1, 1)
	m.RecordTenantRequest("team-a", "query_range", 200, 10*time.Millisecond)
	m.RecordTenantRequest("team-b", "query_range", 200, 10*time.Millisecond)
	m.RecordClientIdentity("client-a", "query_range", 10*time.Millisecond, 10)
	m.RecordClientIdentity("client-b", "query_range", 10*time.Millisecond, 10)

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/metrics", nil)
	m.Handler(w, r)

	body := w.Body.String()
	if !strings.Contains(body, `tenant="team-a"`) {
		t.Fatal("expected first tenant label to be retained")
	}
	if !strings.Contains(body, `tenant="__overflow__"`) {
		t.Fatal("expected overflow tenant bucket in metrics output")
	}
	if strings.Contains(body, `tenant="team-b"`) {
		t.Fatal("expected second tenant to be folded into overflow bucket")
	}
	if !strings.Contains(body, `client="client-a"`) {
		t.Fatal("expected first client label to be retained")
	}
	if !strings.Contains(body, `client="__overflow__"`) {
		t.Fatal("expected overflow client bucket in metrics output")
	}
	if strings.Contains(body, `client="client-b"`) {
		t.Fatal("expected second client to be folded into overflow bucket")
	}
}
