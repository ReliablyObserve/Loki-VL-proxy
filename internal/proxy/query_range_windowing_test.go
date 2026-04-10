package proxy

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ReliablyObserve/Loki-VL-proxy/internal/cache"
)

func TestQueryRangeWindow_ExpandingRangeReusesCachedWindows(t *testing.T) {
	var backendCalls atomic.Int64
	seenWindows := map[string]int{}
	var mu sync.Mutex

	vlBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backendCalls.Add(1)
		if r.URL.Path != "/select/logsql/query" {
			t.Fatalf("unexpected backend path: %s", r.URL.Path)
		}
		_ = r.ParseForm()
		start := r.Form.Get("start")
		end := r.Form.Get("end")
		key := start + ":" + end
		mu.Lock()
		seenWindows[key]++
		mu.Unlock()

		endNs, _ := strconv.ParseInt(end, 10, 64)
		msg := fmt.Sprintf("window=%s", key)
		_, _ = fmt.Fprintf(w, "{\"_time\":%q,\"_msg\":%q,\"_stream\":\"{app=\\\"nginx\\\"}\"}\n", time.Unix(0, endNs).UTC().Format(time.RFC3339Nano), msg)
	}))
	defer vlBackend.Close()

	p := newWindowingTestProxy(t, vlBackend.URL)
	start := time.Now().Add(-48 * time.Hour).UTC().Truncate(time.Hour).UnixNano()
	endA := start + int64(3*time.Hour) - 1
	endB := start + int64(4*time.Hour) - 1

	reqA := httptest.NewRequest("GET", fmt.Sprintf("/loki/api/v1/query_range?query=%s&start=%d&end=%d&limit=100", url.QueryEscape(`{app="nginx"}`), start, endA), nil)
	wA := httptest.NewRecorder()
	p.handleQueryRange(wA, reqA)
	if wA.Code != http.StatusOK {
		t.Fatalf("unexpected status for initial range: %d body=%s", wA.Code, wA.Body.String())
	}
	if got := backendCalls.Load(); got != 3 {
		t.Fatalf("expected 3 backend window calls for initial range, got %d", got)
	}
	mu.Lock()
	if len(seenWindows) != 3 {
		t.Fatalf("expected 3 unique initial windows, got %d", len(seenWindows))
	}
	mu.Unlock()

	reqB := httptest.NewRequest("GET", fmt.Sprintf("/loki/api/v1/query_range?query=%s&start=%d&end=%d&limit=100", url.QueryEscape(`{app="nginx"}`), start, endB), nil)
	wB := httptest.NewRecorder()
	p.handleQueryRange(wB, reqB)
	if wB.Code != http.StatusOK {
		t.Fatalf("unexpected status for expanded range: %d body=%s", wB.Code, wB.Body.String())
	}
	if got := backendCalls.Load(); got != 4 {
		t.Fatalf("expected exactly one additional backend call after expansion, got %d", got)
	}
	mu.Lock()
	if len(seenWindows) != 4 {
		t.Fatalf("expected 4 unique windows after expansion, got %d", len(seenWindows))
	}
	mu.Unlock()
}

