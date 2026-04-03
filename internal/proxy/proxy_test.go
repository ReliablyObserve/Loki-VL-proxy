package proxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/szibis/Loki-VL-proxy/internal/cache"
)

// TestLokiResponseContract_Labels verifies /loki/api/v1/labels returns
// the exact Loki response format: {"status":"success","data":[...strings]}
func TestLokiResponseContract_Labels(t *testing.T) {
	// Mock VL backend returning field_names
	vlBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/select/logsql/field_names" {
			t.Errorf("unexpected VL path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"values": []map[string]interface{}{
				{"value": "app", "hits": 100},
				{"value": "env", "hits": 50},
				{"value": "_msg", "hits": 200},
			},
		})
	}))
	defer vlBackend.Close()

	p := newTestProxy(t, vlBackend.URL)
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/loki/api/v1/labels", nil)
	p.handleLabels(w, r)

	assertStatusCode(t, w, http.StatusOK)
	assertContentType(t, w, "application/json")

	var resp map[string]interface{}
	mustUnmarshal(t, w.Body.Bytes(), &resp)

	// Loki contract: status MUST be "success"
	if resp["status"] != "success" {
		t.Errorf("expected status=success, got %v", resp["status"])
	}

	// Loki contract: data MUST be a string array
	data, ok := resp["data"].([]interface{})
	if !ok {
		t.Fatalf("data must be an array, got %T", resp["data"])
	}

	// Every element must be a string
	for i, v := range data {
		if _, ok := v.(string); !ok {
			t.Errorf("data[%d] must be string, got %T: %v", i, v, v)
		}
	}

	if len(data) != 3 {
		t.Errorf("expected 3 labels, got %d", len(data))
	}
}

// TestLokiResponseContract_LabelValues verifies /loki/api/v1/label/{name}/values.
func TestLokiResponseContract_LabelValues(t *testing.T) {
	vlBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify proxy sends correct VL field param
		field := r.URL.Query().Get("field")
		if field != "app" {
			t.Errorf("expected field=app, got %q", field)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"values": []map[string]interface{}{
				{"value": "nginx", "hits": 100},
				{"value": "api", "hits": 50},
			},
		})
	}))
	defer vlBackend.Close()

	p := newTestProxy(t, vlBackend.URL)
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/loki/api/v1/label/app/values", nil)
	p.handleLabelValues(w, r)

	assertStatusCode(t, w, http.StatusOK)

	var resp map[string]interface{}
	mustUnmarshal(t, w.Body.Bytes(), &resp)

	if resp["status"] != "success" {
		t.Errorf("expected status=success, got %v", resp["status"])
	}

	data, ok := resp["data"].([]interface{})
	if !ok {
		t.Fatalf("data must be an array, got %T", resp["data"])
	}

	values := make([]string, len(data))
	for i, v := range data {
		s, ok := v.(string)
		if !ok {
			t.Fatalf("data[%d] must be string, got %T", i, v)
		}
		values[i] = s
	}

	if len(values) != 2 || values[0] != "nginx" || values[1] != "api" {
		t.Errorf("unexpected label values: %v", values)
	}
}

