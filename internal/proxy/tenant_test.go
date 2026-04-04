package proxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/szibis/Loki-VL-proxy/internal/cache"
)

// =============================================================================
// Tenant mapping tests — string org IDs to VL numeric AccountID
// =============================================================================

func TestTenant_StringMapping(t *testing.T) {
	var receivedAccountID, receivedProjectID string
	vlBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAccountID = r.Header.Get("AccountID")
		receivedProjectID = r.Header.Get("ProjectID")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"values": []map[string]interface{}{},
		})
	}))
	defer vlBackend.Close()

	tenantMap := map[string]TenantMapping{
		"team-alpha": {AccountID: "100", ProjectID: "1"},
		"team-beta":  {AccountID: "200", ProjectID: "2"},
		"ops-prod":   {AccountID: "300", ProjectID: "0"},
	}

	c := cache.New(60*time.Second, 1000)
	p, _ := New(Config{
		BackendURL: vlBackend.URL,
		Cache:      c,
		LogLevel:   "error",
		TenantMap:  tenantMap,
	})

	// Test mapped string tenant
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/loki/api/v1/labels", nil)
	r.Header.Set("X-Scope-OrgID", "team-alpha")
	p.handleLabels(w, r)

	if receivedAccountID != "100" {
		t.Errorf("expected AccountID=100, got %q", receivedAccountID)
	}
	if receivedProjectID != "1" {
		t.Errorf("expected ProjectID=1, got %q", receivedProjectID)
	}
}

func TestTenant_UnmappedStringRejected(t *testing.T) {
	var backendCalled bool
	vlBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backendCalled = true
		json.NewEncoder(w).Encode(map[string]interface{}{
			"values": []map[string]interface{}{},
		})
	}))
	defer vlBackend.Close()

	c := cache.New(60*time.Second, 1000)
	p, _ := New(Config{
		BackendURL: vlBackend.URL,
		Cache:      c,
		LogLevel:   "error",
		TenantMap:  map[string]TenantMapping{"known": {AccountID: "1", ProjectID: "0"}},
	})

	mux := http.NewServeMux()
	p.RegisterRoutes(mux)

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/loki/api/v1/labels", nil)
	r.Header.Set("X-Scope-OrgID", "unknown-tenant")
	mux.ServeHTTP(w, r)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for unknown tenant, got %d body=%s", w.Code, w.Body.String())
	}
	if backendCalled {
		t.Fatal("backend should not be called for unknown tenant")
	}
}

func TestTenant_NumericPassthrough(t *testing.T) {
	var receivedAccountID string
	vlBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAccountID = r.Header.Get("AccountID")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"values": []map[string]interface{}{},
		})
	}))
	defer vlBackend.Close()

	c := cache.New(60*time.Second, 1000)
	p, _ := New(Config{
		BackendURL: vlBackend.URL,
		Cache:      c,
		LogLevel:   "error",
	})

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/loki/api/v1/labels", nil)
	r.Header.Set("X-Scope-OrgID", "42")
	p.handleLabels(w, r)

	if receivedAccountID != "42" {
		t.Errorf("expected AccountID=42 for numeric org, got %q", receivedAccountID)
	}
}

func TestTenant_NoHeader(t *testing.T) {
	var receivedAccountID string
	vlBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAccountID = r.Header.Get("AccountID")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"values": []map[string]interface{}{},
		})
	}))
	defer vlBackend.Close()

	c := cache.New(60*time.Second, 1000)
	p, _ := New(Config{BackendURL: vlBackend.URL, Cache: c, LogLevel: "error"})

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/loki/api/v1/labels", nil)
	p.handleLabels(w, r)

	if receivedAccountID != "" {
		t.Errorf("expected no AccountID when no OrgID, got %q", receivedAccountID)
	}
}

