package proxy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
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

func TestLabelSurface_LabelValuesIndexedBrowseUsesHotsetAndOffsetWithoutBackendRefetch(t *testing.T) {
	fieldValuesCalls := 0
	vlBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/select/logsql/stream_field_names":
			http.Error(w, "unsupported", http.StatusNotFound)
		case "/select/logsql/field_values":
			fieldValuesCalls++
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"values":[{"value":"delta","hits":1},{"value":"alpha","hits":1},{"value":"gamma","hits":1},{"value":"beta","hits":1}]}`))
		default:
			t.Fatalf("unexpected backend path %s", r.URL.Path)
		}
	}))
	defer vlBackend.Close()

	p, err := New(Config{
		BackendURL:                 vlBackend.URL,
		Cache:                      cache.New(60*time.Second, 1000),
		LogLevel:                   "error",
		LabelStyle:                 LabelStyleUnderscores,
		MetadataFieldMode:          MetadataFieldModeTranslated,
		LabelValuesIndexedCache:    true,
		LabelValuesHotLimit:        2,
		LabelValuesIndexMaxEntries: 1000,
	})
	if err != nil {
		t.Fatalf("failed to create proxy: %v", err)
	}

	w1 := httptest.NewRecorder()
	r1 := httptest.NewRequest(http.MethodGet, "/loki/api/v1/label/app/values", nil)
	p.handleLabelValues(w1, r1)

	var resp1 struct {
		Data []string `json:"data"`
	}
	if err := json.Unmarshal(w1.Body.Bytes(), &resp1); err != nil {
		t.Fatalf("decode first label values response: %v", err)
	}
	if len(resp1.Data) != 2 || resp1.Data[0] != "alpha" || resp1.Data[1] != "beta" {
		t.Fatalf("expected hotset-first values [alpha beta], got %v", resp1.Data)
	}

	w2 := httptest.NewRecorder()
	r2 := httptest.NewRequest(http.MethodGet, "/loki/api/v1/label/app/values?offset=2&limit=2", nil)
	p.handleLabelValues(w2, r2)

	var resp2 struct {
		Data []string `json:"data"`
	}
	if err := json.Unmarshal(w2.Body.Bytes(), &resp2); err != nil {
		t.Fatalf("decode second label values response: %v", err)
	}
	if len(resp2.Data) != 2 || resp2.Data[0] != "delta" || resp2.Data[1] != "gamma" {
		t.Fatalf("expected offset window [delta gamma], got %v", resp2.Data)
	}
	if fieldValuesCalls != 1 {
		t.Fatalf("expected single backend field_values call with indexed browse cache, got %d", fieldValuesCalls)
	}
}

func TestLabelSurface_LabelValuesIndexedBrowseSearchUsesIndex(t *testing.T) {
	fieldValuesCalls := 0
	vlBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/select/logsql/stream_field_names":
			http.Error(w, "unsupported", http.StatusNotFound)
		case "/select/logsql/field_values":
			fieldValuesCalls++
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"values":[{"value":"alpha","hits":1},{"value":"beta","hits":1},{"value":"delta","hits":1},{"value":"gamma","hits":1}]}`))
		default:
			t.Fatalf("unexpected backend path %s", r.URL.Path)
		}
	}))
	defer vlBackend.Close()

	p, err := New(Config{
		BackendURL:                 vlBackend.URL,
		Cache:                      cache.New(60*time.Second, 1000),
		LogLevel:                   "error",
		LabelStyle:                 LabelStyleUnderscores,
		MetadataFieldMode:          MetadataFieldModeTranslated,
		LabelValuesIndexedCache:    true,
		LabelValuesHotLimit:        2,
		LabelValuesIndexMaxEntries: 1000,
	})
	if err != nil {
		t.Fatalf("failed to create proxy: %v", err)
	}

	seedW := httptest.NewRecorder()
	seedReq := httptest.NewRequest(http.MethodGet, "/loki/api/v1/label/app/values", nil)
	p.handleLabelValues(seedW, seedReq)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/loki/api/v1/label/app/values?search=ta&limit=10", nil)
	p.handleLabelValues(w, r)

	var resp struct {
		Data []string `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode search label values response: %v", err)
	}
	if len(resp.Data) != 2 || resp.Data[0] != "beta" || resp.Data[1] != "delta" {
		t.Fatalf("expected search-filtered indexed values [beta delta], got %v", resp.Data)
	}
	if fieldValuesCalls != 1 {
		t.Fatalf("expected search browse to use index without extra backend calls, got %d", fieldValuesCalls)
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

func TestLabelSurface_TargetLabelInventoryLookupUsesCache(t *testing.T) {
	streamFieldCalls := 0
	vlBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/select/logsql/stream_field_names":
			streamFieldCalls++
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"values":[{"value":"custom.pipeline.processing","hits":3}]}`))
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
	params.Set("start", "1")
	params.Set("end", "2")

	ctx := context.WithValue(context.Background(), orgIDKey, "team-a")

	got1 := p.resolveTargetLabelFields(ctx, "custom_pipeline_processing", params)
	got2 := p.resolveTargetLabelFields(ctx, "custom_pipeline_processing", params)
	if len(got1) != 1 || got1[0] != "custom.pipeline.processing" {
		t.Fatalf("expected first lookup to resolve custom.pipeline.processing, got %v", got1)
	}
	if len(got2) != 1 || got2[0] != "custom.pipeline.processing" {
		t.Fatalf("expected second lookup to resolve custom.pipeline.processing, got %v", got2)
	}
	if streamFieldCalls != 1 {
		t.Fatalf("expected one stream_field_names call due cache reuse, got %d", streamFieldCalls)
	}
}

