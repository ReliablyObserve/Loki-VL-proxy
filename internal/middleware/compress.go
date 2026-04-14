package middleware

import (
	"bufio"
	"compress/gzip"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"

	"github.com/klauspost/compress/zstd"
)

type responseCompressor interface {
	io.WriteCloser
	Flush() error
}

type responseCompressionMode string

const (
	ResponseCompressionNone responseCompressionMode = "none"
	ResponseCompressionAuto responseCompressionMode = "auto"
	ResponseCompressionGzip responseCompressionMode = "gzip"
	ResponseCompressionZstd responseCompressionMode = "zstd"
)

// compressedResponseWriter wraps http.ResponseWriter to compress the response body.
type compressedResponseWriter struct {
	http.ResponseWriter
	writer     responseCompressor
	statusCode int
}

func (w *compressedResponseWriter) Write(b []byte) (int, error) {
	return w.writer.Write(b)
}

func (w *compressedResponseWriter) WriteHeader(code int) {
	w.statusCode = code
	// Content length is no longer known after compression.
	w.Header().Del("Content-Length")
	w.ResponseWriter.WriteHeader(code)
}

func (w *compressedResponseWriter) Flush() {
	_ = w.writer.Flush()
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *compressedResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := w.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}
	return nil, nil, fmt.Errorf("hijack not supported")
}

// gzip.Writer pool to reduce allocations
var gzipWriterPool = sync.Pool{
	New: func() interface{} {
		w, _ := gzip.NewWriterLevel(io.Discard, gzip.BestSpeed)
		return w
	},
}

var zstdWriterPool = sync.Pool{
	New: func() interface{} {
		w, err := zstd.NewWriter(io.Discard, zstd.WithEncoderLevel(zstd.SpeedFastest))
		if err != nil {
			panic(err)
		}
		return w
	},
}

// GzipHandler is kept for backward compatibility with existing tests/callers.
func GzipHandler(next http.Handler) http.Handler {
	return CompressionHandler(next, string(ResponseCompressionGzip))
}

// CompressionHandler negotiates response compression with clients.
// "auto" prefers zstd, then gzip, based on the client's Accept-Encoding header.
func CompressionHandler(next http.Handler, mode string) http.Handler {
	selectedMode := normalizeCompressionMode(mode)
	if selectedMode == ResponseCompressionNone {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isWebSocketUpgrade(r) {
			next.ServeHTTP(w, r)
			return
		}

		encoding := negotiateResponseEncoding(r.Header.Get("Accept-Encoding"), selectedMode)
		if encoding == "" {
			next.ServeHTTP(w, r)
			return
		}

		compressor, release := acquireResponseCompressor(encoding, w)
		defer release()

		w.Header().Set("Content-Encoding", encoding)
		w.Header().Set("Vary", "Accept-Encoding")

		cw := &compressedResponseWriter{
			ResponseWriter: w,
			writer:         compressor,
		}

		next.ServeHTTP(cw, r)
		_ = compressor.Close()
	})
}

func normalizeCompressionMode(mode string) responseCompressionMode {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", string(ResponseCompressionAuto):
		return ResponseCompressionAuto
	case string(ResponseCompressionNone):
		return ResponseCompressionNone
	case string(ResponseCompressionGzip):
		return ResponseCompressionGzip
	case string(ResponseCompressionZstd):
		return ResponseCompressionZstd
	default:
		return ResponseCompressionAuto
	}
}

func negotiateResponseEncoding(acceptEncoding string, mode responseCompressionMode) string {
	switch mode {
	case ResponseCompressionGzip:
		if acceptsEncoding(acceptEncoding, "gzip") {
			return "gzip"
		}
	case ResponseCompressionZstd:
		if acceptsEncoding(acceptEncoding, "zstd") {
			return "zstd"
		}
	case ResponseCompressionAuto:
		if acceptsEncoding(acceptEncoding, "zstd") {
			return "zstd"
		}
		if acceptsEncoding(acceptEncoding, "gzip") {
			return "gzip"
		}
	}
	return ""
}

func acceptsEncoding(header, encoding string) bool {
	for _, part := range strings.Split(strings.ToLower(header), ",") {
		token := strings.TrimSpace(strings.SplitN(part, ";", 2)[0])
		if token == encoding || token == "*" {
			return true
		}
	}
	return false
}

func acquireResponseCompressor(encoding string, dst io.Writer) (responseCompressor, func()) {
	switch encoding {
	case "zstd":
		zw := zstdWriterPool.Get().(*zstd.Encoder)
		zw.Reset(dst)
		return zw, func() {
			zw.Reset(io.Discard)
			zstdWriterPool.Put(zw)
		}
	default:
		gz := gzipWriterPool.Get().(*gzip.Writer)
		gz.Reset(dst)
		return gz, func() {
			gz.Reset(io.Discard)
			gzipWriterPool.Put(gz)
		}
	}
}

func isWebSocketUpgrade(r *http.Request) bool {
	return strings.Contains(strings.ToLower(r.Header.Get("Connection")), "upgrade") &&
		strings.EqualFold(r.Header.Get("Upgrade"), "websocket")
}
