package main

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ReliablyObserve/Loki-VL-proxy/internal/metrics"
)

func TestHTTPConnRotator_RequestLimitClosesHTTP1Keepalive(t *testing.T) {
	m := metrics.NewMetrics()
	rotator := newHTTPConnRotator(httpConnRotationConfig{
		maxRequests: 2,
	}, m, nil)
	if rotator == nil {
		t.Fatal("expected rotator")
	}
	connA, connB := net.Pipe()
	defer func() { _ = connA.Close() }()
	defer func() { _ = connB.Close() }()

	ctx := rotator.ConnContextHook()(context.Background(), connA)
	handler := rotator.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req1 := httptest.NewRequest(http.MethodGet, "http://example.test/loki/api/v1/query_range", nil).WithContext(ctx)
	req1.ProtoMajor = 1
	rec1 := httptest.NewRecorder()
	handler.ServeHTTP(rec1, req1)
	if got := rec1.Header().Get("Connection"); got != "" {
		t.Fatalf("expected first request to stay open, got Connection=%q", got)
	}

	req2 := httptest.NewRequest(http.MethodGet, "http://example.test/loki/api/v1/query_range", nil).WithContext(ctx)
	req2.ProtoMajor = 1
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)
	if got := rec2.Header().Get("Connection"); got != "close" {
		t.Fatalf("expected request limit rotation, got Connection=%q", got)
	}

	metricsRec := httptest.NewRecorder()
	m.Handler(metricsRec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(metricsRec.Body.String(), `loki_vl_proxy_http_connection_rotations_total{reason="request_limit"} 1`) {
		t.Fatalf("expected request_limit rotation metric, got\n%s", metricsRec.Body.String())
	}
}

func TestHTTPConnRotator_OverloadAgeOnlyAppliesToHTTP1(t *testing.T) {
	m := metrics.NewMetrics()
	rotator := newHTTPConnRotator(httpConnRotationConfig{
		maxAge:         10 * time.Minute,
		overloadMaxAge: 30 * time.Second,
	}, m, func() bool { return true })
	if rotator == nil {
		t.Fatal("expected rotator")
	}
	state := &httpConnState{
		acceptedAt:     time.Now().Add(-2 * time.Minute),
		maxAge:         10 * time.Minute,
		overloadMaxAge: 30 * time.Second,
	}
	h1Req := httptest.NewRequest(http.MethodGet, "http://example.test/loki/api/v1/query_range", nil).WithContext(context.WithValue(context.Background(), httpConnStateContextKey{}, state))
	h1Req.ProtoMajor = 1
	if reason := rotator.recordAndDecide(h1Req, state, time.Now()); reason != "overload" {
		t.Fatalf("expected overload rotation, got %q", reason)
	}

	h2Req := httptest.NewRequest(http.MethodGet, "http://example.test/loki/api/v1/query_range", nil).WithContext(context.WithValue(context.Background(), httpConnStateContextKey{}, state))
	h2Req.ProtoMajor = 2
	if reason := rotator.recordAndDecide(h2Req, state, time.Now()); reason != "" {
		t.Fatalf("expected HTTP/2 request to bypass rotation, got %q", reason)
	}
}

func TestJitterDurationNeverReturnsNegativeAge(t *testing.T) {
	if got := jitterDuration(5*time.Second, 30*time.Second, nil, 1); got <= 0 {
		t.Fatalf("expected positive jittered duration, got %s", got)
	}
}