func TestLabelSurface_LabelValuesIndexPersistsOnShutdownAndRestoresOnStartup(t *testing.T) {
	vlBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unused in test", http.StatusNotFound)
	}))
	defer vlBackend.Close()

	snapshotPath := filepath.Join(t.TempDir(), "label-values-index.json")

	p1, err := New(Config{
		BackendURL:                      vlBackend.URL,
		Cache:                           cache.New(60*time.Second, 1000),
		LogLevel:                        "error",
		LabelStyle:                      LabelStyleUnderscores,
		MetadataFieldMode:               MetadataFieldModeTranslated,
		LabelValuesIndexedCache:         true,
		LabelValuesHotLimit:             100,
		LabelValuesIndexMaxEntries:      1000,
		LabelValuesIndexPersistPath:     snapshotPath,
		LabelValuesIndexPersistInterval: time.Hour,
	})
	if err != nil {
		t.Fatalf("create proxy #1: %v", err)
	}
	p1.Init()

	p1.updateLabelValuesIndex("team-a", "host_id", []string{"zeta", "alpha", "beta"})
	if err := p1.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown proxy #1: %v", err)
	}

	p2, err := New(Config{
		BackendURL:                      vlBackend.URL,
		Cache:                           cache.New(60*time.Second, 1000),
		LogLevel:                        "error",
		LabelStyle:                      LabelStyleUnderscores,
		MetadataFieldMode:               MetadataFieldModeTranslated,
		LabelValuesIndexedCache:         true,
		LabelValuesHotLimit:             100,
		LabelValuesIndexMaxEntries:      1000,
		LabelValuesIndexPersistPath:     snapshotPath,
		LabelValuesIndexPersistInterval: time.Hour,
	})
	if err != nil {
		t.Fatalf("create proxy #2: %v", err)
	}
	p2.Init()
	defer func() { _ = p2.Shutdown(context.Background()) }()

	got, ok := p2.selectLabelValuesFromIndex("team-a", "host_id", "", 0, 10)
	if !ok {
		t.Fatalf("expected restored label-values index to be available")
	}
	want := []string{"alpha", "beta", "zeta"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected restored values: want=%v got=%v", want, got)
	}
}

func TestLabelSurface_LabelValuesIndexStartupWarmsFromPeersWhenDiskStale(t *testing.T) {
	vlBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unused in test", http.StatusNotFound)
	}))
	defer vlBackend.Close()

	now := time.Now().UTC()
	peerSnapshot := labelValuesIndexSnapshot{
		Version:         1,
		SavedAtUnixNano: now.UnixNano(),
		StatesByKey: map[string]map[string]labelValueIndexEntry{
			labelValuesIndexKey("team-a", "host_id"): {
				"from-peer": {SeenCount: 9, LastSeen: now.UnixNano()},
			},
		},
	}
	peerPayload, err := json.Marshal(peerSnapshot)
	if err != nil {
		t.Fatalf("marshal peer snapshot: %v", err)
	}
	ownerCache := cache.New(5*time.Minute, 1000)
	ownerCache.SetWithTTL(labelValuesIndexSnapshotCacheKey, peerPayload, 5*time.Minute)

	ownerPeer := cache.NewPeerCache(cache.PeerConfig{SelfAddr: "owner:3100"})
	peerSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/_cache/get" {
			http.NotFound(w, r)
			return
		}
		ownerPeer.ServeHTTP(w, r, ownerCache)
	}))
	defer peerSrv.Close()

	peerAddr := strings.TrimPrefix(peerSrv.URL, "http://")
	localPeer := cache.NewPeerCache(cache.PeerConfig{
		SelfAddr:      "self:3100",
		DiscoveryType: "static",
		StaticPeers:   peerAddr,
		Timeout:       800 * time.Millisecond,
	})

	localCache := cache.New(5*time.Minute, 1000)
	localCache.SetL3(localPeer)

	stalePath := filepath.Join(t.TempDir(), "label-values-index.json")
	staleSnapshot := labelValuesIndexSnapshot{
		Version:         1,
		SavedAtUnixNano: now.Add(-10 * time.Minute).UnixNano(),
		StatesByKey: map[string]map[string]labelValueIndexEntry{
			labelValuesIndexKey("team-a", "host_id"): {
				"from-disk": {SeenCount: 1, LastSeen: now.Add(-10 * time.Minute).UnixNano()},
			},
		},
	}
	stalePayload, err := json.Marshal(staleSnapshot)
	if err != nil {
		t.Fatalf("marshal stale snapshot: %v", err)
	}
	if err := os.WriteFile(stalePath, stalePayload, 0o644); err != nil {
		t.Fatalf("write stale snapshot: %v", err)
	}

	p, err := New(Config{
		BackendURL:                      vlBackend.URL,
		Cache:                           localCache,
		PeerCache:                       localPeer,
		LogLevel:                        "error",
		LabelStyle:                      LabelStyleUnderscores,
		MetadataFieldMode:               MetadataFieldModeTranslated,
		LabelValuesIndexedCache:         true,
		LabelValuesHotLimit:             100,
		LabelValuesIndexMaxEntries:      1000,
		LabelValuesIndexPersistPath:     stalePath,
		LabelValuesIndexPersistInterval: time.Hour,
		LabelValuesIndexStartupStale:    30 * time.Second,
		LabelValuesIndexPeerWarmTimeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("create proxy: %v", err)
	}
	p.Init()
	defer func() { _ = p.Shutdown(context.Background()) }()

	got, ok := p.selectLabelValuesFromIndex("team-a", "host_id", "", 0, 10)
	if !ok {
		t.Fatalf("expected peer-warmed label-values index to be available")
	}
	want := []string{"from-peer"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected peer-warmed values: want=%v got=%v", want, got)
	}
}