// TestLokiResponseContract_QueryRange_Streams verifies log query responses
// match the Loki streams format exactly.
func TestLokiResponseContract_QueryRange_Streams(t *testing.T) {
	vlBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// VL /select/logsql/query returns NDJSON
		w.Header().Set("Content-Type", "application/stream+json")
		lines := []string{
			`{"_time":"2024-01-15T10:30:00Z","_msg":"error in service","_stream":"{app=\"nginx\"}","level":"error"}`,
			`{"_time":"2024-01-15T10:30:01Z","_msg":"request completed","_stream":"{app=\"nginx\"}","level":"info"}`,
		}
		for _, line := range lines {
			w.Write([]byte(line + "\n"))
		}
	}))
	defer vlBackend.Close()

	p := newTestProxy(t, vlBackend.URL)
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/loki/api/v1/query_range?query=%7Bapp%3D%22nginx%22%7D&start=1705312200&end=1705312300&limit=100", nil)
	p.handleQueryRange(w, r)

	assertStatusCode(t, w, http.StatusOK)

	var resp map[string]interface{}
	mustUnmarshal(t, w.Body.Bytes(), &resp)

	// Loki contract checks
	if resp["status"] != "success" {
		t.Fatalf("expected status=success, got %v", resp["status"])
	}

	data, ok := resp["data"].(map[string]interface{})
	if !ok {
		t.Fatalf("data must be an object, got %T", resp["data"])
	}

	// For log queries, resultType MUST be "streams"
	if data["resultType"] != "streams" {
		t.Errorf("expected resultType=streams, got %v", data["resultType"])
	}

	result, ok := data["result"].([]interface{})
	if !ok {
		t.Fatalf("result must be an array, got %T", data["result"])
	}

	if len(result) == 0 {
		t.Fatal("result must not be empty — VL returned 2 log lines")
	}

	// Each result entry must have "stream" (map) and "values" (array of [ts, line])
	for i, entry := range result {
		obj, ok := entry.(map[string]interface{})
		if !ok {
			t.Fatalf("result[%d] must be object, got %T", i, entry)
		}

		// "stream" must be a map of string→string
		stream, ok := obj["stream"].(map[string]interface{})
		if !ok {
			t.Fatalf("result[%d].stream must be object, got %T", i, obj["stream"])
		}
		for k, v := range stream {
			if _, ok := v.(string); !ok {
				t.Errorf("result[%d].stream[%q] must be string, got %T", i, k, v)
			}
		}

		// "values" must be array of [nanosecond_string, log_line_string]
		values, ok := obj["values"].([]interface{})
		if !ok {
			t.Fatalf("result[%d].values must be array, got %T", i, obj["values"])
		}

		for j, val := range values {
			pair, ok := val.([]interface{})
			if !ok || len(pair) != 2 {
				t.Errorf("result[%d].values[%d] must be [ts, line] pair, got %v", i, j, val)
				continue
			}

			// ts must be a string of nanosecond epoch
			ts, ok := pair[0].(string)
			if !ok {
				t.Errorf("result[%d].values[%d][0] (timestamp) must be string, got %T", i, j, pair[0])
			}
			if len(ts) < 10 {
				t.Errorf("result[%d].values[%d][0] timestamp too short: %q", i, j, ts)
			}

			// line must be a string
			if _, ok := pair[1].(string); !ok {
				t.Errorf("result[%d].values[%d][1] (line) must be string, got %T", i, j, pair[1])
			}
		}
	}
}

// TestLokiResponseContract_Series verifies /loki/api/v1/series returns
// {"status":"success","data":[{label_map}, ...]}
func TestLokiResponseContract_Series(t *testing.T) {
	vlBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"values": []map[string]interface{}{
				{"value": `{app="nginx",env="prod"}`, "hits": 100},
				{"value": `{app="api",env="staging"}`, "hits": 50},
			},
		})
	}))
	defer vlBackend.Close()

	p := newTestProxy(t, vlBackend.URL)
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/loki/api/v1/series?match[]=%7Bapp%3D%22nginx%22%7D", nil)
	r.ParseForm()
	p.handleSeries(w, r)

	assertStatusCode(t, w, http.StatusOK)

	var resp map[string]interface{}
	mustUnmarshal(t, w.Body.Bytes(), &resp)

	if resp["status"] != "success" {
		t.Errorf("expected status=success, got %v", resp["status"])
	}

	data, ok := resp["data"].([]interface{})
	if !ok {
		t.Fatalf("data must be array, got %T", resp["data"])
	}

	// Each entry must be a label map (string→string)
	for i, entry := range data {
		labelMap, ok := entry.(map[string]interface{})
		if !ok {
			t.Fatalf("data[%d] must be object, got %T", i, entry)
		}
		for k, v := range labelMap {
			if _, ok := v.(string); !ok {
				t.Errorf("data[%d][%q] must be string, got %T", i, k, v)
			}
		}
	}
}

