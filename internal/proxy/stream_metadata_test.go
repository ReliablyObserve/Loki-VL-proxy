package proxy

import (
	"testing"
	"time"

	"github.com/ReliablyObserve/Loki-VL-proxy/internal/cache"
)

func newStreamMetadataTestProxy(t *testing.T, emitStructuredMetadata bool) *Proxy {
	t.Helper()
	p, err := New(Config{
		BackendURL:             "http://unused",
		Cache:                  cache.New(30*time.Second, 100),
		LogLevel:               "error",
		EmitStructuredMetadata: emitStructuredMetadata,
		MetadataFieldMode:      MetadataFieldModeHybrid,
		LabelStyle:             LabelStylePassthrough,
	})
	if err != nil {
		t.Fatalf("failed to create proxy: %v", err)
	}
	return p
}

func TestVLLogsToLokiStreams_DefaultValuesStayTwoTuple(t *testing.T) {
	p := newStreamMetadataTestProxy(t, false)
	body := []byte(`{"_time":"2026-01-01T00:00:00Z","_msg":"ok","_stream":"{job=\"otel-proxy\",level=\"info\"}","service.name":"otel-app","deployment.environment.name":"dev","level":"info"}` + "\n")

	streams := p.vlLogsToLokiStreams(body, `{job="otel-proxy"}`)
	if len(streams) != 1 {
		t.Fatalf("expected 1 stream, got %d", len(streams))
	}
	values, ok := streams[0]["values"].([]interface{})
	if !ok || len(values) != 1 {
		t.Fatalf("expected one stream value, got %#v", streams[0]["values"])
	}
	pair, ok := values[0].([]interface{})
	if !ok {
		t.Fatalf("expected stream value to be tuple, got %T", values[0])
	}
	if len(pair) != 2 {
		t.Fatalf("expected canonical [ts, line] tuple, got %v", pair)
	}
}

func TestVLLogsToLokiStreams_EmitStructuredMetadataAddsThirdTupleValue(t *testing.T) {
	p := newStreamMetadataTestProxy(t, true)
	body := []byte(`{"_time":"2026-01-01T00:00:00Z","_msg":"ok","_stream":"{job=\"otel-proxy\",level=\"info\"}","service.name":"otel-app","deployment.environment.name":"dev","level":"info"}` + "\n")

	streams := p.vlLogsToLokiStreams(body, `{job="otel-proxy"}`)
	if len(streams) != 1 {
		t.Fatalf("expected 1 stream, got %d", len(streams))
	}
	values, ok := streams[0]["values"].([]interface{})
	if !ok || len(values) != 1 {
		t.Fatalf("expected one stream value, got %#v", streams[0]["values"])
	}
	pair, ok := values[0].([]interface{})
	if !ok {
		t.Fatalf("expected stream value to be tuple, got %T", values[0])
	}
	if len(pair) != 3 {
		t.Fatalf("expected [ts, line, metadata] tuple, got %v", pair)
	}
	meta, ok := pair[2].(map[string]interface{})
	if !ok {
		t.Fatalf("expected metadata object in tuple[2], got %T", pair[2])
	}
	if _, ok := meta["structuredMetadata"]; !ok {
		t.Fatalf("expected structuredMetadata payload, got %v", meta)
	}
}