func TestQueryRangeWindow_LimitAndDirection(t *testing.T) {
	vlBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		startNs, _ := strconv.ParseInt(r.Form.Get("start"), 10, 64)
		endNs, _ := strconv.ParseInt(r.Form.Get("end"), 10, 64)
		query := r.Form.Get("query")
		desc := strings.Contains(query, "_time desc")

		var tsA, tsB int64
		if desc {
			tsA, tsB = endNs, maxInt64(startNs, endNs-1)
		} else {
			tsA, tsB = startNs, minInt64(endNs, startNs+1)
		}
		_, _ = fmt.Fprintf(w, "{\"_time\":%q,\"_msg\":\"a\",\"_stream\":\"{app=\\\"nginx\\\"}\"}\n", time.Unix(0, tsA).UTC().Format(time.RFC3339Nano))
		_, _ = fmt.Fprintf(w, "{\"_time\":%q,\"_msg\":\"b\",\"_stream\":\"{app=\\\"nginx\\\"}\"}\n", time.Unix(0, tsB).UTC().Format(time.RFC3339Nano))
	}))
	defer vlBackend.Close()

	p := newWindowingTestProxy(t, vlBackend.URL)
	start := time.Now().Add(-24 * time.Hour).UTC().Truncate(time.Hour).UnixNano()
	end := start + int64(2*time.Hour) - 1
	oldestWindowStart := start
	newestWindowEnd := end

	backwardReq := httptest.NewRequest("GET", fmt.Sprintf("/loki/api/v1/query_range?query=%s&start=%d&end=%d&limit=2", url.QueryEscape(`{app="nginx"}`), start, end), nil)
	backwardResp := httptest.NewRecorder()
	p.handleQueryRange(backwardResp, backwardReq)
	assertQueryRangeFirstTimestamp(t, backwardResp, newestWindowEnd)

	forwardReq := httptest.NewRequest("GET", fmt.Sprintf("/loki/api/v1/query_range?query=%s&start=%d&end=%d&limit=2&direction=forward", url.QueryEscape(`{app="nginx"}`), start, end), nil)
	forwardResp := httptest.NewRecorder()
	p.handleQueryRange(forwardResp, forwardReq)
	assertQueryRangeFirstTimestamp(t, forwardResp, oldestWindowStart)
}

func TestQueryRangeWindow_ParserChainBraceHeavyLine(t *testing.T) {
	vlBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		startNs, _ := strconv.ParseInt(r.Form.Get("start"), 10, 64)
		msg := `time="2026-01-01T00:00:00Z" level=error msg="Drop if no profiles matched {json_like=true}"`
		_, _ = fmt.Fprintf(w, "{\"_time\":%q,\"_msg\":%q,\"_stream\":\"{app=\\\"iptables\\\"}\"}\n", time.Unix(0, startNs).UTC().Format(time.RFC3339Nano), msg)
	}))
	defer vlBackend.Close()

	p := newWindowingTestProxy(t, vlBackend.URL)
	start := time.Now().Add(-12 * time.Hour).UTC().Truncate(time.Hour).UnixNano()
	end := start + int64(2*time.Hour) - 1
	q := `{app="iptables"} | json | logfmt | drop __error__, __error_details__`
	req := httptest.NewRequest("GET", fmt.Sprintf("/loki/api/v1/query_range?query=%s&start=%d&end=%d&limit=100", url.QueryEscape(q), start, end), nil)
	resp := httptest.NewRecorder()
	p.handleQueryRange(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", resp.Code, resp.Body.String())
	}
	var parsed map[string]interface{}
	if err := json.Unmarshal(resp.Body.Bytes(), &parsed); err != nil {
		t.Fatalf("failed to decode response: %v body=%s", err, resp.Body.String())
	}
	data, _ := parsed["data"].(map[string]interface{})
	result, _ := data["result"].([]interface{})
	if len(result) == 0 {
		t.Fatalf("expected non-empty result for parser-chain query")
	}
	streamObj, _ := result[0].(map[string]interface{})
	values, _ := streamObj["values"].([]interface{})
	if len(values) == 0 {
		t.Fatalf("expected non-empty values")
	}
	first, ok := values[0].([]interface{})
	if !ok || len(first) < 2 {
		t.Fatalf("expected Loki tuple shape []interface{} with at least 2 items, got %#v", values[0])
	}
}