// TestLokiResponseContract_BuildInfo verifies buildinfo response.
func TestLokiResponseContract_BuildInfo(t *testing.T) {
	p := newTestProxy(t, "http://unused")
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/loki/api/v1/status/buildinfo", nil)
	p.handleBuildInfo(w, r)

	var resp map[string]interface{}
	mustUnmarshal(t, w.Body.Bytes(), &resp)

	if resp["status"] != "success" {
		t.Errorf("expected status=success, got %v", resp["status"])
	}

	data, ok := resp["data"].(map[string]interface{})
	if !ok {
		t.Fatalf("data must be object, got %T", resp["data"])
	}

	// Loki buildinfo must have version
	if _, ok := data["version"]; !ok {
		t.Error("buildinfo missing 'version' field")
	}
}

// TestLokiResponseContract_IndexStats verifies stub returns valid structure.
func TestLokiResponseContract_IndexStats(t *testing.T) {
	p := newTestProxy(t, "http://unused")
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/loki/api/v1/index/stats?query=%7B%7D", nil)
	p.handleIndexStats(w, r)

	var resp map[string]interface{}
	mustUnmarshal(t, w.Body.Bytes(), &resp)

	// Loki index/stats returns: streams, chunks, entries, bytes (all numbers)
	for _, field := range []string{"streams", "chunks", "bytes", "entries"} {
		if _, ok := resp[field]; !ok {
			t.Errorf("index/stats missing field %q", field)
		}
	}
}

// TestLokiResponseContract_Volume verifies volume stub.
func TestLokiResponseContract_Volume(t *testing.T) {
	p := newTestProxy(t, "http://unused")
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/loki/api/v1/index/volume", nil)
	p.handleVolume(w, r)

	var resp map[string]interface{}
	mustUnmarshal(t, w.Body.Bytes(), &resp)

	if resp["status"] != "success" {
		t.Errorf("expected status=success")
	}
	data, ok := resp["data"].(map[string]interface{})
	if !ok {
		t.Fatalf("data must be object")
	}
	if data["resultType"] != "vector" {
		t.Errorf("expected resultType=vector, got %v", data["resultType"])
	}
}

// TestLokiResponseContract_VolumeRange verifies volume_range stub.
func TestLokiResponseContract_VolumeRange(t *testing.T) {
	p := newTestProxy(t, "http://unused")
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/loki/api/v1/index/volume_range", nil)
	p.handleVolumeRange(w, r)

	var resp map[string]interface{}
	mustUnmarshal(t, w.Body.Bytes(), &resp)

	if resp["status"] != "success" {
		t.Errorf("expected status=success")
	}
	data, ok := resp["data"].(map[string]interface{})
	if !ok {
		t.Fatalf("data must be object")
	}
	if data["resultType"] != "matrix" {
		t.Errorf("expected resultType=matrix, got %v", data["resultType"])
	}
}

// TestCacheProtectsBackend verifies repeated label requests hit cache.
func TestCacheProtectsBackend(t *testing.T) {
	callCount := 0
	vlBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		json.NewEncoder(w).Encode(map[string]interface{}{
			"values": []map[string]interface{}{
				{"value": "app", "hits": 100},
			},
		})
	}))
	defer vlBackend.Close()

	p := newTestProxy(t, vlBackend.URL)

	// First call — cache miss
	w1 := httptest.NewRecorder()
	r1 := httptest.NewRequest("GET", "/loki/api/v1/labels?start=1&end=2", nil)
	p.handleLabels(w1, r1)
	if callCount != 1 {
		t.Fatalf("expected 1 backend call, got %d", callCount)
	}

	// Second call with same params — cache hit
	w2 := httptest.NewRecorder()
	r2 := httptest.NewRequest("GET", "/loki/api/v1/labels?start=1&end=2", nil)
	p.handleLabels(w2, r2)
	if callCount != 1 {
		t.Errorf("expected cache hit (still 1 backend call), got %d", callCount)
	}

	// Third call with different params — cache miss
	w3 := httptest.NewRecorder()
	r3 := httptest.NewRequest("GET", "/loki/api/v1/labels?start=3&end=4", nil)
	p.handleLabels(w3, r3)
	if callCount != 2 {
		t.Errorf("expected 2 backend calls after cache miss, got %d", callCount)
	}
}

