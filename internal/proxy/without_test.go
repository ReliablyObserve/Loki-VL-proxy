package proxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/szibis/Loki-VL-proxy/internal/cache"
)

// =============================================================================
// without() proper implementation — exclude labels from grouping
// =============================================================================

func TestWithout_ExcludesLabelsFromGrouping(t *testing.T) {
	// VL returns results grouped by all labels (app, pod, level)
	vlBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Return 3 series with different app+pod+level combinations
		w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[
			{"metric":{"app":"nginx","pod":"p1","level":"error"},"value":[1609459200,"10"]},
			{"metric":{"app":"nginx","pod":"p2","level":"error"},"value":[1609459200,"20"]},
			{"metric":{"app":"nginx","pod":"p1","level":"warn"},"value":[1609459200,"5"]}
		]}}`))
	}))
	defer vlBackend.Close()

	c := cache.New(60*time.Second, 10000)
	p, _ := New(Config{BackendURL: vlBackend.URL, Cache: c, LogLevel: "error"})

	// sum without (pod) → should group by all labels EXCEPT pod → group by (app, level)
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", `/loki/api/v1/query?query=sum+without+(pod)+(rate({app="nginx"}[5m]))&time=1609459200`, nil)
	mux := http.NewServeMux()
	p.RegisterRoutes(mux)
	mux.ServeHTTP(w, r)

	var resp struct {
		Status string `json:"status"`
		Data   struct {
			Result []struct {
				Metric map[string]string `json:"metric"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("parse error: %v, body: %s", err, w.Body.String())
	}

	if resp.Status != "success" {
		t.Fatalf("expected success, got %q", resp.Status)
	}

	// After without(pod), results should NOT have "pod" in metric labels
	for _, series := range resp.Data.Result {
		if _, hasPod := series.Metric["pod"]; hasPod {
			t.Errorf("without(pod) should remove 'pod' from metric labels, got %v", series.Metric)
		}
	}
}

func TestWithout_MultipleLabels(t *testing.T) {
	vlBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[
			{"metric":{"app":"nginx","pod":"p1","node":"n1"},"value":[1609459200,"10"]}
		]}}`))
	}))
	defer vlBackend.Close()

	c := cache.New(60*time.Second, 10000)
	p, _ := New(Config{BackendURL: vlBackend.URL, Cache: c, LogLevel: "error"})

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", `/loki/api/v1/query?query=sum+without+(pod,+node)+(rate({app="nginx"}[5m]))&time=1609459200`, nil)
	mux := http.NewServeMux()
	p.RegisterRoutes(mux)
	mux.ServeHTTP(w, r)

	var resp struct {
		Data struct {
			Result []struct {
				Metric map[string]string `json:"metric"`
			} `json:"result"`
		} `json:"data"`
	}
	json.Unmarshal(w.Body.Bytes(), &resp)

	for _, series := range resp.Data.Result {
		if _, has := series.Metric["pod"]; has {
			t.Errorf("without(pod, node) should remove 'pod', got %v", series.Metric)
		}
		if _, has := series.Metric["node"]; has {
			t.Errorf("without(pod, node) should remove 'node', got %v", series.Metric)
		}
		// "app" should still be present
		if _, has := series.Metric["app"]; !has {
			t.Errorf("without(pod, node) should keep 'app', got %v", series.Metric)
		}
	}
}

// =============================================================================
// on()/ignoring() proper implementation — label-subset matching
// =============================================================================

func TestOn_BinaryMatchesBySubset(t *testing.T) {
	callNum := 0
	vlBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callNum++
		w.Header().Set("Content-Type", "application/json")
		if callNum == 1 {
			// Left side: rate({app="a"}[5m])
			w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[
				{"metric":{"app":"a","pod":"p1"},"value":[1609459200,"100"]},
				{"metric":{"app":"a","pod":"p2"},"value":[1609459200,"200"]}
			]}}`))
		} else {
			// Right side: rate({app="a",level="error"}[5m])
			w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[
				{"metric":{"app":"a","level":"error"},"value":[1609459200,"10"]}
			]}}`))
		}
	}))
	defer vlBackend.Close()

	c := cache.New(60*time.Second, 10000)
	p, _ := New(Config{BackendURL: vlBackend.URL, Cache: c, LogLevel: "error"})

	// rate({app="a"}[5m]) / on(app) rate({app="a",level="error"}[5m])
	// on(app) means match only on "app" label — both sides have app="a" so they match
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/loki/api/v1/query?time=1609459200", nil)
	q := r.URL.Query()
	q.Set("query", `rate({app="a"}[5m]) / on(app) rate({app="a",level="error"}[5m])`)
	r.URL.RawQuery = q.Encode()
	mux := http.NewServeMux()
	p.RegisterRoutes(mux)
	mux.ServeHTTP(w, r)

	var resp struct {
		Status string `json:"status"`
		Data   struct {
			Result []struct {
				Metric map[string]string `json:"metric"`
				Value  []interface{}     `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	json.Unmarshal(w.Body.Bytes(), &resp)

	if resp.Status != "success" {
		t.Fatalf("expected success, got %q (body: %s)", resp.Status, w.Body.String())
	}

	// With on(app), both left series (p1, p2) should match the single right series
	// because they share app="a". Without on(app), exact key match would find no matches.
	if len(resp.Data.Result) == 0 {
		t.Error("on(app) should produce results by matching on app label subset")
	}
}

func TestIgnoring_ExcludesLabelsFromMatch(t *testing.T) {
	callNum := 0
	vlBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callNum++
		w.Header().Set("Content-Type", "application/json")
		if callNum == 1 {
			w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[
				{"metric":{"app":"a","pod":"p1"},"value":[1609459200,"100"]}
			]}}`))
		} else {
			w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[
				{"metric":{"app":"a","pod":"p2"},"value":[1609459200,"10"]}
			]}}`))
		}
	}))
	defer vlBackend.Close()

	c := cache.New(60*time.Second, 10000)
	p, _ := New(Config{BackendURL: vlBackend.URL, Cache: c, LogLevel: "error"})

	// rate({...}) / ignoring(pod) rate({...})
	// ignoring(pod) means match on all labels EXCEPT pod
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/loki/api/v1/query?time=1609459200", nil)
	q := r.URL.Query()
	q.Set("query", `rate({app="a"}[5m]) / ignoring(pod) rate({app="a"}[5m])`)
	r.URL.RawQuery = q.Encode()
	mux := http.NewServeMux()
	p.RegisterRoutes(mux)
	mux.ServeHTTP(w, r)

	var resp struct {
		Status string `json:"status"`
		Data   struct {
			Result []struct {
				Value []interface{} `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	json.Unmarshal(w.Body.Bytes(), &resp)

	// ignoring(pod) means the two series match on app="a" even though pod differs
	if len(resp.Data.Result) == 0 {
		t.Error("ignoring(pod) should produce results by ignoring pod in matching")
	}
}