func TestTenant_AuthEnabledRequiresHeader(t *testing.T) {
	vlBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("backend should not be called when tenant header is required")
	}))
	defer vlBackend.Close()

	c := cache.New(60*time.Second, 1000)
	p, _ := New(Config{
		BackendURL:  vlBackend.URL,
		Cache:       c,
		LogLevel:    "error",
		AuthEnabled: true,
	})

	mux := http.NewServeMux()
	p.RegisterRoutes(mux)

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/loki/api/v1/labels", nil)
	mux.ServeHTTP(w, r)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 when auth.enabled=true and X-Scope-OrgID is missing, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestTenant_GlobalBypassDisabledWhenMappingsConfigured(t *testing.T) {
	vlBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("backend should not be called when global tenant bypass is disabled")
	}))
	defer vlBackend.Close()

	c := cache.New(60*time.Second, 1000)
	p, _ := New(Config{
		BackendURL: vlBackend.URL,
		Cache:      c,
		LogLevel:   "error",
		TenantMap:  map[string]TenantMapping{"known": {AccountID: "1", ProjectID: "0"}},
	})

	mux := http.NewServeMux()
	p.RegisterRoutes(mux)

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/loki/api/v1/labels", nil)
	r.Header.Set("X-Scope-OrgID", "*")
	mux.ServeHTTP(w, r)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for wildcard tenant bypass, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestTenant_GlobalBypassAllowedWithoutMappings(t *testing.T) {
	tests := []struct {
		name  string
		orgID string
	}{
		{name: "wildcard", orgID: "*"},
		{name: "zero", orgID: "0"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var receivedAccountID, receivedProjectID string
			vlBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				receivedAccountID = r.Header.Get("AccountID")
				receivedProjectID = r.Header.Get("ProjectID")
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"values": []map[string]interface{}{},
				})
			}))
			defer vlBackend.Close()

			c := cache.New(60*time.Second, 1000)
			p, _ := New(Config{BackendURL: vlBackend.URL, Cache: c, LogLevel: "error"})

			mux := http.NewServeMux()
			p.RegisterRoutes(mux)

			w := httptest.NewRecorder()
			r := httptest.NewRequest("GET", "/loki/api/v1/labels", nil)
			r.Header.Set("X-Scope-OrgID", tc.orgID)
			mux.ServeHTTP(w, r)

			if w.Code != http.StatusOK {
				t.Fatalf("expected 200 for OrgID=%q without tenant mappings, got %d body=%s", tc.orgID, w.Code, w.Body.String())
			}
			if receivedAccountID != "" || receivedProjectID != "" {
				t.Fatalf("expected OrgID=%q to use backend default tenant, got AccountID=%q ProjectID=%q", tc.orgID, receivedAccountID, receivedProjectID)
			}
		})
	}
}

func TestTenant_GlobalBypassRequiresOptInWhenMappingsConfigured(t *testing.T) {
	vlBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("backend should not be called when tenant mappings are configured and global bypass is disabled")
	}))
	defer vlBackend.Close()

	c := cache.New(60*time.Second, 1000)
	p, _ := New(Config{
		BackendURL: vlBackend.URL,
		Cache:      c,
		LogLevel:   "error",
		TenantMap:  map[string]TenantMapping{"known": {AccountID: "1", ProjectID: "0"}},
	})

	mux := http.NewServeMux()
	p.RegisterRoutes(mux)

	for _, orgID := range []string{"*", "0"} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("GET", "/loki/api/v1/labels", nil)
		r.Header.Set("X-Scope-OrgID", orgID)
		mux.ServeHTTP(w, r)

		if w.Code != http.StatusForbidden {
			t.Fatalf("expected 403 for OrgID=%q when tenant mappings are configured, got %d body=%s", orgID, w.Code, w.Body.String())
		}
	}
}

func TestTenant_GlobalBypassAllowedWhenMappingsConfiguredAndOptedIn(t *testing.T) {
	tests := []struct {
		name  string
		orgID string
	}{
		{name: "wildcard", orgID: "*"},
		{name: "zero", orgID: "0"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var receivedAccountID, receivedProjectID string
			vlBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				receivedAccountID = r.Header.Get("AccountID")
				receivedProjectID = r.Header.Get("ProjectID")
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"values": []map[string]interface{}{},
				})
			}))
			defer vlBackend.Close()

			c := cache.New(60*time.Second, 1000)
			p, _ := New(Config{
				BackendURL:        vlBackend.URL,
				Cache:             c,
				LogLevel:          "error",
				TenantMap:         map[string]TenantMapping{"known": {AccountID: "1", ProjectID: "0"}},
				AllowGlobalTenant: true,
			})

			mux := http.NewServeMux()
			p.RegisterRoutes(mux)

			w := httptest.NewRecorder()
			r := httptest.NewRequest("GET", "/loki/api/v1/labels", nil)
			r.Header.Set("X-Scope-OrgID", tc.orgID)
			mux.ServeHTTP(w, r)

			if w.Code != http.StatusOK {
				t.Fatalf("expected 200 for OrgID=%q when global bypass is enabled, got %d body=%s", tc.orgID, w.Code, w.Body.String())
			}
			if receivedAccountID != "" || receivedProjectID != "" {
				t.Fatalf("expected OrgID=%q to use backend default tenant, got AccountID=%q ProjectID=%q", tc.orgID, receivedAccountID, receivedProjectID)
			}
		})
	}
}

func TestTenant_MultiTenantHeaderRejected(t *testing.T) {
	vlBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("backend should not be called for multi-tenant header values")
	}))
	defer vlBackend.Close()

	c := cache.New(60*time.Second, 1000)
	p, _ := New(Config{BackendURL: vlBackend.URL, Cache: c, LogLevel: "error"})

	mux := http.NewServeMux()
	p.RegisterRoutes(mux)

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/loki/api/v1/labels", nil)
	r.Header.Set("X-Scope-OrgID", "tenant-a|tenant-b")
	mux.ServeHTTP(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for multi-tenant X-Scope-OrgID, got %d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "multi-tenant") {
		t.Fatalf("expected multi-tenant rejection message, got %s", w.Body.String())
	}
}
