package proxy

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/szibis/Loki-VL-proxy/internal/cache"
)

func TestTailHardening_RejectsBrowserOriginsByDefault(t *testing.T) {
	vlBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, `{"_time":"2024-01-15T10:30:00Z","_msg":"test log line","app":"nginx"}`)
	}))
	defer vlBackend.Close()

	c := cache.New(60*time.Second, 1000)
	p, err := New(Config{BackendURL: vlBackend.URL, Cache: c, LogLevel: "error"})
	if err != nil {
		t.Fatalf("failed to create proxy: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(p.handleTail))
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "?query={app%3D%22nginx%22}"
	header := http.Header{}
	header.Set("Origin", "https://grafana.example.com")
	_, resp, err := websocket.DefaultDialer.Dial(wsURL, header)
	if err == nil {
		t.Fatal("expected websocket dial to fail for untrusted origin")
	}
	if resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 for untrusted origin, got resp=%v err=%v", resp, err)
	}
}

func TestTailHardening_AllowsConfiguredOrigin(t *testing.T) {
	vlBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, `{"_time":"2024-01-15T10:30:00Z","_msg":"test log line","app":"nginx"}`)
	}))
	defer vlBackend.Close()

	c := cache.New(60*time.Second, 1000)
	p, err := New(Config{
		BackendURL:         vlBackend.URL,
		Cache:              c,
		LogLevel:           "error",
		TailAllowedOrigins: []string{"https://grafana.example.com"},
	})
	if err != nil {
		t.Fatalf("failed to create proxy: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(p.handleTail))
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "?query={app%3D%22nginx%22}"
	header := http.Header{}
	header.Set("Origin", "https://grafana.example.com")
	ws, resp, err := websocket.DefaultDialer.Dial(wsURL, header)
	if err != nil {
		t.Fatalf("websocket dial failed: %v (resp=%v)", err, resp)
	}
	defer ws.Close()

	_, msg, err := ws.ReadMessage()
	if err != nil {
		t.Fatalf("websocket read failed: %v", err)
	}

	var frame map[string]interface{}
	if err := json.Unmarshal(msg, &frame); err != nil {
		t.Fatalf("invalid JSON frame: %v", err)
	}
	if _, ok := frame["streams"]; !ok {
		t.Fatalf("expected Loki tail frame, got %v", frame)
	}
}

func TestTailHardening_BackendFailureReturnsHTTPStatusBeforeUpgrade(t *testing.T) {
	vlBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "backend unauthorized", http.StatusUnauthorized)
	}))
	defer vlBackend.Close()

	c := cache.New(60*time.Second, 1000)
	p, err := New(Config{BackendURL: vlBackend.URL, Cache: c, LogLevel: "error"})
	if err != nil {
		t.Fatalf("failed to create proxy: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(p.handleTail))
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "?query={app%3D%22nginx%22}"
	_, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err == nil {
		t.Fatal("expected websocket dial to fail on upstream auth failure")
	}
	if resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected upstream 401 to be returned before upgrade, got resp=%v err=%v", resp, err)
	}
}

func TestTailHardening_UsesDedicatedStreamingClient(t *testing.T) {
	p := newTestProxy(t, "http://unused")
	if p.tailClient == nil {
		t.Fatal("expected dedicated tail client to be configured")
	}
	if p.tailClient.Timeout != 0 {
		t.Fatalf("expected tail client to disable overall timeout, got %s", p.tailClient.Timeout)
	}
	if p.client.Timeout == 0 {
		t.Fatal("expected regular backend client to retain bounded timeout")
	}
}
