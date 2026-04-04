package proxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/szibis/Loki-VL-proxy/internal/cache"
)

func TestDatasourceCompat_ForwardsConfiguredCookies(t *testing.T) {
	var gotCookie string
	vlBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if cookie, err := r.Cookie("grafana_session"); err == nil {
			gotCookie = cookie.Value
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"values": []map[string]interface{}{},
		})
	}))
	defer vlBackend.Close()

	c := cache.New(60*time.Second, 1000)
	p, err := New(Config{
		BackendURL:     vlBackend.URL,
		Cache:          c,
		LogLevel:       "error",
		ForwardCookies: []string{"grafana_session"},
	})
	if err != nil {
		t.Fatalf("failed to create proxy: %v", err)
	}

	req := httptest.NewRequest("GET", "/loki/api/v1/labels", nil)
	req.AddCookie(&http.Cookie{Name: "grafana_session", Value: "abc123"})
	req.AddCookie(&http.Cookie{Name: "ignored_cookie", Value: "skip-me"})
	w := httptest.NewRecorder()
	p.handleLabels(w, req)

	if gotCookie != "abc123" {
		t.Fatalf("expected configured cookie to be forwarded, got %q", gotCookie)
	}
}

func TestDatasourceCompat_BackendTimeoutIsConfigurable(t *testing.T) {
	c := cache.New(60*time.Second, 1000)
	p, err := New(Config{
		BackendURL:     "http://unused",
		Cache:          c,
		LogLevel:       "error",
		BackendTimeout: 10 * time.Minute,
	})
	if err != nil {
		t.Fatalf("failed to create proxy: %v", err)
	}

	if p.client.Timeout != 10*time.Minute {
		t.Fatalf("expected backend timeout to be configurable, got %s", p.client.Timeout)
	}
}