func TestQueryRangeWindow_MultiTenantCompatibility(t *testing.T) {
	vlBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		startNs, _ := strconv.ParseInt(r.Form.Get("start"), 10, 64)
		tenant := r.Header.Get("X-Scope-OrgID")
		_, _ = fmt.Fprintf(w, "{\"_time\":%q,\"_msg\":%q,\"_stream\":\"{app=\\\"nginx\\\"}\"}\n", time.Unix(0, startNs).UTC().Format(time.RFC3339Nano), "tenant="+tenant)
	}))
	defer vlBackend.Close()

	p := newWindowingTestProxy(t, vlBackend.URL)
	start := time.Now().Add(-8 * time.Hour).UTC().Truncate(time.Hour).UnixNano()
	end := start + int64(2*time.Hour) - 1
	req := httptest.NewRequest("GET", fmt.Sprintf("/loki/api/v1/query_range?query=%s&start=%d&end=%d&limit=100", url.QueryEscape(`{app="nginx"}`), start, end), nil)
	req.Header.Set("X-Scope-OrgID", "1|2")
	resp := httptest.NewRecorder()
	p.handleQueryRange(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", resp.Code, resp.Body.String())
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(resp.Body.Bytes(), &parsed); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	data, _ := parsed["data"].(map[string]interface{})
	result, _ := data["result"].([]interface{})
	seenTenants := map[string]bool{}
	for _, item := range result {
		streamObj, _ := item.(map[string]interface{})
		streamLabels, _ := streamObj["stream"].(map[string]interface{})
		if tenant, ok := streamLabels["__tenant_id__"].(string); ok {
			seenTenants[tenant] = true
		}
	}
	if !seenTenants["1"] || !seenTenants["2"] {
		t.Fatalf("expected merged response to include __tenant_id__ labels for both tenants, got=%v", seenTenants)
	}
}

func newWindowingTestProxy(t *testing.T, backendURL string) *Proxy {
	t.Helper()
	c := cache.New(60*time.Second, 10000)
	p, err := New(Config{
		BackendURL:                      backendURL,
		Cache:                           c,
		LogLevel:                        "error",
		QueryRangeWindowingEnabled:      true,
		QueryRangeSplitInterval:         time.Hour,
		QueryRangeMaxParallel:           2,
		QueryRangeAdaptiveParallel:      false,
		QueryRangeParallelMin:           1,
		QueryRangeParallelMax:           2,
		QueryRangeLatencyTarget:         1500 * time.Millisecond,
		QueryRangeLatencyBackoff:        3 * time.Second,
		QueryRangeAdaptiveCooldown:      30 * time.Second,
		QueryRangeErrorBackoffThreshold: 0.02,
		QueryRangeFreshness:             10 * time.Minute,
		QueryRangeRecentCacheTTL:        0,
		QueryRangeHistoryCacheTTL:       24 * time.Hour,
	})
	if err != nil {
		t.Fatalf("failed to create proxy: %v", err)
	}
	return p
}

func assertQueryRangeFirstTimestamp(t *testing.T, rec *httptest.ResponseRecorder, expectedTs int64) {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}
	var parsed map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &parsed); err != nil {
		t.Fatalf("failed to parse response: %v body=%s", err, rec.Body.String())
	}
	data, _ := parsed["data"].(map[string]interface{})
	result, _ := data["result"].([]interface{})
	if len(result) == 0 {
		t.Fatalf("expected non-empty result")
	}
	streamObj, _ := result[0].(map[string]interface{})
	values, _ := streamObj["values"].([]interface{})
	if len(values) == 0 {
		t.Fatalf("expected non-empty stream values")
	}
	first, _ := values[0].([]interface{})
	if len(first) < 1 {
		t.Fatalf("expected tuple in values[0], got %#v", values[0])
	}
	tsStr, _ := first[0].(string)
	tsInt, err := strconv.ParseInt(tsStr, 10, 64)
	if err != nil {
		t.Fatalf("expected numeric timestamp, got %q", tsStr)
	}
	if tsInt != expectedTs {
		t.Fatalf("unexpected first timestamp: got=%d want=%d", tsInt, expectedTs)
	}
}

func minInt64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
