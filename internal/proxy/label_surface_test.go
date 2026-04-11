package proxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/ReliablyObserve/Loki-VL-proxy/internal/cache"
)

func TestLabelSurface_LabelsIncludeConfiguredExtras(t *testing.T) {
	vlBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/select/logsql/stream_field_names" {
			t.Fatalf("unexpected backend path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"values":[{"value":"app","hits":10}]}`))
	}))
	defer vlBackend.Close()

	p, err := New(Config{
		BackendURL:        vlBackend.URL,
		Cache:             cache.New(60*time.Second, 1000),
		LogLevel:          "error",
		LabelStyle:        LabelStyleUnderscores,
		ExtraLabelFields:  []string{"host.id", "custom.pipeline.processing"},
		MetadataFieldMode: MetadataFieldModeTranslated,
	})
	if err != nil {
		t.Fatalf("failed to create proxy: %v", err)
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/loki/api/v1/labels", nil)
	p.handleLabels(w, r)

	var resp struct {
		Data []string `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode labels response: %v", err)
	}
	if !contains(resp.Data, "app") || !contains(resp.Data, "host_id") || !contains(resp.Data, "custom_pipeline_processing") {
		t.Fatalf("expected translated extra labels in response, got %v", resp.Data)
	}
	for _, label := range resp.Data {
		if strings.Contains(label, ".") {
			t.Fatalf("expected underscore-only labels in translated mode, got dotted label %q in %v", label, resp.Data)
		}
	}
}

func TestLabelSurface_LabelValuesResolveCustomAliasFromConfiguredExtras(t *testing.T) {
	var requestedField string
	vlBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/select/logsql/stream_field_names":
			http.Error(w, "unsupported", http.StatusNotFound)
		case "/select/logsql/field_values":
			requestedField = r.URL.Query().Get("field")
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"values":[{"value":"i-host-1","hits":1}]}`))
		default:
			t.Fatalf("unexpected backend path %s", r.URL.Path)
		}
	}))
	defer vlBackend.Close()

	p, err := New(Config{
		BackendURL:        vlBackend.URL,
		Cache:             cache.New(60*time.Second, 1000),
		LogLevel:          "error",
		LabelStyle:        LabelStyleUnderscores,
		ExtraLabelFields:  []string{"host.id"},
		MetadataFieldMode: MetadataFieldModeTranslated,
	})
	if err != nil {
		t.Fatalf("failed to create proxy: %v", err)
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/loki/api/v1/label/host_id/values?query=%7Bservice_name%3D%22api%22%7D", nil)
	p.handleLabelValues(w, r)

	if requestedField != "host.id" {
		t.Fatalf("expected host_id alias to resolve to host.id, got %q", requestedField)
	}

	var resp struct {
		Data []string `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode label values response: %v", err)
	}
	if len(resp.Data) != 1 || resp.Data[0] != "i-host-1" {
		t.Fatalf("expected host_id value from resolved host.id field, got %v", resp.Data)
	}
}

func TestLabelSurface_VolumeTargetLabelsResolveCustomAlias(t *testing.T) {
	var requestedField string
	vlBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/select/logsql/stream_field_names":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"values":[{"value":"host.id","hits":2}]}`))
		case "/select/logsql/hits":
			requestedField = r.URL.Query().Get("field")
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"hits":[{"fields":{"host.id":"i-host-1"},"timestamps":["2026-04-04T17:18:49Z"],"values":[3]}]}`))
		default:
			t.Fatalf("unexpected backend path %s", r.URL.Path)
		}
	}))
	defer vlBackend.Close()

	p, err := New(Config{
		BackendURL:        vlBackend.URL,
		Cache:             cache.New(60*time.Second, 1000),
		LogLevel:          "error",
		LabelStyle:        LabelStyleUnderscores,
		MetadataFieldMode: MetadataFieldModeTranslated,
	})
	if err != nil {
		t.Fatalf("failed to create proxy: %v", err)
	}

	params := url.Values{}
	params.Set("query", `{service_name="api"}`)
	params.Set("targetLabels", "host_id")
	params.Set("start", "1")
	params.Set("end", "2")
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/loki/api/v1/index/volume?"+params.Encode(), nil)
	p.handleVolume(w, r)

	if requestedField != "host.id" {
		t.Fatalf("expected volume targetLabels alias host_id to resolve to host.id, got %q", requestedField)
	}

	var resp struct {
		Data struct {
			Result []struct {
				Metric map[string]string `json:"metric"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode volume response: %v", err)
	}
	if len(resp.Data.Result) != 1 {
		t.Fatalf("expected one volume series, got %#v", resp.Data.Result)
	}
	if got := resp.Data.Result[0].Metric["host_id"]; got != "i-host-1" {
		t.Fatalf("expected translated host_id metric label in volume response, got %v", resp.Data.Result[0].Metric)
	}
}

func TestLabelSurface_VolumeRangeTargetLabelsResolveCustomAlias(t *testing.T) {
	var requestedField string
	vlBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/select/logsql/stream_field_names":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"values":[{"value":"host.id","hits":2}]}`))
		case "/select/logsql/hits":
			requestedField = r.URL.Query().Get("field")
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"hits":[{"fields":{"host.id":"i-host-1"},"timestamps":["2026-04-04T17:18:49Z"],"values":[3]}]}`))
		default:
			t.Fatalf("unexpected backend path %s", r.URL.Path)
		}
	}))
	defer vlBackend.Close()

	p, err := New(Config{
		BackendURL:        vlBackend.URL,
		Cache:             cache.New(60*time.Second, 1000),
		LogLevel:          "error",
		LabelStyle:        LabelStyleUnderscores,
		MetadataFieldMode: MetadataFieldModeTranslated,
	})
	if err != nil {
		t.Fatalf("failed to create proxy: %v", err)
	}

	params := url.Values{}
	params.Set("query", `{service_name="api"}`)
	params.Set("targetLabels", "host_id")
	params.Set("start", "1")
	params.Set("end", "2")
	params.Set("step", "60")
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/loki/api/v1/index/volume_range?"+params.Encode(), nil)
	p.handleVolumeRange(w, r)

	if requestedField != "host.id" {
		t.Fatalf("expected volume_range targetLabels alias host_id to resolve to host.id, got %q", requestedField)
	}
}