// TestTranslationPassedToBackend verifies LogQL is translated before forwarding.
func TestTranslationPassedToBackend(t *testing.T) {
	var receivedQuery string
	vlBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		receivedQuery = r.FormValue("query")
		// Return empty NDJSON
		w.Write([]byte{})
	}))
	defer vlBackend.Close()

	p := newTestProxy(t, vlBackend.URL)
	w := httptest.NewRecorder()
	// Send LogQL query with line filter (URL-encoded)
	r := httptest.NewRequest("GET", `/loki/api/v1/query_range?query=%7Bapp%3D%22nginx%22%7D+%7C%3D+%22error%22&start=1&end=2&limit=10`, nil)
	p.handleQueryRange(w, r)

	// Proxy should have translated |= "error" to "error"
	if receivedQuery != `{app="nginx"} "error"` {
		t.Errorf("expected translated query, got %q", receivedQuery)
	}
}

// TestDetectedFieldsResponse verifies /loki/api/v1/detected_fields format.
func TestDetectedFieldsResponse(t *testing.T) {
	vlBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"values": []map[string]interface{}{
				{"value": "level", "hits": 1000},
				{"value": "duration", "hits": 500},
			},
		})
	}))
	defer vlBackend.Close()

	p := newTestProxy(t, vlBackend.URL)
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/loki/api/v1/detected_fields?query=*", nil)
	p.handleDetectedFields(w, r)

	var resp map[string]interface{}
	mustUnmarshal(t, w.Body.Bytes(), &resp)

	fields, ok := resp["fields"].([]interface{})
	if !ok {
		t.Fatalf("fields must be array, got %T", resp["fields"])
	}

	for i, f := range fields {
		obj, ok := f.(map[string]interface{})
		if !ok {
			t.Fatalf("fields[%d] must be object", i)
		}
		// Each field must have label, type, cardinality
		if _, ok := obj["label"]; !ok {
			t.Errorf("fields[%d] missing 'label'", i)
		}
		if _, ok := obj["type"]; !ok {
			t.Errorf("fields[%d] missing 'type'", i)
		}
	}
}

// TestMetricsEndpoint verifies /metrics returns Prometheus format.
func TestMetricsEndpoint(t *testing.T) {
	p := newTestProxy(t, "http://unused")
	mux := http.NewServeMux()
	p.RegisterRoutes(mux)

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/metrics", nil)
	mux.ServeHTTP(w, r)

	body := w.Body.String()
	requiredMetrics := []string{
		"loki_vl_proxy_requests_total",
		"loki_vl_proxy_cache_hits_total",
		"loki_vl_proxy_cache_misses_total",
		"loki_vl_proxy_uptime_seconds",
	}

	for _, m := range requiredMetrics {
		if !containsString(body, m) {
			t.Errorf("/metrics missing: %s", m)
		}
	}
}

// --- helpers ---

func newTestProxy(t *testing.T, backendURL string) *Proxy {
	t.Helper()
	c := cache.New(60*time.Second, 1000)
	p, err := New(Config{
		BackendURL: backendURL,
		Cache:      c,
		LogLevel:   "error", // quiet during tests
	})
	if err != nil {
		t.Fatalf("failed to create proxy: %v", err)
	}
	return p
}

func assertStatusCode(t *testing.T, w *httptest.ResponseRecorder, expected int) {
	t.Helper()
	if w.Code != expected {
		t.Errorf("expected status %d, got %d (body: %s)", expected, w.Code, w.Body.String())
	}
}

func assertContentType(t *testing.T, w *httptest.ResponseRecorder, expected string) {
	t.Helper()
	ct := w.Header().Get("Content-Type")
	if ct != expected {
		t.Errorf("expected Content-Type %q, got %q", expected, ct)
	}
}

func mustUnmarshal(t *testing.T, data []byte, v interface{}) {
	t.Helper()
	if err := json.Unmarshal(data, v); err != nil {
		t.Fatalf("failed to unmarshal JSON: %v (body: %s)", err, string(data))
	}
}

func containsString(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && stringContains(s, substr))
}

func stringContains(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
