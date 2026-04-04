package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	_ "net/http/pprof"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/szibis/Loki-VL-proxy/internal/cache"
	"github.com/szibis/Loki-VL-proxy/internal/metrics"
	mw "github.com/szibis/Loki-VL-proxy/internal/middleware"
	"github.com/szibis/Loki-VL-proxy/internal/proxy"
)

func main() {
	// Server flags
	listenAddr := flag.String("listen", ":3100", "Address to listen on (Loki-compatible frontend)")
	backendURL := flag.String("backend", "http://localhost:9428", "VictoriaLogs backend URL")
	logLevel := flag.String("log-level", "info", "Log level: debug, info, warn, error")

	// Cache flags
	cacheTTL := flag.Duration("cache-ttl", 60*time.Second, "Cache TTL for label/metadata queries")
	cacheMax := flag.Int("cache-max", 10000, "Maximum cache entries")

	// Disk cache flags
	diskCachePath := flag.String("disk-cache-path", "", "Path to L2 disk cache (bbolt). Empty disables.")
	diskCacheCompress := flag.Bool("disk-cache-compress", true, "Gzip compression for disk cache")
	diskCacheFlushSize := flag.Int("disk-cache-flush-size", 100, "Flush write buffer after N entries")
	diskCacheFlushInterval := flag.Duration("disk-cache-flush-interval", 5*time.Second, "Write buffer flush interval")
	// Tenant mapping
	tenantMapJSON := flag.String("tenant-map", "", `JSON tenant mapping: {"org-name":{"account_id":"1","project_id":"0"}}`)

	// OTLP telemetry flags
	otlpEndpoint := flag.String("otlp-endpoint", "", "OTLP HTTP endpoint (e.g., http://otel-collector:4318/v1/metrics)")
	otlpInterval := flag.Duration("otlp-interval", 30*time.Second, "OTLP push interval")
	otlpCompression := flag.String("otlp-compression", "none", "OTLP compression: none, gzip, zstd")
	otlpTimeout := flag.Duration("otlp-timeout", 10*time.Second, "OTLP HTTP request timeout")
	otlpTLSSkipVerify := flag.Bool("otlp-tls-skip-verify", false, "Skip TLS verification for OTLP endpoint")

	// HTTP server hardening
	readTimeout := flag.Duration("http-read-timeout", 30*time.Second, "HTTP server read timeout")
	writeTimeout := flag.Duration("http-write-timeout", 120*time.Second, "HTTP server write timeout")
	idleTimeout := flag.Duration("http-idle-timeout", 120*time.Second, "HTTP server idle timeout")
	maxHeaderBytes := flag.Int("http-max-header-bytes", 1<<20, "HTTP max header size (default: 1MB)")
	maxBodyBytes := flag.Int64("http-max-body-bytes", 10<<20, "HTTP max request body size (default: 10MB)")

	// TLS server
	tlsCertFile := flag.String("tls-cert-file", "", "TLS certificate file for HTTPS server")
	tlsKeyFile := flag.String("tls-key-file", "", "TLS private key file for HTTPS server")
	tlsClientCAFile := flag.String("tls-client-ca-file", "", "CA certificate file used to verify HTTPS client certificates")
	tlsRequireClientCert := flag.Bool("tls-require-client-cert", false, "Require and verify HTTPS client certificates")

	// Response compression
	enableGzip := flag.Bool("response-gzip", true, "Enable gzip response compression for clients that accept it")

	// Grafana datasource compatibility
	maxLines := flag.Int("max-lines", 1000, "Default max lines per query")
	backendTimeout := flag.Duration("backend-timeout", 120*time.Second, "Timeout for non-streaming requests to the VictoriaLogs backend")
	backendBasicAuth := flag.String("backend-basic-auth", "", "Basic auth for VL backend (user:password)")
	backendTLSSkip := flag.Bool("backend-tls-skip-verify", false, "Skip TLS verification for VL backend")
	forwardHeaders := flag.String("forward-headers", "", "Comma-separated list of HTTP headers to forward to VL backend")
	forwardCookies := flag.String("forward-cookies", "", "Comma-separated list of cookie names to forward to VL backend")
	derivedFieldsJSON := flag.String("derived-fields", "", `JSON derived fields: [{"name":"traceID","matcherRegex":"trace_id=([a-f0-9]+)","url":"http://tempo/trace/${__value.raw}"}]`)
	streamResponse := flag.Bool("stream-response", false, "Stream log responses via chunked transfer encoding")

	// Loki-style auth / instrumentation controls
	authEnabled := flag.Bool("auth.enabled", false, "Require X-Scope-OrgID on query requests. When false, requests without a tenant header use the backend default tenant.")
	registerInstrumentation := flag.Bool("server.register-instrumentation", true, "Register instrumentation handlers such as /metrics")
	enablePprof := flag.Bool("server.enable-pprof", false, "Expose /debug/pprof/* handlers")
	enableQueryAnalytics := flag.Bool("server.enable-query-analytics", false, "Expose /debug/queries query analytics")
	adminAuthToken := flag.String("server.admin-auth-token", "", "Bearer token required for admin/debug endpoints when set")
	tailAllowedOrigins := flag.String("tail.allowed-origins", "", "Comma-separated WebSocket Origin allowlist for /loki/api/v1/tail. Empty denies browser origins.")
	metricsMaxTenants := flag.Int("metrics.max-tenants", 256, "Maximum unique tenant labels retained in exported metrics before collapsing into __overflow__")
	metricsMaxClients := flag.Int("metrics.max-clients", 256, "Maximum unique client labels retained in exported metrics before collapsing into __overflow__")
	metricsTrustProxyHeaders := flag.Bool("metrics.trust-proxy-headers", false, "Trust X-Grafana-User and X-Forwarded-For when deriving per-client metrics labels")

	// Label translation
	labelStyle := flag.String("label-style", "passthrough", `Label name translation mode:
  passthrough  - no translation, pass VL field names as-is (use when VL stores underscores)
  underscores  - convert dots to underscores (use when VL stores OTel-style dotted names like service.name)`)
	fieldMappingJSON := flag.String("field-mapping", "", `JSON custom field mappings: [{"vl_field":"service.name","loki_label":"service_name"}]`)
	streamFieldsCSV := flag.String("stream-fields", "", `Comma-separated VL _stream_fields labels for stream selector optimization (e.g., "app,env,namespace")`)
	allowGlobalTenant := flag.Bool("tenant.allow-global", false, `Allow X-Scope-OrgID "*" or "0" to bypass AccountID/ProjectID scoping and use the backend default tenant`)

	// Peer cache (fleet distribution)
	peerSelf := flag.String("peer-self", "", `This instance's address for peer cache (e.g., "10.0.0.1:3100"). Empty disables peer cache.`)
	peerDiscovery := flag.String("peer-discovery", "", `Peer discovery: "dns" (headless service) or "static" (comma-separated)`)
	peerDNS := flag.String("peer-dns", "", `Headless service DNS name for peer discovery (e.g., "proxy-headless.ns.svc.cluster.local")`)
	peerStatic := flag.String("peer-static", "", `Static peer list (e.g., "10.0.0.1:3100,10.0.0.2:3100")`)
	peerAuthToken := flag.String("peer-auth-token", "", "Shared token required on /_cache/get peer-cache requests when set")

	flag.Parse()

	// Environment variable overrides
	if v := os.Getenv("LISTEN_ADDR"); v != "" {
		*listenAddr = v
	}
	if v := os.Getenv("VL_BACKEND_URL"); v != "" {
		*backendURL = v
	}
	if v := os.Getenv("TENANT_MAP"); v != "" && *tenantMapJSON == "" {
		*tenantMapJSON = v
	}
	if v := os.Getenv("OTLP_ENDPOINT"); v != "" && *otlpEndpoint == "" {
		*otlpEndpoint = v
	}
	if v := os.Getenv("OTLP_COMPRESSION"); v != "" && *otlpCompression == "none" {
		*otlpCompression = v
	}
	if v := os.Getenv("LABEL_STYLE"); v != "" && *labelStyle == "passthrough" {
		*labelStyle = v
	}
	if v := os.Getenv("FIELD_MAPPING"); v != "" && *fieldMappingJSON == "" {
		*fieldMappingJSON = v
	}

	// Parse tenant map
	var tenantMap map[string]proxy.TenantMapping
	if *tenantMapJSON != "" {
		if err := json.Unmarshal([]byte(*tenantMapJSON), &tenantMap); err != nil {
			log.Fatalf("Failed to parse -tenant-map JSON: %v", err)
		}
		log.Printf("Loaded %d tenant mappings", len(tenantMap))
	}

	// L1 in-memory cache
	c := cache.New(*cacheTTL, *cacheMax)

	// L2 disk cache (compression + write-back buffer)
	if *diskCachePath != "" {
		dc, err := cache.NewDiskCache(cache.DiskCacheConfig{
			Path:          *diskCachePath,
			Compression:   *diskCacheCompress,
			FlushSize:     *diskCacheFlushSize,
			FlushInterval: *diskCacheFlushInterval,
		})
		if err != nil {
			log.Fatalf("Failed to open disk cache: %v", err)
		}
		defer func() { _ = dc.Close() }()
		c.SetL2(dc)
		log.Printf("L2 disk cache enabled at %s (compress=%v, flush=%d/%s)",
			*diskCachePath, *diskCacheCompress, *diskCacheFlushSize, *diskCacheFlushInterval)
	}

	// Parse forward headers
	var fwdHeaders []string
	if *forwardHeaders != "" {
		for _, h := range strings.Split(*forwardHeaders, ",") {
			h = strings.TrimSpace(h)
			if h != "" {
				fwdHeaders = append(fwdHeaders, h)
			}
		}
	}

	// Parse field mappings
	var fieldMappings []proxy.FieldMapping
	if *fieldMappingJSON != "" {
		if err := json.Unmarshal([]byte(*fieldMappingJSON), &fieldMappings); err != nil {
			log.Fatalf("Failed to parse -field-mapping JSON: %v", err)
		}
		log.Printf("Loaded %d custom field mappings", len(fieldMappings))
	}

	// Validate label style
	ls := proxy.LabelStyle(*labelStyle)
	switch ls {
	case proxy.LabelStylePassthrough, proxy.LabelStyleUnderscores:
		// valid
	default:
		log.Fatalf("Invalid -label-style: %q (must be 'passthrough' or 'underscores')", *labelStyle)
	}
	if ls == proxy.LabelStyleUnderscores {
		log.Printf("Label style: underscores (VL dotted field names → Loki underscore labels)")
	}

	// Parse derived fields
	var derivedFields []proxy.DerivedField
	if *derivedFieldsJSON != "" {
		if err := json.Unmarshal([]byte(*derivedFieldsJSON), &derivedFields); err != nil {
			log.Fatalf("Failed to parse -derived-fields JSON: %v", err)
		}
		log.Printf("Loaded %d derived fields", len(derivedFields))
	}

	// Create peer cache if configured
	var peerCache *cache.PeerCache
	if *peerSelf != "" && *peerDiscovery != "" {
		peerCache = cache.NewPeerCache(cache.PeerConfig{
			SelfAddr:      *peerSelf,
			DiscoveryType: *peerDiscovery,
			DNSName:       *peerDNS,
			StaticPeers:   *peerStatic,
			Port:          3100,
		})
		c.SetL3(peerCache)
		log.Printf("Peer cache enabled: self=%s discovery=%s", *peerSelf, *peerDiscovery)
	}

	// Create proxy
	p, err := proxy.New(proxy.Config{
		BackendURL:               *backendURL,
		Cache:                    c,
		LogLevel:                 *logLevel,
		TenantMap:                tenantMap,
		MaxLines:                 *maxLines,
		BackendTimeout:           *backendTimeout,
		BackendBasicAuth:         *backendBasicAuth,
		BackendTLSSkip:           *backendTLSSkip,
		ForwardHeaders:           fwdHeaders,
		ForwardCookies:           parseCSV(*forwardCookies),
		DerivedFields:            derivedFields,
		StreamResponse:           *streamResponse,
		AuthEnabled:              *authEnabled,
		AllowGlobalTenant:        *allowGlobalTenant,
		RegisterInstrumentation:  registerInstrumentation,
		EnablePprof:              *enablePprof,
		EnableQueryAnalytics:     *enableQueryAnalytics,
		AdminAuthToken:           *adminAuthToken,
		TailAllowedOrigins:       parseCSV(*tailAllowedOrigins),
		MetricsMaxTenants:        *metricsMaxTenants,
		MetricsMaxClients:        *metricsMaxClients,
		MetricsTrustProxyHeaders: *metricsTrustProxyHeaders,
		LabelStyle:               ls,
		FieldMappings:            fieldMappings,
		StreamFields:             parseCSV(*streamFieldsCSV),
		PeerCache:                peerCache,
		PeerAuthToken:            *peerAuthToken,
	})
	if err != nil {
		log.Fatalf("Failed to create proxy: %v", err)
	}
	p.Init()

	// Start OTLP telemetry push
	if *otlpEndpoint != "" {
		pusher := metrics.NewOTLPPusher(metrics.OTLPConfig{
			Endpoint:      *otlpEndpoint,
			Interval:      *otlpInterval,
			Compression:   metrics.OTLPCompression(*otlpCompression),
			Timeout:       *otlpTimeout,
			TLSSkipVerify: *otlpTLSSkipVerify,
		}, p.GetMetrics())
		pusher.Start()
		defer pusher.Stop()
		log.Printf("OTLP push → %s (every %s, compression=%s)", *otlpEndpoint, *otlpInterval, *otlpCompression)
	}

	mux := http.NewServeMux()
	p.RegisterRoutes(mux)

	// Middleware chain: body limit → gzip compression
	handler := maxBodyHandler(*maxBodyBytes, mux)
	if *enableGzip {
		handler = mw.GzipHandler(handler)
	}

	// Hardened HTTP server with timeouts
	srv := &http.Server{
		Addr:           *listenAddr,
		Handler:        handler,
		ReadTimeout:    *readTimeout,
		WriteTimeout:   *writeTimeout,
		IdleTimeout:    *idleTimeout,
		MaxHeaderBytes: *maxHeaderBytes,
	}
	if *tlsClientCAFile != "" || *tlsRequireClientCert {
		tlsCfg, err := buildServerTLSConfig(*tlsClientCAFile, *tlsRequireClientCert)
		if err != nil {
			log.Fatalf("Failed to configure server TLS client authentication: %v", err)
		}
		srv.TLSConfig = tlsCfg
	}

	// SIGHUP config reload for tenant-map and field-mapping
	reloadCh := make(chan os.Signal, 1)
	signal.Notify(reloadCh, syscall.SIGHUP)
	go func() {
		for range reloadCh {
			log.Println("SIGHUP received, reloading configuration...")
			if v := os.Getenv("TENANT_MAP"); v != "" {
				var newTenantMap map[string]proxy.TenantMapping
				if err := json.Unmarshal([]byte(v), &newTenantMap); err != nil {
					log.Printf("Failed to reload TENANT_MAP: %v", err)
				} else {
					p.ReloadTenantMap(newTenantMap)
					log.Printf("Reloaded %d tenant mappings", len(newTenantMap))
				}
			}
			if v := os.Getenv("FIELD_MAPPING"); v != "" {
				var newMappings []proxy.FieldMapping
				if err := json.Unmarshal([]byte(v), &newMappings); err != nil {
					log.Printf("Failed to reload FIELD_MAPPING: %v", err)
				} else {
					p.ReloadFieldMappings(newMappings)
					log.Printf("Reloaded %d field mappings", len(newMappings))
				}
			}
		}
	}()

	// Graceful shutdown on SIGTERM/SIGINT
	shutdownCh := make(chan os.Signal, 1)
	signal.Notify(shutdownCh, syscall.SIGTERM, syscall.SIGINT)

	go func() {
		if *tlsCertFile != "" && *tlsKeyFile != "" {
			log.Printf("Loki-VL-proxy listening on %s (TLS), backend: %s", *listenAddr, *backendURL)
			if err := srv.ListenAndServeTLS(*tlsCertFile, *tlsKeyFile); err != nil && err != http.ErrServerClosed {
				log.Fatalf("TLS server failed: %v", err)
			}
		} else {
			log.Printf("Loki-VL-proxy listening on %s, backend: %s", *listenAddr, *backendURL)
			if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				log.Fatalf("Server failed: %v", err)
			}
		}
	}()

	sig := <-shutdownCh
	log.Printf("Received %v, shutting down gracefully...", sig)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("HTTP shutdown error: %v", err)
	}
	log.Println("Shutdown complete")
}

// maxBodyHandler limits the request body size to prevent resource exhaustion.
func maxBodyHandler(maxBytes int64, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
		}
		next.ServeHTTP(w, r)
	})
}

func parseCSV(s string) []string {
	if s == "" {
		return nil
	}
	var result []string
	for _, f := range strings.Split(s, ",") {
		f = strings.TrimSpace(f)
		if f != "" {
			result = append(result, f)
		}
	}
	return result
}

func buildServerTLSConfig(clientCAFile string, requireClientCert bool) (*tls.Config, error) {
	if clientCAFile == "" {
		if requireClientCert {
			return nil, fmt.Errorf("tls-client-ca-file is required when tls-require-client-cert is enabled")
		}
		return nil, nil
	}

	caPEM, err := os.ReadFile(clientCAFile)
	if err != nil {
		return nil, fmt.Errorf("read client CA file: %w", err)
	}

	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("failed to parse client CA PEM")
	}

	clientAuth := tls.VerifyClientCertIfGiven
	if requireClientCert {
		clientAuth = tls.RequireAndVerifyClientCert
	}

	return &tls.Config{
		MinVersion: tls.VersionTLS12,
		ClientCAs:  pool,
		ClientAuth: clientAuth,
	}, nil
}
